package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// fakeFetch serves a fixed release layout from memory. It records every URL it
// was asked for, which is how "never downloaded" is asserted rather than
// assumed.
type fakeFetch struct {
	files map[string][]byte
	asked []string
	err   error
}

func (f *fakeFetch) Get(_ context.Context, url string, _ int64) ([]byte, error) {
	f.asked = append(f.asked, url)
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.files[url]
	if !ok {
		return nil, errors.New("404")
	}
	return b, nil
}

func (f *fakeFetch) Stream(_ context.Context, url string, _ int64) (io.ReadCloser, error) {
	f.asked = append(f.asked, url)
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.files[url]
	if !ok {
		return nil, errors.New("404")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

const testBase = "https://mirror.example/v1.1.0"

// releaseFixture builds a signed release for version v whose binary is a shell
// script printing that version — enough for the pre-flight to be real rather
// than mocked.
func releaseFixture(t *testing.T, v string) (*fakeFetch, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	binary := []byte("#!/bin/sh\necho " + v + "\n")
	sum := sha256.Sum256(binary)
	name := artifactName("amd64")
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	return &fakeFetch{files: map[string][]byte{
		testBase + "/" + manifestName:  sums,
		testBase + "/" + signatureName: ed25519.Sign(priv, sums),
		testBase + "/" + name:          binary,
	}}, pub
}

func trust(t *testing.T, keys ...ed25519.PublicKey) {
	t.Helper()
	prev := publicKeys
	t.Cleanup(func() { publicKeys = prev })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, base64.StdEncoding.EncodeToString(k))
	}
	publicKeys = strings.Join(parts, ",")
}

// newUpdater builds an updater over a temp dir with a plausible running binary
// in it, and reports the exit codes it was asked for.
func newUpdater(t *testing.T, version string, f Fetcher) (*Updater, string, *[]int) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "cypher-agent")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatalf("writing the running binary: %v", err)
	}
	exits := &[]int{}
	u := New(Config{
		Version:    version,
		BinaryPath: binary,
		StateDir:   t.TempDir(),
		GOARCH:     "amd64",
		Probation:  time.Minute,
		Jitter:     0,
		Fetch:      f,
		Exit:       func(code int) { *exits = append(*exits, code) },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return u, binary, exits
}

// The property the whole feature rests on: with no baked-in release key there
// is nothing to verify against, so the updater does not run — and it says so
// rather than sitting at IDLE looking like a host with nothing to do. This is
// the opposite of a stubbed check (ADR-010 §3).
func TestABuildWithNoReleaseKeyRefusesToUpdateAndSaysSo(t *testing.T) {
	trust(t) // no keys
	f, _ := releaseFixture(t, "v1.1.0")
	u, binary, exits := newUpdater(t, "v1.0.0", f)

	if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_DISABLED {
		t.Fatalf("phase = %v, want DISABLED", got)
	}
	if !strings.Contains(u.Status().GetDetail(), "release key") {
		t.Fatalf("detail = %q, want it to name the missing key", u.Status().GetDetail())
	}

	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})
	if len(f.asked) != 0 {
		t.Fatalf("fetched %v with no key to verify against", f.asked)
	}
	if len(*exits) != 0 {
		t.Fatalf("exited %v", *exits)
	}
	assertUnchanged(t, binary, "v1.0.0")
}

// A manifest signed by a key we do not trust must be refused before anything is
// downloaded, and nothing must be renamed.
func TestAManifestSignedByAnUntrustedKeyIsRefusedBeforeTheDownload(t *testing.T) {
	f, _ := releaseFixture(t, "v1.1.0")
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	trust(t, other)

	u, binary, exits := newUpdater(t, "v1.0.0", f)
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})

	if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_FAILED {
		t.Fatalf("phase = %v, want FAILED", got)
	}
	for _, url := range f.asked {
		if strings.Contains(url, artifactName("amd64")) {
			t.Fatalf("downloaded the binary despite a bad signature: %v", f.asked)
		}
	}
	if len(*exits) != 0 {
		t.Fatalf("exited %v", *exits)
	}
	assertUnchanged(t, binary, "v1.0.0")
}

// A binary whose bytes do not match the signed digest is refused, and the
// staged file is removed rather than left behind for a later pass to trust.
func TestBytesThatDoNotMatchTheSignedDigestNeverReachTheBinary(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	f.files[testBase+"/"+artifactName("amd64")] = []byte("#!/bin/sh\necho v1.1.0\n# tampered\n")

	u, binary, exits := newUpdater(t, "v1.0.0", f)
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})

	if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_FAILED {
		t.Fatalf("phase = %v, want FAILED", got)
	}
	if _, err := os.Stat(stagedPath(binary)); err == nil {
		t.Fatal("the staged binary was left behind after a digest mismatch")
	}
	if len(*exits) != 0 {
		t.Fatalf("exited %v", *exits)
	}
	assertUnchanged(t, binary, "v1.0.0")
}

// The happy path, end to end: verify, download, pre-flight, swap, exit — and
// the previous binary is kept in its one slot so a rollback has something to
// return to.
func TestAVerifiedReleaseIsStagedPreflightedAndSwappedIn(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	u, binary, exits := newUpdater(t, "v1.0.0", f)

	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})

	if len(*exits) != 1 || (*exits)[0] != 0 {
		t.Fatalf("exits = %v, want one clean exit", *exits)
	}
	assertUnchanged(t, binary, "v1.1.0")
	if _, err := os.Stat(previousPath(binary)); err != nil {
		t.Fatalf("no previous slot kept: %v", err)
	}
	// And a boot marker is waiting for the new process, armed before the swap.
	m, err := readMarker(u.cfg.StateDir)
	if err != nil || m == nil {
		t.Fatalf("no boot marker written: %v", err)
	}
	if m.Target != "v1.1.0" || m.Previous != "v1.0.0" {
		t.Fatalf("marker = %+v", m)
	}
}

// Converging twice equals converging once, and here the second convergence is
// performed by a DIFFERENT BINARY: it reads the same desired state, finds its
// version equal, and does nothing.
func TestTheNewBinaryConvergesOnTheSameDesiredStateWithoutWork(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	u, _, _ := newUpdater(t, "v1.1.0", f)

	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})

	if len(f.asked) != 0 {
		t.Fatalf("the new binary fetched %v for a version it already is", f.asked)
	}
	if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_IDLE {
		t.Fatalf("phase = %v, want IDLE", got)
	}
}

// Going backwards is refused unless the panel says so explicitly. It stops an
// accident — a stale desired set, a mistyped tag, a restored snapshot walking
// the fleet backwards — and the refusal names the remedy.
func TestADowngradeIsRefusedUnlessTheChannelAllowsIt(t *testing.T) {
	f, pub := releaseFixture(t, "v0.9.0")
	trust(t, pub)
	u, _, _ := newUpdater(t, "v1.0.0", f)

	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v0.9.0", ArtifactBase: testBase})
	if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_FAILED {
		t.Fatalf("phase = %v, want FAILED", got)
	}
	if len(f.asked) != 0 {
		t.Fatalf("fetched %v for a refused downgrade", f.asked)
	}

	// With the flag it proceeds — and downloads again rather than trusting
	// whatever .prev happens to hold, because a rollback that skipped
	// verification would not be a verified rollback.
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v0.9.0", ArtifactBase: testBase, Rollback: true})
	if len(f.asked) == 0 {
		t.Fatal("a permitted rollback fetched nothing")
	}
}

// The failure this design exists for: the new binary runs but cannot dial home.
// Rolling back must need no network, no plane and no decision.
func TestAnUpdateThatCannotDialHomeRollsItselfBackWithoutThePlane(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	u, binary, _ := newUpdater(t, "v1.0.0", f)
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})
	assertUnchanged(t, binary, "v1.1.0")

	// The next start is the new binary. Its first pass records attempt one.
	next, exits := resume(t, "v1.1.0", binary, u.cfg.StateDir)
	next.Recover(context.Background())
	if len(*exits) != 0 {
		t.Fatalf("the first start of a new binary rolled back immediately: %v", *exits)
	}
	assertUnchanged(t, binary, "v1.1.0")

	// It did not dial home, so it starts again — and this time rolls back.
	third, exits3 := resume(t, "v1.1.0", binary, u.cfg.StateDir)
	third.Recover(context.Background())
	if len(*exits3) != 1 {
		t.Fatalf("exits = %v, want one clean exit after the rollback", *exits3)
	}
	assertUnchanged(t, binary, "v1.0.0")

	// And the binary that comes back knows it is the survivor, so the panel's
	// amber row has something to say.
	old, _ := resume(t, "v1.0.0", binary, u.cfg.StateDir)
	old.ReadRolledBack()
	st := old.Status()
	if st.GetPhase() != agentv1.AgentUpdateStatus_PHASE_ROLLED_BACK || st.GetTargetVersion() != "v1.1.0" {
		t.Fatalf("status = %+v, want a rolled-back v1.1.0", st)
	}
}

// Dialling home clears the marker, so the NEXT ordinary restart is not read as
// a failed attempt.
func TestDiallingHomeClearsTheProbation(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	u, binary, _ := newUpdater(t, "v1.0.0", f)
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: testBase})

	next, exits := resume(t, "v1.1.0", binary, u.cfg.StateDir)
	next.Recover(context.Background())
	next.DialedHome()

	third, exits3 := resume(t, "v1.1.0", binary, u.cfg.StateDir)
	third.Recover(context.Background())
	if len(*exits) != 0 || len(*exits3) != 0 {
		t.Fatalf("rolled back after a successful dial home: %v %v", *exits, *exits3)
	}
	assertUnchanged(t, binary, "v1.1.0")
}

func resume(t *testing.T, version, binary, stateDir string) (*Updater, *[]int) {
	t.Helper()
	exits := &[]int{}
	return New(Config{
		Version:    version,
		BinaryPath: binary,
		StateDir:   stateDir,
		GOARCH:     "amd64",
		Probation:  time.Minute,
		Fetch:      &fakeFetch{files: map[string][]byte{}},
		Exit:       func(code int) { *exits = append(*exits, code) },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), exits
}

// assertUnchanged reads the binary at path and checks which version it prints —
// the only honest way to ask "was it swapped".
func assertUnchanged(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("reading the binary: %v", err)
	}
	if !strings.Contains(string(b), want) {
		t.Fatalf("binary is not %s: %q", want, string(b))
	}
}

// The tag is bounded on THIS side too, not only at the plane. ADR-010's threat
// model says a compromised plane can choose only among genuine releases — the
// signature makes that true of the bytes, and this makes it true of the
// destination, which the signature says nothing about.
func TestATagThatIsNotTagShapedNeverBecomesAURL(t *testing.T) {
	f, pub := releaseFixture(t, "v1.1.0")
	trust(t, pub)
	u, binary, exits := newUpdater(t, "v1.0.0", f)

	for _, bad := range []string{
		"../../../etc/passwd",
		"v1.1.0/../../other",
		"https://evil.example/v1.1.0",
		"latest",
	} {
		u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: bad, ArtifactBase: testBase})
		if got := u.Status().GetPhase(); got != agentv1.AgentUpdateStatus_PHASE_FAILED {
			t.Fatalf("Apply(%q) phase = %v, want FAILED", bad, got)
		}
	}
	if len(f.asked) != 0 {
		t.Fatalf("fetched %v for a version that is not a tag", f.asked)
	}
	if len(*exits) != 0 {
		t.Fatalf("exited %v", *exits)
	}
	assertUnchanged(t, binary, "v1.0.0")

	// And an artifact base that could climb out of its own prefix is refused
	// before the first fetch, whatever the tag says.
	u.Apply(context.Background(), &agentv1.AgentUpdateSpec{Version: "v1.1.0", ArtifactBase: "https://mirror.example/a/../../b"})
	if len(f.asked) != 0 {
		t.Fatalf("fetched %v through a traversing artifact base", f.asked)
	}
}

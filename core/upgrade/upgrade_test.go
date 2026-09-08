package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeFetcher serves a fixture release.
type fakeFetcher struct {
	files map[string][]byte
	err   error
}

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	for name, body := range f.files {
		if strings.HasSuffix(url, "/"+name) {
			return body, nil
		}
	}
	return nil, fmt.Errorf("no such file: %s", url)
}

// release builds a signed fixture and returns the fetcher and the public key.
func release(t *testing.T, m Manifest) (fakeFetcher, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	body, _ := json.Marshal(m)
	sum := sha256.Sum256(body)
	binary := []byte("#!/bin/true\n")
	binSum := sha256.Sum256(binary)
	binName := AssetName("amd64")

	sums := fmt.Sprintf("%s  release.json\n%s  %s\n",
		hex.EncodeToString(sum[:]), hex.EncodeToString(binSum[:]), binName)
	sig := ed25519.Sign(priv, []byte(sums))

	return fakeFetcher{files: map[string][]byte{
		"SHA256SUMS":     []byte(sums),
		"SHA256SUMS.sig": []byte(base64.StdEncoding.EncodeToString(sig)),
		"release.json":   body,
		binName:          binary,
	}}, base64.StdEncoding.EncodeToString(pub)
}

func withKey(t *testing.T, key string) {
	t.Helper()
	prev := ReleasePublicKey
	ReleasePublicKey = key
	t.Cleanup(func() { ReleasePublicKey = prev })
}

const baseURL = "https://example.invalid/releases/%s"

// The whole chain, and the reason it exists: a SHA256SUMS file hosted beside
// the binaries proves integrity in transit and NOTHING about origin — whoever
// can replace an asset can replace the sums next to it. The signature is what a
// compromised release cannot forge.
func TestAVerifiedReleaseIsTheOnlyThingThatPasses(t *testing.T) {
	f, key := release(t, Manifest{Version: "v1.1.0", SchemaVersion: 43, AgentMinVersion: "v1.0.2"})
	withKey(t, key)

	rel, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if err != nil {
		t.Fatalf("a genuine release did not verify: %v", err)
	}
	if rel.Manifest.AgentMinVersion != "v1.0.2" {
		t.Errorf("manifest = %+v", rel.Manifest)
	}
}

// The attack this exists to stop: an attacker who can replace the assets
// replaces the binary AND the sums, but cannot produce a signature.
func TestATamperedManifestIsRefused(t *testing.T) {
	f, key := release(t, Manifest{Version: "v1.1.0"})
	withKey(t, key)
	f.files["SHA256SUMS"] = []byte(strings.Replace(string(f.files["SHA256SUMS"]), "0", "1", 1))

	_, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a tampered manifest verified: %v", err)
	}
}

// A different key is a different project. This is what stops a fork's release
// installing itself over this one.
func TestAReleaseSignedByAnotherKeyIsRefused(t *testing.T) {
	f, _ := release(t, Manifest{Version: "v1.1.0"})
	_, otherKey := release(t, Manifest{Version: "v1.1.0"})
	withKey(t, otherKey)

	_, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a release signed by another key verified: %v", err)
	}
}

// A build with no baked key must refuse rather than proceed unverified: an
// unsigned build must not be able to install a signed one on trust.
func TestABuildWithNoKeyCannotInstallAnything(t *testing.T) {
	f, _ := release(t, Manifest{Version: "v1.1.0"})
	withKey(t, "")
	_, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a keyless build accepted a release: %v", err)
	}
}

// release.json is covered BY the signed manifest, so swapping it alone fails.
func TestAnUncoveredReleaseJsonIsRefused(t *testing.T) {
	f, key := release(t, Manifest{Version: "v1.1.0"})
	withKey(t, key)
	f.files["release.json"] = []byte(`{"version":"v1.1.0","agent_min_version":"v9.9.9"}`)

	_, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a swapped release.json verified: %v", err)
	}
}

// A manifest that names a different version than was asked for is refused: it
// is how a downgrade would be smuggled past a caller that only checked the
// signature.
func TestAManifestForAnotherVersionIsRefused(t *testing.T) {
	f, key := release(t, Manifest{Version: "v1.0.0"})
	withKey(t, key)
	_, err := VerifyRelease(context.Background(), f, baseURL, "v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a mismatched manifest verified: %v", err)
	}
}

// ─── pre-flight ────────────────────────────────────────────────────────────

func basePreflight() PreflightInput {
	return PreflightInput{
		Mode: ModeAssisted, FromVersion: "v1.0.3",
		Release:       VerifiedRelease{Manifest: Manifest{Version: "v1.1.0", AgentMinVersion: "v1.0.2"}},
		DatabaseBytes: 100 << 20, FreeBytes: 40 << 30, MinFreeBytes: 1 << 30,
		Agents: []AgentVersion{{Name: "node-1", Version: "v1.0.3", Online: true}},
		Now:    time.Now(),
	}
}

// An unverifiable artifact is the ONE thing this feature exists to not install,
// so it is a refusal rather than a warning.
func TestAnUnverifiableReleaseRefusesRatherThanWarns(t *testing.T) {
	in := basePreflight()
	in.VerifyErr = ErrUnverifiable
	pf := RunPreflight(in)
	if pf.CanProceed {
		t.Fatal("the pre-flight allowed an unverifiable release")
	}
	if statusOf(pf, "signature") != CheckRefused {
		t.Errorf("signature check = %q, want refused", statusOf(pf, "signature"))
	}
}

// Both numbers, named. A disk that fills DURING an upgrade is how the reference
// platforms produce a panel that is neither the old version nor the new one.
func TestNotEnoughDiskRefusesAndNamesBothNumbers(t *testing.T) {
	in := basePreflight()
	in.DatabaseBytes = 20 << 30
	in.FreeBytes = 5 << 30
	pf := RunPreflight(in)
	if pf.CanProceed {
		t.Fatal("the pre-flight allowed an upgrade with no room for a snapshot")
	}
	text := textOf(pf, "disk")
	if !strings.Contains(text, "free") || !strings.Contains(text, "needs") {
		t.Errorf("the disk refusal does not name both numbers: %q", text)
	}
}

// Orphaning the fleet's management plane earns a typed confirm — and it stays
// ONE dialog, because confirmations never stack.
func TestAnAgentBelowTheFloorNeedsATypedConfirm(t *testing.T) {
	in := basePreflight()
	in.Agents = []AgentVersion{
		{Name: "node-1", Version: "v1.0.3", Online: true},
		{Name: "old-node", Version: "v0.9.0", Online: true},
	}
	pf := RunPreflight(in)
	if pf.CanProceed {
		t.Fatal("an upgrade that would orphan an agent was allowed without a confirm")
	}
	if !pf.NeedsTypedConfirm {
		t.Fatal("no typed confirm was demanded")
	}
	if len(pf.IncompatibleAgents) != 1 || pf.IncompatibleAgents[0] != "old-node" {
		t.Errorf("incompatible agents = %v, want [old-node] — the refusal has to NAME them", pf.IncompatibleAgents)
	}
}

// An agent that is merely OFFLINE neither blocks nor counts: a refusal an
// operator judges irrelevant is a refusal they stop reading.
func TestAnOfflineAgentNeitherBlocksNorCounts(t *testing.T) {
	in := basePreflight()
	in.Agents = []AgentVersion{
		{Name: "node-1", Version: "v1.0.3", Online: true},
		{Name: "gone", Version: "v0.1.0", Online: false},
	}
	pf := RunPreflight(in)
	if !pf.CanProceed {
		t.Fatalf("an offline agent blocked the upgrade: %+v", pf.Checks)
	}
	if len(pf.IncompatibleAgents) != 0 {
		t.Errorf("an offline agent was counted: %v", pf.IncompatibleAgents)
	}
}

// Running work is a COURTESY, not a correctness requirement — so it warns and
// never blocks.
func TestRunningWorkWarnsButNeverBlocks(t *testing.T) {
	in := basePreflight()
	in.RunningWork = 3
	pf := RunPreflight(in)
	if !pf.CanProceed {
		t.Fatal("a running deploy blocked the upgrade; builds run on agents and survive a plane restart")
	}
	if statusOf(pf, "quiescence") != CheckWarn {
		t.Errorf("quiescence = %q, want warn", statusOf(pf, "quiescence"))
	}
}

// A panel that pretends to a capability it does not have is worse than one that
// hands over cleanly.
func TestAContainerInstallCannotProceedAndSaysSo(t *testing.T) {
	in := basePreflight()
	in.Mode = ModeManual
	pf := RunPreflight(in)
	if pf.CanProceed {
		t.Fatal("a container install offered to upgrade itself")
	}
	if textOf(pf, "mode") == "" {
		t.Error("no line explains why, so the screen would just refuse")
	}
	// The other checks still ran: they are all reads, and the operator still
	// needs to know whether the release verifies before they run the commands.
	if statusOf(pf, "signature") != CheckOK {
		t.Error("the checks were skipped in manual mode; they are all reads and still apply")
	}
}

// A stale request must not run an upgrade an hour after somebody asked.
func TestAnExpiredRequestIsNotActedOn(t *testing.T) {
	now := time.Now()
	r := Request{ExpiresAt: now.Add(-time.Minute)}
	if !r.Expired(now) {
		t.Error("an expired request did not read as expired")
	}
	if (Request{ExpiresAt: now.Add(time.Minute)}).Expired(now) {
		t.Error("a live request read as expired")
	}
}

// Version ordering, including the case that decides whether a downgrade is
// even attempted.
func TestVersionOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.0.2", "v1.1.0", true},
		{"v1.1.0", "v1.0.2", false},
		{"v1.1.0", "v1.1.0", false},
		{"1.0.0", "v1.0.1", true},
		{"v1.2.0-rc1", "v1.2.1", true},
		// An unparseable version is never "older": a string nobody recognises
		// must not be the reason an upgrade is refused or a rollback allowed.
		{"garbage", "v1.0.0", false},
		{"v1.0.0", "garbage", false},
	}
	for _, c := range cases {
		if got := Older(c.a, c.b); got != c.want {
			t.Errorf("Older(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func statusOf(pf Preflight, key string) string {
	for _, c := range pf.Checks {
		if c.Key == key {
			return c.Status
		}
	}
	return ""
}

func textOf(pf Preflight, key string) string {
	for _, c := range pf.Checks {
		if c.Key == key {
			return c.Text + " " + c.Remedy
		}
	}
	return ""
}

// A version reaches VerifyRelease from an API query parameter and is
// interpolated into a release URL, so its SHAPE is a security boundary
// (code scanning go/request-forgery).
//
// Without the bound, "../../" aims the panel's own fetcher at an arbitrary path
// on the release host and "?" changes the request's query. Sanitising after the
// string has become a URL is the approach that keeps being wrong; refusing
// anything that is not a release tag is the one that holds.
func TestOnlyAReleaseTagCanReachAURL(t *testing.T) {
	valid := []string{"v1.2.3", "1.2.3", "v0.0.1", "v1.2.3-rc1", "v10.20.30-beta.2"}
	for _, v := range valid {
		if !ValidTag(v) {
			t.Errorf("ValidTag(%q) = false, want true", v)
		}
	}
	hostile := []string{
		"../../etc/passwd",
		"v1.0.0/../../../secrets",
		"v1.0.0?x=1",
		"v1.0.0#frag",
		"v1.0.0 v2.0.0",
		"https://evil.example/x",
		"v1.0.0%2f..%2f",
		"v1.0.0\nHost: evil",
		"",
		"latest",
		"v1.0.0" + strings.Repeat("0", 60),
	}
	for _, v := range hostile {
		if ValidTag(v) {
			t.Errorf("ValidTag(%q) = true — this string would be interpolated into a URL", v)
		}
	}
}

// The bound is enforced INSIDE VerifyRelease, not only at the handler, so no
// call path can skip it.
func TestVerifyReleaseRefusesANonTagBeforeItFetchesAnything(t *testing.T) {
	f, key := release(t, Manifest{Version: "v1.1.0"})
	withKey(t, key)
	// A fetcher that would answer anything at all: if the check happened after
	// the fetch, this test would pass for the wrong reason, so the assertion is
	// that the ERROR names the tag rather than a signature.
	_, err := VerifyRelease(context.Background(), f, baseURL, "../../v1.1.0")
	if !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("a traversal version was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "not a release tag") {
		t.Errorf("error = %q; the refusal should name the shape, which means it happened before any fetch", err)
	}
}

// Rotation ships two keys for one release: the panel must accept a manifest
// signed by EITHER, and a malformed second entry must not disable the first.
func TestEitherOfTwoBakedKeysVerifies(t *testing.T) {
	m := Manifest{Version: "v0.2.0", AgentMinVersion: "v0.1.0"}
	f, key := release(t, m)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	other := base64.StdEncoding.EncodeToString(otherPub)

	for _, list := range []string{other + "," + key, key + "," + other, key + ",not-a-key"} {
		withKey(t, list)
		if _, err := VerifyRelease(context.Background(), f, baseURL, "v0.2.0"); err != nil {
			t.Fatalf("key list %q: %v", list, err)
		}
	}
	withKey(t, other)
	if _, err := VerifyRelease(context.Background(), f, baseURL, "v0.2.0"); err == nil {
		t.Fatal("a manifest signed by a key not in the list verified")
	}
}

// `sha256sum ./*` names files "./name"; the release file is called "name".
func TestParseSumsDropsDotSlash(t *testing.T) {
	sums := ParseSums("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  ./cypherd-linux-amd64\n" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef *release.json\n")
	for _, name := range []string{"cypherd-linux-amd64", "release.json"} {
		if _, ok := sums[name]; !ok {
			t.Fatalf("%s missing from %v", name, sums)
		}
	}
}

// The default install runs Postgres in a container and installs no client on
// the host, so the dump tool is a command line, not a path.
func TestSnapshotToolOverrideIsACommandLine(t *testing.T) {
	t.Setenv("CYPHERD_SNAPSHOT_PGDUMP", "docker exec -i cypherpanel-postgres pg_dump")
	tool, args, err := ResolvePgDump("postgres://u:p@127.0.0.1:5432/db")
	if err != nil {
		t.Fatalf("ResolvePgDump: %v", err)
	}
	if tool != "docker" || strings.Join(args, " ") != "exec -i cypherpanel-postgres pg_dump" {
		t.Fatalf("tool=%q args=%v", tool, args)
	}
}

// A rollback is bounded to versions this host actually ran, and the host has to
// have WRITTEN that record: the first version scanned for files nothing wrote,
// so every rollback was refused.
func TestTheHelperRemembersWhatItRan(t *testing.T) {
	dir := Dir(t.TempDir())
	h := &Helper{o: HelperOptions{Dir: dir, FromVersion: "v0.1.0", Now: time.Now}}
	if h.ranBefore("v0.1.0") {
		t.Fatal("a version nothing recorded counted as run")
	}
	h.remember("v0.1.0")
	if !h.ranBefore("v0.1.0") {
		t.Fatal("the recorded version does not count as run")
	}
	h.remember("main") // not a tag; must not be recorded
	if h.ranBefore("main") {
		t.Fatal("a branch name was recorded as a version")
	}
}

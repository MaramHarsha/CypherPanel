package localjoin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	env    []string
	script string
	err    error
	out    string
	calls  int
}

func (f *fakeRunner) Run(_ context.Context, script string, env []string) ([]byte, error) {
	f.calls++
	f.script, f.env = script, env
	return []byte(f.out), f.err
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newRequest(t *testing.T, dir Dir) Request {
	t.Helper()
	r := NewRequest(Request{
		ID: "ljn_1", Token: "jt_x.y", EnrollAddr: "1.2.3.4:8443",
		PlaneHTTP: "http://1.2.3.4:8080", CAFingerprint: "abc", ServerID: "srv_1",
	}, time.Now().UTC(), "non_1")
	if err := dir.WriteRequest(r); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	return r
}

// The helper runs THE SAME installer the pasted command runs, with the same
// variables. Keeping them identical is the whole point: the two paths differ in
// who types them and in nothing else (local-server.md §3).
func TestTheHelperRunsTheSameInstallerWithTheSameVariables(t *testing.T) {
	dir := Dir(t.TempDir())
	newRequest(t, dir)
	run := &fakeRunner{}

	if err := Run(context.Background(), Options{
		Dir: dir, Script: "#!/bin/sh\necho installer", Runner: run, Log: quiet(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("installer ran %d times", run.calls)
	}
	if !strings.Contains(run.script, "echo installer") {
		t.Fatalf("a different script was run: %q", run.script)
	}
	joined := strings.Join(run.env, " ")
	for _, want := range []string{
		"CYPHER_PLANE=1.2.3.4:8443",
		"CYPHER_PLANE_HTTP=http://1.2.3.4:8080",
		"CYPHER_TOKEN=jt_x.y",
		"CYPHER_CA_FINGERPRINT=abc",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env is missing %s: %v", want, run.env)
		}
	}
	st, ok, err := dir.ReadStatus()
	if err != nil || !ok {
		t.Fatalf("no status written: %v", err)
	}
	if st.Phase != PhaseSucceeded || st.ServerID != "srv_1" {
		t.Fatalf("status = %+v", st)
	}
}

// The request is read AND DELETED before any work happens. A helper that
// crashed mid-install must not find the same request waiting and enroll a
// second agent on the same host.
func TestTheRequestIsConsumedBeforeTheInstallerRuns(t *testing.T) {
	dir := Dir(t.TempDir())
	newRequest(t, dir)
	run := &fakeRunner{}

	if err := Run(context.Background(), Options{Dir: dir, Runner: run, Log: quiet()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(string(dir), RequestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the request survived the run")
	}
	// A second firing of the path unit is a no-op rather than a second install.
	if err := Run(context.Background(), Options{Dir: dir, Runner: run, Log: quiet()}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("the installer ran %d times for one request", run.calls)
	}
}

// A stale request left by a crash must not enroll an agent an hour after the
// click. The path unit makes exactly that possible, so the expiry is checked
// here rather than trusted to the token's own lifetime.
func TestAnExpiredRequestIsRefusedWithoutRunningAnything(t *testing.T) {
	dir := Dir(t.TempDir())
	stale := NewRequest(Request{
		ID: "ljn_old", Token: "jt_x.y", EnrollAddr: "a:1", PlaneHTTP: "http://a",
		CAFingerprint: "abc", ServerID: "srv_1",
	}, time.Now().UTC().Add(-time.Hour), "non")
	if err := dir.WriteRequest(stale); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := Run(context.Background(), Options{Dir: dir, Runner: run, Log: quiet()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.calls != 0 {
		t.Fatal("an expired request ran the installer")
	}
	st, _, _ := dir.ReadStatus()
	if st.Phase != PhaseFailed || !strings.Contains(st.Detail, "expired") {
		t.Fatalf("status = %+v", st)
	}
}

// A failed install reports the installer's own last sentence — its `fail`
// helper prints the reason last, and an exit status alone is not actionable.
// The environment must never reach the status: it carries the join token.
func TestAFailedInstallReportsTheInstallersReasonAndNeverTheToken(t *testing.T) {
	dir := Dir(t.TempDir())
	newRequest(t, dir)
	run := &fakeRunner{
		err: errors.New("exit status 1"),
		out: "\x1b[36m=>\x1b[0m downloading cypher-agent\n\x1b[31merror:\x1b[0m could not download the agent binary\n",
	}
	if err := Run(context.Background(), Options{Dir: dir, Runner: run, Log: quiet()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st, _, _ := dir.ReadStatus()
	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q", st.Phase)
	}
	if !strings.Contains(st.Detail, "could not download the agent binary") {
		t.Fatalf("detail = %q, want the installer's own reason", st.Detail)
	}
	if strings.Contains(st.Detail, "\x1b") {
		t.Fatalf("terminal colour codes reached the dialog: %q", st.Detail)
	}
	raw, err := os.ReadFile(filepath.Join(string(dir), StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "jt_x.y") {
		t.Fatal("the join token was written into the status file")
	}
}

// The request is 0640: readable by the helper's group, by nobody else. It
// carries a live join token, so the mode is the control.
func TestTheRequestIsNotWorldReadable(t *testing.T) {
	dir := Dir(t.TempDir())
	newRequest(t, dir)
	info, err := os.Stat(filepath.Join(string(dir), RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("request mode = %v, want 0640", perm)
	}
}

// The request must never gain a field naming another machine — that absence is
// what stops this from becoming a remote-execution primitive (§2). A reflection
// test is the only thing that survives a later edit.
func TestNoFieldOnARequestCanNameAnotherMachine(t *testing.T) {
	body, err := json.Marshal(Request{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		switch name {
		case "host", "hostname", "target", "target_host", "address", "addr", "ssh_host", "remote":
			t.Fatalf("Request carries %q — this helper runs on the machine it is already on, "+
				"and a field naming another turns it into remote execution (local-server.md §2)", name)
		}
	}
}

// The identity file is the agent's own record of which Server it IS, which is
// why the panel reads it rather than guessing from hostnames.
func TestTheLocalServerIsReadFromTheAgentsOwnIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	if _, ok := LocalServerID(path); ok {
		t.Fatal("reported a server before the agent enrolled")
	}
	if err := os.WriteFile(path, []byte(`{"server_id":"srv_abc","plane_addr":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, ok := LocalServerID(path)
	if !ok || id != "srv_abc" {
		t.Fatalf("LocalServerID = %q, %v", id, ok)
	}
}

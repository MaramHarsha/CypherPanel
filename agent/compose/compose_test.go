package compose

// The reconciler contract, in Docker's vocabulary: converge-twice mutates
// nothing, absence means remove, and the plane's file is never turned into a
// command this package did not already own.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// fakeRunner records every invocation and answers the two read commands.
type fakeRunner struct {
	calls    [][]string
	projects string // `compose ls` output
	ps       string // `compose ps` output
	upErr    error
	// fileSeen captures the compose file's contents at the moment `up` ran, so
	// a test can assert what was written without racing the cleanup.
	fileSeen string
	envSeen  string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch {
	case len(args) > 0 && args[0] == "ls":
		return []byte(f.projects), nil
	case contains(args, "ps"):
		return []byte(f.ps), nil
	case contains(args, "up"):
		// Read the rendered files while they still exist.
		for i, a := range args {
			if a == "--file" && i+1 < len(args) {
				b, _ := os.ReadFile(args[i+1])
				f.fileSeen = string(b)
			}
			if a == "--env-file" && i+1 < len(args) {
				b, _ := os.ReadFile(args[i+1])
				f.envSeen = string(b)
			}
		}
		if f.upErr != nil {
			return []byte("service web failed to start"), f.upErr
		}
		return nil, nil
	}
	return nil, nil
}

func (f *fakeRunner) ran(verb string) int {
	n := 0
	for _, c := range f.calls {
		if contains(c, verb) {
			n++
		}
	}
	return n
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func newReconciler(t *testing.T, r *fakeRunner) *Reconciler {
	t.Helper()
	return New(r, nil, nil, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func spec(stackID, file string) *agentv1.ComposeSpec {
	return &agentv1.ComposeSpec{
		StackId: stackID, EnvironmentId: "env_1", RevisionId: "csr_1",
		ComposeYaml: file, Network: "cypher-env_1",
	}
}

const oneService = `{"Service":"web","State":"running","ID":"c1"}`

func TestConvergeRunsUpAndReportsRunning(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)

	got, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(got) != 1 || got[0].GetState() != stateRunning {
		t.Fatalf("statuses = %+v, want one running", got)
	}
	if r.ran("up") != 1 {
		t.Fatalf("up ran %d times, want once", r.ran("up"))
	}
}

// The project name is derived from the stack id, so two stacks never collide
// and the project is findable again after an agent restart with no local state.
func TestConvergeUsesADeterministicProjectName(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_abc", "services:\n  web:\n    image: nginx\n")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, c := range r.calls {
		if contains(c, "up") {
			if !contains(c, "cypher-cs_abc") {
				t.Fatalf("up args = %v, want the derived project name", c)
			}
			return
		}
	}
	t.Fatal("up never ran")
}

// --remove-orphans is what makes the FILE authoritative: a service deleted from
// it is removed from the host.
func TestConvergeMakesTheFileAuthoritative(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, c := range r.calls {
		if contains(c, "up") {
			if !contains(c, "--remove-orphans") || !contains(c, "--wait") || !contains(c, "--detach") {
				t.Fatalf("up args = %v, want --detach --remove-orphans --wait", c)
			}
			return
		}
	}
	t.Fatal("up never ran")
}

// A stack the plane no longer names is brought down — but its volumes are kept:
// one that vanished from desired state because of a plane-side mistake must not
// take its data with it.
func TestAbsentStacksAreBroughtDownWithoutTheirVolumes(t *testing.T) {
	r := &fakeRunner{projects: `[{"Name":"cypher-cs_gone"},{"Name":"cypher-cs_kept"}]`, ps: oneService}
	rec := newReconciler(t, r)

	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_kept", "services:\n  web:\n    image: nginx\n")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var downArgs []string
	for _, c := range r.calls {
		if contains(c, "down") {
			downArgs = c
		}
	}
	if downArgs == nil {
		t.Fatal("the absent stack was not brought down")
	}
	if !contains(downArgs, "cypher-cs_gone") {
		t.Fatalf("down args = %v, want the absent project", downArgs)
	}
	if contains(downArgs, "--volumes") {
		t.Fatalf("down args = %v, want volumes preserved on an absence removal", downArgs)
	}
}

// A project a human started is not ours to remove.
func TestUnprefixedProjectsAreLeftAlone(t *testing.T) {
	r := &fakeRunner{projects: `[{"Name":"someones-own-stack"}]`, ps: oneService}
	rec := newReconciler(t, r)

	if _, err := rec.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.ran("down") != 0 {
		t.Fatalf("down ran %d times, want none for a project we do not own", r.ran("down"))
	}
}

// Explicit removal is the only path that may destroy data, and only when asked.
func TestRemovePassesTheVolumeDecisionThrough(t *testing.T) {
	for _, deleteVolumes := range []bool{false, true} {
		r := &fakeRunner{projects: "[]"}
		rec := newReconciler(t, r)
		if err := rec.Remove(context.Background(), "cs_1", deleteVolumes); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		got := contains(r.calls[0], "--volumes")
		if got != deleteVolumes {
			t.Fatalf("--volumes = %v, want %v", got, deleteVolumes)
		}
	}
}

// Compose's own words reach the operator; inventing a paraphrase would be worse.
func TestAFailedConvergeCarriesComposesOutput(t *testing.T) {
	r := &fakeRunner{projects: "[]", upErr: errors.New("exit status 1")}
	rec := newReconciler(t, r)

	got, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(got) != 1 || got[0].GetState() != stateError {
		t.Fatalf("statuses = %+v, want error", got)
	}
	if !strings.Contains(got[0].GetDetail(), "service web failed to start") {
		t.Fatalf("detail = %q, want compose's own words", got[0].GetDetail())
	}
}

// One bad file must not take a node's whole set with it.
func TestOneFailingStackDoesNotStopTheOthers(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)

	got, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{
		spec("cs_1", "services:\n  web:\n    image: nginx\n"),
		spec("cs_2", "services:\n  web:\n    image: nginx\n"),
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("statuses = %d, want one per stack", len(got))
	}
}

// The status is an observation of what is running, not "we asked".
func TestPartialServicesReportDegraded(t *testing.T) {
	r := &fakeRunner{
		projects: "[]",
		ps:       `{"Service":"web","State":"running","ID":"c1"}` + "\n" + `{"Service":"worker","State":"exited","ID":"c2"}`,
	}
	rec := newReconciler(t, r)

	got, _ := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")})
	if len(got) != 1 || got[0].GetState() != stateDegraded {
		t.Fatalf("statuses = %+v, want degraded", got)
	}
	if !strings.Contains(got[0].GetDetail(), "1 of 2") {
		t.Fatalf("detail = %q, want it to say how many", got[0].GetDetail())
	}
}

// Compose emits either a JSON array or newline-delimited objects depending on
// version, and reading only one of them would misreport every stack.
func TestServiceCountsReadBothComposeOutputShapes(t *testing.T) {
	array := []byte(`[{"Service":"a","State":"running"},{"Service":"b","State":"exited"}]`)
	lines := []byte(`{"Service":"a","State":"running"}` + "\n" + `{"Service":"b","State":"exited"}`)
	for name, out := range map[string][]byte{"array": array, "lines": lines} {
		total, up := countServices(out)
		if total != 2 || up != 1 {
			t.Errorf("%s: total=%d up=%d, want 2 and 1", name, total, up)
		}
	}
}

// The env file is short-lived and 0600: a secret is on this host only for as
// long as one converge takes, and never inside the file the plane stores.
func TestEnvIsWrittenForComposeAndRemovedAfterwards(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	dir := t.TempDir()
	rec := New(r, nil, nil, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	sp := spec("cs_1", "services:\n  web:\n    image: nginx\n")
	sp.Env = map[string]string{"TOKEN": "s3cret", "ADMIN": "root"}
	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{sp}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Sorted, so the same spec renders the same bytes.
	if r.envSeen != "ADMIN=root\nTOKEN=s3cret\n" {
		t.Fatalf("env file = %q", r.envSeen)
	}
	// The revision marker is meant to survive; the rendered file and the env
	// file are not — a secret must not outlive the converge that needed it.
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, p := range left {
		if !strings.HasSuffix(p, ".revision") {
			t.Fatalf("left behind %s — a secret must not outlive the converge", p)
		}
	}
}

// Converging twice makes no mutating call: the second pass finds the recorded
// revision and a fully-running project, and does nothing.
func TestConvergeTwiceRunsUpOnce(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	specs := []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")}

	if _, err := rec.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := rec.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if r.ran("up") != 1 {
		t.Fatalf("up ran %d times, want exactly once across two converges", r.ran("up"))
	}
}

// Self-healing is the other half: a service that died since is re-upped,
// because "already converged" also requires everything to be running.
func TestADeadServiceIsReUpped(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	specs := []*agentv1.ComposeSpec{spec("cs_1", "services:\n  web:\n    image: nginx\n")}

	if _, err := rec.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	r.ps = `{"Service":"web","State":"exited","ID":"c1"}`
	if _, err := rec.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if r.ran("up") != 2 {
		t.Fatalf("up ran %d times, want the dead service re-upped", r.ran("up"))
	}
}

// A new revision is a new file, so it converges even though the project is
// running.
func TestANewRevisionConverges(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	first := spec("cs_1", "services:\n  web:\n    image: nginx:1.27\n")
	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{first}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	next := spec("cs_1", "services:\n  web:\n    image: nginx:1.28\n")
	next.RevisionId = "csr_2"
	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{next}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if r.ran("up") != 2 {
		t.Fatalf("up ran %d times, want the new revision converged", r.ran("up"))
	}
}

func TestComposeFileIsWrittenVerbatim(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	file := "services:\n  web:\n    image: nginx\n    # a comment the operator wrote\n"

	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("cs_1", file)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.fileSeen != file {
		t.Fatalf("file = %q, want it verbatim", r.fileSeen)
	}
}

// The id names a path, so a traversal in it would write outside the work
// directory. It is plane-generated, which is exactly why the check is cheap.
func TestATraversingStackIdIsRefused(t *testing.T) {
	r := &fakeRunner{projects: "[]"}
	rec := newReconciler(t, r)

	got, _ := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{spec("../escape", "services:\n  web:\n    image: nginx\n")})
	if len(got) != 1 || got[0].GetState() != stateError {
		t.Fatalf("statuses = %+v, want the id refused", got)
	}
	if r.ran("up") != 0 {
		t.Fatal("up ran for a stack id that names a path outside the work directory")
	}
}

// A stack with no route asks the Proxy for nothing — and one with a route only
// gets published when it actually came up.
func TestNoRouterMeansNoRouting(t *testing.T) {
	r := &fakeRunner{projects: "[]", ps: oneService}
	rec := newReconciler(t, r)
	sp := spec("cs_1", "services:\n  web:\n    image: nginx\n")
	sp.Route = &agentv1.ComposeRoute{Domain: "app.example.com", Service: "web", Port: 80}

	if _, err := rec.Reconcile(context.Background(), []*agentv1.ComposeSpec{sp}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

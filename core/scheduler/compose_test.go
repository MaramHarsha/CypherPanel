package scheduler

// The plane's half of a Compose Stack (compose-stacks.md §4): what goes on the
// wire, and what comes back.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

const stackFile = "services:\n  web:\n    image: nginx\n"

func TestComposeSpecCarriesTheFileVerbatim(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	stack := fs.addComposeStack("cs_1", "srv_1", stackFile)
	s := newScheduler(fs, fb)

	spec, err := s.composeSpec(context.Background(), stack)
	if err != nil {
		t.Fatalf("composeSpec: %v", err)
	}
	if spec.GetComposeYaml() != stackFile {
		t.Fatalf("file = %q, want it verbatim", spec.GetComposeYaml())
	}
	if spec.GetNetwork() != "cypher-env_1" {
		t.Fatalf("network = %q, want the environment network", spec.GetNetwork())
	}
}

// The env is unsealed at publish time, exactly as an application's is: the
// plaintext exists only inside the mTLS-carried spec.
func TestComposeSpecUnsealsTheEnv(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	stack := fs.addComposeStack("cs_1", "srv_1", stackFile)
	fs.composeEnv["cs_1"] = []domain.ComposeEnvVar{
		{Key: "TOKEN", ValueCT: []byte("sealed:s3cret"), ValueNonce: []byte("n")},
	}
	s := newScheduler(fs, fb)

	spec, err := s.composeSpec(context.Background(), stack)
	if err != nil {
		t.Fatalf("composeSpec: %v", err)
	}
	if spec.GetEnv()["TOKEN"] != "s3cret" {
		t.Fatalf("env = %v, want it unsealed", spec.GetEnv())
	}
}

// A value that will not open is a sealing-key problem, not this stack's data,
// so it fails loudly rather than shipping a file with a hole in it.
func TestComposeSpecFailsOnAnUnsealableValue(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	stack := fs.addComposeStack("cs_1", "srv_1", stackFile)
	fs.composeEnv["cs_1"] = []domain.ComposeEnvVar{{Key: "TOKEN", ValueCT: []byte("x"), ValueNonce: []byte("n")}}
	s := New(fs, fb, failingOpener{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := s.composeSpec(context.Background(), stack); err == nil {
		t.Fatal("composeSpec succeeded with an unsealable variable")
	}
}

func TestComposeSpecCarriesTheRoute(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	stack := fs.addComposeStack("cs_1", "srv_1", stackFile)
	stack.Route = domain.ComposeRoute{Domain: "app.example.com", Service: "web", Port: 80, HTTPS: true}
	fs.composeStacks["cs_1"] = stack
	s := newScheduler(fs, fb)

	spec, err := s.composeSpec(context.Background(), stack)
	if err != nil {
		t.Fatalf("composeSpec: %v", err)
	}
	if spec.GetRoute().GetService() != "web" || spec.GetRoute().GetPort() != 80 {
		t.Fatalf("route = %+v", spec.GetRoute())
	}
}

func TestAStackWithNoRouteCarriesNone(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	stack := fs.addComposeStack("cs_1", "srv_1", stackFile)
	s := newScheduler(fs, fb)

	spec, _ := s.composeSpec(context.Background(), stack)
	if spec.GetRoute() != nil {
		t.Fatalf("route = %+v, want none", spec.GetRoute())
	}
}

func TestConvergeStackPublishesWork(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addComposeStack("cs_1", "srv_1", stackFile)
	s := newScheduler(fs, fb)

	before := fb.count()
	if err := s.ConvergeStack(context.Background(), "cs_1"); err != nil {
		t.Fatalf("ConvergeStack: %v", err)
	}
	if fb.count() <= before {
		t.Fatal("no converge work was published")
	}
	// The plane says "converging" only after the work is out, so a failed
	// publish never leaves a stack claiming to be doing something it is not.
	if got := fs.composeStacks["cs_1"].Status; got != domain.AppDeploying {
		t.Fatalf("status = %q, want deploying", got)
	}
}

// A stack that has never been deployed has nothing to converge toward.
func TestConvergeStackIsANoOpBeforeTheFirstDeploy(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.composeStacks["cs_bare"] = domain.ComposeStack{ID: "cs_bare", EnvironmentID: "env_1", ServerID: "srv_1"}
	s := newScheduler(fs, fb)

	before := fb.count()
	if err := s.ConvergeStack(context.Background(), "cs_bare"); err != nil {
		t.Fatalf("ConvergeStack: %v", err)
	}
	if fb.count() != before {
		t.Fatal("work was published for a stack with no desired revision")
	}
}

// Desired state is the complete set for one server: absence is what removes a
// stack, so a stack with no revision is omitted rather than shipped empty.
func TestDesiredStateCarriesDeployedStacksOnly(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addComposeStack("cs_deployed", "srv_1", stackFile)
	fs.composeStacks["cs_never"] = domain.ComposeStack{ID: "cs_never", EnvironmentID: "env_1", ServerID: "srv_1"}
	fs.composeStacks["cs_elsewhere"] = func() domain.ComposeStack {
		st := fs.addComposeStack("cs_elsewhere", "srv_2", stackFile)
		return st
	}()
	s := newScheduler(fs, fb)

	specs, err := s.composeSpecsFor(context.Background(), "srv_1")
	if err != nil {
		t.Fatalf("composeSpecsFor: %v", err)
	}
	if len(specs) != 1 || specs[0].GetStackId() != "cs_deployed" {
		t.Fatalf("specs = %+v, want only this server's deployed stack", specs)
	}
}

// ─── observation (ADR-005) ──────────────────────────────────────────────────

func TestComposeStatusIsRecorded(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addComposeStack("cs_1", "srv_1", stackFile)
	s := newScheduler(fs, fb)

	s.HandleComposeStatus(context.Background(), "srv_1", &agentv1.ComposeStatus{
		StackId: "cs_1", RevisionId: "csr_cs_1", State: domain.AppRunning,
	})
	got := fs.composeStacks["cs_1"]
	if got.Status != domain.AppRunning || got.ObservedRevisionID != "csr_cs_1" {
		t.Fatalf("stack = %+v, want the observation recorded", got)
	}
}

// Only the server the stack runs on may report it — the same reporter check
// application and database status already make.
func TestComposeStatusFromTheWrongServerIsIgnored(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addComposeStack("cs_1", "srv_1", stackFile)
	s := newScheduler(fs, fb)

	s.HandleComposeStatus(context.Background(), "srv_evil", &agentv1.ComposeStatus{
		StackId: "cs_1", State: domain.AppError, Detail: "not mine to say",
	})
	if got := fs.composeStacks["cs_1"].Status; got == domain.AppError {
		t.Fatal("a status from another server was recorded")
	}
}

func TestComposeStatusForADeletedStackIsDropped(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	s := newScheduler(fs, fb)
	// Must not panic or error: the observation is simply moot.
	s.HandleComposeStatus(context.Background(), "srv_1", &agentv1.ComposeStatus{StackId: "cs_gone", State: domain.AppRunning})
}

func TestRemoveStackPublishesAbsence(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	s := newScheduler(fs, fb)

	before := fb.count()
	if err := s.RemoveStack(context.Background(), "srv_1", "cs_1", true); err != nil {
		t.Fatalf("RemoveStack: %v", err)
	}
	if fb.count() <= before {
		t.Fatal("no removal work was published")
	}
}

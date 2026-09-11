package scheduler

// Deploy protection at the pipeline level (deploy-protection.md §3, §4;
// acceptance 1, 2, 3, 5). The gate is a stub here on purpose: what these tests
// prove is the SCHEDULER's half — that a parked deploy publishes nothing,
// survives a restart, re-enters the ordinary queue when approved, and ends
// failed with the rejecter's sentence when refused. The window arithmetic
// itself is core/protection's to test.

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// stubGate answers with a fixed admission and records what was parked.
type stubGate struct {
	admission domain.DeployAdmission
	admitErr  error
	parkErr   error
	parked    []parkedCall
}

type parkedCall struct {
	deploymentID  string
	environmentID string
	requestedBy   string
	requiredRole  string
}

func (g *stubGate) Admit(context.Context, string) (domain.DeployAdmission, error) {
	return g.admission, g.admitErr
}

func (g *stubGate) Park(_ context.Context, dep domain.Deployment, envID, requestedBy, requiredRole string) error {
	g.parked = append(g.parked, parkedCall{dep.ID, envID, requestedBy, requiredRole})
	return g.parkErr
}

func approvalGate() *stubGate {
	return &stubGate{admission: domain.DeployAdmission{NeedsApproval: true, RequiredRole: domain.RoleOwner}}
}

// Acceptance 1: a deploy into a protected environment comes back
// awaiting_approval, publishes NO work item, and leaves the application's own
// status alone.
func TestDeployParksAndPublishesNothing(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	gate := approvalGate()
	s.SetGate(gate)

	dep, err := s.DeployAs(context.Background(), "app_1", "manual", "", "usr_alex")
	if err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	if dep.Status != domain.DeployAwaitingApproval {
		t.Fatalf("status = %s, want awaiting_approval", dep.Status)
	}
	if dep.Detail != "waiting for approval from an owner" {
		t.Fatalf("detail = %q", dep.Detail)
	}
	if fb.count() != 0 {
		t.Fatalf("a parked deploy published %d work items; the agent must observe nothing", fb.count())
	}
	// start() is what sets an app to deploying, and a parked deploy never
	// reaches it: the app keeps reporting what it is actually doing.
	if got := fs.apps["app_1"].Status; got != domain.AppStopped {
		t.Fatalf("app status = %s, want it untouched (stopped)", got)
	}
	if len(gate.parked) != 1 {
		t.Fatalf("gate parked %d deployments, want 1", len(gate.parked))
	}
	got := gate.parked[0]
	if got.deploymentID != dep.ID || got.environmentID != "env_1" ||
		got.requestedBy != "usr_alex" || got.requiredRole != domain.RoleOwner {
		t.Fatalf("park call = %+v", got)
	}
	// A revision was still created — the deploy exists, it is just waiting.
	if len(fs.revisions) != 1 {
		t.Fatalf("revisions = %d, want 1", len(fs.revisions))
	}
}

// Acceptance 2: approving runs the ordinary pipeline, unchanged.
func TestApproveDeploymentRunsTheOrdinaryPipeline(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	s.SetGate(approvalGate())

	dep, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex")
	if err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	started, err := s.ApproveDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatalf("ApproveDeployment: %v", err)
	}
	if started.Status != domain.DeployBuilding {
		t.Fatalf("approved status = %s, want building", started.Status)
	}
	if fb.count() != 1 {
		t.Fatalf("work items after approval = %d, want 1", fb.count())
	}
	if fs.apps["app_1"].Status != domain.AppDeploying {
		t.Fatalf("app status = %s, want deploying", fs.apps["app_1"].Status)
	}
	// Approving twice is refused rather than publishing a second build.
	if _, err := s.ApproveDeployment(ctx, dep.ID); !errors.Is(err, ErrNotParked) {
		t.Fatalf("second approve = %v, want ErrNotParked", err)
	}
	if fb.count() != 1 {
		t.Fatalf("a second approve published another work item (%d)", fb.count())
	}
}

// Acceptance 3: rejecting ends the deployment failed with the given detail, and
// no container is ever touched.
func TestRejectDeploymentEndsItFailed(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	s.SetGate(approvalGate())

	dep, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex")
	if err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	detail := "rejected by sam@acme.com: shipping Monday"
	failed, err := s.RejectDeployment(ctx, dep.ID, detail)
	if err != nil {
		t.Fatalf("RejectDeployment: %v", err)
	}
	if failed.Status != domain.DeployFailed || failed.Detail != detail {
		t.Fatalf("rejected = %+v", failed)
	}
	if fb.count() != 0 {
		t.Fatalf("rejecting published %d work items", fb.count())
	}
	// The application never entered 'deploying', so nothing has to be taken
	// back and its status still says what it is doing.
	if got := fs.apps["app_1"].Status; got != domain.AppStopped {
		t.Fatalf("app status = %s, want it untouched", got)
	}
	if _, err := s.RejectDeployment(ctx, dep.ID, detail); !errors.Is(err, ErrNotParked) {
		t.Fatalf("second reject = %v, want ErrNotParked", err)
	}
}

// Acceptance 5: a plane restart mid-approval is indistinguishable from no
// restart — Recover leaves the parked deployment alone, and approving it still
// works afterwards.
func TestRecoverLeavesParkedDeploymentsAlone(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.servers = []domain.Server{{ID: "srv_1", Status: domain.StatusRunning}}
	s := newScheduler(fs, fb)
	s.SetGate(approvalGate())

	dep, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex")
	if err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	before := fb.count()

	// A fresh scheduler over the same state — the restart.
	s2 := newScheduler(fs, fb)
	s2.SetGate(approvalGate())
	if err := s2.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if fb.count() != before {
		t.Fatalf("Recover published %d new work items for a parked deploy", fb.count()-before)
	}
	after, err := fs.GetDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if after.Status != domain.DeployAwaitingApproval {
		t.Fatalf("status after Recover = %s, want awaiting_approval", after.Status)
	}
	if started, err := s2.ApproveDeployment(ctx, dep.ID); err != nil || started.Status != domain.DeployBuilding {
		t.Fatalf("approving after a restart = %+v, %v", started, err)
	}
}

// A frozen environment refuses BEFORE anything is written: no Revision, no
// Deployment, nothing published (acceptance 8's "no rows left behind").
func TestDeployRefusedByFreezeWritesNothing(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	s.SetGate(&stubGate{admission: domain.DeployAdmission{
		Frozen:       true,
		FreezeDetail: "production is frozen until Mon 08:00 Europe/Berlin",
	}})

	_, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex")
	if !errors.Is(err, ErrFrozen) {
		t.Fatalf("DeployAs = %v, want ErrFrozen", err)
	}
	var frozen *FrozenError
	if !errors.As(err, &frozen) || frozen.Detail != "production is frozen until Mon 08:00 Europe/Berlin" {
		t.Fatalf("frozen error = %v", err)
	}
	if len(fs.revisions) != 0 || len(fs.deployments) != 0 || fb.count() != 0 {
		t.Fatalf("a refused deploy left %d revisions, %d deployments and %d work items",
			len(fs.revisions), len(fs.deployments), fb.count())
	}
}

// A rollback changes what is serving, so it passes the same gate — refused
// inside a freeze, parked when approval is required.
func TestRollbackPassesTheGate(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	// A shipped deployment to roll back to.
	first, err := s.Deploy(ctx, "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if _, err := fs.SetRevisionImage(ctx, first.RevisionID, "cypher/app_1:"+first.RevisionID); err != nil {
		t.Fatalf("SetRevisionImage: %v", err)
	}
	if _, err := fs.UpdateDeploymentStatus(ctx, first.ID, domain.DeploySucceeded, ""); err != nil {
		t.Fatalf("UpdateDeploymentStatus: %v", err)
	}
	baseline := fb.count()

	s.SetGate(&stubGate{admission: domain.DeployAdmission{Frozen: true, FreezeDetail: "production is frozen until Mon 08:00 Europe/Berlin"}})
	if _, err := s.RollbackAs(ctx, first.ID, "usr_alex"); !errors.Is(err, ErrFrozen) {
		t.Fatalf("frozen rollback = %v, want ErrFrozen", err)
	}
	if fb.count() != baseline {
		t.Fatalf("a refused rollback published work")
	}

	gate := approvalGate()
	s.SetGate(gate)
	parked, err := s.RollbackAs(ctx, first.ID, "usr_alex")
	if err != nil {
		t.Fatalf("RollbackAs: %v", err)
	}
	if parked.Status != domain.DeployAwaitingApproval {
		t.Fatalf("rollback status = %s, want awaiting_approval", parked.Status)
	}
	if fb.count() != baseline {
		t.Fatalf("a parked rollback published %d work items", fb.count()-baseline)
	}
	if len(gate.parked) != 1 || gate.parked[0].requestedBy != "usr_alex" {
		t.Fatalf("park call = %+v", gate.parked)
	}
}

// Fail closed: a gate that cannot answer refuses the deploy, and writes
// nothing.
func TestDeployFailsClosedWhenTheGateErrors(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	s.SetGate(&stubGate{admitErr: errors.New("database is unreachable")})

	if _, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex"); err == nil {
		t.Fatal("an unanswerable gate admitted the deploy")
	}
	if len(fs.deployments) != 0 || fb.count() != 0 {
		t.Fatal("a fail-closed refusal still wrote something")
	}
}

// A deployment parked with no approval row could never be decided, so a Park
// that fails ends it rather than stranding it.
func TestParkFailureEndsTheDeployment(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	gate := approvalGate()
	gate.parkErr = errors.New("approval row could not be written")
	s.SetGate(gate)

	dep, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex")
	if err == nil {
		t.Fatal("a failed park was reported as a successful deploy")
	}
	if dep.Status != domain.DeployFailed {
		t.Fatalf("status = %s, want failed — a stranded parked deploy can never be decided", dep.Status)
	}
	if fb.count() != 0 {
		t.Fatalf("the failed park published %d work items", fb.count())
	}
}

// A parked deploy holds no pipeline slot: a later deploy on the same
// application starts immediately instead of queueing behind it.
func TestParkedDeploymentDoesNotBlockTheQueue(t *testing.T) {
	ctx := context.Background()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	s.SetGate(approvalGate())

	if _, err := s.DeployAs(ctx, "app_1", "manual", "", "usr_alex"); err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	// Protection turned off (or the environment changed): the next deploy must
	// not sit behind the parked one.
	s.SetGate(&stubGate{})
	next, err := s.Deploy(ctx, "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if next.Status != domain.DeployBuilding {
		t.Fatalf("later deploy status = %s, want building — a parked deploy must hold no slot", next.Status)
	}
}

// With no gate wired, nothing is protected: the panel behaves exactly as it did
// before this feature existed (acceptance 10).
func TestNoGateMeansNoProtection(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, err := s.DeployAs(context.Background(), "app_1", "manual", "", "usr_alex")
	if err != nil {
		t.Fatalf("DeployAs: %v", err)
	}
	if dep.Status != domain.DeployBuilding || fb.count() != 1 {
		t.Fatalf("ungated deploy = %+v with %d work items", dep, fb.count())
	}
}

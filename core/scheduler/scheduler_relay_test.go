package scheduler

// Multi-server pipeline tests (builder-role-and-relay.md §2, §5–6): build
// routing by role, the distribute stage's transitions, event authorization
// against the persisted builder, and crash recovery of the relay stage.

import (
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

func (f *fakeBus) subjectsPublished() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.work))
	for _, p := range f.work {
		out = append(out, p.subject)
	}
	return out
}

func (f *fakeBus) has(subject string) bool {
	for _, s := range f.subjectsPublished() {
		if s == subject {
			return true
		}
	}
	return false
}

// multiServerDeploy stands up an app on a worker-role target with a running
// builder-role server and starts a deployment.
func multiServerDeploy(t *testing.T) (*fakeStore, *fakeBus, *Scheduler, domain.Deployment) {
	t.Helper()
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_w")
	fs.servers = []domain.Server{
		{ID: "srv_w", Role: domain.RoleWorker, Status: domain.StatusRunning},
		{ID: "srv_b", Role: domain.RoleBuilder, Status: domain.StatusRunning},
	}
	s := newScheduler(fs, fb)
	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return fs, fb, s, dep
}

func TestDeployRoutesBuildToBuilderRole(t *testing.T) {
	fs, fb, _, dep := multiServerDeploy(t)

	p, ok := fb.last()
	if !ok || p.subject != subjects.Build("srv_b") {
		t.Fatalf("build published on %+v, want %s", p, subjects.Build("srv_b"))
	}
	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.BuilderServerID == nil || *got.BuilderServerID != "srv_b" {
		t.Fatalf("builder_server_id = %v, want srv_b", got.BuilderServerID)
	}
}

func TestDeployPrefersLocalBuildOnAllRole(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.servers = []domain.Server{
		{ID: "srv_1", Role: domain.RoleAll, Status: domain.StatusRunning},
		{ID: "srv_b", Role: domain.RoleBuilder, Status: domain.StatusRunning},
	}
	s := newScheduler(fs, fb)
	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if p, _ := fb.last(); p.subject != subjects.Build("srv_1") {
		t.Fatalf("build on %s, want the target itself (ADR-008 local path)", p.subject)
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.BuilderServerID != nil {
		t.Fatalf("builder_server_id = %v, want nil for local build", got.BuilderServerID)
	}
}

func TestDeployNoBuilderFailsFast(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_w")
	fs.servers = []domain.Server{{ID: "srv_w", Role: domain.RoleWorker, Status: domain.StatusRunning}}
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err == nil || !strings.Contains(err.Error(), "no builder available") {
		t.Fatalf("Deploy err = %v, want no-builder failure", err)
	}
	if fb.count() != 0 {
		t.Fatal("work published despite no builder")
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployFailed {
		t.Fatalf("deployment status = %s, want failed", got.Status)
	}
}

// A stopped builder is not eligible: builds must not queue onto a dead host.
func TestDeploySkipsOfflineBuilder(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_w")
	fs.servers = []domain.Server{
		{ID: "srv_w", Role: domain.RoleWorker, Status: domain.StatusRunning},
		{ID: "srv_b", Role: domain.RoleBuilder, Status: domain.StatusUnknown},
	}
	s := newScheduler(fs, fb)
	if _, err := s.Deploy(context.Background(), "app_1", "manual", ""); err == nil || !strings.Contains(err.Error(), "no builder available") {
		t.Fatalf("Deploy err = %v, want no-builder failure", err)
	}
	if fb.count() != 0 {
		t.Fatal("work published to an offline builder")
	}
}

func buildEvent(dep domain.Deployment, outcome agentv1.DeployEvent_Outcome) *agentv1.DeployEvent {
	return &agentv1.DeployEvent{
		DeploymentId: dep.ID, AppId: dep.ApplicationID,
		Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: outcome,
	}
}

func distributeEvent(dep domain.Deployment, outcome agentv1.DeployEvent_Outcome) *agentv1.DeployEvent {
	return &agentv1.DeployEvent{
		DeploymentId: dep.ID, AppId: dep.ApplicationID,
		Stage: agentv1.DeployEvent_STAGE_DISTRIBUTE, Outcome: outcome,
	}
}

func TestBuildSuccessStartsDistributeOnMultiServer(t *testing.T) {
	fs, fb, s, dep := multiServerDeploy(t)

	s.HandleDeployEvent(context.Background(), "srv_b", buildEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))

	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployDistributing {
		t.Fatalf("status = %s, want distributing", got.Status)
	}
	if !fb.has(subjects.PushImage("srv_b")) {
		t.Fatalf("no push work on %s; published %v", subjects.PushImage("srv_b"), fb.subjectsPublished())
	}
	if !fb.has(subjects.Distribute("srv_w")) {
		t.Fatalf("no distribute work on %s; published %v", subjects.Distribute("srv_w"), fb.subjectsPublished())
	}
	if fb.has(subjects.Rollout("srv_w")) {
		t.Fatal("rollout published before the target obtained the image")
	}
}

// Only the recorded builder may report the build stage — the target itself
// has no standing there on a multi-server deployment (spec §5).
func TestBuildEventOnlyFromRecordedBuilder(t *testing.T) {
	fs, _, s, dep := multiServerDeploy(t)

	s.HandleDeployEvent(context.Background(), "srv_w", buildEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployBuilding {
		t.Fatalf("status = %s, want building (event from non-builder must not advance)", got.Status)
	}

	s.HandleDeployEvent(context.Background(), "srv_evil", buildEvent(dep, agentv1.DeployEvent_OUTCOME_FAILED))
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployBuilding {
		t.Fatalf("status = %s, want building (stranger must not fail the deployment)", got.Status)
	}
}

func TestDistributeSuccessFromTargetStartsRollout(t *testing.T) {
	fs, fb, s, dep := multiServerDeploy(t)
	s.HandleDeployEvent(context.Background(), "srv_b", buildEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))

	// A builder-side success is informational only (spec §2).
	s.HandleDeployEvent(context.Background(), "srv_b", distributeEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployDistributing {
		t.Fatalf("status = %s, want distributing after builder-side success", got.Status)
	}

	s.HandleDeployEvent(context.Background(), "srv_w", distributeEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))
	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployRollingOut {
		t.Fatalf("status = %s, want rolling_out", got.Status)
	}
	if !fb.has(subjects.Rollout("srv_w")) {
		t.Fatalf("no rollout work; published %v", fb.subjectsPublished())
	}
}

func TestDistributeFailureFailsDeployment(t *testing.T) {
	fs, _, s, dep := multiServerDeploy(t)
	s.HandleDeployEvent(context.Background(), "srv_b", buildEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))

	s.HandleDeployEvent(context.Background(), "srv_b", distributeEvent(dep, agentv1.DeployEvent_OUTCOME_FAILED))
	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployFailed || !strings.Contains(got.Detail, "distribute failed") {
		t.Fatalf("deployment = %s (%q), want failed with distribute detail", got.Status, got.Detail)
	}
}

// A plane restart mid-distribute republishes both relay work items — the
// stage must survive losing every in-memory session (spec §6).
func TestRecoverRepublishesDistributeWork(t *testing.T) {
	fs, _, s, dep := multiServerDeploy(t)
	s.HandleDeployEvent(context.Background(), "srv_b", buildEvent(dep, agentv1.DeployEvent_OUTCOME_SUCCEEDED))

	fb2 := &fakeBus{}
	s2 := newScheduler(fs, fb2)
	if err := s2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !fb2.has(subjects.PushImage("srv_b")) || !fb2.has(subjects.Distribute("srv_w")) {
		t.Fatalf("recover published %v, want push+distribute republished", fb2.subjectsPublished())
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployDistributing {
		t.Fatalf("status = %s, want distributing preserved", got.Status)
	}
}

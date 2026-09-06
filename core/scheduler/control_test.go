package scheduler

// Deployment control (deployment-control.md): cancelling a deploy, restarting
// an application, and the two health transitions that are news.

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ─── cancel (§2) ────────────────────────────────────────────────────────────

func TestCancelEndsAQueuedDeploy(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	// The first deploy occupies the pipeline; the second queues behind it.
	if _, err := s.Deploy(context.Background(), "app_1", "manual", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	queued, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if queued.Status != domain.DeployQueued {
		t.Fatalf("second deploy status = %s, want queued", queued.Status)
	}

	cancelled, err := s.Cancel(context.Background(), queued.ID, "ops@example.com")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != domain.DeployFailed {
		t.Fatalf("status = %s, want failed — a cancelled deploy is one that did not ship", cancelled.Status)
	}
	if cancelled.Detail != "cancelled by ops@example.com" {
		t.Fatalf("detail = %q, want it to name who cancelled", cancelled.Detail)
	}
}

func TestCancelEndsAnInFlightBuild(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if dep.Status != domain.DeployBuilding {
		t.Fatalf("status = %s, want building", dep.Status)
	}

	cancelled, err := s.Cancel(context.Background(), dep.ID, "ops@example.com")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != domain.DeployFailed {
		t.Fatalf("status = %s, want failed", cancelled.Status)
	}
	// The 'deploying' override start() applied is taken back: the plane cannot
	// re-observe, so leaving it would pulse DEPLOYING for ever against a
	// deployment the panel already calls finished.
	app, err := fs.GetApplication(context.Background(), "app_1")
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if app.Status == domain.AppDeploying {
		t.Fatal("the application is still marked deploying after its deploy was cancelled")
	}
}

// Once desired state has moved, "cancelling" would leave it naming a revision
// the panel claims it abandoned while every agent goes on converging.
func TestCancelRefusedWhileRollingOut(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	rolling, err := fs.GetDeployment(context.Background(), dep.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if rolling.Status != domain.DeployRollingOut {
		t.Fatalf("status = %s, want rolling_out", rolling.Status)
	}

	got, err := s.Cancel(context.Background(), dep.ID, "ops@example.com")
	if !errors.Is(err, ErrCannotCancel) {
		t.Fatalf("err = %v, want ErrCannotCancel", err)
	}
	// The refusal carries the status, because that is what the API branches
	// its "roll back instead" wording on.
	if got.Status != domain.DeployRollingOut {
		t.Fatalf("returned status = %s, want the current one", got.Status)
	}
}

func TestCancelRefusedOnceFinished(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_FAILED,
	})

	if _, err := s.Cancel(context.Background(), dep.ID, "ops@example.com"); !errors.Is(err, ErrCannotCancel) {
		t.Fatalf("err = %v, want ErrCannotCancel for a finished deploy", err)
	}
}

// A cancellation is an operator's decision, not an infrastructure failure —
// announcing "deploy failed" in Slack would misrepresent it.
func TestCancelAnnouncesNothing(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	if _, err := s.Cancel(context.Background(), dep.ID, "ops@example.com"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(rec.deploys) != 0 {
		t.Fatalf("announced %d outcomes, want none for a cancellation", len(rec.deploys))
	}
}

// Cancelling the head of the queue releases the slot behind it.
func TestCancelPromotesTheNextQueuedDeploy(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	active, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	next, _ := s.Deploy(context.Background(), "app_1", "manual", "")

	if _, err := s.Cancel(context.Background(), active.ID, "ops@example.com"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	promoted, err := fs.GetDeployment(context.Background(), next.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if promoted.Status != domain.DeployBuilding {
		t.Fatalf("queued deploy status = %s, want it promoted to building", promoted.Status)
	}
}

// ─── restart (§3) ───────────────────────────────────────────────────────────

// deployed drives one application to a rolled-out revision, which is the state
// a restart needs.
func deployed(t *testing.T, s *Scheduler, fs *fakeStore, appID, serverID string) domain.Application {
	t.Helper()
	dep, err := s.Deploy(context.Background(), appID, "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	s.HandleDeployEvent(context.Background(), serverID, &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	app, err := fs.GetApplication(context.Background(), appID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	return app
}

func TestRestartIssuesATokenAndConverges(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	before := deployed(t, s, fs, "app_1", "srv_1")
	workBefore := fb.count()

	restarted, err := s.Restart(context.Background(), "app_1")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if restarted.RestartToken == "" || restarted.RestartToken == before.RestartToken {
		t.Fatalf("restart token = %q, want a fresh one", restarted.RestartToken)
	}
	if fb.count() <= workBefore {
		t.Fatal("no converge work was published, so the restart would wait for the next sync")
	}
}

// A restart is not a deploy: it must not create a revision or a deployment, and
// it must not move desired state — an operator restarting a wedged container
// must not silently ship the config someone edited an hour ago.
func TestRestartShipsNothing(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	before := deployed(t, s, fs, "app_1", "srv_1")
	revsBefore, depsBefore := len(fs.revisions), len(fs.deployments)

	after, err := s.Restart(context.Background(), "app_1")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(fs.revisions) != revsBefore {
		t.Fatalf("revisions %d -> %d, want none created", revsBefore, len(fs.revisions))
	}
	if len(fs.deployments) != depsBefore {
		t.Fatalf("deployments %d -> %d, want none created", depsBefore, len(fs.deployments))
	}
	if before.DesiredRevisionID == nil || after.DesiredRevisionID == nil ||
		*before.DesiredRevisionID != *after.DesiredRevisionID {
		t.Fatalf("desired revision moved: %v -> %v", before.DesiredRevisionID, after.DesiredRevisionID)
	}
}

func TestRestartRefusedBeforeTheFirstDeploy(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	if _, err := s.Restart(context.Background(), "app_1"); !errors.Is(err, ErrNeverDeployed) {
		t.Fatalf("err = %v, want ErrNeverDeployed", err)
	}
}

// The token rides on the spec, which is the whole mechanism: without it the
// agent has no difference to converge on.
func TestRestartTokenReachesTheSpec(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	deployed(t, s, fs, "app_1", "srv_1")

	restarted, err := s.Restart(context.Background(), "app_1")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	app, err = fs.GetApplication(context.Background(), "app_1")
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	rev, err := fs.GetRevision(context.Background(), *app.DesiredRevisionID)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	spec, err := s.buildSpec(context.Background(), app, rev, envStrict)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.GetRestartToken() != restarted.RestartToken {
		t.Fatalf("spec token = %q, want %q", spec.GetRestartToken(), restarted.RestartToken)
	}
}

// ─── crash and recovery (§5) ────────────────────────────────────────────────

// observe drives one status observation through the plane.
func observe(t *testing.T, s *Scheduler, serverID, appID, state, detail string) {
	t.Helper()
	s.HandleAppStatus(context.Background(), serverID, &agentv1.AppStatus{
		AppId: appID, RevisionId: "rev_1", State: state, Detail: detail,
	})
}

func TestRunningToErrorAnnouncesACrash(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")
	observe(t, s, "srv_1", "app_1", domain.AppError, "exit code 137")

	if len(rec.health) != 1 || rec.health[0] != domain.EventAppCrashed+"/exit code 137" {
		t.Fatalf("health events = %v, want one crash carrying the container's own words", rec.health)
	}
}

func TestErrorToRunningAnnouncesARecovery(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")
	observe(t, s, "srv_1", "app_1", domain.AppError, "exit code 137")
	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")

	if len(rec.health) != 2 || rec.health[1] != domain.EventAppRecovered+"/" {
		t.Fatalf("health events = %v, want a crash then a recovery", rec.health)
	}
}

// An agent reports continuously. Firing per observation would deliver a message
// every few seconds for one dead container, and a muted channel loses the next
// real crash too.
func TestRepeatedErrorObservationsAnnounceOnce(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")
	for i := 0; i < 5; i++ {
		observe(t, s, "srv_1", "app_1", domain.AppError, "exit code 137")
	}
	if len(rec.health) != 1 {
		t.Fatalf("health events = %v, want exactly one", rec.health)
	}
}

// A rollout whose health gate fails reports `error` while the OLD container is
// still serving. Nothing crashed, and deploy.failed already says the deploy did
// not land — so this must not also page.
func TestAFailedDeployDoesNotAnnounceACrash(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	// start() sets the plane-driven 'deploying' override, so the previous
	// status at the moment the failure is observed is `deploying`.
	if _, err := s.Deploy(context.Background(), "app_1", "manual", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	observe(t, s, "srv_1", "app_1", domain.AppError, "health check failed")

	if len(rec.health) != 0 {
		t.Fatalf("health events = %v, want none while a deploy is what failed", rec.health)
	}
}

// `degraded` means "serving, with something wrong". A channel that fires on it
// gets muted, and the next real crash goes with it.
func TestDegradedAnnouncesNothing(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")
	observe(t, s, "srv_1", "app_1", domain.AppDegraded, "1 of 2 replicas")

	if len(rec.health) != 0 {
		t.Fatalf("health events = %v, want none for degraded", rec.health)
	}
}

// A stop is a person's decision, not a failure.
func TestStoppedAnnouncesNothing(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	observe(t, s, "srv_1", "app_1", domain.AppRunning, "")
	observe(t, s, "srv_1", "app_1", domain.AppStopped, "")

	if len(rec.health) != 0 {
		t.Fatalf("health events = %v, want none for a stop", rec.health)
	}
}

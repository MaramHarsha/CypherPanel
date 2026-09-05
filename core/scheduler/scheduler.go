// Package scheduler drives the deploy pipeline (docs/features/
// application-deploy.md §3): it decomposes a Deployment into durable work
// items on work.<server_id>.*, consumes the agent's DeployEvent and AppStatus
// reports, and advances deployment records — asserting success only from
// observed state, never from work-item completion (ADR-005).
//
// Everything here is idempotent by construction (ENGINEERING rules 12–13):
// work items carry the deployment id as idempotency key, re-publishing after
// a crash is safe (JetStream dedup + idempotent consumers), and event
// redelivery re-runs no side effect on a deployment that already advanced.
//
// Deployment concurrency is per-application serialization: one active
// pipeline per app; later deploys queue and are promoted when the active one
// reaches a terminal state (§3, semantics per Dokploy's concurrency.ts —
// mechanism ours).
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/dns"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ErrRevisionNotBuilt is returned when a rollback targets a revision that
// never produced an image (its build failed or never ran).
var ErrRevisionNotBuilt = errors.New("scheduler: revision was never built")

// ErrFrozen marks a deploy refused because its environment is inside a freeze
// window (deploy-protection.md §4). Nothing is written when it is returned — no
// Revision, no Deployment — so a refused deploy leaves no orphan behind, and a
// GitHub delivery can simply be redelivered once the window lifts.
var ErrFrozen = errors.New("scheduler: environment is frozen")

// ErrNotParked refuses a decision on a deployment that is not awaiting
// approval — one already running, already finished, or already decided.
// Handlers map it to 409.
var ErrNotParked = errors.New("scheduler: deployment is not awaiting approval")

// FrozenError carries the sentence the 409 body shows: which environment is
// frozen and when it lifts, e.g. "production is frozen until Mon 08:00
// Europe/Berlin". It wraps ErrFrozen, so callers may match either.
type FrozenError struct{ Detail string }

func (e *FrozenError) Error() string { return e.Detail }
func (e *FrozenError) Unwrap() error { return ErrFrozen }

// ErrRevisionDataInvalid marks a revision whose stored data cannot be turned
// into a container spec at all — today, a config snapshot that will not parse.
// It is permanent and scoped to one application: no retry can produce a spec,
// which is what lets DesiredStateFor omit that one application instead of
// failing a whole node's sync. Every other build failure is treated as
// infrastructure and fails the sync (see DesiredStateFor).
var ErrRevisionDataInvalid = errors.New("scheduler: revision data is invalid")

// Store is the persistence the scheduler needs (consumer-defined).
type Store interface {
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	ListApplicationsByServer(ctx context.Context, serverID string) ([]domain.Application, error)
	SetApplicationDesiredRevision(ctx context.Context, appID, revisionID string) (domain.Application, error)
	SetApplicationStatus(ctx context.Context, appID, status, detail string) error
	SetApplicationObservedStatus(ctx context.Context, appID, status, detail, observedRevisionID string, observedAt time.Time) error
	ListEnvVars(ctx context.Context, appID string) ([]domain.EnvVar, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)

	CreateRevision(ctx context.Context, id, appID, sourceCommit string, configSnapshot []byte) (domain.Revision, error)
	GetRevision(ctx context.Context, id string) (domain.Revision, error)
	SetRevisionImage(ctx context.Context, id, image string) (domain.Revision, error)
	SetRevisionSourceCommit(ctx context.Context, id, commitSHA string) (domain.Revision, error)

	CreateDeployment(ctx context.Context, id, appID, revisionID, trigger string) (domain.Deployment, error)
	SetDeploymentBuilder(ctx context.Context, id, builderServerID string) (domain.Deployment, error)
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	UpdateDeploymentStatus(ctx context.Context, id string, status domain.DeploymentStatus, detail string) (domain.Deployment, error)
	ListActiveDeployments(ctx context.Context) ([]domain.Deployment, error)
	ListActiveDeploymentsByApplication(ctx context.Context, appID string) ([]domain.Deployment, error)

	ListServers(ctx context.Context) ([]domain.Server, error)

	// GetPanelTLS is the panel's ACME account, carried to every node inside
	// DesiredState (agent-identity-and-tls.md §4). store.ErrNotFound means TLS
	// is not configured, which is a normal state, not a failure.
	GetPanelTLS(ctx context.Context) (domain.PanelTLS, error)

	GetDeployKey(ctx context.Context, id string) (domain.DeployKey, error)

	// Phase 3: Managed Databases (managed-databases.md)
	GetDatabase(ctx context.Context, id string) (domain.Database, error)
	ListDatabasesByServer(ctx context.Context, serverID string) ([]domain.Database, error)
	GetDatabaseRevision(ctx context.Context, id string) (domain.DatabaseRevision, error)
	SetDatabaseObservedStatus(ctx context.Context, id, status, detail, observedRevisionID string, observedAt time.Time) error
	ListPendingDeleteDatabases(ctx context.Context) ([]domain.Database, error)
	DeleteDatabase(ctx context.Context, id string) error

	// Phase 3: database backups (managed-databases.md §7)
	GetDatabaseBackup(ctx context.Context, id string) (domain.DatabaseBackup, error)
	ListEnabledBackupSchedules(ctx context.Context) ([]domain.DatabaseBackup, error)
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
	CreateBackupRecord(ctx context.Context, r domain.BackupRecord) (domain.BackupRecord, error)
	GetBackupRecord(ctx context.Context, id string) (domain.BackupRecord, error)
	UpdateBackupRecord(ctx context.Context, id, objectKey string, sizeBytes int64, status, detail string, finishedAt *time.Time) error
	SetDatabaseBackupLastRun(ctx context.Context, id string, lastRunAt *time.Time, lastStatus string) error
	ListBackupRecords(ctx context.Context, backupID string) ([]domain.BackupRecord, error)
	ListBackupRecordsBeyondRetention(ctx context.Context, backupID string, keep int32) ([]store.PrunableBackupRecord, error)
	DeleteBackupRecordsByObjectKeys(ctx context.Context, keys []string) error

	// Phase 3: scheduled tasks (scheduled-tasks.md, ADR-011)
	ListEnabledScheduledTasksByApp(ctx context.Context, appID string) ([]domain.ScheduledTask, error)
	GetScheduledTask(ctx context.Context, id string) (domain.ScheduledTask, error)
	CreateTaskRun(ctx context.Context, r domain.ScheduledTaskRun) (domain.ScheduledTaskRun, error)
	DeleteOldTaskRuns(ctx context.Context, taskID string, keep int32) error

	// Phase 4: project shared variables (shared-variables.md §4, §5). The
	// resolution read and the two stamps that make "redeploy to apply"
	// derivable — no crypto on either stamp path.
	ListSharedVariablesInScope(ctx context.Context, projectID, environmentID string) ([]domain.SharedVariable, error)
	SetDeploymentEnvResolved(ctx context.Context, id string) error
	ApplyDeploymentEnvStamp(ctx context.Context, deploymentID string) error
}

// Bus is the work-publication side of core/bus (consumer-defined).
type Bus interface {
	PublishWork(ctx context.Context, subject, msgID string, data []byte) error
	EnsureWorkConsumer(ctx context.Context, serverID string) error
}

// Opener opens sealed secrets for the wire (consumer-defined; *secret.Box
// satisfies it). Env vars are decrypted only at spec-build time and travel
// only over mTLS (threat-model §5.1, ENGINEERING rule 23).
type Opener interface {
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// EventSink receives terminal outcomes (consumer-defined; *notify.Manager and
// *webhooks.Manager both satisfy it — notifications.md §4,
// outbound-webhooks.md §5). Registering none means the transitions still
// happen, just unannounced. Implementations must not block — delivery is
// fire-and-forget and must never slow or fail a deploy.
type EventSink interface {
	NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment)
	NotifyBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord)
}

// Scheduler owns pipeline state transitions. Construct with New.
// DomainVerifier answers whether a hostname is inside a Zone this panel can
// manage. Consumer-defined (ENGINEERING rule 6): the scheduler needs one
// question answered, not the whole DNS service.
type DomainVerifier interface {
	Verify(ctx context.Context, host string) (dns.Verification, error)
}

// Gate is the deploy-protection admission check (consumer-defined;
// *protection.Service satisfies it — deploy-protection.md §4). It is consulted
// once, in Deploy and Rollback, at the moment a Deployment is born and before
// any work item is published.
//
// A nil Gate means protection is not wired and every deploy is clear — the same
// nil-guard the optional DomainVerifier uses. It is deliberately NOT a sink:
// the gate can refuse, so its errors are propagated, never swallowed (§5, fail
// closed).
type Gate interface {
	Admit(ctx context.Context, environmentID string) (domain.DeployAdmission, error)
	// Park records the gate decision for a deployment the scheduler has just
	// parked. Its failure fails the deployment: a parked deploy with no
	// approval row could never be decided.
	Park(ctx context.Context, dep domain.Deployment, environmentID, requestedBy, requiredRole string) error
}

type Scheduler struct {
	store  Store
	bus    Bus
	opener Opener
	log    *slog.Logger
	now    func() time.Time

	// sinks receive terminal outcomes. An empty slice is already a no-op, so
	// the call sites need no nil guard (outbound-webhooks.md §5).
	sinks []EventSink

	// dns verifies that a route's domain is one the operator owns
	// (dns-automation.md §4.2). nil when DNS automation is not wired, which
	// routableDomain treats as "nothing is enforced".
	dns DomainVerifier

	// gate is deploy protection (deploy-protection.md). nil when it is not
	// wired, which admit() treats as "every deploy is clear" — the behaviour
	// of every panel before this feature existed.
	gate Gate

	// mu serializes pipeline transitions: deploy requests and event handlers
	// race on the per-app queue, and the transitions are read-modify-write.
	mu sync.Mutex
}

// New wires the scheduler.
func New(st Store, b Bus, opener Opener, log *slog.Logger) *Scheduler {
	return &Scheduler{store: st, bus: b, opener: opener, log: log, now: time.Now}
}

// AddSink registers a consumer of terminal outcomes. Kept separate from New so
// announcing stays an opt-in add-on (no sinks = silent). It replaces the
// earlier single-notifier setter rather than sitting beside it: two ways to
// register one thing is how a second sink gets silently dropped
// (outbound-webhooks.md §5).
func (s *Scheduler) AddSink(k EventSink) { s.sinks = append(s.sinks, k) }

// emitDeploy hands a deploy's terminal outcome to every sink. Sinks return
// immediately after detaching, so this never blocks the pipeline.
func (s *Scheduler) emitDeploy(ctx context.Context, app domain.Application, dep domain.Deployment) {
	for _, k := range s.sinks {
		k.NotifyDeploy(ctx, app, dep)
	}
}

// emitBackup hands a backup's terminal outcome to every sink.
func (s *Scheduler) emitBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord) {
	for _, k := range s.sinks {
		k.NotifyBackup(ctx, db, rec)
	}
}

// configSnapshot is the immutable per-revision config (stored as the
// revision's config_snapshot JSON): what rollback restores. Env vars are
// deliberately not part of it — they are sealed rows applied at rollout time.
type configSnapshot struct {
	Port    int    `json:"port"`
	Network string `json:"network"`
	// Pull marks the revision's image as a registry reference the target agent
	// fetches itself (source.kind=image). In the snapshot — not derived from the
	// app's current source — so rolling back to an image revision still pulls
	// after the app was re-pointed at a git source, and vice versa.
	Pull  bool `json:"pull,omitempty"`
	Route struct {
		Domain     string `json:"domain"`
		HTTPS      bool   `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health struct {
		Kind            string `json:"kind"`
		Path            string `json:"path"`
		IntervalSeconds int    `json:"interval_seconds"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		Retries         int    `json:"retries"`
	} `json:"health"`
}

func snapshotOf(app domain.Application) ([]byte, error) {
	var cs configSnapshot
	cs.Port = app.Runtime.Port
	cs.Network = "cypher-" + app.EnvironmentID
	cs.Pull = app.Source.Kind == "image"
	cs.Route.Domain = app.Route.Domain
	cs.Route.HTTPS = app.Route.HTTPS
	cs.Route.PathPrefix = app.Route.PathPrefix
	cs.Health.Kind = app.Health.Kind
	cs.Health.Path = app.Health.Path
	cs.Health.IntervalSeconds = app.Health.IntervalSeconds
	cs.Health.TimeoutSeconds = app.Health.TimeoutSeconds
	cs.Health.Retries = app.Health.Retries
	data, err := json.Marshal(cs)
	if err != nil {
		return nil, fmt.Errorf("scheduler: marshaling config snapshot: %w", err)
	}
	return data, nil
}

// imageTag is the deterministic tag a revision's build produces
// (docs/features/application-deploy.md §3).
func imageTag(appID, revisionID string) string {
	return "cypher/" + appID + ":" + revisionID
}

// Deploy creates a Revision from the application's current configuration and
// a Deployment to roll it out, then starts the pipeline (or queues behind the
// app's active deployment). ref, when non-empty, is the exact commit to build;
// empty builds the configured branch's head.
//
// It is DeployAs with no requester: the machine-triggered path (a template
// install, a preview environment). A deploy a person asked for goes through
// DeployAs so the gate can record who asked (deploy-protection.md §4).
func (s *Scheduler) Deploy(ctx context.Context, appID, trigger, ref string) (domain.Deployment, error) {
	return s.DeployAs(ctx, appID, trigger, ref, "")
}

// DeployAs is Deploy attributed to a user. requestedBy is the calling user's
// id, or empty for a deploy no person asked for — a webhook push, a template
// install — which is what a parked deployment stores as a NULL requester
// (deploy-protection.md §2).
func (s *Scheduler) DeployAs(ctx context.Context, appID, trigger, ref, requestedBy string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting application: %w", err)
	}
	// The gate runs BEFORE anything is written (deploy-protection.md §4): a
	// freeze refuses outright, leaving no orphan Revision behind.
	admission, err := s.admit(ctx, app)
	if err != nil {
		return domain.Deployment{}, err
	}
	if admission.Frozen {
		return domain.Deployment{}, &FrozenError{Detail: admission.FreezeDetail}
	}
	snapshot, err := snapshotOf(app)
	if err != nil {
		return domain.Deployment{}, err
	}
	imageSource := app.Source.Kind == "image"
	if imageSource {
		// An image app has no commit to name: the revision's source identity is
		// the configured reference, and the image is known up front — start()
		// sees a built revision and goes straight to rollout, the same path
		// rollback takes. ref (a git commit override) does not apply.
		ref = app.Source.Image
	} else if ref == "" {
		ref = app.Source.Branch
	}
	rev, err := s.store.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, ref, snapshot)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating revision: %w", err)
	}
	if imageSource {
		if rev, err = s.store.SetRevisionImage(ctx, rev.ID, app.Source.Image); err != nil {
			return domain.Deployment{}, fmt.Errorf("scheduler: recording image revision: %w", err)
		}
	}
	dep, err := s.store.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, trigger)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating deployment: %w", err)
	}
	if admission.NeedsApproval {
		return s.park(ctx, dep, app, admission, requestedBy)
	}
	if serr := s.tryStart(ctx, dep); serr != nil {
		// Return the current record alongside the error: a fail-fast start
		// (no builder, bad deploy key) already wrote status=failed with the
		// reason, and callers surface that record.
		if fresh, err := s.store.GetDeployment(ctx, dep.ID); err == nil {
			return fresh, serr
		}
		return dep, serr
	}
	return s.store.GetDeployment(ctx, dep.ID)
}

// Rollback starts a Deployment that re-points the application at the revision
// a previous deployment shipped — same pipeline, build skipped (the image
// exists; the agent's revision-window GC retains it).
//
// It is RollbackAs with no requester; a rollback a person asked for goes
// through RollbackAs.
func (s *Scheduler) Rollback(ctx context.Context, deploymentID string) (domain.Deployment, error) {
	return s.RollbackAs(ctx, deploymentID, "")
}

// RollbackAs is Rollback attributed to a user. A rollback is a deploy — it
// changes what is serving — so it passes the same gate, and inside a freeze it
// is refused for the same reason a forward deploy is (deploy-protection.md §1).
func (s *Scheduler) RollbackAs(ctx context.Context, deploymentID, requestedBy string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	src, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting deployment: %w", err)
	}
	rev, err := s.store.GetRevision(ctx, src.RevisionID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting revision: %w", err)
	}
	if rev.Image == "" {
		return domain.Deployment{}, ErrRevisionNotBuilt
	}
	app, err := s.store.GetApplication(ctx, src.ApplicationID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting application: %w", err)
	}
	admission, err := s.admit(ctx, app)
	if err != nil {
		return domain.Deployment{}, err
	}
	if admission.Frozen {
		return domain.Deployment{}, &FrozenError{Detail: admission.FreezeDetail}
	}
	dep, err := s.store.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), src.ApplicationID, rev.ID, "rollback")
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating rollback deployment: %w", err)
	}
	if admission.NeedsApproval {
		return s.park(ctx, dep, app, admission, requestedBy)
	}
	if serr := s.tryStart(ctx, dep); serr != nil {
		if fresh, err := s.store.GetDeployment(ctx, dep.ID); err == nil {
			return fresh, serr
		}
		return dep, serr
	}
	return s.store.GetDeployment(ctx, dep.ID)
}

// ─── Deploy protection (deploy-protection.md §§3–4) ─────────────────────────

// admit asks the gate whether this application's environment will accept a
// deploy right now. No gate means no protection, which is how every panel
// behaved before the feature existed. A gate that errors REFUSES: fail closed,
// because a protection control that fails open is worse than none (§5).
func (s *Scheduler) admit(ctx context.Context, app domain.Application) (domain.DeployAdmission, error) {
	if s.gate == nil {
		return domain.DeployAdmission{}, nil
	}
	adm, err := s.gate.Admit(ctx, app.EnvironmentID)
	if err != nil {
		return domain.DeployAdmission{}, fmt.Errorf("scheduler: evaluating deploy protection for %s: %w", app.ID, err)
	}
	return adm, nil
}

// park holds a freshly created Deployment at the gate: it moves to
// awaiting_approval and NO work item is published, so the agent observes
// nothing and the application's own status is untouched — start() is what sets
// an app to deploying, and a parked deploy never reaches it (§3).
//
// Callers hold s.mu.
func (s *Scheduler) park(ctx context.Context, dep domain.Deployment, app domain.Application,
	adm domain.DeployAdmission, requestedBy string) (domain.Deployment, error) {
	parked, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployAwaitingApproval,
		"waiting for approval from "+articleFor(adm.RequiredRole))
	if err != nil {
		return dep, err
	}
	if perr := s.gate.Park(ctx, parked, app.EnvironmentID, requestedBy, adm.RequiredRole); perr != nil {
		// A deployment parked with no approval row could never be decided —
		// no approve or reject route can reach it. Ending it is the honest
		// outcome, and the detail says what to do about it.
		s.log.Error("recording deploy approval", "deployment_id", parked.ID,
			"app_id", app.ID, "environment_id", app.EnvironmentID, "error", perr)
		failed, ferr := s.failParked(ctx, parked, "could not record the approval request — deploy again")
		if ferr != nil {
			return parked, perr
		}
		return failed, perr
	}
	return parked, nil
}

// ApproveDeployment releases a parked deployment into the ordinary queue: it
// becomes queued and tryStart promotes it if it is at the head, so two
// approvals granted at once serialize exactly like two manual deploys (§3).
//
// It asserts the parked state rather than trusting the caller, because the
// approval row and the deployment row are written separately and only this
// assertion makes "approved twice" harmless.
func (s *Scheduler) ApproveDeployment(ctx context.Context, deploymentID string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dep, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting deployment: %w", err)
	}
	if !dep.Status.Parked() {
		return dep, ErrNotParked
	}
	queued, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployQueued, "")
	if err != nil {
		return dep, err
	}
	s.log.Info("parked deployment approved", "deployment_id", dep.ID, "app_id", dep.ApplicationID)
	if serr := s.tryStart(ctx, queued); serr != nil {
		if fresh, gerr := s.store.GetDeployment(ctx, queued.ID); gerr == nil {
			return fresh, serr
		}
		return queued, serr
	}
	return s.store.GetDeployment(ctx, queued.ID)
}

// RejectDeployment ends a parked deployment as failed, carrying detail — the
// sentence naming the rejecter and their reason (§3).
func (s *Scheduler) RejectDeployment(ctx context.Context, deploymentID, detail string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dep, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting deployment: %w", err)
	}
	if !dep.Status.Parked() {
		return dep, ErrNotParked
	}
	return s.failParked(ctx, dep, detail)
}

// failParked ends a deployment that never started. Deliberately NOT fail():
//
//   - there is no 'deploying' override to take back — start() never ran, so
//     the application has been reporting what it is actually doing all along;
//   - no queued successor to promote — a parked deploy holds no pipeline slot
//     (the queue queries exclude awaiting_approval), so ending it frees
//     nothing;
//   - nothing is emitted to the notifier/webhook sinks. Their taxonomy is
//     observed OUTCOMES (notifications.md §3), and announcing "deploy failed"
//     in Slack would misrepresent a governance decision as an infrastructure
//     failure. The requester is told directly, in the inbox, by the item that
//     names who rejected it and why (§9).
//
// Callers hold s.mu.
func (s *Scheduler) failParked(ctx context.Context, dep domain.Deployment, detail string) (domain.Deployment, error) {
	failed, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployFailed, detail)
	if err != nil {
		return dep, fmt.Errorf("scheduler: ending parked deployment: %w", err)
	}
	s.log.Info("parked deployment ended", "deployment_id", dep.ID,
		"app_id", dep.ApplicationID, "detail", detail)
	return failed, nil
}

// articleFor renders a role with its article, so a parked deployment's detail
// reads "waiting for approval from an owner" rather than "from owner".
func articleFor(role string) string {
	switch role {
	case domain.RoleAdmin, domain.RoleOwner:
		return "an " + role
	case domain.RoleMember:
		return "a " + role
	default:
		return "an approver"
	}
}

// RemoveApp publishes desired absence for a deleted application. Called after
// the row is gone; the agent's next sync would converge eventually, but the
// explicit work item makes teardown immediate.
func (s *Scheduler) RemoveApp(ctx context.Context, serverID, appID string) error {
	work := &agentv1.RemoveWork{DeploymentId: "remove-" + appID, AppId: appID}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling remove work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.Remove(serverID), work.GetDeploymentId(), data); err != nil {
		return fmt.Errorf("scheduler: publishing remove: %w", err)
	}
	return nil
}

// tryStart starts dep if it is at the head of its application's queue;
// otherwise it stays queued until the active deployment terminates. Callers
// hold s.mu.
func (s *Scheduler) tryStart(ctx context.Context, dep domain.Deployment) error {
	active, err := s.store.ListActiveDeploymentsByApplication(ctx, dep.ApplicationID)
	if err != nil {
		return fmt.Errorf("scheduler: listing active deployments: %w", err)
	}
	if len(active) == 0 || active[0].ID != dep.ID {
		return nil // queued behind the running pipeline
	}
	return s.start(ctx, dep)
}

// start advances a queued deployment into its first stage. Callers hold s.mu.
func (s *Scheduler) start(ctx context.Context, dep domain.Deployment) error {
	rev, err := s.store.GetRevision(ctx, dep.RevisionID)
	if err != nil {
		return fmt.Errorf("scheduler: getting revision: %w", err)
	}
	app, err := s.store.GetApplication(ctx, dep.ApplicationID)
	if err != nil {
		return fmt.Errorf("scheduler: getting application: %w", err)
	}
	if err := s.store.SetApplicationStatus(ctx, app.ID, domain.AppDeploying, ""); err != nil {
		return err
	}
	// Fail fast on an unresolvable {{shared.KEY}} (shared-variables.md §4).
	// Deliberately BEFORE the rev.Image branch, so a build-first deploy and a
	// rollout-first one (rollback, image-source app) fail identically — and
	// before a builder is selected or any work item is published, so no build
	// minutes are spent and nothing reaches work.*. The running container is
	// untouched.
	if _, err := s.resolveEnv(ctx, app, envStrict); err != nil {
		var unresolved *UnresolvedReferenceError
		if errors.As(err, &unresolved) {
			s.fail(ctx, dep, unresolved.Error())
		} else {
			s.fail(ctx, dep, "could not resolve this application's environment")
		}
		return err
	}
	if rev.Image != "" {
		// Already built (rollback): straight to rollout.
		return s.startRollout(ctx, dep, app, rev)
	}
	if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployBuilding, ""); err != nil {
		return err
	}

	builderID, err := s.selectBuilder(ctx, app)
	if err != nil {
		// No eligible builder is a fleet-configuration error, not a transient
		// fault: fail fast rather than queue forever (spec §2).
		s.fail(ctx, dep, err.Error())
		return err
	}
	if builderID != app.Runtime.ServerID {
		if dep, err = s.store.SetDeploymentBuilder(ctx, dep.ID, builderID); err != nil {
			return fmt.Errorf("scheduler: recording builder: %w", err)
		}
	}

	work, err := s.buildWork(ctx, dep, app, rev)
	if err != nil {
		// A dangling or unopenable deploy key is a configuration error, not
		// a transient fault: fail the deployment (promoting any queued
		// successor) so it doesn't sit in building forever waiting for a
		// build event that can never arrive.
		s.fail(ctx, dep, err.Error())
		return err
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling build work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.Build(builderID), dep.ID+".build", data); err != nil {
		return fmt.Errorf("scheduler: publishing build: %w", err)
	}
	return nil
}

// selectBuilder picks where a deployment builds (builder-role-and-relay.md
// §2): the app's own server when its role builds (the ADR-008 local path),
// else the first running builder-role server, else any other running server
// that builds. Selection is deterministic (store list order).
func (s *Scheduler) selectBuilder(ctx context.Context, app domain.Application) (string, error) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return "", fmt.Errorf("scheduler: listing servers: %w", err)
	}
	// The target's own capability decides first, independent of list order:
	// a remote builder must never steal a build the target can run locally
	// (ADR-008 path 1).
	targetSeen := false
	var dedicated, fallback string
	for _, srv := range servers {
		if srv.ID == app.Runtime.ServerID {
			targetSeen = true
			if srv.Builds() {
				return srv.ID, nil
			}
			continue
		}
		if !srv.Builds() || srv.Status != domain.StatusRunning {
			continue
		}
		if srv.Role == domain.RoleBuilder {
			if dedicated == "" {
				dedicated = srv.ID
			}
		} else if fallback == "" {
			fallback = srv.ID
		}
	}
	if !targetSeen {
		// No row for the target (it may have raced a delete): keep the local
		// path — the same default as an unset role.
		return app.Runtime.ServerID, nil
	}
	if dedicated != "" {
		return dedicated, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("no builder available: target server has role worker and no builder-role server is running")
}

// builderFor is the server a deployment's build was (or will be) routed to.
func builderFor(dep domain.Deployment, app domain.Application) string {
	if dep.BuilderServerID != nil {
		return *dep.BuilderServerID
	}
	return app.Runtime.ServerID
}

// buildWork assembles one deployment's build work item. The referenced deploy
// key is unsealed only here, at work-build time, and travels only inside the
// mTLS-carried BuildWork (deploy-key-private-repos.md §4; ENGINEERING rule
// 23). It is never logged (rule 20). The slice builds on the app's own server
// (ADR-008: builder = target, no distribution); a builder role split
// re-routes the publish subject later.
func (s *Scheduler) buildWork(ctx context.Context, dep domain.Deployment, app domain.Application, rev domain.Revision) (*agentv1.BuildWork, error) {
	var deployKeyPem string
	if app.Source.DeployKeyID != nil {
		dk, err := s.store.GetDeployKey(ctx, *app.Source.DeployKeyID)
		if err != nil {
			return nil, fmt.Errorf("scheduler: getting deploy key: %w", err)
		}
		priv, err := s.opener.Open(dk.PrivateKeyCT, dk.PrivateKeyNonce)
		if err != nil {
			return nil, fmt.Errorf("scheduler: unsealing deploy key: %w", err)
		}
		deployKeyPem = string(priv)
	}
	return &agentv1.BuildWork{
		DeploymentId:   dep.ID,
		AppId:          app.ID,
		RepoUrl:        app.Source.Repo,
		CommitSha:      rev.SourceCommit,
		DockerfilePath: app.Build.DockerfilePath,
		BuildContext:   app.Build.Context,
		Image:          imageTag(app.ID, rev.ID),
		DeployKeyPem:   deployKeyPem,
		BuildKind:      app.Build.Kind,
		// A synthesized static image must listen where the route and health
		// check already expect it, not on whatever its base image defaults to.
		RuntimePort: uint32(app.Runtime.Port), //nolint:gosec // validated 1–65535
	}, nil
}

// startRollout re-points desired state at the (built) revision and publishes
// the rollout work item. Callers hold s.mu.
func (s *Scheduler) startRollout(ctx context.Context, dep domain.Deployment, app domain.Application, rev domain.Revision) error {
	if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployRollingOut, ""); err != nil {
		return err
	}
	if _, err := s.store.SetApplicationDesiredRevision(ctx, app.ID, rev.ID); err != nil {
		return err
	}
	// Stamped BEFORE buildSpec reads the environment, not after.
	//
	// The stamp is compared against shared_variables.updated_at to derive
	// "redeploy to apply" (§5), so it must not be later than the moment the
	// values were actually read. Taken afterwards, an edit landing during
	// buildSpec got updated_at < env_resolved_at and the application was marked
	// CLEAN while this rollout shipped the value from before the edit — the one
	// direction §5 promises is impossible. Taken first, that same edit lands
	// after the stamp and the marker correctly reads pending: the rollout may
	// be redundant, but it can never hide a needed one.
	//
	// Best-effort: a stamp failure must not abort a rollout that is otherwise
	// ready to publish — it only leaves the marker showing pending, which is
	// the safe direction.
	if err := s.store.SetDeploymentEnvResolved(ctx, dep.ID); err != nil {
		s.log.Error("stamping resolved environment", "deployment_id", dep.ID, "error", err)
	}
	spec, err := s.buildSpec(ctx, app, rev, envStrict)
	if err != nil {
		return err
	}
	work := &agentv1.RolloutWork{DeploymentId: dep.ID, Spec: spec}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling rollout work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.Rollout(app.Runtime.ServerID), dep.ID+".rollout", data); err != nil {
		return fmt.Errorf("scheduler: publishing rollout: %w", err)
	}
	return nil
}

// envPolicy decides what an unresolvable {{shared.KEY}} does while an
// application's environment is assembled (shared-variables.md §4).
type envPolicy int

const (
	// envStrict fails the caller. A deploy or a converge push must never ship a
	// half-resolved environment, and failing before anything reaches work.* is
	// what makes the mistake cost nothing.
	envStrict envPolicy = iota
	// envOmitMissing drops the offending key and keeps the application. A sync
	// reply is the complete desired set and absence means REMOVE (ADR-005), so
	// a data-entry mistake must never read as "tear this container down".
	// Omitting the key is safe: container identity is the revision id, so the
	// running container is untouched, and a later recreate leaves the variable
	// UNSET rather than empty — which fails loudly inside the workload instead
	// of silently.
	envOmitMissing
)

// UnresolvedReferenceError is a deployment-visible failure: an environment
// variable references a shared variable that is not defined in the application's
// scope (shared-variables.md §4). Its message is the deployment's `detail`, so
// it reads as a sentence and names the missing KEY — never the value.
type UnresolvedReferenceError struct {
	// EnvKey is the application's own environment variable holding the reference.
	EnvKey string
	// SharedKey is the shared variable that did not resolve.
	SharedKey string
	// Environment is the name of the environment resolution was attempted in.
	Environment string
}

func (e *UnresolvedReferenceError) Error() string {
	return fmt.Sprintf(
		"environment variable %s references {{shared.%s}}, which is not defined in this project or in environment %q",
		e.EnvKey, e.SharedKey, e.Environment)
}

// resolveEnv assembles an application's environment for the wire: its own
// sealed variables, unsealed, with every {{shared.KEY}} expanded against the
// shared variables in force for its environment (shared-variables.md §4).
//
// This is the single resolution point. Both plaintext maps live only in this
// stack frame, nothing is logged, and every error names a KEY rather than a
// value (ENGINEERING rule 20). The shared table is read only when some variable
// actually carries a reference, so an application that uses none costs exactly
// what it did before this feature existed.
func (s *Scheduler) resolveEnv(ctx context.Context, app domain.Application, pol envPolicy) (map[string]string, error) {
	sealed, err := s.store.ListEnvVars(ctx, app.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing env vars: %w", err)
	}
	referencing := false
	for _, v := range sealed {
		if len(v.SharedRefs) > 0 {
			referencing = true
			break
		}
	}

	var (
		shared  map[string]string
		envName string
	)
	if referencing {
		environment, err := s.store.GetEnvironment(ctx, app.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("scheduler: getting environment of %s: %w", app.ID, err)
		}
		envName = environment.Name
		rows, err := s.store.ListSharedVariablesInScope(ctx, environment.ProjectID, environment.ID)
		if err != nil {
			return nil, fmt.Errorf("scheduler: listing shared variables for %s: %w", app.ID, err)
		}
		shared = make(map[string]string, len(rows))
		for _, sv := range rows {
			plain, err := s.opener.Open(sv.ValueCT, sv.ValueNonce)
			if err != nil {
				return nil, fmt.Errorf("scheduler: unsealing shared variable %s: %w", sv.Key, err)
			}
			shared[sv.Key] = string(plain)
		}
	}

	env := make(map[string]string, len(sealed))
	for _, v := range sealed {
		plain, err := s.opener.Open(v.ValueCT, v.ValueNonce)
		if err != nil {
			// Never include the key's value context in the error (rule 20).
			return nil, fmt.Errorf("scheduler: unsealing env var %s: %w", v.Key, err)
		}
		if len(v.SharedRefs) == 0 {
			env[v.Key] = string(plain)
			continue
		}
		expanded, err := sharedvars.Expand(string(plain), shared)
		if err != nil {
			var missing *sharedvars.MissingReferenceError
			if errors.As(err, &missing) {
				unresolved := &UnresolvedReferenceError{EnvKey: v.Key, SharedKey: missing.Key, Environment: envName}
				if pol == envStrict {
					return nil, unresolved
				}
				s.log.Warn("desired state: omitting an environment variable with an unresolvable shared reference",
					"app_id", app.ID, "env_key", v.Key, "shared_key", missing.Key)
				continue
			}
			return nil, fmt.Errorf("scheduler: expanding env var %s of %s: %w", v.Key, app.ID, err)
		}
		env[v.Key] = expanded
	}
	return env, nil
}

// buildSpec assembles the wire AppSpec from the revision's immutable config
// snapshot, the built image, and the app's current (decrypted, shared-expanded)
// env vars.
// routableDomain is where domain ownership is ENFORCED (dns-automation.md §4.2).
//
// An unverified domain is blanked on the wire, so the agent writes no router
// rule for it: the application deploys and runs, it is simply not published at
// a hostname nobody proved they own. Certificates follow routing, so Traefik
// also never asks Let's Encrypt for a name we cannot prove — which keeps an
// unverified domain from burning the ACME rate limit.
//
// Blanking rather than refusing the deploy is deliberate. The operator fixes
// the zone in Cloudflare and redeploys; nothing has to be re-entered, and the
// desired state was never wrong, only unverifiable.
//
// With no DNS Provider configured, Verify reports Enforced=false and every
// domain passes — an install that never connects Cloudflare behaves exactly as
// it did before this feature existed.
func (s *Scheduler) routableDomain(ctx context.Context, domainName string) string {
	if domainName == "" || s.dns == nil {
		return domainName
	}
	v, err := s.dns.Verify(ctx, domainName)
	if err != nil {
		// Fail OPEN on an infrastructure error, not on a verdict. A database
		// hiccup must not silently un-route every domain on the panel; a real
		// "this is not your domain" is a verdict, and that path returns no
		// error.
		s.log.Error("scheduler: verifying domain, routing it anyway", "domain", domainName, "error", err)
		return domainName
	}
	if v.Enforced && !v.Verified {
		s.log.Warn("scheduler: domain is not in a connected DNS zone, not routing it",
			"domain", domainName, "zones", v.AvailableZones)
		return ""
	}
	return domainName
}

func (s *Scheduler) buildSpec(ctx context.Context, app domain.Application, rev domain.Revision, pol envPolicy) (*agentv1.AppSpec, error) {
	var cs configSnapshot
	if err := json.Unmarshal(rev.ConfigSnapshot, &cs); err != nil {
		// Tagged as permanent and app-scoped: a stored snapshot that will not
		// parse never will, so DesiredStateFor may omit this one application
		// rather than fail the node's whole sync.
		return nil, fmt.Errorf("%w: parsing config snapshot of %s: %w", ErrRevisionDataInvalid, rev.ID, err)
	}
	env, err := s.resolveEnv(ctx, app, pol)
	if err != nil {
		return nil, err
	}
	image := rev.Image
	if image == "" {
		image = imageTag(app.ID, rev.ID)
	}
	// Scheduled tasks are current app state (mutable independently of the
	// revision), carried as declarative desired state — the agent runs them in
	// this app's container (scheduled-tasks.md §3, ADR-011).
	tasks, err := s.scheduledTasksFor(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	return &agentv1.AppSpec{
		AppId:         app.ID,
		EnvironmentId: app.EnvironmentID,
		RevisionId:    rev.ID,
		Image:         image,
		Port:          uint32(cs.Port),
		Env:           env,
		Network:       cs.Network,
		Health: &agentv1.HealthCheck{
			Kind:            cs.Health.Kind,
			Path:            cs.Health.Path,
			IntervalSeconds: uint32(cs.Health.IntervalSeconds),
			TimeoutSeconds:  uint32(cs.Health.TimeoutSeconds),
			Retries:         uint32(cs.Health.Retries),
		},
		Route: &agentv1.RouteSpec{
			Domain:     s.routableDomain(ctx, cs.Route.Domain),
			Https:      cs.Route.HTTPS,
			PathPrefix: cs.Route.PathPrefix,
		},
		ScheduledTasks: tasks,
		Pull:           cs.Pull,
		// Resource limits are current app state (not per-revision snapshot),
		// applied at rollout like env vars; nil = 0 = no limit on the wire.
		CpuLimit:      cpuLimitValue(app.Runtime.CPULimit),
		MemoryLimitMb: memLimitValue(app.Runtime.MemoryLimitMB),
		Volumes:       volumeMounts(app.ID, app.Volumes),
		Ports:         portMappings(app.Ports),
	}, nil
}

// portMappings maps an app's raw host-port publishes to the wire.
func portMappings(ports []domain.PortMapping) []*agentv1.PortMapping {
	if len(ports) == 0 {
		return nil
	}
	out := make([]*agentv1.PortMapping, 0, len(ports))
	for _, p := range ports {
		out = append(out, &agentv1.PortMapping{
			HostPort:      uint32(p.HostPort),
			ContainerPort: uint32(p.ContainerPort),
			Protocol:      p.Protocol,
		})
	}
	return out
}

// volumeMounts resolves an app's declared volumes to wire mounts, computing the
// deterministic managed volume name (cypher-appvol-<app>-<name>) per mount.
func volumeMounts(appID string, vols []domain.VolumeMount) []*agentv1.VolumeMount {
	if len(vols) == 0 {
		return nil
	}
	out := make([]*agentv1.VolumeMount, 0, len(vols))
	for _, v := range vols {
		out = append(out, &agentv1.VolumeMount{
			VolumeName: "cypher-appvol-" + appID + "-" + v.Name,
			Path:       v.Path,
		})
	}
	return out
}

// cpuLimitValue / memLimitValue map the nullable domain limit to the wire's
// "0 = no limit" convention.
func cpuLimitValue(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func memLimitValue(p *int) uint32 {
	if p == nil || *p < 0 {
		return 0
	}
	return uint32(*p)
}

// HandleDeployEvent advances the pipeline on a work item's terminal outcome.
// Safe under redelivery: a deployment already past the reported stage (or
// terminal) ignores the event.
func (s *Scheduler) HandleDeployEvent(ctx context.Context, serverID string, ev *agentv1.DeployEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dep, err := s.store.GetDeployment(ctx, ev.GetDeploymentId())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("deploy event: loading deployment", "deployment_id", ev.GetDeploymentId(), "error", err)
		}
		return // deleted app's cascade, or a remove event — nothing to advance
	}
	if dep.Status.Terminal() {
		return // redelivery after completion
	}
	app, err := s.store.GetApplication(ctx, dep.ApplicationID)
	if err != nil {
		s.log.Error("deploy event: loading application", "deployment_id", dep.ID, "error", err)
		return
	}
	// The subject pins which server published the event; only servers with a
	// persisted part in this deployment may advance it — the target always,
	// plus the recorded builder for the build/distribute stages
	// (builder-role-and-relay.md §5). A compromised agent's blast radius
	// stays its own workloads and builds (threat-model §5.2).
	builderID := builderFor(dep, app)
	allowed := serverID == app.Runtime.ServerID
	switch ev.GetStage() {
	case agentv1.DeployEvent_STAGE_BUILD:
		allowed = serverID == builderID
	case agentv1.DeployEvent_STAGE_DISTRIBUTE:
		allowed = allowed || serverID == builderID
	case agentv1.DeployEvent_STAGE_ROLLOUT, agentv1.DeployEvent_STAGE_REMOVE, agentv1.DeployEvent_STAGE_UNSPECIFIED:
	}
	if !allowed {
		s.log.Warn("deploy event from a server with no part in the deployment",
			"deployment_id", dep.ID, "app_id", app.ID, "stage", ev.GetStage().String(),
			"reported_by", serverID, "runs_on", app.Runtime.ServerID, "builds_on", builderID)
		return
	}

	switch ev.GetStage() {
	case agentv1.DeployEvent_STAGE_BUILD:
		if dep.Status != domain.DeployBuilding {
			return
		}
		if ev.GetOutcome() == agentv1.DeployEvent_OUTCOME_FAILED {
			s.fail(ctx, dep, "build failed: "+ev.GetDetail())
			return
		}
		rev, err := s.store.SetRevisionImage(ctx, dep.RevisionID, imageTag(dep.ApplicationID, dep.RevisionID))
		if err != nil {
			s.log.Error("deploy event: recording image", "deployment_id", dep.ID, "error", err)
			return
		}
		if sha := ev.GetCommitSha(); sha != "" && rev.SourceCommit != sha {
			if rev, err = s.store.SetRevisionSourceCommit(ctx, rev.ID, sha); err != nil {
				s.log.Error("deploy event: recording commit", "deployment_id", dep.ID, "error", err)
				return
			}
		}
		if builderID != app.Runtime.ServerID {
			// The image is on the builder's daemon, not the target's: relay
			// it before rolling out (builder-role-and-relay.md §2).
			if err := s.startDistribute(ctx, dep, app, rev); err != nil {
				s.log.Error("deploy event: starting distribute", "deployment_id", dep.ID, "error", err)
			}
			return
		}
		if err := s.startRollout(ctx, dep, app, rev); err != nil {
			s.log.Error("deploy event: starting rollout", "deployment_id", dep.ID, "error", err)
		}

	case agentv1.DeployEvent_STAGE_DISTRIBUTE:
		if dep.Status != domain.DeployDistributing {
			return
		}
		if ev.GetOutcome() == agentv1.DeployEvent_OUTCOME_FAILED {
			s.fail(ctx, dep, "distribute failed: "+ev.GetDetail())
			return
		}
		// Only the target's success proves the image is where it must run;
		// a builder-side success is informational (spec §2).
		if serverID != app.Runtime.ServerID {
			return
		}
		rev, err := s.store.GetRevision(ctx, dep.RevisionID)
		if err != nil {
			s.log.Error("deploy event: loading revision", "deployment_id", dep.ID, "error", err)
			return
		}
		if err := s.startRollout(ctx, dep, app, rev); err != nil {
			s.log.Error("deploy event: starting rollout", "deployment_id", dep.ID, "error", err)
		}

	case agentv1.DeployEvent_STAGE_ROLLOUT:
		if dep.Status != domain.DeployRollingOut {
			return
		}
		if ev.GetOutcome() == agentv1.DeployEvent_OUTCOME_FAILED {
			s.fail(ctx, dep, "rollout failed: "+ev.GetDetail())
		}
		// Success is asserted only from the AppStatus observation that
		// follows (ADR-005) — HandleAppStatus completes the deployment.

	case agentv1.DeployEvent_STAGE_REMOVE:
		// Removals have no deployment to advance.

	case agentv1.DeployEvent_STAGE_UNSPECIFIED:
	}
}

// startDistribute advances a multi-server deployment into the relay stage:
// the builder pushes the image to the plane, the target pulls and loads it
// (builder-role-and-relay.md §2–3). Both items are idempotent under
// redelivery; the target's STAGE_DISTRIBUTE success advances to rollout.
// Callers hold s.mu.
func (s *Scheduler) startDistribute(ctx context.Context, dep domain.Deployment, app domain.Application, rev domain.Revision) error {
	if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployDistributing, ""); err != nil {
		return err
	}
	image := rev.Image
	if image == "" {
		image = imageTag(app.ID, rev.ID)
	}
	builderID := builderFor(dep, app)

	push, err := proto.Marshal(&agentv1.PushImageWork{DeploymentId: dep.ID, AppId: app.ID, Image: image})
	if err != nil {
		return fmt.Errorf("scheduler: marshaling push work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.PushImage(builderID), dep.ID+".push", push); err != nil {
		return fmt.Errorf("scheduler: publishing push: %w", err)
	}
	dist, err := proto.Marshal(&agentv1.DistributeWork{DeploymentId: dep.ID, AppId: app.ID, Image: image})
	if err != nil {
		return fmt.Errorf("scheduler: marshaling distribute work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.Distribute(app.Runtime.ServerID), dep.ID+".distribute", dist); err != nil {
		return fmt.Errorf("scheduler: publishing distribute: %w", err)
	}
	return nil
}

// HandleAppStatus records an observation and completes any rolling-out
// deployment whose revision is now observed serving (ADR-005: the only path
// to "succeeded").
func (s *Scheduler) HandleAppStatus(ctx context.Context, serverID string, st *agentv1.AppStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.GetApplication(ctx, st.GetAppId())
	if err != nil {
		return // deleted app; observation is moot
	}
	// Only the server the app runs on may report its status (threat-model
	// §5.2 — the subject pins the reporter's identity).
	if app.Runtime.ServerID != serverID {
		s.log.Warn("app status from a server the app does not run on",
			"app_id", app.ID, "reported_by", serverID, "runs_on", app.Runtime.ServerID)
		return
	}
	observedAt := s.now()
	if ts := st.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}
	if err := s.store.SetApplicationObservedStatus(ctx, st.GetAppId(), st.GetState(), st.GetDetail(), st.GetRevisionId(), observedAt); err != nil {
		s.log.Error("app status: recording observation", "app_id", st.GetAppId(), "error", err)
		return
	}
	if st.GetState() != domain.AppRunning {
		return
	}
	s.pinRevisionImage(ctx, st)
	active, err := s.store.ListActiveDeploymentsByApplication(ctx, st.GetAppId())
	if err != nil {
		s.log.Error("app status: listing active deployments", "app_id", st.GetAppId(), "error", err)
		return
	}
	for _, dep := range active {
		if dep.Status == domain.DeployRollingOut && dep.RevisionID == st.GetRevisionId() {
			done, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeploySucceeded, "")
			if err != nil {
				s.log.Error("app status: completing deployment", "deployment_id", dep.ID, "error", err)
				return
			}
			// The environment this rollout froze is now the one actually
			// running, so the "redeploy to apply" marker clears. Only this
			// path — an observation of the revision serving — moves the stamp,
			// which is why a failed deploy can never mark an app clean
			// (shared-variables.md §5).
			if err := s.store.ApplyDeploymentEnvStamp(ctx, dep.ID); err != nil {
				s.log.Error("app status: applying resolved environment stamp", "deployment_id", dep.ID, "error", err)
			}
			s.log.Info("deployment succeeded", "deployment_id", dep.ID, "app_id", dep.ApplicationID, "revision_id", dep.RevisionID, "server_id", serverID)
			s.emitDeploy(ctx, app, done)
			s.promoteNext(ctx, dep.ApplicationID)
			return
		}
	}
}

// pinRevisionImage records the immutable digest the agent observed running, so
// the revision names the artifact it actually shipped rather than a tag that
// can move underneath it. Without this, rolling back to a revision created from
// `acme/web:latest` would re-pull that tag and start whatever it points at now
// while reporting the old revision restored.
//
// The plane cannot resolve a digest itself — it never talks to a registry
// (ADR-001) — so this is only knowable as an observation (ADR-005). Best
// effort: a failure here leaves the revision as it was and never affects the
// deployment's outcome.
func (s *Scheduler) pinRevisionImage(ctx context.Context, st *agentv1.AppStatus) {
	digest := st.GetResolvedImage()
	if digest == "" {
		return
	}
	rev, err := s.store.GetRevision(ctx, st.GetRevisionId())
	if err != nil || rev.Image == digest {
		return
	}
	// The reporting server owns this app (checked above), but that does not make
	// the revision id it supplied one of the app's. Without this check a
	// compromised agent could report its own app alongside another
	// application's revision and an attacker-chosen digest, overwriting that
	// revision's image — and a later rollback would then pull and run it on a
	// different server. An agent may only pin revisions of the app it reported.
	if rev.ApplicationID != st.GetAppId() {
		s.log.Warn("app status: revision does not belong to the reported application",
			"app_id", st.GetAppId(), "revision_id", rev.ID, "revision_app_id", rev.ApplicationID)
		return
	}
	if _, err := s.store.SetRevisionImage(ctx, rev.ID, digest); err != nil {
		s.log.Warn("pinning revision to observed digest", "revision_id", rev.ID, "error", err)
		return
	}
	s.log.Info("revision pinned to observed digest", "revision_id", rev.ID, "was", rev.Image, "now", digest)
}

// fail terminates a deployment and promotes the next queued one. The
// application's own status stays observation-driven: the previous revision
// keeps serving and the agent's reports say so. Callers hold s.mu.
func (s *Scheduler) fail(ctx context.Context, dep domain.Deployment, detail string) {
	failed, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployFailed, detail)
	if err != nil {
		s.log.Error("failing deployment", "deployment_id", dep.ID, "error", err)
		return
	}
	s.log.Warn("deployment failed", "deployment_id", dep.ID, "app_id", dep.ApplicationID, "detail", detail)
	// Load the app for the announcement and to clear the plane-driven
	// 'deploying' override; a lookup miss just drops the notice (the failure is
	// already recorded and logged).
	if app, err := s.store.GetApplication(ctx, dep.ApplicationID); err == nil {
		s.clearDeployingStatus(ctx, app, detail)
		s.emitDeploy(ctx, app, failed)
	}
	s.promoteNext(ctx, dep.ApplicationID)
}

// clearDeployingStatus takes back the 'deploying' override start() applied.
//
// Status is the agent's observation except while a pipeline runs, when the
// scheduler overrides it (domain.Application.Status). Observations overwrite
// the override as reports arrive — but a build or distribute failure never
// touches a container, so the agent has nothing new to report and the override
// would stick forever: the Deployments tab said FAILED while Overview pulsed
// DEPLOYING indefinitely. That is precisely the state ui-principles §10 forbids
// showing.
//
// The plane cannot re-observe, so it says the truest thing it can derive. A
// previously observed revision means the old container was never disturbed —
// zero-downtime means the previous revision keeps serving a failed rollout — so
// the app is running, and the detail explains that the newest attempt failed.
// With nothing ever observed, nothing is serving, and error is honest.
func (s *Scheduler) clearDeployingStatus(ctx context.Context, app domain.Application, detail string) {
	if app.Status != domain.AppDeploying {
		return // an observation already corrected it; don't fight the agent
	}
	status, msg := domain.AppError, detail
	if app.ObservedRevisionID != "" {
		status = domain.AppRunning
		msg = "last deploy failed (" + detail + "); the previous revision is still serving"
	}
	if err := s.store.SetApplicationStatus(ctx, app.ID, status, msg); err != nil {
		s.log.Error("clearing deploying status", "app_id", app.ID, "error", err)
	}
}

// promoteNext starts the oldest queued deployment for the app, if any.
// Callers hold s.mu.
func (s *Scheduler) promoteNext(ctx context.Context, appID string) {
	active, err := s.store.ListActiveDeploymentsByApplication(ctx, appID)
	if err != nil {
		s.log.Error("promoting next deployment", "app_id", appID, "error", err)
		return
	}
	if len(active) == 0 || active[0].Status != domain.DeployQueued {
		return
	}
	if err := s.start(ctx, active[0]); err != nil {
		s.log.Error("starting promoted deployment", "deployment_id", active[0].ID, "error", err)
	}
}

// taskRunRetention bounds run-history rows kept per task (scheduled-tasks.md §6).
const taskRunRetention = 20

// scheduledTasksFor loads an app's enabled tasks as wire ScheduledTasks. The
// command is argv, carried verbatim (ADR-011 — never assembled into a shell
// string on the plane).
func (s *Scheduler) scheduledTasksFor(ctx context.Context, appID string) ([]*agentv1.ScheduledTask, error) {
	tasks, err := s.store.ListEnabledScheduledTasksByApp(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing scheduled tasks: %w", err)
	}
	out := make([]*agentv1.ScheduledTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, &agentv1.ScheduledTask{Id: t.ID, Schedule: t.Schedule, Command: t.Command})
	}
	return out, nil
}

// ConvergeApp re-declares an app's current desired state to its agent without a
// deployment, so a scheduled-task change takes effect promptly (scheduled-
// tasks.md §4). A no-op when the app has no built desired revision (no container
// to run tasks in yet — the tasks apply on first deploy).
func (s *Scheduler) ConvergeApp(ctx context.Context, appID string) error {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return err
	}
	if app.DesiredRevisionID == nil {
		return nil
	}
	rev, err := s.store.GetRevision(ctx, *app.DesiredRevisionID)
	if err != nil {
		return err
	}
	if rev.Image == "" {
		return nil
	}
	// Strict: a converge push that silently dropped a variable would leave the
	// container running an environment nobody declared. Propagating the error
	// and publishing nothing loses nothing — the container is already running
	// the environment it was deployed with (shared-variables.md §4).
	spec, err := s.buildSpec(ctx, app, rev, envStrict)
	if err != nil {
		return err
	}
	data, err := proto.Marshal(&agentv1.ConvergeWork{Spec: spec})
	if err != nil {
		return fmt.Errorf("scheduler: marshaling converge: %w", err)
	}
	msgID := fmt.Sprintf("%s.converge.%d", appID, s.now().UnixNano())
	if err := s.bus.PublishWork(ctx, subjects.Converge(app.Runtime.ServerID), msgID, data); err != nil {
		return fmt.Errorf("scheduler: publishing converge: %w", err)
	}
	return nil
}

// HandleScheduledTaskRun records a scheduled task's run observation and prunes
// history (ADR-005: the plane records what the agent reports). Only the server
// the task's app runs on may report it (threat-model §5.2).
func (s *Scheduler) HandleScheduledTaskRun(ctx context.Context, serverID string, ev *agentv1.ScheduledTaskRun) {
	task, err := s.store.GetScheduledTask(ctx, ev.GetTaskId())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("task run: loading task", "task_id", ev.GetTaskId(), "error", err)
		}
		return // task deleted (its runs cascade) — nothing to record
	}
	app, err := s.store.GetApplication(ctx, task.ApplicationID)
	if err != nil {
		return
	}
	if app.Runtime.ServerID != serverID {
		s.log.Warn("task run from a server the app does not run on",
			"task_id", task.ID, "reported_by", serverID, "runs_on", app.Runtime.ServerID)
		return
	}
	status := domain.TaskRunSucceeded
	if ev.GetFailed() {
		status = domain.TaskRunFailed
	}
	var finished *time.Time
	if ts := ev.GetFinishedAt(); ts != nil {
		t := ts.AsTime()
		finished = &t
	}
	exit := int(ev.GetExitCode())
	run := domain.ScheduledTaskRun{
		ID:         ev.GetRunId(),
		TaskID:     task.ID,
		Status:     status,
		ExitCode:   &exit,
		FinishedAt: finished,
		OutputTail: ev.GetOutputTail(),
	}
	if ts := ev.GetStartedAt(); ts != nil {
		run.StartedAt = ts.AsTime()
	} else {
		run.StartedAt = s.now()
	}
	if _, err := s.store.CreateTaskRun(ctx, run); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return // redelivered observation, already recorded
		}
		s.log.Error("task run: recording", "task_id", task.ID, "run_id", run.ID, "error", err)
		return
	}
	if err := s.store.DeleteOldTaskRuns(ctx, task.ID, taskRunRetention); err != nil {
		s.log.Error("task run: pruning history", "task_id", task.ID, "error", err)
	}
	s.log.Info("scheduled task run recorded", "task_id", task.ID, "status", status, "exit_code", exit)
}

// DesiredStateFor resolves one server's full desired set — the reply to the
// agent's sync request. Apps whose desired revision was never built are
// omitted (nothing can serve them yet).
//
// The reply is the COMPLETE desired set and replaces what the agent holds, so
// under ADR-005 omitting a running application here is an instruction to tear
// its container down (agent/driver/docker removes what desired state does not
// name; the same is true of databases). A store read that did not answer must
// therefore fail the WHOLE sync rather than silently shrink the set: with no
// reply the agent times out, keeps the desired set it already holds and asks
// again, which costs one sync cycle instead of the fleet. This matters most for
// a `work.<id>.resync` nudge, which re-syncs every node while it is up.
//
// Only a permanent, application-scoped data problem omits one entry — a
// revision row that is gone, a config snapshot that will not parse — because
// there is no spec to send for it and no retry can produce one. The node-wide
// TLS settings at the end are the one deliberate exception, for the reason
// recorded there: their absence means "no resolver", which serves HTTP for a
// cycle rather than removing anything.
func (s *Scheduler) DesiredStateFor(ctx context.Context, serverID string) ([]byte, error) {
	apps, err := s.store.ListApplicationsByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing apps for %s: %w", serverID, err)
	}
	ds := &agentv1.DesiredState{}
	for _, app := range apps {
		if app.DesiredRevisionID == nil {
			continue
		}
		rev, err := s.store.GetRevision(ctx, *app.DesiredRevisionID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("scheduler: loading revision %s of app %s for %s: %w", *app.DesiredRevisionID, app.ID, serverID, err)
			}
			s.log.Error("desired state: omitting an app whose desired revision row is gone",
				"server_id", serverID, "app_id", app.ID, "revision_id", *app.DesiredRevisionID, "error", err)
			continue
		}
		if rev.Image == "" {
			continue
		}
		// envOmitMissing: the sync reply is the complete desired set, so
		// omitting the APPLICATION over a data-entry mistake would read as
		// "remove this container" (ADR-005). The offending key is dropped and
		// logged instead (shared-variables.md §4).
		spec, err := s.buildSpec(ctx, app, rev, envOmitMissing)
		if err != nil {
			if !errors.Is(err, ErrRevisionDataInvalid) {
				return nil, fmt.Errorf("scheduler: building spec for app %s of %s: %w", app.ID, serverID, err)
			}
			s.log.Error("desired state: omitting an app whose revision data cannot be read",
				"server_id", serverID, "app_id", app.ID, "revision_id", rev.ID, "error", err)
			continue
		}
		ds.Specs = append(ds.Specs, spec)
	}

	// Phase 3: Managed Databases (managed-databases.md §5)
	dbs, err := s.store.ListDatabasesByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing databases for %s: %w", serverID, err)
	}
	for _, db := range dbs {
		if db.DesiredRevisionID == nil {
			continue
		}
		// Desired state must reflect intent: a database the operator stopped,
		// or one being deleted, is NOT desired-present. Advertising it here
		// would make the agent re-provision it on every sync/drift, fighting
		// the stop/remove work items (ADR-005 consistency).
		if db.PendingDelete || db.DesiredState == domain.DbDesiredStopped {
			continue
		}
		rev, err := s.store.GetDatabaseRevision(ctx, *db.DesiredRevisionID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("scheduler: loading revision %s of database %s for %s: %w", *db.DesiredRevisionID, db.ID, serverID, err)
			}
			s.log.Error("desired state: omitting a database whose desired revision row is gone",
				"server_id", serverID, "db_id", db.ID, "revision_id", *db.DesiredRevisionID, "error", err)
			continue
		}

		// Same builder the work item uses: the agent measures drift by
		// comparing the two, so they must not be able to disagree.
		spec, err := s.dbSpec(db, rev)
		if err != nil {
			// dbSpec fails only when the sealed root password will not open —
			// a sealing-key problem, not this database's data. Omitting the
			// database would delete a running one (managed-databases.md §6).
			return nil, fmt.Errorf("scheduler: building spec for database %s of %s: %w", db.ID, serverID, err)
		}
		ds.DbSpecs = append(ds.DbSpecs, spec)
	}

	// Node-wide TLS settings. A read failure is logged and the field is left
	// empty rather than failing the whole sync: an agent with no desired state
	// converges nothing, which is far worse than an agent that serves HTTP for
	// one sync cycle. The empty value is also the safe one — it makes the node
	// stop promising HTTPS rather than start.
	if tls, err := s.store.GetPanelTLS(ctx); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("desired state: reading panel tls", "server_id", serverID, "error", err)
		}
	} else if tls.Configured() {
		ds.Tls = &agentv1.TLSSettings{AcmeEmail: tls.ACMEEmail, AcmeCaServer: tls.ACMECAServer}
	}

	return proto.Marshal(ds)
}

// RequestResync asks every enrolled server to re-read its desired state
// (agent-identity-and-tls.md §4). It is how a node-wide setting that belongs to
// no single Application — the panel's ACME account — reaches the fleet promptly
// instead of waiting for each agent's next reconnect.
//
// Best-effort per server: one unreachable node must not stop the others, and
// the nudge is only ever an optimization — the setting is already in Postgres,
// which is what makes it true (rule 15).
func (s *Scheduler) RequestResync(ctx context.Context, reason string) error {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: listing servers for resync: %w", err)
	}
	data, err := proto.Marshal(&agentv1.ResyncWork{Reason: reason})
	if err != nil {
		return fmt.Errorf("scheduler: marshaling resync: %w", err)
	}
	var failed int
	for _, srv := range servers {
		if srv.EnrolledAt == nil {
			continue // never joined: nothing is listening on its work subject
		}
		// The message id is time-based rather than derived from the reason: two
		// settings changes a minute apart are two nudges, and JetStream dedup
		// must not swallow the second.
		msgID := fmt.Sprintf("%s.resync.%d", srv.ID, s.now().UnixNano())
		if err := s.bus.PublishWork(ctx, subjects.Resync(srv.ID), msgID, data); err != nil {
			failed++
			s.log.Error("resync: publishing", "server_id", srv.ID, "reason", reason, "error", err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("scheduler: %d of %d servers could not be nudged to resync", failed, len(servers))
	}
	s.log.Info("fleet asked to re-read desired state", "reason", reason, "servers", len(servers))
	return nil
}

// Recover re-drives every non-terminal deployment after a plane restart:
// consumers are re-asserted for all servers, queued deployments try to start,
// and in-flight stages re-publish their work items (idempotent: JetStream
// dedup inside the window, idempotent consumers beyond it).
func (s *Scheduler) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: listing servers: %w", err)
	}
	for _, srv := range servers {
		if err := s.bus.EnsureWorkConsumer(ctx, srv.ID); err != nil {
			s.log.Error("recover: ensuring work consumer", "server_id", srv.ID, "error", err)
		}
	}

	// Phase 3: Recover and publish pending database work items (provision / remove).
	for _, srv := range servers {
		dbs, err := s.store.ListDatabasesByServer(ctx, srv.ID)
		if err != nil {
			s.log.Error("recover: listing databases", "server_id", srv.ID, "error", err)
			continue
		}
		for _, db := range dbs {
			if err := s.reconcileDatabaseLocked(ctx, db.ID); err != nil {
				s.log.Error("recover: reconciling database", "db_id", db.ID, "error", err)
			}
		}
	}

	active, err := s.store.ListActiveDeployments(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: listing active deployments: %w", err)
	}
	for _, dep := range active {
		switch dep.Status {
		case domain.DeployQueued:
			if err := s.tryStart(ctx, dep); err != nil {
				s.log.Error("recover: starting queued deployment", "deployment_id", dep.ID, "error", err)
			}
		case domain.DeployBuilding:
			app, err := s.store.GetApplication(ctx, dep.ApplicationID)
			if err != nil {
				s.log.Error("recover: loading application", "deployment_id", dep.ID, "error", err)
				continue
			}
			rev, err := s.store.GetRevision(ctx, dep.RevisionID)
			if err != nil {
				s.log.Error("recover: loading revision", "deployment_id", dep.ID, "error", err)
				continue
			}
			// Same assembly as start(): the republished work must carry the
			// deploy-key PEM again, or a private-repo build resumed after a
			// plane restart would clone without credentials.
			work, err := s.buildWork(ctx, dep, app, rev)
			if err != nil {
				s.fail(ctx, dep, err.Error())
				continue
			}
			data, merr := proto.Marshal(work)
			if merr != nil {
				continue
			}
			// The recorded builder, not a fresh selection: the original
			// routing may already be building (idempotent redelivery).
			if err := s.bus.PublishWork(ctx, subjects.Build(builderFor(dep, app)), dep.ID+".build", data); err != nil {
				s.log.Error("recover: republishing build", "deployment_id", dep.ID, "error", err)
			}
		case domain.DeployDistributing:
			app, err := s.store.GetApplication(ctx, dep.ApplicationID)
			if err != nil {
				s.log.Error("recover: loading application", "deployment_id", dep.ID, "error", err)
				continue
			}
			rev, err := s.store.GetRevision(ctx, dep.RevisionID)
			if err != nil {
				s.log.Error("recover: loading revision", "deployment_id", dep.ID, "error", err)
				continue
			}
			// Republish both relay items (same msg IDs: deduped inside the
			// window, idempotent beyond it — a target that already loaded the
			// image reports success from HasImage alone, spec §6).
			if err := s.startDistribute(ctx, dep, app, rev); err != nil {
				s.log.Error("recover: republishing distribute", "deployment_id", dep.ID, "error", err)
			}
		case domain.DeployRollingOut:
			app, err := s.store.GetApplication(ctx, dep.ApplicationID)
			if err != nil {
				s.log.Error("recover: loading application", "deployment_id", dep.ID, "error", err)
				continue
			}
			rev, err := s.store.GetRevision(ctx, dep.RevisionID)
			if err != nil {
				s.log.Error("recover: loading revision", "deployment_id", dep.ID, "error", err)
				continue
			}
			if err := s.startRollout(ctx, dep, app, rev); err != nil {
				s.log.Error("recover: republishing rollout", "deployment_id", dep.ID, "error", err)
			}
		case domain.DeploySucceeded, domain.DeployFailed, domain.DeployAwaitingApproval:
			// Unreachable: ListActiveDeployments excludes all three. Listed so
			// the switch names every status, and so a parked deploy's absence
			// from recovery is a stated decision rather than an omission —
			// nothing was published for it, so there is nothing to republish
			// (deploy-protection.md §3; ENGINEERING rule 15, vacuously).
		}
	}
	return nil
}

// SetDomainVerifier wires domain-ownership verification. Optional: without it
// every domain routes, which is exactly how the panel behaved before DNS
// automation existed (dns-automation.md §4.1).
func (s *Scheduler) SetDomainVerifier(v DomainVerifier) { s.dns = v }

// SetGate wires deploy protection. Optional: without it every deploy is
// admitted, which is exactly how the panel behaved before the feature existed
// (deploy-protection.md §4).
func (s *Scheduler) SetGate(g Gate) { s.gate = g }

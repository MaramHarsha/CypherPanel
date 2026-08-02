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

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ErrRevisionNotBuilt is returned when a rollback targets a revision that
// never produced an image (its build failed or never ran).
var ErrRevisionNotBuilt = errors.New("scheduler: revision was never built")

// Store is the persistence the scheduler needs (consumer-defined).
type Store interface {
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	ListApplicationsByServer(ctx context.Context, serverID string) ([]domain.Application, error)
	SetApplicationDesiredRevision(ctx context.Context, appID, revisionID string) (domain.Application, error)
	SetApplicationStatus(ctx context.Context, appID, status, detail string) error
	SetApplicationObservedStatus(ctx context.Context, appID, status, detail, observedRevisionID string, observedAt time.Time) error
	ListEnvVars(ctx context.Context, appID string) ([]domain.EnvVar, error)

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

// Notifier delivers terminal-outcome notifications (consumer-defined;
// *notify.Manager satisfies it — notifications.md §4). Optional: a nil notifier
// means the transitions still happen, just without messages. Implementations
// must not block — delivery is fire-and-forget (spec §1).
type Notifier interface {
	NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment)
	NotifyBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord)
}

// Scheduler owns pipeline state transitions. Construct with New.
type Scheduler struct {
	store  Store
	bus    Bus
	opener Opener
	log    *slog.Logger
	now    func() time.Time

	// notify is optional; guarded at every call site (may be nil).
	notify Notifier

	// mu serializes pipeline transitions: deploy requests and event handlers
	// race on the per-app queue, and the transitions are read-modify-write.
	mu sync.Mutex
}

// New wires the scheduler.
func New(st Store, b Bus, opener Opener, log *slog.Logger) *Scheduler {
	return &Scheduler{store: st, bus: b, opener: opener, log: log, now: time.Now}
}

// SetNotifier attaches an optional notifier for terminal-outcome messages. Kept
// separate from New so notifications stay an opt-in add-on (nil = disabled).
func (s *Scheduler) SetNotifier(n Notifier) { s.notify = n }

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
func (s *Scheduler) Deploy(ctx context.Context, appID, trigger, ref string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: getting application: %w", err)
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
func (s *Scheduler) Rollback(ctx context.Context, deploymentID string) (domain.Deployment, error) {
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
	dep, err := s.store.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), src.ApplicationID, rev.ID, "rollback")
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating rollback deployment: %w", err)
	}
	if serr := s.tryStart(ctx, dep); serr != nil {
		if fresh, err := s.store.GetDeployment(ctx, dep.ID); err == nil {
			return fresh, serr
		}
		return dep, serr
	}
	return s.store.GetDeployment(ctx, dep.ID)
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
	spec, err := s.buildSpec(ctx, app, rev)
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

// buildSpec assembles the wire AppSpec from the revision's immutable config
// snapshot, the built image, and the app's current (decrypted) env vars.
func (s *Scheduler) buildSpec(ctx context.Context, app domain.Application, rev domain.Revision) (*agentv1.AppSpec, error) {
	var cs configSnapshot
	if err := json.Unmarshal(rev.ConfigSnapshot, &cs); err != nil {
		return nil, fmt.Errorf("scheduler: parsing config snapshot of %s: %w", rev.ID, err)
	}
	sealed, err := s.store.ListEnvVars(ctx, app.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing env vars: %w", err)
	}
	env := make(map[string]string, len(sealed))
	for _, v := range sealed {
		plain, err := s.opener.Open(v.ValueCT, v.ValueNonce)
		if err != nil {
			// Never include the key's value context in the error (rule 20).
			return nil, fmt.Errorf("scheduler: unsealing env var %s: %w", v.Key, err)
		}
		env[v.Key] = string(plain)
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
			Domain:     cs.Route.Domain,
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
			s.log.Info("deployment succeeded", "deployment_id", dep.ID, "app_id", dep.ApplicationID, "revision_id", dep.RevisionID, "server_id", serverID)
			if s.notify != nil {
				s.notify.NotifyDeploy(ctx, app, done)
			}
			s.promoteNext(ctx, dep.ApplicationID)
			return
		}
	}
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
	// Load the app for the notification and to clear the plane-driven
	// 'deploying' override; a lookup miss just drops the notice (the failure is
	// already recorded and logged).
	if app, err := s.store.GetApplication(ctx, dep.ApplicationID); err == nil {
		s.clearDeployingStatus(ctx, app, detail)
		if s.notify != nil {
			s.notify.NotifyDeploy(ctx, app, failed)
		}
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
	spec, err := s.buildSpec(ctx, app, rev)
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
			s.log.Error("desired state: loading revision", "app_id", app.ID, "error", err)
			continue
		}
		if rev.Image == "" {
			continue
		}
		spec, err := s.buildSpec(ctx, app, rev)
		if err != nil {
			s.log.Error("desired state: building spec", "app_id", app.ID, "error", err)
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
			s.log.Error("desired state: loading db revision", "db_id", db.ID, "error", err)
			continue
		}

		defaults := domain.EngineDefaults(db.Engine, db.Version)
		env := make(map[string]string)

		if db.RequirePassword && len(db.RootPasswordCT) > 0 {
			pwd, err := s.opener.Open(db.RootPasswordCT, db.RootPasswordNonce)
			if err != nil {
				s.log.Error("desired state: decrypting db root password", "db_id", db.ID, "error", err)
				continue
			}
			if defaults.PasswordEnv != "" {
				env[defaults.PasswordEnv] = string(pwd)
			}
		}

		spec := &agentv1.DbSpec{
			DbId:          db.ID,
			EnvironmentId: db.EnvironmentID,
			RevisionId:    rev.ID,
			Engine:        string(db.Engine),
			Image:         defaults.Image,
			VolumeName:    db.VolumeName,
			DataPath:      db.DataPath,
			Network:       db.Network,
			Env:           env,
			HealthCmd:     defaults.HealthCmd,
		}

		if db.ExposePort != nil {
			spec.ExposePort = uint32(*db.ExposePort)
		}
		if db.CPULimit != nil {
			spec.CpuLimit = *db.CPULimit
		}
		if db.MemoryLimitMB != nil {
			spec.MemoryLimitMb = uint32(*db.MemoryLimitMB)
		}

		ds.DbSpecs = append(ds.DbSpecs, spec)
	}

	return proto.Marshal(ds)
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
		case domain.DeploySucceeded, domain.DeployFailed:
		}
	}
	return nil
}

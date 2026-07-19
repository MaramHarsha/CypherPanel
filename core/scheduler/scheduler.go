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
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	UpdateDeploymentStatus(ctx context.Context, id string, status domain.DeploymentStatus, detail string) (domain.Deployment, error)
	ListActiveDeployments(ctx context.Context) ([]domain.Deployment, error)
	ListActiveDeploymentsByApplication(ctx context.Context, appID string) ([]domain.Deployment, error)

	ListServers(ctx context.Context) ([]domain.Server, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)

	GetDeployKey(ctx context.Context, id string) (domain.DeployKey, error)
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

// Scheduler owns pipeline state transitions. Construct with New.
type Scheduler struct {
	store  Store
	bus    Bus
	opener Opener
	log    *slog.Logger
	now    func() time.Time

	// mu serializes pipeline transitions: deploy requests and event handlers
	// race on the per-app queue, and the transitions are read-modify-write.
	mu sync.Mutex
}

// New wires the scheduler.
func New(st Store, b Bus, opener Opener, log *slog.Logger) *Scheduler {
	return &Scheduler{store: st, bus: b, opener: opener, log: log, now: time.Now}
}

// configSnapshot is the immutable per-revision config (stored as the
// revision's config_snapshot JSON): what rollback restores. Env vars are
// deliberately not part of it — they are sealed rows applied at rollout time.
type configSnapshot struct {
	Port    int    `json:"port"`
	Network string `json:"network"`
	Route   struct {
		Domain     string `json:"domain"`
		HTTPS      bool   `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health struct {
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
	cs.Route.Domain = app.Route.Domain
	cs.Route.HTTPS = app.Route.HTTPS
	cs.Route.PathPrefix = app.Route.PathPrefix
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
	if ref == "" {
		ref = app.Source.Branch
	}
	rev, err := s.store.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, ref, snapshot)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating revision: %w", err)
	}
	dep, err := s.store.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, trigger)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating deployment: %w", err)
	}
	if err := s.tryStart(ctx, dep); err != nil {
		return dep, err
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
	if err := s.tryStart(ctx, dep); err != nil {
		return dep, err
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

	targetServer, err := s.store.GetServer(ctx, app.Runtime.ServerID)
	if err != nil {
		return fmt.Errorf("scheduler: getting target server: %w", err)
	}

	var builderID string
	if targetServer.Role == "both" {
		builderID = targetServer.ID
	} else {
		servers, err := s.store.ListServers(ctx)
		if err != nil {
			return fmt.Errorf("scheduler: listing servers for builder selection: %w", err)
		}
		for _, srv := range servers {
			if (srv.Role == "builder" || srv.Role == "both") && srv.Status == domain.StatusRunning {
				// Simple first-match selection for Phase 2.
				builderID = srv.ID
				break
			}
		}
		if builderID == "" {
			return fmt.Errorf("scheduler: no builder server available (target is docker-only)")
		}
	}

	var deployKeyPem string
	if app.Source.DeployKeyID != nil {
		dk, err := s.store.GetDeployKey(ctx, *app.Source.DeployKeyID)
		if err != nil {
			return fmt.Errorf("scheduler: getting deploy key: %w", err)
		}
		priv, err := s.opener.Open(dk.PrivateKeyCT, dk.PrivateKeyNonce)
		if err != nil {
			return fmt.Errorf("scheduler: unsealing deploy key: %w", err)
		}
		deployKeyPem = string(priv)
	}

	work := &agentv1.BuildWork{
		DeploymentId:   dep.ID,
		AppId:          app.ID,
		RepoUrl:        app.Source.Repo,
		CommitSha:      rev.SourceCommit,
		DockerfilePath: app.Build.DockerfilePath,
		BuildContext:   app.Build.Context,
		Image:          imageTag(app.ID, rev.ID),
		DeployKeyPem:   deployKeyPem,
		UploadRelay:    builderID != app.Runtime.ServerID,
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

// startDistribute transitions a deployment to distributing and publishes the
// distribute work item to the target server. It also starts the relay.
func (s *Scheduler) startDistribute(ctx context.Context, dep domain.Deployment, app domain.Application, rev domain.Revision, builderID string) error {
	if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployDistributing, ""); err != nil {
		return err
	}
	
	// Relay stream is created by bus, target will pull from it.
	work := &agentv1.DistributeWork{
		DeploymentId:   dep.ID,
		AppId:          app.ID,
		Image:          imageTag(app.ID, rev.ID),
		SourceServerId: builderID,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling distribute work: %w", err)
	}
	
	if err := s.bus.PublishWork(ctx, subjects.Distribute(app.Runtime.ServerID), dep.ID+".distribute", data); err != nil {
		return fmt.Errorf("scheduler: publishing distribute: %w", err)
	}
	return nil
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
	return &agentv1.AppSpec{
		AppId:         app.ID,
		EnvironmentId: app.EnvironmentID,
		RevisionId:    rev.ID,
		Image:         image,
		Port:          uint32(cs.Port),
		Env:           env,
		Network:       cs.Network,
		Health: &agentv1.HealthCheck{
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
	}, nil
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
	eventServer, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		s.log.Error("deploy event: loading server", "server_id", serverID, "error", err)
		return
	}
	
	// A compromised agent's blast radius stays its own workloads (threat-model §5.2).
	// Build and relay upload events can come from any authorized builder.
	// Distribute and rollout events must come from the application's runtime server.
	switch ev.GetStage() {
	case agentv1.DeployEvent_STAGE_BUILD, agentv1.DeployEvent_STAGE_RELAY_UPLOAD:
		if eventServer.Role != "both" && eventServer.Role != "builder" && app.Runtime.ServerID != serverID {
			s.log.Warn("build/upload event from unauthorized server",
				"deployment_id", dep.ID, "app_id", app.ID, "reported_by", serverID)
			return
		}
	default:
		if app.Runtime.ServerID != serverID {
			s.log.Warn("deploy event from a server the app does not run on",
				"deployment_id", dep.ID, "app_id", app.ID, "reported_by", serverID, "runs_on", app.Runtime.ServerID)
			return
		}
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
		if app.Runtime.ServerID == serverID {
			// Local build, no relay needed.
			if err := s.startRollout(ctx, dep, app, rev); err != nil {
				s.log.Error("deploy event: advancing to rollout", "deployment_id", dep.ID, "error", err)
			}
		} else {
			// Built on a dedicated builder. Wait for the relay upload to finish.
			// The builder will publish STAGE_RELAY_UPLOAD next.
		}

	case agentv1.DeployEvent_STAGE_RELAY_UPLOAD:
		if dep.Status != domain.DeployBuilding {
			return
		}
		if ev.GetOutcome() == agentv1.DeployEvent_OUTCOME_FAILED {
			s.fail(ctx, dep, "relay upload failed: "+ev.GetDetail())
			return
		}
		rev, err := s.store.GetRevision(ctx, dep.RevisionID)
		if err != nil {
			s.log.Error("deploy event: getting revision for distribute", "deployment_id", dep.ID, "error", err)
			return
		}
		// Upload finished; start distribution to the target server.
		if err := s.startDistribute(ctx, dep, app, rev, serverID); err != nil {
			s.log.Error("deploy event: advancing to distribute", "deployment_id", dep.ID, "error", err)
		}

	case agentv1.DeployEvent_STAGE_DISTRIBUTE, agentv1.DeployEvent_STAGE_RELAY_DOWNLOAD:
		if dep.Status != domain.DeployDistributing {
			return
		}
		if ev.GetOutcome() == agentv1.DeployEvent_OUTCOME_FAILED {
			s.fail(ctx, dep, "distribution failed: "+ev.GetDetail())
			return
		}
		rev, err := s.store.GetRevision(ctx, dep.RevisionID)
		if err != nil {
			s.log.Error("deploy event: getting revision for rollout", "deployment_id", dep.ID, "error", err)
			return
		}
		if err := s.startRollout(ctx, dep, app, rev); err != nil {
			s.log.Error("deploy event: advancing to rollout", "deployment_id", dep.ID, "error", err)
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
		// Removals have no deployment.

	case agentv1.DeployEvent_STAGE_UNSPECIFIED:
	}
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
			if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeploySucceeded, ""); err != nil {
				s.log.Error("app status: completing deployment", "deployment_id", dep.ID, "error", err)
				return
			}
			s.log.Info("deployment succeeded", "deployment_id", dep.ID, "app_id", dep.ApplicationID, "revision_id", dep.RevisionID, "server_id", serverID)
			s.promoteNext(ctx, dep.ApplicationID)
			return
		}
	}
}

// fail terminates a deployment and promotes the next queued one. The
// application's own status stays observation-driven: the previous revision
// keeps serving and the agent's reports say so. Callers hold s.mu.
func (s *Scheduler) fail(ctx context.Context, dep domain.Deployment, detail string) {
	if _, err := s.store.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployFailed, detail); err != nil {
		s.log.Error("failing deployment", "deployment_id", dep.ID, "error", err)
		return
	}
	s.log.Warn("deployment failed", "deployment_id", dep.ID, "app_id", dep.ApplicationID, "detail", detail)
	s.promoteNext(ctx, dep.ApplicationID)
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
			work := &agentv1.BuildWork{
				DeploymentId:   dep.ID,
				AppId:          app.ID,
				RepoUrl:        app.Source.Repo,
				CommitSha:      rev.SourceCommit,
				DockerfilePath: app.Build.DockerfilePath,
				BuildContext:   app.Build.Context,
				Image:          imageTag(app.ID, rev.ID),
			}
			data, merr := proto.Marshal(work)
			if merr != nil {
				continue
			}
			if err := s.bus.PublishWork(ctx, subjects.Build(app.Runtime.ServerID), dep.ID+".build", data); err != nil {
				s.log.Error("recover: republishing build", "deployment_id", dep.ID, "error", err)
			}
		case domain.DeployRollingOut, domain.DeployDistributing:
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

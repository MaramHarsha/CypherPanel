// Package worker consumes work items from the control plane, maintains the
// local desired-application set, and drives the orchestrator reconciler to
// converge reality — reporting only what it observes (ADR-005).
//
// The worker talks to the data plane through the small consumer-defined Bus
// seam (ENGINEERING rule 6), never a concrete NATS type: the production
// implementation (natsBus) wraps the agent's *nats.Conn and its JetStream
// pull subscription, and tests drive the full dispatch/ack logic against a
// fake — so the agent module never depends on the NATS *server*.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/MaramHarsha/cypherpanel/agent/builder"
	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ErrNoWork is what a Bus returns from FetchWork when the fetch deadline
// elapsed with nothing delivered — a normal idle, not a failure.
var ErrNoWork = errors.New("worker: no work available")

// Message is one delivered work item. The worker decides its fate: Ack on
// success, NakWithDelay to retry after a backoff, Term to drop it for good
// (poison message). InProgress resets the redelivery timer during long work.
type Message interface {
	Subject() string
	Data() []byte
	Ack() error
	Term() error
	NakWithDelay(delay time.Duration) error
	InProgress() error
	// NumDelivered is how many times this item has been delivered (1 on the
	// first try); it drives the poison-message cutoff.
	NumDelivered() uint64
}

// ImageRelay moves images through the plane's transient relay for
// multi-server deployments (consumer-defined; *relay.Client satisfies it —
// builder-role-and-relay.md §3). Both operations are idempotent under
// redelivery. A worker without one (no plane address persisted) fails relay
// work items with a clear error instead of guessing.
type ImageRelay interface {
	PushImage(ctx context.Context, deploymentID, image string) error
	PullImage(ctx context.Context, deploymentID, image string) error
}

// BackupRunner executes one database backup or restore and returns the terminal
// event to report (consumer-defined; *docker.BackupExecutor satisfies it —
// managed-databases.md §7). Nil on nodes that run no databases.
type BackupRunner interface {
	ExecuteBackup(ctx context.Context, work *agentv1.DbBackupWork) *agentv1.DbBackupEvent
	// ExecuteRestore reports each step it reaches through progress before
	// returning the terminal event. A restore takes the database offline, so
	// how far along it is is the answer someone is waiting for.
	ExecuteRestore(ctx context.Context, work *agentv1.DbRestoreWork, progress func(*agentv1.DbRestoreEvent)) *agentv1.DbRestoreEvent
	ExecutePrune(ctx context.Context, work *agentv1.DbBackupPruneWork) *agentv1.DbBackupPruneEvent
}

// Bus is everything the worker needs from the data-plane connection
// (consumer-defined). natsBus is the production implementation.
type Bus interface {
	// Request performs a request/reply — the desired-state sync on connect.
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)
	// Publish sends a fire-and-forget message (app statuses, deploy events,
	// build/runtime logs).
	Publish(subject string, data []byte) error
	// FetchWork blocks up to an internal deadline for the next work item,
	// returning ErrNoWork on an idle timeout so the caller can loop.
	FetchWork(ctx context.Context) (Message, error)
}

// maxDeliveries is the poison-message cutoff: a work item that fails to
// reconcile this many times is Term'd rather than redelivered forever.
const maxDeliveries = 3

// errNoRelay marks a host that cannot reach the plane's relay (no persisted
// plane address). Redelivery cannot fix a missing address, so the item fails
// immediately with the remedy in the detail.
var errNoRelay = errors.New("no relay configured: set CYPHER_PLANE_ADDR or re-enroll the agent")

// relayTransferTimeout caps one relay attempt end to end; an expired attempt
// redelivers and retries with a fresh session (builder-role-and-relay.md §6).
const relayTransferTimeout = 15 * time.Minute

// defaultDriftInterval bounds how stale the node may drift between work items:
// with no work arriving, the worker still re-converges (retrying failed GC and
// drains) and re-publishes observed statuses this often, so the plane's view
// of an app never fossilizes at deploy time (ADR-005: status is observation).
const defaultDriftInterval = 60 * time.Second

// Worker consumes work items, manages the local desired state, and invokes the
// orchestrator driver to converge reality.
type Worker struct {
	bus           Bus
	serverID      string
	driver        driver.Reconciler
	dbReconciler  driver.DbReconciler
	composeRec    driver.ComposeReconciler
	backup        BackupRunner
	builder       *builder.Builder
	relay         ImageRelay
	log           *slog.Logger
	driftInterval time.Duration
	cron          CronRunner
	proxyTLS      ProxyTLS

	mu           sync.Mutex
	state        map[string]*agentv1.AppSpec     // map[app_id]spec
	dbState      map[string]*agentv1.DbSpec      // map[db_id]spec
	composeState map[string]*agentv1.ComposeSpec // map[stack_id]spec
}

// CronRunner arms scheduled tasks from desired state and fires them on schedule
// (consumer-defined; *cron.Runner satisfies it — scheduled-tasks.md, ADR-011).
// Optional: nil on builder-role agents and when the feature is unwired.
type CronRunner interface {
	Sync(specs []*agentv1.AppSpec)
	Run(ctx context.Context)
}

// ProxyTLS receives the panel's ACME account from desired state
// (consumer-defined; *proxy.Traefik satisfies it — agent-identity-and-tls.md
// §4). Deliberately not part of driver.Reconciler: the account is node-wide,
// not per-application, and it is the Proxy — not the orchestrator driver — that
// acts on it. Optional: nil on builder-role agents, which run no Proxy.
type ProxyTLS interface {
	// SetACME records the account. Called on every desired-state sync, so it
	// must be idempotent and must not block.
	SetACME(acmeEmail, acmeCAServer string)
}

// New creates a new Worker. drv is nil on builder-role agents (nothing runs
// there — builder-role-and-relay.md §1); dbRec/bkp are nil when the agent runs
// no databases; bld is nil on worker-role agents; rly is nil when the agent has
// no plane relay address.
func New(bus Bus, serverID string, drv driver.Reconciler, dbRec driver.DbReconciler, bkp BackupRunner, bld *builder.Builder, rly ImageRelay, log *slog.Logger) *Worker {
	return &Worker{
		bus:           bus,
		serverID:      serverID,
		driver:        drv,
		dbReconciler:  dbRec,
		backup:        bkp,
		builder:       bld,
		relay:         rly,
		log:           log,
		driftInterval: defaultDriftInterval,
		state:         make(map[string]*agentv1.AppSpec),
		dbState:       make(map[string]*agentv1.DbSpec),
		composeState:  make(map[string]*agentv1.ComposeSpec),
	}
}

// SetComposeReconciler attaches the Compose Stack reconciler. Kept out of New
// for the reason cron is: it is an opt-in add-on wired only on app-role agents,
// and a node without it behaves exactly as it did before the feature existed
// (compose-stacks.md §4).
func (w *Worker) SetComposeReconciler(c driver.ComposeReconciler) { w.composeRec = c }

// SetCron attaches the scheduled-task runner. Kept out of New so cron stays an
// opt-in add-on wired only on app-role agents (scheduled-tasks.md §5).
func (w *Worker) SetCron(c CronRunner) { w.cron = c }

// SetProxyTLS attaches the Proxy's TLS settings sink, wired only on nodes that
// run a Proxy (agent-identity-and-tls.md §4).
func (w *Worker) SetProxyTLS(p ProxyTLS) { w.proxyTLS = p }

// Run performs an initial desired-state sync, converges once on boot, then
// processes work items until the context is canceled. Between work items it
// runs a periodic drift reconcile on the same goroutine (the driver is not
// built for concurrent convergence), so idle nodes still self-heal.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.sync(ctx); err != nil {
		return fmt.Errorf("worker: initial sync failed: %w", err)
	}
	if w.cron != nil {
		go w.cron.Run(ctx) // fires scheduled tasks on its own clock (ADR-011)
	}
	lastConverge := time.Now()

	w.log.Info("worker consuming work items", "consumer", subjects.WorkConsumer(w.serverID))

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		msg, err := w.bus.FetchWork(ctx)
		if err != nil {
			// Shutdown is not idleness: a drift reconcile against a canceled
			// context would only fail and log noise, so leave immediately.
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if errors.Is(err, ErrNoWork) {
				if time.Since(lastConverge) >= w.driftInterval {
					if rerr := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); rerr != nil {
						w.log.Error("worker: drift reconcile", "error", rerr)
					}
					lastConverge = time.Now()
				}
				continue
			}
			w.log.Error("worker: fetching work", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second): // backoff
			}
			continue
		}
		w.handleMsg(ctx, msg)
		lastConverge = time.Now()
	}
}

// sync fetches the desired set and converges on it, retrying the convergence
// until it succeeds. It is the boot path: an agent that cannot reach desired
// state has nothing else to do, so it keeps trying.
func (w *Worker) sync(ctx context.Context) error {
	if err := w.syncState(ctx); err != nil {
		return err
	}
	// Converge once with no trigger to reach desired state on boot.
	for {
		if err := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err == nil {
			return nil
		}
		w.log.Error("worker: initial reconcile failed, retrying in 2s")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// syncState fetches the authoritative desired set from the plane and replaces
// what this agent holds. It does not converge — the caller decides whether a
// failed convergence should be retried in place (boot) or handed back to the
// work item that asked for it (a resync nudge).
func (w *Worker) syncState(ctx context.Context) error {
	data, err := w.bus.Request(ctx, subjects.Sync(w.serverID), nil)
	if err != nil {
		return err
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		return fmt.Errorf("unmarshaling desired state: %w", err)
	}

	// The sync reply is the COMPLETE desired set, so it replaces what is held
	// rather than merging into it (ADR-005: absence means removal). On boot the
	// maps are empty and the two are the same; on a re-sync — a resync nudge —
	// merging would resurrect an application the plane has since removed.
	state := make(map[string]*agentv1.AppSpec, len(ds.Specs))
	for _, spec := range ds.Specs {
		state[spec.AppId] = spec
	}
	dbState := make(map[string]*agentv1.DbSpec, len(ds.DbSpecs))
	for _, spec := range ds.DbSpecs {
		dbState[spec.DbId] = spec
	}
	composeState := make(map[string]*agentv1.ComposeSpec, len(ds.ComposeSpecs))
	for _, spec := range ds.ComposeSpecs {
		composeState[spec.StackId] = spec
	}
	w.mu.Lock()
	w.state, w.dbState, w.composeState = state, dbState, composeState
	w.mu.Unlock()

	// Node-wide TLS settings ride along with the desired set: one panel, one
	// ACME account, every node (agent-identity-and-tls.md §4). An empty
	// acme_email is a meaningful value — "no certificate resolver" — so it is
	// applied exactly like a non-empty one.
	if w.proxyTLS != nil {
		w.proxyTLS.SetACME(ds.GetTls().GetAcmeEmail(), ds.GetTls().GetAcmeCaServer())
	}

	w.log.Info("worker: desired-state sync complete",
		"apps", len(ds.Specs),
		"databases", len(ds.DbSpecs),
		"compose_stacks", len(ds.ComposeSpecs),
		"tls_configured", ds.GetTls().GetAcmeEmail() != "",
	)
	return nil
}

func (w *Worker) handleMsg(ctx context.Context, msg Message) {
	subject := msg.Subject()
	w.log.Info("worker: received work item", "subject", subject)

	var deploymentID string
	var appID string
	var stage agentv1.DeployEvent_Stage
	var commitSha string

	switch {
	case strings.HasSuffix(subject, ".rollout"):
		var work agentv1.RolloutWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling rollout work", "error", err)
			_ = msg.Term()
			return
		}
		if work.Spec == nil {
			w.log.Error("worker: rollout work missing spec")
			_ = msg.Term()
			return
		}
		deploymentID = work.DeploymentId
		appID = work.Spec.AppId
		stage = agentv1.DeployEvent_STAGE_ROLLOUT

		w.mu.Lock()
		w.state[appID] = work.Spec
		w.mu.Unlock()

	case strings.HasSuffix(subject, ".compose.converge"):
		// A stack's desired state, re-declared so a deploy lands promptly.
		// Carries no command verb: the reconciler owns its own invocation and
		// this only says which file to converge toward (compose-stacks.md §4).
		var work agentv1.ComposeConvergeWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil || work.Spec == nil {
			w.log.Error("worker: unmarshaling compose converge work", "error", err)
			_ = msg.Term()
			return
		}
		if w.composeRec == nil {
			w.log.Error("worker: received compose work but no compose reconciler is wired")
			_ = msg.Term()
			return
		}
		w.mu.Lock()
		if w.composeState == nil {
			w.composeState = map[string]*agentv1.ComposeSpec{}
		}
		w.composeState[work.Spec.StackId] = work.Spec
		w.mu.Unlock()
		if err := w.reconcileCompose(ctx); err != nil {
			w.log.Error("worker: compose converge reconcile", "stack_id", work.Spec.StackId, "error", err)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".compose.remove"):
		var work agentv1.ComposeRemoveWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling compose remove work", "error", err)
			_ = msg.Term()
			return
		}
		if w.composeRec == nil {
			_ = msg.Term()
			return
		}
		w.mu.Lock()
		delete(w.composeState, work.StackId)
		w.mu.Unlock()
		if err := w.composeRec.Remove(ctx, work.StackId, work.DeleteVolumes); err != nil {
			w.log.Error("worker: compose remove", "stack_id", work.StackId, "error", err)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".converge"):
		// Re-declared desired state without a deployment (ConvergeWork): update
		// the spec and reconcile silently — no deploy event. Propagates a
		// scheduled-task change; the container reconcile is a no-op
		// (scheduled-tasks.md §4).
		var work agentv1.ConvergeWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil || work.Spec == nil {
			w.log.Error("worker: unmarshaling converge work", "error", err)
			_ = msg.Term()
			return
		}
		w.mu.Lock()
		w.state[work.Spec.AppId] = work.Spec
		w.mu.Unlock()
		if err := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err != nil {
			w.log.Error("worker: converge reconcile", "error", err)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".resync"):
		// Re-read the authoritative desired set and converge. Idempotent by
		// construction: it is the same thing the agent does on connect, so a
		// redelivered nudge costs one request and changes nothing
		// (agent-identity-and-tls.md §4).
		var work agentv1.ResyncWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling resync work", "error", err)
			_ = msg.Term()
			return
		}
		w.log.Info("worker: re-reading desired state", "reason", work.GetReason())
		if err := w.syncState(ctx); err != nil {
			w.log.Error("worker: resync failed", "error", err)
			if msg.NumDelivered() >= maxDeliveries {
				_ = msg.Term()
				return
			}
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		if err := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err != nil {
			// The new desired state is already held; only the convergence
			// failed, and the drift loop retries that every cycle anyway. NAK
			// so the nudge is redelivered and the node converges sooner.
			w.log.Error("worker: resync reconcile", "error", err)
			if msg.NumDelivered() >= maxDeliveries {
				_ = msg.Term()
				return
			}
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".remove") && !strings.HasSuffix(subject, ".db.remove"):
		// The app-remove suffix ".remove" is also a suffix of the database
		// work subject ".db.remove" — exclude it so database removes reach
		// their own case below, not the app reconciler.
		var work agentv1.RemoveWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling remove work", "error", err)
			_ = msg.Term()
			return
		}
		deploymentID = work.DeploymentId
		appID = work.AppId
		stage = agentv1.DeployEvent_STAGE_REMOVE

		w.mu.Lock()
		delete(w.state, appID)
		w.mu.Unlock()

	case strings.HasSuffix(subject, ".push"):
		var work agentv1.PushImageWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling push work", "error", err)
			_ = msg.Term()
			return
		}
		w.handleRelay(ctx, msg, work.DeploymentId, work.AppId, "push", func(ctx context.Context) error {
			if w.relay == nil {
				return errNoRelay
			}
			return w.relay.PushImage(ctx, work.DeploymentId, work.Image)
		})
		return

	case strings.HasSuffix(subject, ".distribute"):
		var work agentv1.DistributeWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling distribute work", "error", err)
			_ = msg.Term()
			return
		}
		w.handleRelay(ctx, msg, work.DeploymentId, work.AppId, "distribute", func(ctx context.Context) error {
			if w.relay == nil {
				return errNoRelay
			}
			return w.relay.PullImage(ctx, work.DeploymentId, work.Image)
		})
		return

	case strings.HasSuffix(subject, ".build"):
		var work agentv1.BuildWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling build work", "error", err)
			_ = msg.Term()
			return
		}
		deploymentID = work.DeploymentId
		appID = work.AppId
		stage = agentv1.DeployEvent_STAGE_BUILD
		commitSha = work.CommitSha

		// Stream build logs to the plane; keep the message alive across a long
		// build (msg.InProgress) so the WORK consumer's AckWait can't redeliver
		// it and trigger a concurrent rebuild.
		logSubject := subjects.BuildLog(w.serverID, deploymentID)
		onLog := func(line string) {
			_ = w.bus.Publish(logSubject, []byte(line))
			_ = msg.InProgress()
		}

		if w.builder == nil {
			w.log.Warn("worker: received build work but builder is nil")
			w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, "builder is nil", commitSha)
			_ = msg.Ack()
			return
		}
		resolvedSha, err := w.builder.Build(ctx, &work, onLog)
		if err != nil {
			w.log.Error("worker: build failed", "error", err)
			w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), commitSha)
			_ = msg.Ack()
			return
		}
		w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_SUCCEEDED, "", resolvedSha)
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".db.provision"):
		var work agentv1.DbProvisionWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling db provision work", "error", err)
			_ = msg.Term()
			return
		}
		if work.Spec == nil {
			w.log.Error("worker: db provision work missing spec")
			_ = msg.Term()
			return
		}
		w.mu.Lock()
		w.dbState[work.Spec.DbId] = work.Spec
		w.mu.Unlock()

		if w.dbReconciler == nil {
			w.log.Error("worker: received db provision work but dbReconciler is nil")
			_ = msg.Term()
			return
		}
		// Database provisioning is independent of the app driver: converge the
		// database set directly and return, so a database-only node (no app
		// driver) is not caught by the driver-nil guard below.
		if err := w.reconcileDatabases(ctx); err != nil {
			w.log.Error("worker: db provision reconcile failed", "db_id", work.Spec.DbId, "error", err)
			if msg.NumDelivered() >= maxDeliveries {
				_ = msg.Term()
			} else {
				_ = msg.NakWithDelay(5 * time.Second)
			}
			return
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".db.remove"):
		var work agentv1.DbRemoveWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling db remove work", "error", err)
			_ = msg.Term()
			return
		}
		w.mu.Lock()
		delete(w.dbState, work.DbId)
		w.mu.Unlock()

		if w.dbReconciler == nil {
			w.log.Error("worker: received db remove work but dbReconciler is nil")
			_ = msg.Term()
			return
		}

		if err := w.dbReconciler.RemoveDatabase(ctx, work.DbId, work.DeleteVolume); err != nil {
			w.log.Error("worker: failed to remove database", "db_id", work.DbId, "error", err)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}

		// Once successfully removed, publish final stopped status.
		status := &agentv1.DbStatus{
			DbId:       work.DbId,
			State:      "stopped",
			ObservedAt: timestamppb.Now(),
		}
		if data, err := proto.Marshal(status); err == nil {
			_ = w.bus.Publish(subjects.DbState(w.serverID, work.DbId), data)
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".db.backup.prune"):
		// Retention sweep: delete specific S3 objects. Idempotent, so redelivery
		// is safe. Reports which keys were removed; the plane deletes only those
		// rows (self-healing — failures retry on the next prune).
		var work agentv1.DbBackupPruneWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling db backup prune work", "error", err)
			_ = msg.Term()
			return
		}
		if w.backup == nil {
			w.log.Error("worker: received db backup prune work but no backup runner")
			_ = msg.Term()
			return
		}
		event := w.runWithHeartbeat(ctx, msg, func(ctx context.Context) proto.Message {
			return w.backup.ExecutePrune(ctx, &work)
		})
		if data, err := proto.Marshal(event); err == nil {
			_ = w.bus.Publish(subjects.DbBackupPruneState(w.serverID), data)
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".db.backup"):
		var work agentv1.DbBackupWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling db backup work", "error", err)
			_ = msg.Term()
			return
		}
		if w.backup == nil {
			w.log.Error("worker: received db backup work but no backup runner")
			_ = msg.Term()
			return
		}
		// Keep the item in-flight across a long dump/upload, then report the
		// terminal outcome the plane records; the run itself is idempotent
		// (overwrites the same S3 key and in-container temp file).
		event := w.runWithHeartbeat(ctx, msg, func(ctx context.Context) proto.Message {
			return w.backup.ExecuteBackup(ctx, &work)
		})
		if data, err := proto.Marshal(event); err == nil {
			_ = w.bus.Publish(subjects.DbBackupState(w.serverID), data)
		}
		_ = msg.Ack()
		return

	case strings.HasSuffix(subject, ".db.restore"):
		var work agentv1.DbRestoreWork
		if err := proto.Unmarshal(msg.Data(), &work); err != nil {
			w.log.Error("worker: unmarshaling db restore work", "error", err)
			_ = msg.Term()
			return
		}
		if w.backup == nil {
			w.log.Error("worker: received db restore work but no backup runner")
			_ = msg.Term()
			return
		}
		event := w.runWithHeartbeat(ctx, msg, func(ctx context.Context) proto.Message {
			return w.backup.ExecuteRestore(ctx, &work, func(ev *agentv1.DbRestoreEvent) {
				// Best-effort: a dropped progress event costs the screen a
				// step, never the restore. The terminal event is what the
				// record is closed on.
				if data, err := proto.Marshal(ev); err == nil {
					_ = w.bus.Publish(subjects.DbRestoreState(w.serverID), data)
				}
			})
		})
		if data, err := proto.Marshal(event); err == nil {
			_ = w.bus.Publish(subjects.DbRestoreState(w.serverID), data)
		}
		_ = msg.Ack()
		return

	default:
		w.log.Warn("worker: unknown work subject", "subject", subject)
		_ = msg.Term()
		return
	}

	if w.driver == nil {
		// Runtime work routed to a builder-role agent is a plane-side routing
		// bug, not a transient fault: report it and drop the item.
		w.log.Error("worker: runtime work on an agent with no driver (role builder)", "subject", subject)
		w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, "agent has role builder: it runs no applications", commitSha)
		_ = msg.Term()
		return
	}
	if err := w.reconcile(ctx, deploymentID, appID, stage, commitSha); err != nil {
		w.log.Error("worker: reconcile failed completely", "error", err)
		if stage != agentv1.DeployEvent_STAGE_UNSPECIFIED {
			w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), commitSha)
		}
		if msg.NumDelivered() >= maxDeliveries {
			w.log.Error("worker: reconcile failed persistently, terminating message", "deliveries", msg.NumDelivered())
			_ = msg.Term()
		} else {
			_ = msg.NakWithDelay(5 * time.Second)
		}
		return
	}
	_ = msg.Ack()
}

func (w *Worker) reconcile(ctx context.Context, triggerDeploymentID, triggerAppID string, stage agentv1.DeployEvent_Stage, commitSha string) error {
	// Applications: only on nodes that run them (builder-role agents have no
	// app driver — builder-role-and-relay.md §1). Databases converge
	// separately below, so a database-only node still self-heals.
	var statuses []*agentv1.AppStatus
	if w.driver != nil {
		w.mu.Lock()
		desired := make([]*agentv1.AppSpec, 0, len(w.state))
		for _, spec := range w.state {
			desired = append(desired, spec)
		}
		w.mu.Unlock()

		// Re-arm the scheduled-task set from the same desired state, so
		// create/edit/delete changes (carried on AppSpec) take effect
		// (scheduled-tasks.md §5). Cheap and lock-free from the driver's view.
		if w.cron != nil {
			w.cron.Sync(desired)
		}

		var err error
		statuses, err = w.driver.Reconcile(ctx, desired)
		if err != nil {
			return err // total orchestrator failure
		}

		// Publish observed statuses (ADR-005: the plane asserts outcomes only
		// from these observations).
		for _, status := range statuses {
			data, err := proto.Marshal(status)
			if err != nil {
				w.log.Error("worker: marshaling app status", "error", err)
				continue
			}
			if err := w.bus.Publish(subjects.AppState(w.serverID, status.AppId), data); err != nil {
				w.log.Error("worker: publishing app status", "app_id", status.AppId, "error", err)
			}
		}
	}

	// Phase 3: Managed Databases (managed-databases.md §6). Independent of the
	// app driver; a no-op when this node runs no databases.
	if err := w.reconcileDatabases(ctx); err != nil {
		w.log.Error("worker: database reconcile failed", "error", err)
	}

	// V1: Compose Stacks (compose-stacks.md §4). Also independent of the app
	// driver, and a no-op when this node runs no stacks.
	if err := w.reconcileCompose(ctx); err != nil {
		w.log.Error("worker: compose reconcile failed", "error", err)
	}

	// Publish the terminal outcome for the triggering app work item, if any. A
	// failed rollout or teardown surfaces as the triggered app's 'error'
	// AppStatus; anything else is success.
	if stage != agentv1.DeployEvent_STAGE_UNSPECIFIED {
		outcome := agentv1.DeployEvent_OUTCOME_SUCCEEDED
		var detail string
		for _, st := range statuses {
			if st.AppId == triggerAppID {
				if st.State == "error" {
					outcome = agentv1.DeployEvent_OUTCOME_FAILED
					detail = st.Detail
				}
				break
			}
		}
		w.emitEvent(triggerDeploymentID, triggerAppID, stage, outcome, detail, commitSha)
	}

	return nil
}

// reconcileDatabases converges the local database set toward w.dbState and
// publishes each observed DbStatus. A no-op when this node has no database
// reconciler. The returned error is reserved for a total daemon failure; the
// caller decides whether to retry the triggering work item.
func (w *Worker) reconcileDatabases(ctx context.Context) error {
	if w.dbReconciler == nil {
		return nil
	}
	w.mu.Lock()
	desiredDbs := make([]*agentv1.DbSpec, 0, len(w.dbState))
	for _, spec := range w.dbState {
		desiredDbs = append(desiredDbs, spec)
	}
	w.mu.Unlock()

	dbStatuses, err := w.dbReconciler.ReconcileDatabases(ctx, desiredDbs)
	if err != nil {
		return err
	}
	for _, status := range dbStatuses {
		data, err := proto.Marshal(status)
		if err != nil {
			w.log.Error("worker: marshaling db status", "error", err)
			continue
		}
		if err := w.bus.Publish(subjects.DbState(w.serverID, status.DbId), data); err != nil {
			w.log.Error("worker: publishing db status", "db_id", status.DbId, "error", err)
		}
	}
	return nil
}

// reconcileCompose converges the local Compose Stack set toward
// w.composeState and reports what it observed (compose-stacks.md §4).
func (w *Worker) reconcileCompose(ctx context.Context) error {
	if w.composeRec == nil {
		return nil
	}
	w.mu.Lock()
	desired := make([]*agentv1.ComposeSpec, 0, len(w.composeState))
	for _, spec := range w.composeState {
		desired = append(desired, spec)
	}
	w.mu.Unlock()

	statuses, err := w.composeRec.Reconcile(ctx, desired)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		data, merr := proto.Marshal(status)
		if merr != nil {
			w.log.Error("worker: marshaling compose status", "error", merr)
			continue
		}
		if perr := w.bus.Publish(subjects.ComposeState(w.serverID, status.StackId), data); perr != nil {
			w.log.Error("worker: publishing compose status", "stack_id", status.StackId, "error", perr)
		}
	}
	return nil
}

// runWithHeartbeat runs a backup/restore to completion while keeping the work
// item in-flight (periodic InProgress), so a long dump/upload can't be
// redelivered mid-run. The goroutine is fully stopped before the caller acks.
func (w *Worker) runWithHeartbeat(ctx context.Context, msg Message, run func(context.Context) proto.Message) proto.Message {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = msg.InProgress()
			}
		}
	}()
	event := run(ctx)
	close(stop)
	<-done
	return event
}

// handleRelay runs one relay work item (push on builders, distribute on
// targets) with the worker's standard delivery discipline: InProgress
// heartbeats across the transfer, NAK-with-backoff on transient failure, a
// terminal STAGE_DISTRIBUTE failure at the poison cutoff. Only a target's
// success emits an event — it alone proves the image is where it must run
// (builder-role-and-relay.md §2).
func (w *Worker) handleRelay(ctx context.Context, msg Message, deploymentID, appID, kind string, run func(context.Context) error) {
	w.log.Info("worker: relay work", "kind", kind, "deployment_id", deploymentID)
	tctx, cancel := context.WithTimeout(ctx, relayTransferTimeout)
	defer cancel()

	// Keep the item alive across a long transfer so AckWait can't redeliver
	// it mid-stream and race the active session. The goroutine must be fully
	// stopped before the item is acked/termed/naked below — an InProgress
	// racing a terminal disposition would reset a cursor we just settled.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = msg.InProgress()
			}
		}
	}()
	err := run(tctx)
	close(stop)
	<-done

	if err != nil {
		if errors.Is(err, errNoRelay) {
			w.log.Error("worker: relay unavailable", "kind", kind, "error", err)
			w.emitEvent(deploymentID, appID, agentv1.DeployEvent_STAGE_DISTRIBUTE, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), "")
			_ = msg.Term()
			return
		}
		w.log.Error("worker: relay transfer failed", "kind", kind, "deployment_id", deploymentID, "error", err)
		if msg.NumDelivered() >= maxDeliveries {
			w.emitEvent(deploymentID, appID, agentv1.DeployEvent_STAGE_DISTRIBUTE, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), "")
			_ = msg.Term()
			return
		}
		_ = msg.NakWithDelay(5 * time.Second)
		return
	}
	if kind == "distribute" {
		w.emitEvent(deploymentID, appID, agentv1.DeployEvent_STAGE_DISTRIBUTE, agentv1.DeployEvent_OUTCOME_SUCCEEDED, "", "")
	}
	_ = msg.Ack()
}

func (w *Worker) emitEvent(deploymentID, appID string, stage agentv1.DeployEvent_Stage, outcome agentv1.DeployEvent_Outcome, detail, commitSha string) {
	ev := &agentv1.DeployEvent{
		DeploymentId: deploymentID,
		AppId:        appID,
		Stage:        stage,
		Outcome:      outcome,
		Detail:       detail,
		CommitSha:    commitSha,
	}
	data, err := proto.Marshal(ev)
	if err != nil {
		w.log.Error("worker: marshaling deploy event", "error", err)
		return
	}
	if err := w.bus.Publish(subjects.DeployState(w.serverID), data); err != nil {
		w.log.Error("worker: publishing deploy event", "deployment_id", deploymentID, "error", err)
	}
}

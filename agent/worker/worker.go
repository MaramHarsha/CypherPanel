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

// Worker consumes work items, manages the local desired state, and invokes the
// orchestrator driver to converge reality.
type Worker struct {
	bus      Bus
	serverID string
	driver   driver.Reconciler
	builder  *builder.Builder
	log      *slog.Logger

	mu    sync.Mutex
	state map[string]*agentv1.AppSpec // map[app_id]spec
}

// New creates a new Worker.
func New(bus Bus, serverID string, drv driver.Reconciler, bld *builder.Builder, log *slog.Logger) *Worker {
	return &Worker{
		bus:      bus,
		serverID: serverID,
		driver:   drv,
		builder:  bld,
		log:      log,
		state:    make(map[string]*agentv1.AppSpec),
	}
}

// Run performs an initial desired-state sync, converges once on boot, then
// processes work items until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.sync(ctx); err != nil {
		return fmt.Errorf("worker: initial sync failed: %w", err)
	}

	w.log.Info("worker consuming work items", "consumer", subjects.WorkConsumer(w.serverID))

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		msg, err := w.bus.FetchWork(ctx)
		if err != nil {
			if errors.Is(err, ErrNoWork) || errors.Is(err, context.Canceled) {
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
	}
}

func (w *Worker) sync(ctx context.Context) error {
	data, err := w.bus.Request(ctx, subjects.Sync(w.serverID), nil)
	if err != nil {
		return err
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		return fmt.Errorf("unmarshaling desired state: %w", err)
	}

	w.mu.Lock()
	for _, spec := range ds.Specs {
		w.state[spec.AppId] = spec
	}
	w.mu.Unlock()

	w.log.Info("worker: initial sync complete", "apps", len(ds.Specs))
	// Converge once with no trigger to reach desired state on boot.
	for {
		if err := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err == nil {
			break
		}
		w.log.Error("worker: initial reconcile failed, retrying in 2s")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
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

	case strings.HasSuffix(subject, ".remove"):
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

	default:
		w.log.Warn("worker: unknown work subject", "subject", subject)
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
	w.mu.Lock()
	desired := make([]*agentv1.AppSpec, 0, len(w.state))
	for _, spec := range w.state {
		desired = append(desired, spec)
	}
	w.mu.Unlock()

	statuses, err := w.driver.Reconcile(ctx, desired)
	if err != nil {
		return err // total orchestrator failure
	}

	// Publish observed statuses (ADR-005: the plane asserts outcomes only from
	// these observations).
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

	// Publish the terminal outcome for the triggering work item, if any. A
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

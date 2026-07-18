package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/agent/builder"
	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// Worker consumes work items from JetStream, manages the local desired state,
// and invokes the orchestrator driver to converge reality.
type Worker struct {
	nc       *nats.Conn
	serverID string
	driver   driver.Reconciler
	builder  *builder.Builder
	log      *slog.Logger

	mu    sync.Mutex
	state map[string]*agentv1.AppSpec // map[app_id]spec
}

// New creates a new Worker.
func New(nc *nats.Conn, serverID string, drv driver.Reconciler, bld *builder.Builder, log *slog.Logger) *Worker {
	return &Worker{
		nc:       nc,
		serverID: serverID,
		driver:   drv,
		builder:  bld,
		log:      log,
		state:    make(map[string]*agentv1.AppSpec),
	}
}

// Run starts the worker: it performs an initial sync to fetch the desired state,
// reconciles it once, and then processes incoming work items in a loop.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.sync(ctx); err != nil {
		return fmt.Errorf("worker: initial sync failed: %w", err)
	}

	js, err := w.nc.JetStream()
	if err != nil {
		return fmt.Errorf("worker: getting jetstream context: %w", err)
	}

	consumerName := subjects.WorkConsumer(w.serverID)
	// We bind to the consumer the control plane created for us.
	sub, err := js.PullSubscribe(subjects.WorkForServer(w.serverID), consumerName, nats.Bind("WORK", consumerName))
	if err != nil {
		return fmt.Errorf("worker: binding to work consumer %s: %w", consumerName, err)
	}

	w.log.Info("worker consuming work items", "consumer", consumerName)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			w.log.Error("worker: fetching work", "error", err)
			time.Sleep(1 * time.Second) // backoff
			continue
		}

		w.handleMsg(ctx, msgs[0])
	}
}

func (w *Worker) sync(ctx context.Context) error {
	msg, err := w.nc.Request(subjects.Sync(w.serverID), nil, 10*time.Second)
	if err != nil {
		return err
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(msg.Data, &ds); err != nil {
		return fmt.Errorf("unmarshaling desired state: %w", err)
	}

	w.mu.Lock()
	for _, spec := range ds.Specs {
		w.state[spec.AppId] = spec
	}
	w.mu.Unlock()

	w.log.Info("worker: initial sync complete", "apps", len(ds.Specs))
	// Reconcile once with no trigger to converge on boot.
	if err := w.reconcile(ctx, "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err != nil {
		w.log.Error("worker: initial reconcile failed", "error", err)
		// We log the error but don't fail sync; the daemon might be temporarily down.
	}
	return nil
}

func (w *Worker) handleMsg(ctx context.Context, msg *nats.Msg) {
	defer func() {
		if err := msg.Ack(); err != nil {
			w.log.Error("worker: acking message", "error", err)
		}
	}()

	subject := msg.Subject
	w.log.Info("worker: received work item", "subject", subject)

	var deploymentID string
	var appID string
	var stage agentv1.DeployEvent_Stage
	var commitSha string

	switch {
	case strings.HasSuffix(subject, ".rollout"):
		var work agentv1.RolloutWork
		if err := proto.Unmarshal(msg.Data, &work); err != nil {
			w.log.Error("worker: unmarshaling rollout work", "error", err)
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
		if err := proto.Unmarshal(msg.Data, &work); err != nil {
			w.log.Error("worker: unmarshaling remove work", "error", err)
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
		if err := proto.Unmarshal(msg.Data, &work); err != nil {
			w.log.Error("worker: unmarshaling build work", "error", err)
			return
		}
		deploymentID = work.DeploymentId
		appID = work.AppId
		stage = agentv1.DeployEvent_STAGE_BUILD
		commitSha = work.CommitSha

		// Stream logs to logs.build.<deployment_id>
		onLog := func(line string) {
			if w.nc != nil {
				subject := fmt.Sprintf("logs.build.%s", deploymentID)
				_ = w.nc.Publish(subject, []byte(line))
			}
		}

		if w.builder != nil {
			err := w.builder.Build(ctx, &work, onLog)
			if err != nil {
				w.log.Error("worker: build failed", "error", err)
				w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), commitSha)
				return
			}
		} else {
			w.log.Warn("worker: received build work but builder is nil")
		}

		w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_SUCCEEDED, "", commitSha)
		return

	default:
		w.log.Warn("worker: unknown work subject", "subject", subject)
		return
	}

	if err := w.reconcile(ctx, deploymentID, appID, stage, commitSha); err != nil {
		w.log.Error("worker: reconcile failed completely", "error", err)
		if stage != agentv1.DeployEvent_STAGE_UNSPECIFIED {
			w.emitEvent(deploymentID, appID, stage, agentv1.DeployEvent_OUTCOME_FAILED, err.Error(), commitSha)
		}
	}
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
		return err // Total orchestrator failure
	}

	// Publish observed statuses
	for _, status := range statuses {
		data, err := proto.Marshal(status)
		if err != nil {
			w.log.Error("worker: marshaling app status", "error", err)
			continue
		}
		if err := w.nc.Publish(subjects.AppState(w.serverID, status.AppId), data); err != nil {
			w.log.Error("worker: publishing app status", "app_id", status.AppId, "error", err)
		}
	}

	// Publish outcome event for the triggered deployment, if any
	if stage != agentv1.DeployEvent_STAGE_UNSPECIFIED {
		outcome := agentv1.DeployEvent_OUTCOME_SUCCEEDED
		var detail string

		// If a rollout failed, the AppStatus for it will be 'error'.
		// A failed teardown (remove) also reports 'error'.
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
	if err := w.nc.Publish(subjects.DeployState(w.serverID), data); err != nil {
		w.log.Error("worker: publishing deploy event", "deployment_id", deploymentID, "error", err)
	}
}

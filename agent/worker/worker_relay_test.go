package worker

// Relay work-item tests (builder-role-and-relay.md §2–3, §6): push and
// distribute dispatch, redelivery discipline, the HasImage-anchored
// idempotency living in the relay client, and the builder-role guards.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

type fakeRelay struct {
	mu       sync.Mutex
	pushes   []string
	pulls    []string
	pushErr  error
	pullErr  error
	canceled bool
}

func (r *fakeRelay) PushImage(ctx context.Context, deploymentID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, deploymentID)
	if ctx.Err() != nil {
		r.canceled = true
	}
	return r.pushErr
}

func (r *fakeRelay) PullImage(_ context.Context, deploymentID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pulls = append(r.pulls, deploymentID)
	return r.pullErr
}

func pushMsg(t *testing.T, serverID, depID string) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.PushImageWork{DeploymentId: depID, AppId: "app1", Image: "cypher/app1:rev1"})
	if err != nil {
		t.Fatalf("marshal push: %v", err)
	}
	return &fakeMessage{subject: subjects.PushImage(serverID), data: data}
}

func distributeMsg(t *testing.T, serverID, depID string) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.DistributeWork{DeploymentId: depID, AppId: "app1", Image: "cypher/app1:rev1"})
	if err != nil {
		t.Fatalf("marshal distribute: %v", err)
	}
	return &fakeMessage{subject: subjects.Distribute(serverID), data: data}
}

func lastEvent(t *testing.T, bus *fakeBus, serverID string) *agentv1.DeployEvent {
	t.Helper()
	evs := bus.publishedOn(subjects.DeployState(serverID))
	if len(evs) == 0 {
		return nil
	}
	var ev agentv1.DeployEvent
	if err := proto.Unmarshal(evs[len(evs)-1], &ev); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	return &ev
}

// A successful push acks silently: the target's distribute success is the
// stage's outcome of record, a builder-side event would be intention, not
// observation (spec §2).
func TestPushSuccessAcksWithoutEvent(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	rly := &fakeRelay{}
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, rly, quietLog())

	msg := pushMsg(t, "srv1", "dep1")
	w.handleMsg(context.Background(), msg)

	if !msg.acked || msg.termed || msg.naked {
		t.Fatalf("msg state acked=%v termed=%v naked=%v, want clean ack", msg.acked, msg.termed, msg.naked)
	}
	if len(rly.pushes) != 1 || rly.pushes[0] != "dep1" {
		t.Fatalf("pushes = %v, want [dep1]", rly.pushes)
	}
	if ev := lastEvent(t, bus, "srv1"); ev != nil {
		t.Fatalf("unexpected event %+v on push success", ev)
	}
}

// A successful pull reports STAGE_DISTRIBUTE success — the observation the
// scheduler advances on.
func TestDistributeSuccessEmitsEvent(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	rly := &fakeRelay{}
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, rly, quietLog())

	msg := distributeMsg(t, "srv1", "dep1")
	w.handleMsg(context.Background(), msg)

	if !msg.acked {
		t.Fatal("distribute success not acked")
	}
	ev := lastEvent(t, bus, "srv1")
	if ev == nil || ev.GetStage() != agentv1.DeployEvent_STAGE_DISTRIBUTE || ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_SUCCEEDED {
		t.Fatalf("event = %+v, want distribute success", ev)
	}
}

// Transient relay failures NAK for redelivery; the poison cutoff turns the
// last failure into a terminal STAGE_DISTRIBUTE event so the deployment
// fails instead of hanging (spec §6).
func TestRelayFailureNaksThenTerms(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	rly := &fakeRelay{pushErr: errors.New("peer did not arrive in time")}
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, rly, quietLog())

	msg := pushMsg(t, "srv1", "dep1")
	w.handleMsg(context.Background(), msg)
	if !msg.naked || msg.termed {
		t.Fatalf("first failure naked=%v termed=%v, want NAK only", msg.naked, msg.termed)
	}
	if ev := lastEvent(t, bus, "srv1"); ev != nil {
		t.Fatalf("premature event %+v before poison cutoff", ev)
	}

	last := pushMsg(t, "srv1", "dep1")
	last.delivery = maxDeliveries
	w.handleMsg(context.Background(), last)
	if !last.termed {
		t.Fatal("poison delivery not termed")
	}
	ev := lastEvent(t, bus, "srv1")
	if ev == nil || ev.GetStage() != agentv1.DeployEvent_STAGE_DISTRIBUTE || ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_FAILED {
		t.Fatalf("event = %+v, want terminal distribute failure", ev)
	}
}

// No relay client (identity predates plane_addr): redelivery cannot help, so
// the item terminates immediately with the remedy in the detail.
func TestRelayWorkWithoutRelayFailsImmediately(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, nil, quietLog())

	msg := distributeMsg(t, "srv1", "dep1")
	w.handleMsg(context.Background(), msg)

	if !msg.termed {
		t.Fatal("relay work without a relay must term, not redeliver")
	}
	ev := lastEvent(t, bus, "srv1")
	if ev == nil || ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_FAILED || ev.GetDetail() == "" {
		t.Fatalf("event = %+v, want failure with remedy detail", ev)
	}
}

// Runtime work routed to a builder-role agent (nil driver) is a routing bug:
// report failure and drop the item rather than pretending to converge.
func TestBuilderRoleRejectsRolloutWork(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	w := New(bus, "srv1", nil, nil, nil, &fakeRelay{}, quietLog())

	msg := rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"})
	w.handleMsg(context.Background(), msg)

	if !msg.termed {
		t.Fatal("rollout on driverless agent must term")
	}
	ev := lastEvent(t, bus, "srv1")
	if ev == nil || ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_FAILED {
		t.Fatalf("event = %+v, want rollout failure", ev)
	}
}

// A driverless (builder-role) worker's drift reconcile is a clean no-op — it
// must not crash or publish phantom observations.
func TestBuilderRoleReconcileIsNoOp(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	w := New(bus, "srv1", nil, nil, nil, &fakeRelay{}, quietLog())

	if err := w.reconcile(context.Background(), "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := len(bus.publishedOn(subjects.DeployState("srv1"))); n != 0 {
		t.Fatalf("driverless reconcile published %d events", n)
	}
}

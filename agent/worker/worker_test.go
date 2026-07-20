package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// The worker is exercised entirely through the Bus seam — no embedded NATS
// server — so the agent module never depends on the NATS server. The real
// JetStream binding (natsBus) is thin plumbing covered by the multi-node
// integration harness.

// ── fakes ─────────────────────────────────────────────────────────────────

type fakeMessage struct {
	subject  string
	data     []byte
	delivery uint64
	acked    bool
	termed   bool
	naked    bool
	progress int
}

func (m *fakeMessage) Subject() string                  { return m.subject }
func (m *fakeMessage) Data() []byte                     { return m.data }
func (m *fakeMessage) Ack() error                       { m.acked = true; return nil }
func (m *fakeMessage) Term() error                      { m.termed = true; return nil }
func (m *fakeMessage) NakWithDelay(time.Duration) error { m.naked = true; return nil }
func (m *fakeMessage) InProgress() error                { m.progress++; return nil }
func (m *fakeMessage) NumDelivered() uint64 {
	if m.delivery == 0 {
		return 1
	}
	return m.delivery
}

type fakeBus struct {
	mu        sync.Mutex
	syncReply []byte
	work      chan Message
	published map[string][][]byte
}

func newFakeBus(reply []byte) *fakeBus {
	return &fakeBus{
		syncReply: reply,
		work:      make(chan Message, 16),
		published: map[string][][]byte{},
	}
}

func (b *fakeBus) Request(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return b.syncReply, nil
}

func (b *fakeBus) Publish(subject string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	b.published[subject] = append(b.published[subject], cp)
	return nil
}

func (b *fakeBus) FetchWork(ctx context.Context) (Message, error) {
	select {
	case <-ctx.Done():
		return nil, context.Canceled
	case m := <-b.work:
		return m, nil
	case <-time.After(20 * time.Millisecond):
		return nil, ErrNoWork
	}
}

func (b *fakeBus) publishedOn(subject string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published[subject]
}

// recordingDriver captures the desired sets it is asked to converge and
// returns configurable statuses.
type recordingDriver struct {
	mu        sync.Mutex
	lastSet   []*agentv1.AppSpec
	calls     int
	stateByID map[string]string // app_id -> reported state (default "running")
	err       error
}

func (d *recordingDriver) Name() string { return "fake" }

func (d *recordingDriver) Reconcile(_ context.Context, desired []*agentv1.AppSpec) ([]*agentv1.AppStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.lastSet = desired
	if d.err != nil {
		return nil, d.err
	}
	var out []*agentv1.AppStatus
	for _, s := range desired {
		state := "running"
		if d.stateByID != nil {
			if v, ok := d.stateByID[s.AppId]; ok {
				state = v
			}
		}
		out = append(out, &agentv1.AppStatus{AppId: s.AppId, RevisionId: s.RevisionId, State: state})
	}
	return out, nil
}

func (d *recordingDriver) desired() []*agentv1.AppSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastSet
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func desiredStateBytes(t *testing.T, specs ...*agentv1.AppSpec) []byte {
	t.Helper()
	data, err := proto.Marshal(&agentv1.DesiredState{Specs: specs})
	if err != nil {
		t.Fatalf("marshal desired state: %v", err)
	}
	return data
}

func rolloutMsg(t *testing.T, serverID, depID string, spec *agentv1.AppSpec) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.RolloutWork{DeploymentId: depID, Spec: spec})
	if err != nil {
		t.Fatalf("marshal rollout: %v", err)
	}
	return &fakeMessage{subject: subjects.Rollout(serverID), data: data}
}

// ── tests ─────────────────────────────────────────────────────────────────

// Sync fetches the desired set and converges it once on boot.
func TestSyncConvergesDesiredSet(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"}))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := drv.desired(); len(got) != 1 || got[0].AppId != "app1" {
		t.Fatalf("desired set = %+v, want [app1]", got)
	}
	if len(bus.publishedOn(subjects.AppState("srv1", "app1"))) != 1 {
		t.Fatal("app status not published after boot convergence")
	}
}

// A rollout work item updates desired state, converges, and publishes both the
// AppStatus and a ROLLOUT deploy event.
func TestRolloutReconcilesAndReports(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	msg := rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev2"})
	w.handleMsg(context.Background(), msg)

	if !msg.acked {
		t.Fatal("rollout message not acked on success")
	}
	if got := drv.desired(); len(got) != 1 || got[0].RevisionId != "rev2" {
		t.Fatalf("desired = %+v, want rev2", got)
	}
	pub := bus.publishedOn(subjects.DeployState("srv1"))
	if len(pub) != 1 {
		t.Fatal("no deploy event published for rollout")
	}
	ev := decodeEvent(t, pub[0])
	if ev.GetStage() != agentv1.DeployEvent_STAGE_ROLLOUT || ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_SUCCEEDED {
		t.Fatalf("event = %+v, want rollout succeeded", ev)
	}
}

// A rollout whose app reports 'error' yields a FAILED deploy event (the plane
// keeps the previous revision serving).
func TestRolloutErrorReportsFailure(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{stateByID: map[string]string{"app1": "error"}}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	msg := rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev2"})
	w.handleMsg(context.Background(), msg)

	ev := decodeEvent(t, bus.publishedOn(subjects.DeployState("srv1"))[0])
	if ev.GetOutcome() != agentv1.DeployEvent_OUTCOME_FAILED {
		t.Fatalf("outcome = %v, want failed", ev.GetOutcome())
	}
	if !msg.acked {
		t.Fatal("message should be acked — a per-app failure is a handled outcome, not a redelivery")
	}
}

// A total orchestrator failure Naks for retry until the delivery cap, then
// Terms the poison message.
func TestReconcileFailureNaksThenTerms(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{err: errors.New("daemon unreachable")}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	first := rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"})
	first.delivery = 1
	w.handleMsg(context.Background(), first)
	if !first.naked || first.termed {
		t.Fatalf("first failure should Nak, not Term: %+v", first)
	}

	poison := rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"})
	poison.delivery = maxDeliveries
	w.handleMsg(context.Background(), poison)
	if !poison.termed {
		t.Fatalf("delivery %d should Term the poison message", maxDeliveries)
	}
}

// A malformed payload is Term'd (never redelivered) and does not converge.
func TestMalformedWorkIsTermed(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	msg := &fakeMessage{subject: subjects.Rollout("srv1"), data: []byte("not a protobuf")}
	w.handleMsg(context.Background(), msg)
	if !msg.termed {
		t.Fatal("malformed work should be Term'd")
	}
	if drv.calls != 0 {
		t.Fatal("malformed work must not trigger a reconcile")
	}
}

// A remove work item drops the app from desired state and reconciles.
func TestRemoveDropsFromDesired(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"}))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())
	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	data, _ := proto.Marshal(&agentv1.RemoveWork{DeploymentId: "remove-app1", AppId: "app1"})
	msg := &fakeMessage{subject: subjects.Remove("srv1"), data: data}
	w.handleMsg(context.Background(), msg)

	if got := drv.desired(); len(got) != 0 {
		t.Fatalf("desired = %+v, want empty after remove", got)
	}
	if !msg.acked {
		t.Fatal("remove message not acked")
	}
}

// Run performs the boot sync then consumes a queued work item, ending on
// context cancellation.
func TestRunConsumesQueuedWork(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())

	bus.work <- rolloutMsg(t, "srv1", "dep1", &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	deadline := time.After(3 * time.Second)
	for len(drv.desired()) != 1 {
		select {
		case <-deadline:
			t.Fatal("worker did not converge the queued work item")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func decodeEvent(t *testing.T, data []byte) *agentv1.DeployEvent {
	t.Helper()
	var ev agentv1.DeployEvent
	if err := proto.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return &ev
}

// An idle worker (no work items) still re-converges on the drift interval —
// retrying failed GC/drains and refreshing observed statuses so the plane's
// view never fossilizes at deploy time.
func TestIdleWorkerDriftReconciles(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"}))
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, quietLog())
	w.driftInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	// Boot sync = 1 call; with no work arriving, drift must add more. Wait for
	// BOTH the reconcile count and the status publications: the driver counts
	// a call when Reconcile starts, before the worker publishes the resulting
	// statuses, so checking publications only after calls reach 3 races the
	// in-flight publish (this exact flake failed in CI).
	deadline := time.After(3 * time.Second)
	for {
		drv.mu.Lock()
		calls := drv.calls
		drv.mu.Unlock()
		published := len(bus.publishedOn(subjects.AppState("srv1", "app1")))
		if calls >= 3 && published >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("idle worker: %d reconciles, %d status publications; want >=3 of each (drift reconcile not firing or statuses not refreshed)", calls, published)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

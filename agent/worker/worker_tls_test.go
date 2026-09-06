package worker

// Desired-state TLS settings and the resync nudge (agent-identity-and-tls.md
// §4). The panel owns one ACME account; it reaches every node inside the
// desired set, and a change to it is propagated by asking nodes to re-read
// that set rather than by pushing a second copy of the truth.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// recordingProxyTLS captures what the worker hands the Proxy.
type recordingProxyTLS struct {
	mu       sync.Mutex
	calls    int
	email    string
	caServer string
}

func (p *recordingProxyTLS) SetACME(email, caServer string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.email, p.caServer = email, caServer
}

func (p *recordingProxyTLS) snapshot() (int, string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.email, p.caServer
}

// settableBus lets a test change the sync reply between syncs — which is the
// whole point of a resync nudge — and fail the request outright, which is what
// the plane does when it cannot resolve a complete desired set.
type settableBus struct {
	*fakeBus
	syncErr error // guarded by fakeBus.mu
}

func (b *settableBus) setReply(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncReply = data
}

func (b *settableBus) failSync(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncErr = err
}

func (b *settableBus) Request(context.Context, string, []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.syncErr != nil {
		return nil, b.syncErr
	}
	return b.syncReply, nil
}

func desiredStateWithTLS(t *testing.T, tls *agentv1.TLSSettings, specs ...*agentv1.AppSpec) []byte {
	t.Helper()
	data, err := proto.Marshal(&agentv1.DesiredState{Specs: specs, Tls: tls})
	if err != nil {
		t.Fatalf("marshal desired state: %v", err)
	}
	return data
}

func resyncMsg(t *testing.T, serverID, reason string) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.ResyncWork{Reason: reason})
	if err != nil {
		t.Fatalf("marshal resync: %v", err)
	}
	return &fakeMessage{subject: subjects.Resync(serverID), data: data}
}

func TestSyncCarriesTLSSettingsToTheProxy(t *testing.T) {
	bus := newFakeBus(desiredStateWithTLS(t,
		&agentv1.TLSSettings{AcmeEmail: "ops@example.com", AcmeCaServer: "https://acme.example.com/dir"},
		&agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"},
	))
	prx := &recordingProxyTLS{}
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, nil, nil, quietLog())
	w.SetProxyTLS(prx)

	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	calls, email, caServer := prx.snapshot()
	if calls != 1 || email != "ops@example.com" || caServer != "https://acme.example.com/dir" {
		t.Fatalf("SetACME(%d) = %q, %q; want the panel's account", calls, email, caServer)
	}
}

// An empty account is a meaningful value — "no resolver on this node" — and
// must be applied, not skipped. Skipping it would leave a node that once had
// TLS configured pointing routes at a resolver the panel has since removed.
func TestSyncAppliesAnEmptyTLSAccount(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1"}))
	prx := &recordingProxyTLS{}
	w := New(bus, "srv1", &recordingDriver{}, nil, nil, nil, nil, quietLog())
	w.SetProxyTLS(prx)

	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	calls, email, _ := prx.snapshot()
	if calls != 1 || email != "" {
		t.Fatalf("SetACME(%d) = %q; want one call with an empty account", calls, email)
	}
}

// The resync nudge re-reads desired state, picks up the new account, and acks.
func TestResyncWorkRereadsDesiredState(t *testing.T) {
	bus := &settableBus{fakeBus: newFakeBus(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1"}))}
	prx := &recordingProxyTLS{}
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, nil, quietLog())
	w.SetProxyTLS(prx)

	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, email, _ := prx.snapshot(); email != "" {
		t.Fatalf("email = %q before the panel configured TLS", email)
	}

	// The operator turns TLS on.
	bus.setReply(desiredStateWithTLS(t,
		&agentv1.TLSSettings{AcmeEmail: "ops@example.com"},
		&agentv1.AppSpec{AppId: "app1"},
	))
	msg := resyncMsg(t, "srv1", "panel tls changed")
	w.handleMsg(context.Background(), msg)

	if !msg.acked {
		t.Fatalf("resync message not acked: %+v", msg)
	}
	if _, email, _ := prx.snapshot(); email != "ops@example.com" {
		t.Fatalf("email = %q after the resync, want ops@example.com", email)
	}
}

// Redelivery costs one request and changes nothing (rule 12): the nudge carries
// no state, so applying it twice equals applying it once.
func TestResyncIsIdempotentUnderRedelivery(t *testing.T) {
	reply := desiredStateWithTLS(t,
		&agentv1.TLSSettings{AcmeEmail: "ops@example.com"},
		&agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"},
	)
	bus := &settableBus{fakeBus: newFakeBus(reply)}
	prx := &recordingProxyTLS{}
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, nil, quietLog())
	w.SetProxyTLS(prx)

	first := resyncMsg(t, "srv1", "panel tls changed")
	w.handleMsg(context.Background(), first)
	afterFirst := drv.desired()

	second := resyncMsg(t, "srv1", "panel tls changed")
	second.delivery = 2
	w.handleMsg(context.Background(), second)
	afterSecond := drv.desired()

	if !first.acked || !second.acked {
		t.Fatalf("both deliveries should ack: %+v %+v", first, second)
	}
	if len(afterFirst) != len(afterSecond) {
		t.Fatalf("desired set changed across redelivery: %d then %d", len(afterFirst), len(afterSecond))
	}
	_, email, _ := prx.snapshot()
	if email != "ops@example.com" {
		t.Fatalf("email = %q after redelivery", email)
	}
}

// A re-sync REPLACES the desired set; it does not merge into it. An application
// the plane has removed must not survive because the agent once knew about it.
func TestResyncRemovesApplicationsThePlaneDropped(t *testing.T) {
	bus := &settableBus{fakeBus: newFakeBus(desiredStateBytes(t,
		&agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"},
		&agentv1.AppSpec{AppId: "app2", RevisionId: "rev1"},
	))}
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, nil, quietLog())

	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := drv.desired(); len(got) != 2 {
		t.Fatalf("desired = %d apps, want 2", len(got))
	}

	bus.setReply(desiredStateBytes(t, &agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"}))
	w.handleMsg(context.Background(), resyncMsg(t, "srv1", "app removed"))

	got := drv.desired()
	if len(got) != 1 || got[0].GetAppId() != "app1" {
		t.Fatalf("desired after resync = %+v, want just app1", got)
	}
}

// A worker with no Proxy (a builder-role agent) simply ignores the settings.
func TestSyncWithoutAProxyIgnoresTLSSettings(t *testing.T) {
	bus := newFakeBus(desiredStateWithTLS(t, &agentv1.TLSSettings{AcmeEmail: "ops@example.com"}))
	w := New(bus, "srv1", nil, nil, nil, nil, nil, quietLog())
	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync on a builder-role agent: %v", err)
	}
}

// The other half of "the reply replaces what is held": when the plane cannot
// answer — it fails the sync rather than send a set it knows is incomplete
// (scheduler.DesiredStateFor) — the agent keeps the desired set it already has
// and NAKs, so nothing is torn down over a momentary store failure and the
// nudge is redelivered.
func TestResyncKeepsTheDesiredSetWhenThePlaneCannotAnswer(t *testing.T) {
	bus := &settableBus{fakeBus: newFakeBus(desiredStateBytes(t,
		&agentv1.AppSpec{AppId: "app1", RevisionId: "rev1"},
		&agentv1.AppSpec{AppId: "app2", RevisionId: "rev1"},
	))}
	drv := &recordingDriver{}
	w := New(bus, "srv1", drv, nil, nil, nil, nil, quietLog())

	if err := w.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := drv.desired(); len(got) != 2 {
		t.Fatalf("desired = %d apps, want 2", len(got))
	}

	bus.failSync(errors.New("nats: timeout"))
	msg := resyncMsg(t, "srv1", "panel tls changed")
	w.handleMsg(context.Background(), msg)

	if msg.acked || msg.termed || !msg.naked {
		t.Fatalf("failed resync = %+v, want a NAK so it is redelivered", msg)
	}
	// The held set is untouched: the next convergence still declares both apps.
	if err := w.reconcile(context.Background(), "", "", agentv1.DeployEvent_STAGE_UNSPECIFIED, ""); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := drv.desired(); len(got) != 2 {
		t.Fatalf("desired = %d apps after a failed resync, want both still declared", len(got))
	}
}

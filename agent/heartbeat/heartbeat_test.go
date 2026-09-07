package heartbeat

import (
	"errors"
	"sync"
	"testing"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// A subsystem failing in a retry loop must reach the plane. Before this, the
// agent hard-coded READY, so a Proxy that could never bind :80 left its server
// green while every routed deploy failed (ui-principles §10).
func TestStatusReflectsSubsystemHealth(t *testing.T) {
	h := &Health{}
	p := &Publisher{health: h}

	if got := p.status(); got != agentv1.AgentStatus_AGENT_STATUS_READY {
		t.Fatalf("fresh health: got %v, want READY", got)
	}

	h.Set("proxy", errors.New("proxy: bind :80: address already in use"))
	if got := p.status(); got != agentv1.AgentStatus_AGENT_STATUS_DEGRADED {
		t.Errorf("after failure: got %v, want DEGRADED", got)
	}

	// Recovery must clear it — a server that stays amber after the operator
	// frees the port is the same lie in the other direction.
	h.Set("proxy", nil)
	if got := p.status(); got != agentv1.AgentStatus_AGENT_STATUS_READY {
		t.Errorf("after recovery: got %v, want READY", got)
	}
}

// A publisher wired without a health holder is valid and always ready — that is
// what builder-role agents and the unit tests use.
func TestStatusNilHealthIsReady(t *testing.T) {
	p := &Publisher{}
	if got := p.status(); got != agentv1.AgentStatus_AGENT_STATUS_READY {
		t.Errorf("nil health: got %v, want READY", got)
	}
}

// The reconcile loop writes while the heartbeat ticker reads; the race detector
// runs this in CI (-race).
func TestHealthIsConcurrencySafe(t *testing.T) {
	h := &Health{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); h.Set("proxy", errors.New("boom")) }()
		go func() { defer wg.Done(); _ = h.Err() }()
		if i%2 == 0 {
			wg.Add(1)
			go func() { defer wg.Done(); h.Set("proxy", nil) }()
		}
	}
	wg.Wait()
}

// Two reporters must not clobber each other. Before Health was keyed by
// subsystem there was one slot, so a node whose Proxy could not bind :80 went
// green the moment an unrelated subsystem cleared itself.
func TestOneSubsystemClearingItselfDoesNotClearAnother(t *testing.T) {
	h := &Health{}
	h.Set("proxy", errors.New("bind :80: address already in use"))
	h.Set("agent-update", errors.New("rolled back"))

	h.Set("agent-update", nil)
	if h.Err() == nil {
		t.Fatal("clearing the updater cleared the proxy's failure too")
	}
	h.Set("proxy", nil)
	if h.Err() != nil {
		t.Fatalf("still failing with nothing recorded: %v", h.Err())
	}
}

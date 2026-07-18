package subjects

import (
	"strings"
	"testing"
)

// TestHeartbeatInsideServerScope is the invariant the layout exists for: a
// server's heartbeat subject must fall under that server's state wildcard, so
// the bus's per-agent publish grant (StateForServer alone) covers it.
func TestHeartbeatInsideServerScope(t *testing.T) {
	const id = "srv_alpha"
	hb := Heartbeat(id)
	scope := strings.TrimSuffix(StateForServer(id), ">")
	if !strings.HasPrefix(hb, scope) {
		t.Errorf("Heartbeat(%q) = %q is outside the server scope %q", id, hb, StateForServer(id))
	}
}

// TestHeartbeatMatchesConsumerFilter: the consumer-side wildcard must match
// exactly the subjects agents publish on.
func TestHeartbeatMatchesConsumerFilter(t *testing.T) {
	hb := Heartbeat("srv_alpha")
	// NATS wildcard matching for "state.*.heartbeat": three tokens, first
	// "state", last "heartbeat".
	parts := strings.Split(hb, ".")
	filter := strings.Split(HeartbeatAll, ".")
	if len(parts) != len(filter) {
		t.Fatalf("Heartbeat %q has %d tokens, filter %q has %d", hb, len(parts), HeartbeatAll, len(filter))
	}
	for i, f := range filter {
		if f != "*" && f != parts[i] {
			t.Errorf("token %d: heartbeat %q does not match filter %q", i, hb, HeartbeatAll)
		}
	}
}

// TestNoCrossServerOverlap: one server's subjects must never fall inside
// another server's scope (threat-model §5.2). Guards against separator bugs
// like an ID that prefixes another.
func TestNoCrossServerOverlap(t *testing.T) {
	scopeA := strings.TrimSuffix(StateForServer("srv_a"), ">")
	for _, other := range []string{Heartbeat("srv_ab"), Heartbeat("srv_b"), StateForServer("srv_ab")} {
		if strings.HasPrefix(other, scopeA) {
			t.Errorf("%q falls inside srv_a's scope %q", other, scopeA)
		}
	}
}

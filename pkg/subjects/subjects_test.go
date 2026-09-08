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

// TestWorkAndStateSubjectsInsideServerScopes: every per-server subject the
// Phase 2 pipeline uses must fall inside the wildcard its side is granted —
// publications inside StateForServer/LogsForServer, work items inside
// WorkForServer — so the bus grants need no enumeration.
func TestWorkAndStateSubjectsInsideServerScopes(t *testing.T) {
	const id = "srv_alpha"
	stateScope := strings.TrimSuffix(StateForServer(id), ">")
	for _, s := range []string{DeployState(id), AppState(id, "app_x"), Sync(id)} {
		if !strings.HasPrefix(s, stateScope) {
			t.Errorf("%q is outside the state scope %q", s, stateScope)
		}
	}
	workScope := strings.TrimSuffix(WorkForServer(id), ">")
	for _, s := range []string{Rollout(id), Remove(id), Build(id)} {
		if !strings.HasPrefix(s, workScope) {
			t.Errorf("%q is outside the work scope %q", s, workScope)
		}
	}
}

// TestPlaneWildcardsMatchAgentSubjects: the plane's consumption wildcards must
// match exactly what agents publish.
func TestPlaneWildcardsMatchAgentSubjects(t *testing.T) {
	match := func(subject, filter string) bool {
		st, ft := strings.Split(subject, "."), strings.Split(filter, ".")
		for i, f := range ft {
			if f == ">" {
				return i < len(st)
			}
			if i >= len(st) || (f != "*" && f != st[i]) {
				return false
			}
		}
		return len(st) == len(ft)
	}
	if !match(DeployState("srv_a"), DeployStateAll) {
		t.Errorf("%q does not match %q", DeployState("srv_a"), DeployStateAll)
	}
	if !match(AppState("srv_a", "app_x"), AppStateAll) {
		t.Errorf("%q does not match %q", AppState("srv_a", "app_x"), AppStateAll)
	}
	if !match(Sync("srv_a"), SyncAll) {
		t.Errorf("%q does not match %q", Sync("srv_a"), SyncAll)
	}
	// The deploy-state wildcard must NOT swallow app statuses or syncs.
	if match(AppState("srv_a", "app_x"), DeployStateAll) || match(Sync("srv_a"), DeployStateAll) {
		t.Error("DeployStateAll matches subjects it must not")
	}
}

// TestWorkConsumerNameHasNoDots: consumer names become JS API subject tokens;
// a dot would split the token and break the per-agent grants.
func TestWorkConsumerNameHasNoDots(t *testing.T) {
	if strings.Contains(WorkConsumer("srv_alpha"), ".") {
		t.Errorf("WorkConsumer name %q contains a dot", WorkConsumer("srv_alpha"))
	}
}

// TestInboxPrefixIsOneTokenAndPerServer: the inbox prefix becomes the first
// token of every reply subject, so it must carry no dot, must sit inside the
// server's own inbox grant, and must never fall inside another server's.
func TestInboxPrefixIsOneTokenAndPerServer(t *testing.T) {
	prefix := InboxPrefix("srv_alpha")
	if strings.Contains(prefix, ".") {
		t.Fatalf("InboxPrefix %q contains a dot", prefix)
	}
	scope := strings.TrimSuffix(InboxForServer("srv_alpha"), ">")
	if !strings.HasPrefix(prefix+".abc.1", scope) {
		t.Errorf("reply subject %q is outside the inbox grant %q", prefix+".abc.1", InboxForServer("srv_alpha"))
	}
	for _, other := range []string{InboxPrefix("srv_alphab") + ".x", InboxPrefix("srv_beta") + ".x", "_INBOX.x"} {
		if strings.HasPrefix(other, scope) {
			t.Errorf("%q falls inside srv_alpha's inbox scope %q", other, scope)
		}
	}
}

// Package subjects defines the NATS subject contract shared across the
// agent↔plane wire (ADR-003). Both core/bus (the embedded server and its
// consumers) and cypher-agent (the client) import these, so the two sides can
// never drift. Subjects are contracts: change them only additively
// (ENGINEERING rule 14). The subject families are documented in core/bus.
package subjects

// Subject families (ADR-003):
//
//	state.<server-id>.*  — agent status/heartbeat/deploy events (this file, Phase 1)
//	work.<server-id>.*   — commands to agents (Phase 2)
//	logs.<server-id>.*   — build/runtime log streams (Phase 2)
//
// Every per-server subject lives under its server's segment, so one wildcard
// per family covers a server's entire scope — the authorization grants in
// core/bus are exactly StateForServer/WorkForServer, nothing enumerated.
const (
	StatePrefix     = "state."
	heartbeatSuffix = ".heartbeat"
	HeartbeatAll    = "state.*.heartbeat"
	WorkPrefix      = "work."
)

// Heartbeat is the subject an agent publishes its heartbeats on. It sits
// inside the server's state.<id>.> scope, so the per-agent publish grant
// (threat-model §5.2) needs no special case for it.
func Heartbeat(serverID string) string {
	return StatePrefix + serverID + heartbeatSuffix
}

// StateForServer is the wildcard covering all of one server's state subjects,
// used when authorizing an agent's publish scope.
func StateForServer(serverID string) string {
	return StatePrefix + serverID + ".>"
}

// WorkForServer is the wildcard an agent subscribes to for its own work items
// (Phase 2); defined here so the authorization scope is set from day one.
func WorkForServer(serverID string) string {
	return WorkPrefix + serverID + ".>"
}

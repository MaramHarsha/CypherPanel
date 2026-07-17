// Package subjects defines the NATS subject contract shared across the
// agent↔plane wire (ADR-003). Both core/bus (the embedded server and its
// consumers) and cypher-agent (the client) import these, so the two sides can
// never drift. Subjects are contracts: change them only additively
// (ENGINEERING rule 14). The subject families are documented in core/bus.
package subjects

// Subject families (ADR-003):
//
//	state.*  — agent status/heartbeat/deploy events (this file, Phase 1)
//	work.*   — commands to agents (Phase 2)
//	logs.*   — build/runtime log streams (Phase 2)
const (
	StatePrefix     = "state."
	HeartbeatPrefix = "state.heartbeat."
	HeartbeatAll    = "state.heartbeat.>"
	WorkPrefix      = "work."
)

// Heartbeat is the subject an agent publishes its heartbeats on. Per-server so
// the bus can authorize each agent to publish only its own (threat-model §5.2).
func Heartbeat(serverID string) string {
	return HeartbeatPrefix + serverID
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

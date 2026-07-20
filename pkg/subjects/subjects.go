// Package subjects defines the NATS subject contract shared across the
// agent↔plane wire (ADR-003). Both core/bus (the embedded server and its
// consumers) and cypher-agent (the client) import these, so the two sides can
// never drift. Subjects are contracts: change them only additively
// (ENGINEERING rule 14). The subject families are documented in core/bus.
package subjects

// Subject families (ADR-003):
//
//	state.<server-id>.*  — agent status/heartbeat/deploy events + sync requests
//	work.<server-id>.*   — work items to agents (rollout/remove/build)
//	logs.<server-id>.*   — build/runtime log streams
//
// Every per-server subject lives under its server's segment, so one wildcard
// per family covers a server's entire scope — the authorization grants in
// core/bus are exactly StateForServer/WorkForServer/LogsForServer, nothing
// enumerated.
const (
	StatePrefix     = "state."
	heartbeatSuffix = ".heartbeat"
	HeartbeatAll    = "state.*.heartbeat"
	WorkPrefix      = "work."
	LogsPrefix      = "logs."

	// DeployStateAll and AppStateAll are the plane-side consumption wildcards
	// for deploy events and app-status observations.
	DeployStateAll = "state.*.deploy"
	AppStateAll    = "state.*.app.>"
	// SyncAll is where the plane answers desired-state sync requests.
	SyncAll = "state.*.sync"

	// BuildLogAll and RuntimeLogAll are the stream-capture wildcards that
	// split build from runtime logs (bounded-log-retention.md §3): LOGS
	// captures build logs (memory-backed, short retention), RUNTIME_LOGS
	// captures runtime logs (file-backed, bounded retention). The subjects
	// agents publish on are unchanged — only the capture split is new
	// (ENGINEERING rule 14, additive).
	BuildLogAll   = "logs.*.build.>"
	RuntimeLogAll = "logs.*.runtime.>"
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

// WorkForServer is the wildcard an agent subscribes to for its own work items;
// defined here so the authorization scope is set from day one.
func WorkForServer(serverID string) string {
	return WorkPrefix + serverID + ".>"
}

// LogsForServer is the wildcard covering one server's log publications.
func LogsForServer(serverID string) string {
	return LogsPrefix + serverID + ".>"
}

// BuildLog is where a builder-role agent streams one deployment's build logs,
// and where the plane's build-log SSE endpoint subscribes. Both sides MUST
// construct it here (rule 14) so they can never drift apart.
func BuildLog(serverID, deploymentID string) string {
	return LogsPrefix + serverID + ".build." + deploymentID
}

// RuntimeLog is where an agent streams one application's runtime logs, and
// where the plane's runtime-log SSE endpoint subscribes.
func RuntimeLog(serverID, appID string) string {
	return LogsPrefix + serverID + ".runtime." + appID
}

// Rollout, Remove and Build are the work-item subjects for one server. The
// agent routes on the suffix; the payloads are the work.proto messages.
func Rollout(serverID string) string { return WorkPrefix + serverID + ".rollout" }
func Remove(serverID string) string  { return WorkPrefix + serverID + ".remove" }
func Build(serverID string) string   { return WorkPrefix + serverID + ".build" }

// PushImage and Distribute are the multi-server relay work subjects
// (builder-role-and-relay.md §2): the builder pushes a built image to the
// plane's relay, the target obtains it from there. Both sit under their
// server's work.<id>.> scope, so agent authorization is unchanged.
func PushImage(serverID string) string  { return WorkPrefix + serverID + ".push" }
func Distribute(serverID string) string { return WorkPrefix + serverID + ".distribute" }

// DeployState is where an agent reports DeployEvent outcomes.
func DeployState(serverID string) string { return StatePrefix + serverID + ".deploy" }

// AppState is where an agent reports one Application's observed AppStatus.
func AppState(serverID, appID string) string {
	return StatePrefix + serverID + ".app." + appID
}

// Sync is the request/reply subject on which an agent asks the plane for its
// full desired set (DesiredState) — sent on connect, before consuming work.
func Sync(serverID string) string { return StatePrefix + serverID + ".sync" }

// WorkConsumer names the durable JetStream consumer holding one server's
// work-item cursor. The plane creates it (agents only read from it); the name
// carries no dots so it is a valid consumer name and API subject token.
func WorkConsumer(serverID string) string { return "wrk-" + serverID }

// Agent-side JetStream API grants: an agent may read and acknowledge exactly
// its own work consumer, nothing else (threat-model §5.2). These are the
// subjects the nats.go jetstream client uses for consumer lookup, pull
// requests, and acks.
func WorkConsumerInfo(serverID string) string {
	return "$JS.API.CONSUMER.INFO.WORK." + WorkConsumer(serverID)
}
func WorkConsumerNext(serverID string) string {
	return "$JS.API.CONSUMER.MSG.NEXT.WORK." + WorkConsumer(serverID)
}
func WorkConsumerAck(serverID string) string {
	return "$JS.ACK.WORK." + WorkConsumer(serverID) + ".>"
}

// Phase 3: Managed Database subjects (docs/features/managed-databases.md §5).
// All additive (rule 14) — sit under the existing work.<server>.> /
// state.<server>.> authorization scope, so no new per-agent grants needed.

// Database work subjects — plane → agent.
func DbProvision(serverID string) string { return WorkPrefix + serverID + ".db.provision" }
func DbRemove(serverID string) string    { return WorkPrefix + serverID + ".db.remove" }
func DbBackup(serverID string) string    { return WorkPrefix + serverID + ".db.backup" }
func DbRestore(serverID string) string   { return WorkPrefix + serverID + ".db.restore" }

// Database state subjects — agent → plane.
func DbState(serverID, dbID string) string { return StatePrefix + serverID + ".db." + dbID }

// Plane-side consumption wildcards for database status observations.
const (
	DbStateAll       = "state.*.db.>"
	DbBackupStateAll = "state.*.db.backup"
)

// DbBackupState is where an agent reports backup/restore event outcomes.
func DbBackupState(serverID string) string { return StatePrefix + serverID + ".db.backup" }

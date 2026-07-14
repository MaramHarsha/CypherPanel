---
name: jobs-and-agent-tasks
description: How to add a NATS JetStream job/task type that CypherAgents execute. Use for any agent-executed operation — provisioning, SSL issuance, backups, mail/DNS config.
---

# Jobs & Agent Tasks

## Pipeline shape (already built — extend, don't reinvent)

- Stream: `TASKS` (WorkQueue retention, file storage), subjects `tasks.server.<server-uuid>` — one subject per server so agents never share a consumer (`internal/jobs`).
- Core side: persist the task row first (`store.Tasks.Create` → status `pending`), then `jobs.Publisher.Publish`. The task UUID doubles as the JetStream `Nats-Msg-Id` dedup key (10-min window), so crash-retry of "insert then publish" is safe.
- Agent side: durable consumer `agent_<uuid-no-hyphens>` filtered to its own subject, explicit acks, `AckWait` 2m, `MaxDeliver` 5 (`jobs.Consume`).
- Results return via gRPC `ReportTaskResult` (not NATS), which updates the task row status-guardedly (`pending` → `succeeded`/`failed`; redelivered reports are no-ops).

## Adding a new task type — checklist

1. Add a `Type...` constant and payload struct in `internal/jobs/jobs.go`, and register it in `ValidType` (Core rejects unknown types at the API boundary).
2. Add the handler case in the agent's `taskExecutor.Handle` (`cmd/agent/tasks.go`).
3. **The handler must be idempotent.** JetStream redelivers on failure and after ack loss; running the same task twice must succeed harmlessly (pattern: check-then-act, like `SystemUsers.Create` returning nil if the user already exists).
4. Classify failures:
   - Transient (network, lock contention) → return the error; the message naks and redelivers (up to 5 attempts).
   - Impossible-to-succeed (bad payload, unsupported platform, validation) → wrap with `jobs.Permanent(err)`; the task dead-letters and reports failed **immediately** instead of burning retries.
5. Malformed/undecodable messages are `Term()`ed and dropped — never retried.
6. Payloads are JSON, defined as Go structs in `internal/jobs` so Core and Agent share the schema. Payloads must not contain secrets (they sit in the audit trail and stream storage).

## Operational rules

- Handlers get a 90s timeout context; long operations must either fit or be split into resumable steps.
- Agent paths inside handlers come from the `paths.Layout` (e.g. `layout.AccountHome(username)`) — never literals.
- NATS connections take optional credentials (`CYPHER_NATS_CREDS` / `CYPHER_AGENT_NATS_CREDS`); production NATS must not run open.

# ADR-003: Embedded NATS JetStream as queue and event bus

- **Status:** Accepted
- **Date:** 2026-07-17

## Context

Both references need a queue and both pay an infrastructure tax for it: Coolify runs Redis + Laravel Horizon (plus Soketi for websocket fan-out); Dokploy runs Redis + BullMQ (`dokploy/apps/dokploy/server/queues/`). That's an extra stateful service to install, monitor, and back up — against our one-binary + one-database constraint (ADR-001). We need three messaging patterns: durable work dispatch to agents, agent state/event reporting, and high-volume log streaming.

## Decision

**NATS JetStream, embedded in-process** in `cypherd` (NATS is a Go library; the server runs inside our binary). Agents connect as NATS clients over the same mTLS identity as gRPC. Subject families are contracts, documented in `core/bus`:

- `work.*` — commands to agents (durable, at-least-once)
- `state.*` — agent status/heartbeat/deploy events
- `logs.*` — build and runtime log streams (bounded retention)

## Alternatives considered

- **Redis + a Go queue library (asynq etc.).** Proven, but reintroduces a second stateful service. Rejected.
- **RabbitMQ.** Heavyweight for this scale; operationally demanding. Rejected.
- **PostgreSQL LISTEN/NOTIFY + outbox table.** Zero new components and transactionally clean — the strongest alternative. Rejected because log streaming and fan-out to many agents fit it poorly; we do still use Postgres as the state of record, with the bus carrying only transient traffic.

## Consequences

- **At-least-once delivery means every work item must be idempotent** and carry an idempotency key. This is an ENGINEERING.md rule, not a suggestion.
- Offline agents replay missed work via durable consumers — free crash recovery.
- No Redis, no Horizon, no BullMQ, no Soketi equivalents to operate.
- Messages are transient coordination traffic only; **Postgres remains the single state of record**. Losing the bus must never lose state.
- If a multi-node control plane ever becomes a goal, NATS clustering is the escape hatch — but that is explicitly out of scope for v1 (see vision.md).

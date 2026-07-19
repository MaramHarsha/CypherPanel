# Feature spec: Bounded runtime-log retention

> Gives runtime logs a durable retention window so operators can view recent
> application output without being connected at the moment it was emitted.
> Build logs remain transient (they're only relevant during the deploy that
> produced them); runtime logs are the long-lived diagnostic record.
>
> Written 2026-07-19, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Problem

The current `LOGS` JetStream stream is **memory-backed** with a 30-minute
max age. This is fine for build logs (consumed during a deploy's SSE stream)
but insufficient for runtime logs: an operator checking an application's
output hours later sees nothing. The CLAUDE.md status note explicitly lists
"bounded runtime-log retention" as remaining Phase 2 scope.

## 2. Design

Separate build and runtime logs onto two streams with different storage and
retention characteristics:

| Stream         | Subjects                           | Storage  | Max age   | Max bytes | Purpose |
|----------------|-------------------------------------|----------|-----------|-----------|---------|
| `LOGS`         | `logs.*.build.>`                   | Memory   | 30 min    | 64 MiB   | Build log replay during deploy SSE |
| `RUNTIME_LOGS` | `logs.*.runtime.>`                 | **File** | **24 h**  | **512 MiB** | Runtime log persistence |

The `RUNTIME_LOGS` stream is **file-backed** so logs survive a plane restart
within the retention window. This is a deliberate trade-off: bounded disk is
acceptable for structured log lines (small per-message), and it avoids the
memory pressure that an unbounded in-memory stream would cause on a control
plane with many applications.

### Retention bounds

Both max age and max bytes are configurable:

| Env var                           | Default   | Description |
|-----------------------------------|-----------|-------------|
| `CYPHERD_RUNTIME_LOGS_MAX_AGE`   | `24h`     | How long runtime log lines are retained |
| `CYPHERD_RUNTIME_LOGS_MAX_BYTES` | `536870912` (512 MiB) | Disk cap for the stream |

When either limit is reached, the oldest messages are discarded (`DiscardOld`).

## 3. Subject migration

Today, runtime logs are published on `logs.<server>.runtime.<app_id>`, which
is captured by the `LOGS` stream's `logs.>` subject filter. To split them:

1. Narrow the `LOGS` stream's subject filter to `logs.*.build.>` only.
2. Create `RUNTIME_LOGS` with subject filter `logs.*.runtime.>`.

This is **additive** (ENGINEERING rule 14): the subject names agents publish
on do not change. Only the stream capture changes — the plane creates the new
stream and narrows the old one on startup (`CreateOrUpdateStream` is
idempotent and updates subject filters).

## 4. SSE endpoint changes

The runtime-log SSE handler (`GET /api/v1/applications/{id}/logs`) currently
calls `bus.SubscribeLogs` with the `LOGS` stream. It switches to a new
`bus.SubscribeRuntimeLogs` that uses the `RUNTIME_LOGS` stream. The consumer
is still an ephemeral ordered consumer with `DeliverAll`, so:

- **On connect**: replays all retained messages (up to 24 h of history).
- **Then tails**: live messages arrive as the agent publishes them.

Build-log SSE (`GET /api/v1/deployments/{id}/logs`) is unchanged — it
continues using the transient `LOGS` stream.

## 5. Agent-side changes

None. The agent continues publishing runtime logs to `logs.<server>.runtime.<app_id>`
via core-NATS `Publish`. The stream split is transparent — NATS routes the
message to whichever stream captures its subject.

## 6. Security & resource implications

- **Disk usage**: bounded by `MaxBytes` (default 512 MiB). The guard check
  (`guard.CheckDiskHeadroom`) already runs at boot; the RUNTIME_LOGS stream
  does not bypass it.
- **Privacy**: runtime logs may contain application output. They are retained
  only on the plane's own disk (no external drain in Phase 2) and served only
  to authenticated sessions (the SSE endpoint is behind the auth middleware).
  Log drains to external systems are a Phase 4 concern.
- **Performance**: file-backed JetStream is append-only with periodic
  compaction. The per-message overhead is negligible for log lines.

## 7. Acceptance (testable)

1. Deploy an application, generate runtime output → connect to the SSE
   endpoint **after** the output was emitted → the retained lines replay.
2. Wait > `MaxAge` → the old lines are gone; new lines still arrive.
3. Restart `cypherd` → runtime logs that are within the retention window are
   still replayable (they survived on disk).
4. Build logs are **not** on the `RUNTIME_LOGS` stream (they stay on `LOGS`).
5. The `RUNTIME_LOGS` stream's disk usage stays below `MaxBytes` under
   sustained load (verified by checking JetStream stream info).

## 8. Non-goals for this slice

External log drains (syslog, Loki, S3 — Phase 4) · per-application log
retention policies · log search/filtering on the plane (SSE streams raw
lines; structured log search is a UI concern) · compression of stored log
messages (JetStream's file storage already uses efficient encoding) ·
persistent build-log retention (build logs are deployment artifacts, not
operational logs).

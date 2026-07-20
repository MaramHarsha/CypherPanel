# Feature spec: Notifications

> A deploy fails at 3am; a backup silently stops running; a preview never
> tears down. Today the only witness is the panel's own logs. Notifications
> turn the state transitions the control plane **already observes** into
> messages an operator receives on the channel they already watch — Email,
> Discord, Slack, or Telegram. This is a **V1** feature
> ([feature-matrix.md](../product/feature-matrix.md)) and part of the Phase 3
> bucket ([roadmap.md](../roadmap.md)).
>
> Written 2026-07-20, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Event taxonomy ported from Dokploy's
> per-event notification files and Coolify's channel matrix
> ([research/dokploy.md](../../research/dokploy.md) row "Notifications",
> [research/coolify.md](../../research/coolify.md) row "Notifications") — logic
> only, never code (CLAUDE.md rule 1).

## 1. The core idea: react to observed state, don't invent a new one

A **Notifier** is a declarative row — "on these events, deliver to this
channel." That is its desired state. Delivery is not an imperative "poke the
server" path (CLAUDE.md rule 3): no agent and no server are involved at all.
It is a **plane-internal reaction** to the terminal state transitions the
scheduler already computes from agents' observed reports (ADR-005) — the same
transitions that today only write a log line and a status column. The
scheduler owns those transitions; this feature adds a fan-out at the moment
they happen.

```
agent observation ──▶ scheduler transition ──▶ status row (existing)
                                            └─▶ notify.Manager.Notify(event)   ← new
                                                    │
                                                    ▼  (project-scoped, best-effort, async)
                                   ┌────────┬────────┬────────┬──────────┐
                                 email    discord   slack   telegram
```

Delivery is **best-effort, fire-and-forget, and logged** — the reference tools
do the same (queued jobs, no durable per-message retry). A missed message must
never wedge or slow a deploy. Durable delivery with ret/ack is explicitly a
follow-on (§8); it is not needed to close the Phase 3 gate and adding it now
would be speculative machinery (YAGNI).

## 2. The resource model

A **Notifier** is scoped to a **Project** — the same rollup every workload
already has (`Application → Environment → Project`,
`Database → Environment → Project`). An event about a deploy in project *acme*
routes to *acme*'s notifiers. (Team-wide notifiers arrive with teams + roles,
the next Phase 3 item; project scope is forward-compatible — a team notifier
is just a broader scope on the same row shape.)

```
Notifier:
  id            TEXT PK (ntf_ prefix)
  project_id    TEXT NOT NULL → projects(id) ON DELETE CASCADE
  name          TEXT NOT NULL
  channel       TEXT NOT NULL          -- email | discord | slack | telegram
  config_ct     BYTEA NOT NULL         -- sealed channel config JSON (secret.Box)
  config_nonce  BYTEA NOT NULL
  events        TEXT[] NOT NULL        -- subscribed event types (§3)
  enabled       BOOLEAN NOT NULL DEFAULT true
  created_at, updated_at
  UNIQUE(project_id, name)
```

The **channel config is sealed at rest** (`secret.Box`, AES-256-GCM,
threat-model §5.1) because it carries credentials: an SMTP password, a bot
token, a webhook URL (a bearer capability — anyone with it can post to the
channel). It is **never returned in an API response** (rule 20) — responses
carry a masked hint only (§7).

`config` shape per channel (the sealed JSON):

| Channel  | Fields |
|----------|--------|
| email    | `smtp_host`, `smtp_port`, `username`, `password`, `from`, `to` (comma-list), `tls` (bool) |
| discord  | `webhook_url` |
| slack    | `webhook_url` |
| telegram | `bot_token`, `chat_id` |

## 3. Event taxonomy

v1 emits the events the plane **already reaches a terminal decision on** — no
new observation is invented:

| Event key          | Emitted from | When |
|--------------------|--------------|------|
| `deploy.succeeded` | `scheduler.HandleAppStatus` | a deployment completes (running observed) |
| `deploy.failed`    | `scheduler.fail`            | any stage fails (build/distribute/rollout) |
| `backup.succeeded` | `scheduler.HandleDbBackupEvent` | a backup record finishes ok |
| `backup.failed`    | `scheduler.HandleDbBackupEvent` | a backup record fails |

A Notifier subscribes to a subset via its `events` array; an empty selection
is rejected at validation (a notifier that notifies nothing is a
mistake). Preview lifecycle, cert-expiry, and server-threshold events are
follow-ons (§8) — they need observation points that don't all exist yet.

## 4. Dispatch — `core/notify`

A single `notify.Manager` owns fan-out:

```
Manager.Notify(ctx, projectID string, ev Event)
```

1. Load the project's **enabled** notifiers whose `events` contains `ev.Type`.
   (No notifiers → immediate return; the common case is cheap.)
2. For each, unseal `config` (via the shared `Opener`) and hand `(config, ev)`
   to the channel sender.
3. Each send runs **in its own goroutine with a short timeout** (10s); a
   failure is logged (`notify: delivery failed`, channel + notifier id + err)
   and dropped. One dead channel never blocks the others or the caller.

`Event` is a small plane-domain value — `Type`, a human `Title`, a `Body`, a
`Level` (info/error), and a few labeled fields (project, resource name,
detail). Senders render it to their channel's format:

- **discord / slack**: one HTTP `POST` of a JSON body to the stored
  `webhook_url` (Slack `{"text":…}`, Discord `{"content":…}`). stdlib
  `net/http`.
- **telegram**: `POST https://api.telegram.org/bot<token>/sendMessage` with
  `chat_id` + `text`. The bot token lives only in the URL path, built at send
  time from the unsealed config.
- **email**: `net/smtp` (stdlib) — `smtp.SendMail` with optional STARTTLS,
  one message to the `to` list. No new dependency.

The scheduler calls the manager through a **consumer-defined interface** (rule
6) so the dependency is optional and testable:

```go
// in core/scheduler
type Notifier interface {
    NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment)
    NotifyBackup(ctx context.Context, rec domain.BackupRecord, dbName string)
}
```

A `nil` notifier (feature not wired) means the scheduler skips the call — same
nil-guard pattern as `deps.Previews`. The manager resolves the project from
the app/database's environment and builds the `Event`; the scheduler stays
ignorant of channels and rendering.

## 5. Where the calls go (minimal, at the existing transitions)

- `scheduler.go` — after `UpdateDeploymentStatus(…DeploySucceeded)` in
  `HandleAppStatus`: `s.notify.NotifyDeploy(ctx, app, dep)` (success).
- `scheduler.go` — inside `fail(…)` after the status write:
  `s.notify.NotifyDeploy(ctx, app, dep)` (the dep now carries `DeployFailed` +
  detail, so one method covers both outcomes).
- `backups.go` — after `UpdateBackupRecord(…)`:
  `s.notify.NotifyBackup(ctx, rec, dbName)` (rec carries succeeded/failed).

Each is a single line at a point that is already the authoritative moment of
the transition, already under `s.mu`. The manager's own work happens in
goroutines, so holding the lock across the call stays cheap.

## 6. Security

- **Sealed config** (threat-model §5.1): credentials are `secret.Box`-sealed
  at rest, unsealed only at send time, never logged, never returned (§7).
- **No secret leakage into bodies**: notification `Body`/`detail` is built
  from deploy/backup metadata (app name, revision, failure detail already
  surfaced in the API) — never from env vars or sealed material. A fork-PR
  preview carries no secrets (preview-environments.md §6), so a preview-deploy
  notification can't exfiltrate one.
- **Outbound SSRF surface**: webhook/SMTP endpoints are operator-supplied and
  the operator is the trust root here (they already run arbitrary containers).
  We do **not** treat these as attacker-controlled at v1, but we do (a) require
  `https?://` (discord/slack) and reject other schemes, and (b) never follow
  the response body into anything — send-and-forget. Per-tenant egress
  policy belongs with teams + roles, not here.
- **Rate / abuse**: best-effort with a per-send timeout bounds a hung
  endpoint; a notifier can be `enabled=false` to silence it without deleting
  config. No unbounded retry loop exists to amplify.

## 7. API surface (under `/api/v1`)

```
POST   /projects/{id}/notifiers          → 201 Notifier   (create; body carries channel config)
GET    /projects/{id}/notifiers          → [Notifier]
GET    /notifiers/{id}                    → Notifier
PATCH  /notifiers/{id}                    → Notifier       (name, events, enabled, and/or new config)
DELETE /notifiers/{id}                    → 204
POST   /notifiers/{id}/test               → 202            (send a synthetic event to this notifier)
```

The `Notifier` DTO **never** includes the raw config. It carries `channel`,
`events`, `enabled`, and a `config_hint` — a channel-appropriate masked
summary (`smtp.acme.com → ops@acme.com`, `discord webhook •••1a2b`,
`telegram chat 12345`) so the operator can tell notifiers apart without the
secret. A PATCH that omits `config` keeps the sealed value; supplying `config`
reseals it wholesale (no partial-secret merge).

The **test** endpoint (`POST …/test`) delivers a synthetic
`deploy.succeeded`-shaped event through the one notifier so an operator can
confirm wiring at setup time — the single most valuable affordance the
references have (a notifier that silently never fires is the common footgun).

## 8. Acceptance (testable)

1. Create an email notifier on a project subscribed to `deploy.failed`;
   trigger a failing deploy → an SMTP message is sent to the `to` list, and
   the config is never present in any API response.
2. Create a Slack (webhook) notifier subscribed to `deploy.succeeded` +
   `backup.succeeded`; a successful deploy and a successful backup each POST a
   message; a `deploy.failed` for the same project does **not** (unsubscribed).
3. A notifier with `enabled=false` receives no deliveries.
4. `POST /notifiers/{id}/test` delivers exactly one synthetic message on that
   channel and returns 202.
5. A channel whose endpoint is unreachable logs a delivery failure and does
   **not** block the deploy pipeline or the other notifiers (parallel isolation).
6. Two projects with distinct notifiers: an event in project A reaches only
   A's notifiers.

## 9. Out of scope this slice

Durable delivery (retry/ack, dead-letter) · notification history/audit rows ·
per-event message templating / custom bodies · additional channels (Pushover,
Gotify, generic webhook, PagerDuty) · preview-lifecycle, cert-expiry,
server-threshold, and docker-cleanup events (need observation points not all
present yet — V1.x, feature-matrix.md) · team-scoped notifiers (arrive with
teams + roles) · dig/batching and quiet hours · inbound/interactive
(ChatOps) integrations.

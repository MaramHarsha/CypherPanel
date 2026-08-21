# Feature spec: Outbound webhooks (events for machines)

> [notifications.md](notifications.md) gave the **operator** a message when a
> deploy failed at 3am. It gave their **systems** nothing. An outbound webhook
> endpoint is the machine-facing twin of a Notifier: the same observed
> transitions, POSTed as signed JSON to a URL the operator controls, with a
> delivery id, a bounded retry, and a per-attempt record they can read back and
> replay. `core/notify` talks to people; `core/webhooks` talks to machines.
>
> Written 2026-08-21, just before implementing (CLAUDE.md rule 7). Vocabulary per
> [glossary.md](../glossary.md), which gains **Webhook Endpoint**, **Delivery**
> and **Endpoint Health** in the same PR plus a rule that "webhook" is always
> qualified *inbound* (push-to-deploy, `POST /webhooks/github/{id}`) or
> *outbound* (this feature) — unqualified it is as ambiguous as "Service".

## 1. The core idea: a sibling of `core/notify`, not a fifth channel

[notifications.md §9](notifications.md) deferred a "generic webhook" channel. We
are deliberately not building it as that fifth channel, because a human channel
and a machine channel want opposite things: prose versus a stable JSON contract;
the URL as the whole capability versus a per-endpoint HMAC; a dropped send versus
a retried one; a log line versus a queryable record of every attempt. Bolting
retry, delivery ids and an attempt log onto `notify.Manager` would make the four
human channels pay for machine semantics they do not want, and turn the one file
that is deliberately fire-and-forget (`notify.fanOut` — "one dead channel never
blocks the others or the caller") into a queue. So webhooks is a **sibling
package** that reuses exactly two things and forks nothing: the **event
vocabulary** (`domain.Event*` in `core/domain/notify.go`, §3) and the **fan-out
seam** (the scheduler's terminal-transition callback, §5).

Like notifications this is a **plane-internal reaction** to transitions the
scheduler already computes from agents' observed reports
([ADR-005](../adrs/ADR-005-desired-state-reconciliation.md)) — no agent, no
server, no NATS subject, nothing crossing the agent↔plane boundary. The endpoint
row itself *is* desired state ("these events go to this URL", CLAUDE.md rule 3);
delivery is the reaction, not an imperative poke at anything.

```
agent observation ──▶ scheduler transition ──▶ status row (existing)
                                            └─▶ EventSink fan-out (§5)
                                                  ├─▶ notify.Manager   → people   (existing)
                                                  └─▶ webhooks.Manager → machines (new)
                                                          │
                                                  persist delivery ─▶ attempt ─▶ retry sweeper
```

No ADR is required: a package under `core/` is not a top-level directory
(project-structure rule 1), the retry sweeper is the owned-ticker pattern already
used by `previews.RunSweeper` and `scheduler.RunBackupSweeper`, and the
desired-state model is unchanged.

## 2. The resource model

A **Webhook Endpoint** is scoped to a **Project**, exactly like a Notifier — the
same rollup every workload already has (`Application → Environment → Project`).

```
WebhookEndpoint:
  id            TEXT PK (whe_ prefix)
  project_id    TEXT NOT NULL → projects(id) ON DELETE CASCADE
  url           TEXT NOT NULL          -- http(s) receiver
  secret_ct     BYTEA NOT NULL         -- sealed HMAC signing secret (secret.Box)
  secret_nonce  BYTEA NOT NULL
  events        TEXT[] NOT NULL        -- subscribed event keys (§3), non-empty
  enabled       BOOLEAN NOT NULL DEFAULT true
  created_at, updated_at
  UNIQUE (project_id, url)
```

**No `name` column:** a Notifier needs one because "Slack" and "Slack (oncall)"
are otherwise indistinguishable, but an endpoint's URL already names it and is
what the operator debugs with — which is why the design board shows the bare URL
as the row's identity. `UNIQUE (project_id, url)` also stops the double-add that
would silently double every delivery.

**The secret is sealed, not hashed.** API tokens are stored as `sha256(raw)`
because the plane only ever *compares* them ([api-tokens.md §1](api-tokens.md)); a
signing secret must be *used*, so it is sealed with `secret.Box` (AES-256-GCM,
threat-model §5.1) and unsealed only at signing time — the same `*_ct`/`*_nonce`
pair as the inbound app webhook secret (`Application.WebhookSecretCT/Nonce`,
`core/applications/applications.go` L191–219). Minted with `ids.Secret()` and
**returned exactly once** in the create response, mirroring
`createApplicationResponse.Webhook` ("shown exactly once here and never
retrievable again", `handlers_applications.go` L207–217).

A **Delivery** is one event aimed at one endpoint; an **attempt** is one HTTP
request made for it — parent + child, the shape
[scheduled-tasks.md §2](scheduled-tasks.md) established:

```
WebhookDelivery:
  id              TEXT PK (whd_)  -- this id IS the X-CypherPanel-Delivery header
  endpoint_id     TEXT NOT NULL → webhook_endpoints(id) ON DELETE CASCADE
  event_type      TEXT NOT NULL   -- deploy.succeeded | … | webhook.ping
  resource_kind   TEXT NOT NULL   -- application | database
  resource_id     TEXT NOT NULL   -- app_… / db_… — deliberately NOT an FK
  resource_name   TEXT NOT NULL   -- denormalised feed label ("web", "atlas-pg")
  payload         TEXT NOT NULL   -- the exact JSON body bytes that get signed
  status          TEXT NOT NULL   -- pending | succeeded | failed
  attempt         INTEGER NOT NULL DEFAULT 0   -- attempts so far (the board's "×3")
  next_attempt_at TIMESTAMPTZ     -- NULL once terminal; drives the retry sweeper
  redelivery_of   TEXT            -- original delivery id when this is a replay
  created_at, updated_at

WebhookDeliveryAttempt:
  delivery_id     TEXT NOT NULL → webhook_deliveries(id) ON DELETE CASCADE
  attempt         INTEGER NOT NULL           -- 1..4
  response_status INTEGER                    -- NULL on a transport error
  duration_ms     INTEGER NOT NULL
  error           TEXT NOT NULL DEFAULT ''   -- redacted transport error, never a body
  at              TIMESTAMPTZ NOT NULL DEFAULT now()
  PRIMARY KEY (delivery_id, attempt)
```

Three decisions there. **`resource_id` is not a foreign key** — the log is
evidence and must outlive the resource it describes; a cascade would erase the
record of the failure that preceded a deletion, and `resource_name` is
denormalised so the feed still reads `backup.failed · atlas-pg` afterwards.
**`payload` is `TEXT`, not `JSONB`** — the HMAC covers the raw body bytes, and
`JSONB` round-trips through Postgres' own representation, which can reorder keys
and make a redelivery sign different bytes than the log shows. **Attempts have a
composite primary key, no minted id** — `(delivery_id, attempt)` makes an attempt
insert idempotent under redelivery: a duplicate lands as PG `23505` →
`store.ErrConflict`, which the manager treats as "already recorded" (rule 12).

Migration `0021_outbound_webhooks.sql` (0020 is the current highest), goose
Up/Down, additive (rule 16), with `idx_webhook_endpoints_project (project_id)`,
`idx_webhook_deliveries_endpoint (endpoint_id, created_at DESC)` and a partial
`idx_webhook_deliveries_due (next_attempt_at) WHERE status = 'pending'` so the
sweeper touches only what is due. `Down` drops attempts, deliveries, endpoints.

## 3. Event taxonomy — borrowed, not forked

The subscribable set is exactly the constants already in `core/domain/notify.go`:

| Event key | Emitted from | `resource_kind` |
|---|---|---|
| `deploy.succeeded` | `scheduler.HandleAppStatus` | `application` |
| `deploy.failed` | `scheduler.fail` | `application` |
| `backup.succeeded` | `scheduler.HandleDbBackupEvent` | `database` |
| `backup.failed` | `scheduler.HandleDbBackupEvent` | `database` |

To keep one vocabulary in one place, `core/domain/notify.go` gains `EventTypes()`
and `ValidEventType(string) bool`, and `notify.validEvents` — today a private map
in `core/notify/service.go` — is replaced by a call to it. Adding an event key
then means editing one file, and notifiers *and* endpoints gain it at once.

**`scale.changed` is not shipped.** The board lists it as a subscribed chip, but
there is no transition to emit it from: replicas are fixed at 1 for v1
(`applications.go` rejects `runtime.replicas != 1`; openapi.yaml says "Fixed at 1
for v1") and horizontal replica scaling is a **Later** row in
[feature-matrix.md](../product/feature-matrix.md), restated under "Post-v1
directions" in [roadmap.md](../roadmap.md). A key that can never fire is worse
than no key — the endpoint looks wired and stays silent, the exact footgun
[notifications.md §7](notifications.md) built its test affordance to prevent — and
it would be a placeholder implementation (rule 10). When scaling lands,
`scale.changed` is one constant plus one call, and both features pick it up.

**`webhook.ping` is delivery-only:** the ping route (§7) sends a real, signed,
logged delivery with `event_type = webhook.ping`, but the key is not a member of
`EventTypes()` and is rejected inside an `events` array (400). An endpoint always
receives its own pings regardless of subscription — that is what makes ping usable
as the setup check.

## 4. Delivery: sign, attempt, record, retry — `core/webhooks`

The package mirrors `core/notify` file for file: `service.go` (CRUD, sealing,
validation), `webhooks.go` (the Manager), `sign.go` (scheme and headers, where
`notify/channels.go` holds transports).

**Payload** — one envelope for every event, `Content-Type: application/json`:

```json
{ "event": "deploy.failed", "delivery_id": "whd_ba7f…",
  "occurred_at": "2026-08-21T09:14:03Z",
  "project":  { "id": "prj_…", "name": "atlas-crm" },
  "environment": { "id": "env_…", "name": "production" },
  "resource": { "kind": "application", "id": "app_…", "name": "web" },
  "data": { "deployment_id": "dep_…", "revision_id": "rev_…", "status": "failed",
            "trigger": "webhook", "detail": "health check never passed" } }
```

Backup events set `resource.kind` to `database` and carry `backup_record_id`,
`backup_id`, `status`, `detail` in `data`. The envelope is a **published
contract**: fields are only ever added, never removed or retyped (rule 17 binds
what we emit, not only what we accept). The manager resolves `GetEnvironment`
**and** `GetProject` so both names are accurate — `notify.dispatch` currently
assigns the *environment* name to `ev.Project`, and this slice does not inherit
that.

**Signing** — over the raw body, keyed by the endpoint's unsealed secret:

```
X-CypherPanel-Event:     deploy.failed
X-CypherPanel-Delivery:  whd_ba7f…      (stable across retries; new on a redelivery)
X-CypherPanel-Timestamp: 1787303643     (unix seconds, per attempt)
X-CypherPanel-Signature: sha256=<hex>   over  timestamp + "." + rawBody
```

The `sha256=<hex>` shape is deliberate — it is what this repo already parses for
inbound GitHub deliveries (`verifyWebhookSignature`,
`core/api/rest/handlers_deployments.go`), so a receiver can reuse any
GitHub-webhook recipe. The timestamp sits **inside** the signed string so a
captured body cannot be replayed indefinitely. Receiver-side verification,
documented on the endpoint in `openapi.yaml`:

```go
mac := hmac.New(sha256.New, []byte(endpointSecret))
mac.Write([]byte(ts + "." + string(rawBody)))          // ts = X-CypherPanel-Timestamp
got, ok := strings.CutPrefix(sig, "sha256=")           // sig = X-CypherPanel-Signature
want := hex.EncodeToString(mac.Sum(nil))
if !ok || !hmac.Equal([]byte(want), []byte(got)) { reject() }  // constant time — rule 21
if abs(now-atoi(ts)) > 300 { reject() }                        // 5-minute replay window
// Verify BEFORE parsing: a signature over reserialised JSON signs the wrong bytes.
```

**Attempt policy.** An attempt is a POST with a 10s timeout (`deliveryTimeout`,
the bound `core/notify` already uses), redirects **not** followed, response body
read-and-discarded up to 4 KB and never stored (§6). Retryable: transport error,
`429`, any `5xx`. Terminal: any other `4xx`, or a `3xx` — the receiver answered,
and a 401 or 404 will answer the same in five minutes. Four attempts total:
immediately, then `+1 min`, `+5 min`, `+25 min` (×5 backoff, ±20 % jitter), a
~31-minute horizon. Three retries is the board's "retrying ×3"; the jitter keeps
many endpoints pointed at one recovering receiver from re-synchronising.

**Who drives it.** The first attempt runs in a detached goroutine exactly as
`notify.dispatch` does — `context.WithoutCancel(ctx)` plus its own timeout — so a
finished request cannot cancel an in-flight send and the caller returns
immediately. Retries come from `Manager.RunRetrySweeper(ctx, interval)`, an owned
ticker with a cancellation path (rule 7), wired in `main.go` beside
`previewMgr.RunSweeper` and `sched.RunBackupSweeper` on `cfg.SweepInterval` (30s
default). Because `next_attempt_at` is in Postgres and the delivery row is written
**before** the first attempt, a plane restart mid-backoff loses nothing (rule 15).

**Endpoint Health** (the board's `● HEALTHY`) is derived, never stored — from the
ten most recent *terminal* deliveries: `unknown` when the endpoint is disabled or
has none yet, `healthy` when all ten succeeded, `degraded` when the most recent
succeeded but at least one of the ten failed, `failing` when the most recent
exhausted its attempts. A disabled endpoint is `unknown` rather than a fifth word:
nothing is being attempted, so we cannot claim health (ui-principles §5 forbids
faking certainty), and the UI writes the plain word "Disabled" beside the URL.
This is a new, deliberately tiny vocabulary — ui-principles §5's status words
describe a **workload lifecycle** and an endpoint is not a workload, so calling a
URL "running" would be worse copy, not better consistency. Two of the four words
carry over verbatim, glossary.md gains **Endpoint Health** (ui-principles §8
requires that PR first), and rendering goes through the one existing renderer:
`StatusDot`, mapped `healthy` → green dot, `degraded` → amber, `failing` → red
square, `unknown` → hollow — the board's own `●`/`■` pair.

## 5. The fan-out seam — where the calls go

The scheduler holds **one** optional notifier (`core/scheduler/scheduler.go`
L105–137) and calls it at three sites, each guarded by `if s.notify != nil`:
`scheduler.go` L768 (`HandleAppStatus`, after `UpdateDeploymentStatus(…Succeeded)`),
`scheduler.go` L829 (`fail`, after the status write), and `backups.go` L207
(`HandleDbBackupEvent`). That is already the right seam; it is generalised in
place, not duplicated:

```go
// core/scheduler — renamed from Notifier; same two methods, now more than one sink.
type EventSink interface {
    NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment)
    NotifyBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord)
}

func (s *Scheduler) AddSink(k EventSink)   // replaces SetNotifier; sinks []EventSink
```

The three call sites become `s.emitDeploy(...)` / `s.emitBackup(...)`, private
helpers that loop the slice — the `!= nil` guards disappear because an empty slice
is already a no-op — and `main.go` registers both sinks. The rename is
**Go-internal** (rule 17 governs the published REST API, not Go identifiers), and
`SetNotifier` is *replaced* rather than kept beside `AddSink` because two ways to
register one thing is how a second sink gets silently dropped; the two
`scheduler_test.go` call sites change by one word each. The scheduler learns
nothing new — it still hands over domain values and stays ignorant of URLs,
signing and retry, which is why the seam absorbed a second consumer without a
redesign. Delivery must never block or fail a deploy: each sink returns
immediately after detaching (§4), and a store failure while persisting a delivery
is logged with the endpoint and deployment ids and dropped, because the deploy is
already recorded and its status is authoritative.

## 6. Security & bounds

- **Sealed signing secret** (threat-model §5.1): `secret.Box` at rest, unsealed
  only to sign, never logged, never in a response (rule 20); shown once at create
  and once at rotate, so a database read yields ciphertext.
- **The payload carries no sealed material** — only deploy/backup metadata already
  surfaced through the API; never env vars, connection strings, or anything from a
  `*_ct` column. An endpoint URL is nonetheless an operator-chosen egress path for
  project metadata, which is why every route is authorized at the project level.
- **Response bodies are never stored or logged** — status, duration and a
  transport error only. A receiver's error page can carry its own secrets and has
  no size bound we control. The error string goes through the existing `redactURL`
  defense (`core/notify/channels.go`) so a secret-bearing URL cannot ride out
  inside a `*url.Error`; header values are `sanitizeHeader`-stripped of CR/LF.
- **Outbound SSRF surface**: same posture as
  [notifications.md §6](notifications.md) — the operator is the trust root, they
  already run arbitrary containers. We still enforce `http`/`https` only (the same
  `validateHTTPURL` notifiers use), **no redirect following** so a receiver cannot
  bounce a signed body elsewhere, and the 10s per-attempt timeout.
- **Bounded** (threat-model §5.9): four attempts, a ~31-minute horizon, and the
  **200 most recent deliveries per endpoint** retained — older rows pruned on
  insert with the same `DELETE … WHERE id NOT IN (SELECT … LIMIT $2)` pattern as
  `DeleteOldTaskRuns`, attempts cascading. Payloads are metadata-only, so the
  worst case stays under 1 MB per endpoint.
- **Authorization**: project-scoped via new `projectIDForWebhookEndpoint` and
  `projectIDForWebhookDelivery` resolvers in `core/api/rest/authz.go`, minimum
  rank `RoleMember` on every route — **the same rank as notifiers**, deliberately:
  an endpoint is the machine-facing twin of a notifier with the identical risk
  shape (a sealed credential plus an operator-chosen egress URL), so splitting the
  rank would make the pair inconsistent for no security gain. Non-member → 404,
  under-ranked → 403. All routes are ordinary `a.authed`; reads need `read`,
  mutations (including `redeliver` and `ping`) need `write`; nothing triggers a
  deploy, so `deployRoutes` is untouched.

## 7. API surface (under `/api/v1`)

```
POST   /projects/{id}/webhook-endpoints        → 201 {endpoint, secret}  (secret shown once)
GET    /projects/{id}/webhook-endpoints        → [WebhookEndpoint]
GET    /webhook-endpoints/{id}                 → WebhookEndpoint
PATCH  /webhook-endpoints/{id}                 → WebhookEndpoint         (url, events, enabled)
DELETE /webhook-endpoints/{id}                 → 204
POST   /webhook-endpoints/{id}/rotate-secret   → 200 {secret}            (shown once)
POST   /webhook-endpoints/{id}/ping            → 202 WebhookDelivery     (synthetic webhook.ping)
GET    /webhook-endpoints/{id}/deliveries      → 200 {deliveries, next_before}
POST   /webhook-deliveries/{id}/redeliver      → 202 WebhookDelivery
```

The `WebhookEndpoint` DTO carries `id`, `project_id`, `url`, `events`, `enabled`,
`health` (§4), `last_delivery_at` and timestamps — **never** the secret, which is
structurally absent from the struct so it cannot leak by accident. PATCH follows
the repo's uniform semantics: supplied fields overwrite, omitted fields keep.

**Paging** is seek-based and this is the repo's first paged endpoint, so the shape
is a precedent: `?limit=50&before=whd_…` returns newest-first plus a cursor,
`{ "deliveries": [ … ], "next_before": "whd_9c1a…" }`. Offset paging over a
continuously-prepended table re-shows and skips rows as deliveries land
mid-scroll; seeking on `(created_at, id) DESC` is stable and matches the
`ORDER BY … LIMIT $2` recency queries already in `queries/deployments.sql` and
`queries/scheduled_tasks.sql` (ui-principles §7). `limit` defaults to 50 and caps
at 100; an envelope rather than a bare array because a cursor has nowhere else to
live, and a client that ignores it still gets the newest page.

**Rotate** re-seals a fresh secret and returns it once, with **no overlap
window** — two simultaneously valid secrets is real machinery (a second sealed
column, an expiry, a two-signature header) for a rare event the operator
schedules; `ping` confirms the new one in a click, and §9 records the grace window
as the follow-on. **Ping** delivers a synthetic `webhook.ping` (§3) so an operator
can prove the wiring at setup time, before any real deploy — the same affordance
and reasoning as `POST /notifiers/{id}/test` — and it gives a new endpoint its
first terminal delivery, so `health` stops being `unknown` on day one rather than
at the first failure. **Redeliver** mints a **new** delivery (new `whd_` id, new
signature, fresh attempt schedule) copying the original's event, resource and
stored payload bytes, with `redelivery_of` set; the original row is never mutated
because the log is evidence. `409` if the endpoint is disabled, `404` if it is
gone. Every route lands in `core/api/rest/openapi.yaml` first (rule 19) under a
new `webhooks` tag, and the TypeScript client is regenerated from it.

### The panel surface

`web/src/routes/_app/projects/$projectId/settings.tsx` becomes a two-tab layout —
**Notifiers** (`settings/index.tsx`: the existing list and danger zone, moved
unchanged) and **Webhooks** (`settings/webhooks.tsx`) — following the tab strip in
`_app/settings.tsx`. The board datelines the screen `ATLAS-CRM / SETTINGS /
WEBHOOKS`, which is a route, not an anchor; a "General" tab that would let the
danger zone leave the Notifiers tab is a follow-on.

The Webhooks tab: a `+ Add endpoint` action; the lead line *"Notifiers talk to
people; webhooks talk to your systems. Signed with a per-endpoint secret (shown
once)."* with **Notifiers** linking to the sibling tab (ui-principles §11); then
per endpoint the URL in mono, its health badge, and its subscribed event keys as
mono chips. Below, the delivery feed — `● deploy.succeeded · web · 200 · 84ms`
with a relative timestamp, and `■ backup.failed · atlas-pg · 500 · retrying ×3`
with a **redeliver** button; a pending delivery shows `retrying ×N` where a
terminal one shows its duration. The create dialog surfaces the secret once
through `SecretField`, with a copy button, and says it will not be shown again.
All four page states ship, and the feed carries its own four as a data region
(ui-principles §1). Nothing here streams — the SSE `invalidate` event covers only
applications and databases — so mutations invalidate the generated list keys by
hand.

## 8. Acceptance (testable)

1. Create an endpoint subscribed to `deploy.succeeded`; run a passing deploy → the
   receiver gets exactly one POST whose `X-CypherPanel-Signature` verifies against
   the secret returned at create, and the delivery is logged `succeeded` with a
   `200` and a duration. The secret appears in **no** GET, PATCH or list response
   (asserted structurally on the DTO).
2. Receiver answers `500`, `500`, `200` → one delivery row, three attempt rows with
   increasing `attempt`, final status `succeeded`, backoff timings inside the
   documented windows (injected clock, rule 9). Receiver answers `404` → exactly
   one attempt, terminal, no retry scheduled.
3. Receiver answers `500` forever → exactly four attempts, terminal status
   `failed`, `health` becomes `failing`, and the deploy that produced the event
   completes identically to a run with no endpoints configured — the "never blocks
   a deploy" case.
4. Redelivering that failed delivery creates a new row with a fresh id and
   `redelivery_of` set, leaves the original unchanged, and the receiver sees a
   different `X-CypherPanel-Delivery`.
5. `enabled=false` receives nothing on a matching event and reports
   `health: unknown`; `ping` on a disabled endpoint is refused.
6. Paging (real-Postgres store test): with 120 deliveries, `?limit=50` returns the
   50 newest plus a `next_before`; following it twice yields the remaining 70 with
   no repeats and no gaps. The 201st delivery prunes the oldest, attempts included.
7. Restart the plane mid-backoff → a delivery whose `next_attempt_at` has passed is
   attempted by the sweeper afterwards (rule 15). Two projects with distinct
   endpoints: an event in A reaches only A's endpoints (fan-out plus authz
   boundary test, non-member → 404).

## 9. Out of scope this slice

No new event types — `scale.changed` (§3), preview lifecycle, cert expiry, server
thresholds, docker cleanup: they need observation points that do not all exist yet
· no per-endpoint payload templating, field selection, or custom headers · no
secret-rotation grace window (rotation is immediate; two simultaneously valid
secrets is the follow-on) · no filtering or search on the delivery log (`limit` +
`before` only) · no stored response bodies (§6) · no authentication scheme beyond
HMAC (no mTLS to the receiver, no bearer, no OAuth) · no per-endpoint rate
limiting, batching, or ordering guarantee — delivery is **at-least-once and
unordered**, and receivers dedupe on `X-CypherPanel-Delivery` · no live streaming
of the delivery feed · no team-scoped or panel-scoped endpoints (project scope
only, exactly like notifiers) · no automatic disabling of a persistently failing
endpoint (it is reported `failing`, never silently switched off) · **not a log
drain** — this carries control-plane events, not container logs, a separate V1.x
row in [feature-matrix.md](../product/feature-matrix.md) · and no change to the
inbound direction: push-to-deploy webhooks stay where they are.

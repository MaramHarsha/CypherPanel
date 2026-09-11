# Feature spec: Log drains

> The panel keeps 24 h of runtime logs (512 MiB cap) on its own disk
> ([bounded-log-retention.md](bounded-log-retention.md)). A **Log Drain**
> streams them onward to a system built for longer retention and search: Loki,
> syslog, or an S3-compatible bucket by nightly batch. Credentials are sealed
> and write-only, exactly as a Notifier's and a Registry's are.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md); the screen is canvas 13f.

bounded-log-retention.md §8 lists external drains first among five non-goals —
the others are per-application retention policies, log search on the plane,
compression of stored messages and persistent build-log retention — and its §6
promised runtime logs are "retained only on the plane's own disk (no external
drain in Phase 2)". This is the sentence that
stops being true, so it is the sentence this spec has to earn.

## 1. What a drain is, and what it must not become

The panel's retention is a **window**, not an archive: 24 h, 512 MiB,
oldest-first discard. That is the right size for "what happened at 03:00" and
the wrong size for "what did this endpoint return last Tuesday". The fix is not
a bigger window — a bigger window is the silent disk fill
[threat-model §5.9](../security/threat-model.md) names as the failure both
reference platforms are known for, and which
[disk-management.md](disk-management.md) has just finished closing. It is to
hand the lines to something whose job is keeping them.

A drain is therefore the panel's **outbox for log lines**, and the design is
three refusals:

1. **It must never block or slow a deploy** —
   [notifications.md](notifications.md) §1's rule, applied to a stream three
   orders of magnitude busier than a notification.
2. **It must never grow unbounded.** §6, and it costs no new storage for two of
   the three types.
3. **It must never become a second log store on the plane.** The feature matrix
   says why in the row that asks for this: *"long retention and complex queries
   stay off-platform (vision.md footprint budgets)."*

## 2. Where a drain runs: the agent, or the plane

The feature matrix has already guessed — *"agent already tails everything, sinks
are cheap"* is an agent-side design in eight words — and the premise is true, so
it deserves a hearing.

**For the agent.** The lines originate there, so shipping from the host that
produced them is one hop instead of two, and it puts the shipper beside the sink
in the topology where Loki runs next to the workload rather than next to the
panel. It survives a control-plane outage, scales with the fleet for free, and
is what every log agent in the industry actually does.

**For the plane.** Every runtime line is *already* on the bus, in a durable,
bounded, replayable stream with per-consumer cursors — `RUNTIME_LOGS`, file-backed
and `DiscardOld` at 24 h / 512 MiB (`core/bus/bus.go:310-322`,
`core/config/config.go:126,136`). ADR-003 names only the `logs.*` subject family;
the stream itself is bounded-log-retention.md §2–§3's.
An agent-side shipper would rebuild that machinery on each host's local disk — a
spool, a cursor, a retention bound, a backoff — because one that buffers in
memory loses the buffer on every agent restart, and ADR-010 restarts agents on
purpose.

**Decided: the drain runs in the plane.** Four reasons, in the order they bind:

- **Credentials.** Agent-side shipping puts the Loki token, the syslog client
  key and the S3 secret in `DesiredState`, on every node.
  [registries.md](registries.md) §5 settled this argument's shape for the
  closest analogue: *"an agent holds no registry credentials of its own … a
  captured agent disk yields nothing."* A drain credential is worse — one
  credential copied to N hosts rather than one attached to a build — and
  threat-model §5.2 is precisely the one-compromised-agent case.
- **Footprint.** vision.md non-negotiable 1 is a number: agent idle < 50 MB RSS.
  A batching Loki client, a reconnecting syslog client and a gzip spooler are
  not free, and agent-side they are paid on every node — including the $5 VPS
  the vision's success criterion names. The plane pays once (§11).
- **Version skew.** A shipper bug on the agent is a fleet rollout to fix
  (ADR-010); in the plane it is one binary — and third-party wire formats,
  timestamps and backoff are exactly the code where the second bug is found by
  the first bug's fix.
- **It needs nothing on the wire.** Agent-side means a new `DesiredState` field,
  a proto message, a reconciler and an absence-means-remove contract to get
  wrong. Plane-side means none of that: no wire change, no reconciler, no
  `DesiredState` field, `proto/` untouched — one consumer on a stream that
  already exists. It is not *no agent change at all*, and saying so would be
  the kind of claim §8 then contradicts: moving the SigV4 signer to `pkg/s3`
  recompiles `agent/driver/docker`, which
  `agent/cmd/cypher-agent/main.go:262` wires up. That is a refactor with the
  backup tests as its gate, not a protocol change with a fleet rollout behind
  it, and the difference is the whole point of this bullet.

**What it costs, plainly.** *Topology:* a sink far from the panel gets the bytes
the long way round. The agent→plane leg is not the extra hop it looks like —
`agent/stream` publishes every managed container's output to
`logs.<server>.runtime.<app_id>` drain or no drain, to make the panel's own log
pane work — so the only leg this adds is plane→sink, which agent-side would pay
as agent→sink. *A plane outage stops shipping:* true, and it changes nothing,
because the agent publishes with fire-and-forget `nc.Publish`, so a down plane
is already a window of lost lines. This adds no new loss window; it inherits the
one §7 must be honest about. *The plane does more work:* a drain is machinery of
the kind `core/notify` and `core/webhooks` already are, so vision non-negotiable
5 holds — but it is user-configured load, and §11 bounds it.

## 3. The resource model

A **Log Drain** is panel-scoped: it spends the panel's stream, CPU and egress,
and its failure is a fact about the panel rather than about a project. (Who may
create one is the sharper question — §9.)

```
LogDrain:
  id            TEXT PK (ldr_… prefix)
  name          TEXT NOT NULL UNIQUE       -- "loki-main", "cold-archive"
  kind          TEXT NOT NULL              -- loki | syslog | s3
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE          -- NULL = all projects
  target_id     TEXT REFERENCES backup_targets(id) ON DELETE RESTRICT   -- kind=s3 only
  config_ct     BYTEA NOT NULL             -- sealed under the master key (secret.Box)
  config_nonce  BYTEA NOT NULL
  enabled       BOOLEAN NOT NULL DEFAULT true
  -- Observed, written by the shipper; never operator-set.
  last_shipped_at TIMESTAMPTZ
  last_error      TEXT    NOT NULL DEFAULT ''
  last_error_at   TIMESTAMPTZ
  dropped_lines   BIGINT  NOT NULL DEFAULT 0  -- discarded by retention, unshipped
  created_at, updated_at TIMESTAMPTZ
```

`0040_log_drains.sql`: one new table, no existing one altered, and a
`-- +goose Down` that drops it — rule 16 asks for reversible, and every
migration in the tree carries one, including
[0039_server_disk.sql](../../core/store/migrations/0039_server_disk.sql), the
highest today, which drops its three columns.

**The whole config is sealed**, not just its secret field, because *which* half
of a Loki config is a secret changes per deployment (`X-Scope-OrgID` is a tenant
id at one site and an access boundary at another). The API therefore answers with
a masked **`config_hint`** — the field `toNotifierDTO` builds by unsealing
in-process, calling `notify.ConfigHint` and discarding the plaintext
(`core/api/rest/handlers_notifiers.go:25,30-48`), which panel mail
(`handlers_mail.go:29`) and the DNS provider (`handlers_dns.go:30`) already reuse
under that name. Not registries' `token_set` (`handlers_registries.go:45`): a
boolean fits a credential with one secret field, and sealing a whole config is
the case `config_hint` was built for. Alongside it, the non-secret half the list
screen prints as plain DTO fields.

**Scope is all projects, or one project.** Nothing finer: every shipped line
already carries its environment as a label (§4), so a sink filters better than
we can, and a per-environment drain multiplies consumers and connections by the
number of environments. Scope answers a *tenancy* question — whose logs leave
the panel — and tenancy stops at the project.

**An S3 drain names an existing Backup Target**, the panel's S3 destination with
sealed keys, bucket, region and prefix
([0007](../../core/store/migrations/0007_database_backups.sql)). A second sealed
S3 credential inside `config_ct` would mean rotating a Backblaze key in two
places and finding the second one at 02:00 on the night the batch fails.
`ON DELETE RESTRICT` for the reason applications use it for a registry, and
deleting a target a drain uses is a `409` that **names the drain** — which is
work, not a consequence of the constraint. `handleDeleteBackupTarget` maps
`store.ErrInUse` to one fixed, name-free sentence today: *"backup target is in
use by one or more backup schedules"*
(`core/api/rest/handlers_backups.go:178-188`). This feature widens it to name
what actually blocks — schedules *and* drains — on the precedent of registries'
`GET /registries/{id}/used-by` (`core/api/rest/rest.go:725`), which is where
naming the blocker rather than counting it was settled. The cost is
a glossary widening — **Backup Target** becomes *"an S3-compatible storage
destination the panel writes to — database backups, and log archives when a Log
Drain names one"* — and ui-principles §8 requires that PR first.

## 4. What is on the stream, and what a line becomes

Verified, not assumed, because it bounds what a drain may honestly claim:

- `RUNTIME_LOGS` captures `logs.*.runtime.>`, and its only publisher is
  `agent/stream`, which streams containers matching `cypherpanel.managed=docker`
  — a label the **Application** rollout path sets and nothing else does. Managed
  databases carry `cypherpanel.db.managed`; a Compose Stack's containers carry
  compose's own labels. **A drain ships application runtime logs and nothing
  else today** — a gap in those two features, not this one, and the copy says
  "application logs" so nobody discovers it later.
- **A message is a bare, already-normalized line.** `agent/stream` demultiplexes
  Docker's 8-byte frame header, splits on newlines and publishes the bytes —
  but not quite the container's bytes: a trailing `\r` is stripped and an empty
  line is dropped entirely (`agent/stream/stream.go:107-113`). The stream
  descriptor (stdout vs stderr) is read and discarded, and no timestamp is
  attached.

So the record is composed by the drain: `ts` from the JetStream message's own
timestamp, `line` as the agent normalized it, `server` from the subject, and
`app` / `project` / `environment` resolved from the subject's app id.

**A drain ships what the agent normalized, not what the container wrote.** Blank
lines are the visible loss: a format that delimits records with an empty line
arrives without its delimiters, and CRLF output arrives LF. That is the literal
content of §7's *"a best-effort copy of what the panel saw"* — the panel's log
pane reads the same normalized lines, so pane and drain never disagree, which is
the property worth keeping. Widening it is a change in `agent/stream`, on the
hot path, for every reader; not here.

**`ts` is plane receive time, and the docs say so.** Skew is agent→plane
transit — milliseconds, except when an agent reconnects and re-attaches with
`tail=100`, replaying a hundred old lines stamped *now*. Re-deriving emit time by
parsing the line is what every log platform tries and gets wrong on someone's
format; stamping it on the agent is a wire change, a per-line cost on the hot
path and a clock we do not control. Worth doing when agent-side timestamps exist
for other reasons; not before.

**Labels need an app→project lookup** the subject does not carry: one read-only
store query (`ListLogOrigins`) feeding a small map, refreshed on the sweep tick
and on a miss. A line whose app id resolves to nothing is shipped by an
all-projects drain with the ids it has and **dropped by a project-scoped
drain**: it cannot be proven in scope, and sending another team's output to the
wrong sink is the one mistake here that cannot be undone.

**Nothing is redacted.** The panel does not scan log bodies for secrets and will
not pretend to: an application that prints its own connection string prints it
to the drain, exactly as it prints it to the log pane today. The UI says that in
one line where a drain is created.

## 5. The consumer: one drain, one cursor, one owned goroutine

`core/logdrain` is a sibling of `core/notify` and `core/webhooks`, structured
like them: `service.go` (CRUD, sealing, validation), `logdrain.go` (the
Manager), `sinks.go` (the three transports, where `notify/channels.go` holds its
four). Each enabled drain owns one **durable** consumer on `RUNTIME_LOGS`, named for
its drain id, filtered to `logs.*.runtime.>`. Durable, unlike every existing log
consumer, and that is the most important line here: a durable consumer's ack
floor survives a plane restart, so a drain resumes where it stopped instead of
re-shipping a day or skipping one. The plane owns consumer lifecycle exactly as it does for work items
(`Bus.EnsureWorkConsumer`, `core/bus/bus.go:396-410`; threat-model §5.2); the
agent has no part in it and no grant to it.

**There is nothing in `core/bus` to reuse for it.** Durable consumers exist for
WORK and for STATE (`consumeState`, `bus.go:479`) and nowhere else; both log
subscriptions are *ephemeral* ordered consumers that ack nothing
(`SubscribeRuntimeLogs` → `subscribeStream`, `bus.go:339-390`). §2's "one
consumer on a stream that already exists" is exact about the stream and
optimistic about the plumbing: this is a new exported `Bus` method plus a
manual-ack consume path — which is not incidental, because the manual ack is
where §6's entire backpressure design lives.

The Manager is a **reconciler, not an event handler**: each sweep tick it diffs
enabled drains in Postgres against the goroutines and consumers it runs, starts
what is missing, stops what is gone, and deletes the consumer of a drain that no
longer exists. A drain created through the API starts within a tick with no
restart; a plane that crashed mid-change converges on boot. Every goroutine has
an owner, a cancellation path and an observed failure (rule 7).

Shipping is batched — `CYPHERD_DRAIN_BATCH_LINES` lines or
`CYPHERD_DRAIN_BATCH_INTERVAL`, whichever comes first — and **a batch is acked
only after the sink accepts it**. That one rule is the entire backpressure
design.

## 6. When the far end is down

**It cannot block a deploy, structurally.** The drain is a *reader* of a stream
the deploy path writes to: no call site in the scheduler, no `if drain != nil`
in a rollout, nothing awaited. That is stronger than "fire-and-forget in a
detached goroutine" — deleting `core/logdrain` from the binary would change no
other package's behaviour.

**It cannot grow unbounded, and needs no new storage to promise it.** A failing
sink means the batch is not acked, so the cursor does not advance, so the
backlog stays **in `RUNTIME_LOGS`** — already file-backed, already capped at
24 h and 512 MiB, already `DiscardOld`. A drain down an hour resumes an hour
behind. A drain down a week resumes at the oldest message still held, having
lost the rest, and says so: `dropped_lines` counts what the window discarded
unshipped.

That is the second reason to ship from the plane: **the buffer already exists,
is already bounded, and is already paid for.** The shape this repo would
otherwise reach for — a `log_drain_deliveries` table like `webhook_deliveries`,
with attempts and `next_attempt_at` — is right for an event arriving once a
deploy and catastrophic for one arriving thousands of times a second. A row per
log line is threat-model §5.9's disk fill, written by us, on purpose.

**Backoff** is exponential with full jitter from 1 s, capped at
`CYPHERD_DRAIN_MAX_BACKOFF` (60 s) — far shorter than the webhooks' ~31-minute
horizon (immediately, then +1 min, +5 min, +25 min:
`core/webhooks/webhooks.go:33-34,61-64`), because there sleeping is free and here every second asleep spends the
retention window that is acting as the buffer.

**A failing drain is never auto-disabled.** That turns a visible failure into a
silently stopped pipeline that does not resume when the sink returns, and the
operator finds out when they go looking for last week's logs. It retries at the
cap forever, and the panel says which state it is in:

| State | Shown as | Means |
|---|---|---|
| `shipping` | `● SHIPPING` (green) | a batch was accepted recently |
| `idle` | `IDLE` (muted, no dot) | enabled and healthy, nothing to ship |
| `retrying` | `● RETRYING` (amber) | failing under five minutes |
| `failing` | `■ FAILING` (red) | failing five minutes or more |
| `disabled` | `DISABLED` (hollow) | switched off; nothing is attempted |

Derived, never stored — the pattern and the reasoning
[outbound-webhooks.md](outbound-webhooks.md) §4 recorded for Endpoint Health,
including why this is a small separate vocabulary rather than ui-principles §5's
words: those describe a workload lifecycle and a drain is not a workload.
`glossary.md` gains **Log Drain** and **Drain State** in the same PR as §3's
widening.

**Two panel-level inbox kinds**, `drain.failing` and `drain.recovered`, fire on
the **transition** and never per attempt — disk-management.md §5's rule, for its
reason. Panel-level rather than subscribable because a drain may span every
project and a Notifier is scoped to one; channel delivery therefore waits on
panel-level notifiers, which still do not exist. The same real gap, named again
rather than papered over.

## 7. Ordering and delivery: what is promised, and what is not

**Promised.** Within one application, lines ship in the order the plane received
them. From the stream onward, delivery is at-least-once: an unacked batch is
redelivered.

**Not promised, and why:**

1. **Not exactly-once.** A batch the sink accepted but whose ack did not land is
   redelivered, and there is nothing to deduplicate on: the payload is a bare
   line, so two identical lines a second apart are indistinguishable from one
   line delivered twice. An id would have to be stamped at publish time on the
   agent, which is the wire change §4 declined.
2. **Not ordered across applications.** Each application is its own subject; a
   batch interleaves whatever the consumer read, and entries are sorted by
   timestamp only within one label set — what Loki requires, and all it
   requires.
3. **Not delivered end-to-end.** The agent→plane publish is core-NATS
   fire-and-forget: no ack, no local spool, so a line emitted while an agent is
   disconnected never reaches the stream and no drain can ship it. Inherited
   behaviour, not introduced — it is why the panel's own log pane has the same
   hole.
4. **Not complete across a long outage.** §6: the retention window is the
   buffer, and `dropped_lines` makes the loss visible instead of silent.
5. **Duplicates already exist upstream.** `agent/stream` attaches with
   `tail=100`, so a restart replays up to a hundred lines. Nothing deduplicates
   them today and the drain does not either.

The sentence that belongs in the API description and the UI: **a drain is a
best-effort copy of what the panel saw.** It is not a compliance archive, and a
panel that needs one should ship from the workload with a tool built for it.
Strengthening any of that is a project, not a flag — publish acks and an
agent-side spool (undoing §2's footprint argument), a per-line id, and a sink
that can deduplicate, which none of Loki, syslog or S3 is. Saying so costs
nothing; discovering it during an incident costs everything.

## 8. The three types

**Loki.** `POST <url>/loki/api/v1/push` as JSON — not snappy-framed protobuf,
which Loki also accepts but which would add a generated dependency and a
compression library to the plane for a body we already batch. Streams are keyed
by `{cypherpanel_app, cypherpanel_project, cypherpanel_environment,
cypherpanel_server}`; entries are `[<unix nanos as string>, <line>]`, sorted by
timestamp within each stream. Auth is HTTP basic (Grafana Cloud's user/token
pair) and/or an `X-Scope-OrgID` header, both sealed. Bodies cap at 1 MiB; a
larger batch splits.

**Syslog.** RFC 5424 over **TCP**, octet-counted framing (RFC 6587 §3.4.1),
optionally over TLS (RFC 5425). Labels travel as STRUCTURED-DATA
(`cypherpanel@0`), the application name is APP-NAME truncated to the RFC's 48
octets, the line is MSG.

**UDP is refused**, and it is this type's one interesting decision. UDP syslog
is universal and unfalsifiable: a `sendto` that succeeds proves nothing arrived,
so the pill would read `● SHIPPING` on a drain that has shipped nothing since
the collector was decommissioned, and a green light that cannot go red is worse
than no light. The refusal names the collector's TCP port as the remedy, because
every collector has one.

**S3-compatible, nightly batch.** The obvious design — wake at 02:00, read the
last 24 h from the stream, upload — is wrong, and is recorded as rejected so
nobody re-proposes it: a nightly read of a 24-hour window sits exactly on the
retention edge, so any hour the panel was busy, restarted or slow was discarded
before it was read, silently.

So an S3 drain consumes **continuously** like the other two; only its *sink* is
periodic. Lines append as NDJSON to a gzip spool under
`<CYPHERD_DATA_DIR>/drains/<drain id>/`; at the configured hour the spool is
closed, PUT to
`<target prefix>/<drain prefix>/<yyyy>/<mm>/<dd>/<drain>-<seq>.ndjson.gz`, and
removed. A batch is acked when it is durably in the spool, not when the object
lands: the spool sits on the same disk as the stream that would otherwise hold
it, so this trades one bounded local copy for another and buys a day of buffer
against an unreachable bucket. `CYPHERD_DRAIN_SPOOL_MAX_BYTES` (256 MiB
compressed) bounds it — reaching it rolls and uploads early, costing one extra
object, and spools waiting on a failing upload accumulate to twice that before
the oldest is deleted and counted in `dropped_lines`. **This is the one type
that puts a second copy on the plane's disk**, and that bound is the whole
enforcement, because nothing else on the plane is watching. `CYPHERD_MIN_DISK_FREE`
does not cover it: `guard.CheckDiskHeadroom` runs exactly once, at boot, against
the process working directory (`core/cmd/cypherd/main.go:147`) — not
`CYPHERD_DATA_DIR`, not necessarily even the same filesystem — and nothing
re-runs it. `CYPHERD_DISK_WARN_PERCENT` watches agent-reported Docker roots on
servers, never the plane's. disk-management.md §6 says why, and says it as a
decision rather than a gap: *"The control plane reserves nothing and enforces
nothing about its own host — deliberately."* The only plane-side disk signal is
the on-demand `data_dir_free_bytes` on `GET /panel/version`
(`core/api/rest/handlers_panel.go:52-88`). So the number in the config table is
load-bearing, and the create form prints it.

**SigV4 has to move.** The signer exists in the wrong place —
`agent/driver/docker/s3.go`, written for database backups — so it moves to
`pkg/s3` and both sides import it, the shape `pkg/subjects`, `pkg/pki`,
`pkg/ids` and `pkg/proto` already have. Not the `pkg/registryauth` precedent,
which is the obvious citation and the wrong one: it is imported only by
`agent/builder` and `agent/driver/docker`, so registries.md §6's "shared by
plane and agent" is aspirational for that package today. The *reason* is still
that section's, and it survives the correction intact: *"two implementations of
one header is how a pull starts failing on half the fleet."* The honest cost is a refactor of working
code the agent depends on, landing with the backup tests green or not landing.

## 9. API and authorization

`core/api/rest/openapi.yaml` first (rule 19), then the generated client
(rule 25, `web/src/api/gen/…`) — the routes below are written there before a
handler exists, which is where every feature's API work in this repo starts.

Panel-scoped throughout — and that is two explicit edits rather than a property
routes acquire by being panel-shaped. `/api/v1/log-drains` joins
`panelScopePrefixes` (`core/api/rest/rest.go:939-951`), or a project-scoped
token reaches these like any other route; and the mutations join `adminRoutes`
(`rest.go:899-905`), because `requiredAbility` (`rest.go:906-930`) defaults
every unlisted mutation to `AbilityWrite`, and a drain is a stronger credential
than the registry routes already listed there. The rank check in the handler is
still the real gate (below); the ability list is what stops a narrow deploy
token being the way around it.

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/log-drains` | panel admin | state and lag; no credentials, ever |
| `POST /api/v1/log-drains` | **panel owner** | see below |
| `GET /api/v1/log-drains/{id}` | panel admin | adds `lag_lines`, `oldest_unshipped_at`, `dropped_lines` |
| `PATCH /api/v1/log-drains/{id}` | **panel owner** | partial; an omitted credential is not rotated |
| `DELETE /api/v1/log-drains/{id}` | **panel owner** | stops shipping; deletes the durable consumer and any spool |
| `POST /api/v1/log-drains/{id}/test` | panel owner | ships one synthetic line through the real sink |

**Mutations are panel owner, and that is not the reflex answer.** Every other
panel-level credential here — servers, deploy keys, backup targets, the DNS
provider — is panel *admin*. A drain differs because of one verified fact:
`teams.RoleForProject` grants its bypass to `domain.RoleOwner` **only**, so a
panel admin who is not a member of a project cannot read that project's
application logs through the API. An all-projects drain would let them read
every project's logs by pointing one at a Loki they control — escalation through
a settings form — and narrowing it to one project does not help, since the admin
may not be a member of that one either. The rank that may create a drain is the
rank that can already read what a drain exports. Admins keep the read routes,
which carry no credential and no log content: operating a panel means seeing
that a drain exists and is failing.

**Testing happens after saving, and that is a departure.** `POST
/log-drains/{id}/test` ships one synthetic line through the real sink and
answers `200 {ok, detail}` — a reachable sink that refuses the line is
`ok: false`, not an error status (registries.md §7's distinction). The panel
does ship unsaved-config probes, so declining one here needs a reason rather
than a claim the pattern is missing: `POST /projects/{id}/notifiers/test` →
`handleTestNotifierConfig` (`core/api/rest/rest.go:576`,
`handlers_notifiers.go:290-323`) and `POST /registries/test` →
`handleTestRegistryConfig` (`rest.go:720`), which registries.md §7 describes as
proving a credential before it is saved — and `ConnectionDialog` already calls
the first one from its Test button (`web/src/components/connection-dialog.tsx:411`,
via the generated `useTestNotifierConfig`).

The reason is §10. An unsaved probe is a repeatable outbound request into the
panel's own network that leaves no row, no owner and no audit event behind;
a saved drain is authorized, audited and revocable, which is exactly the
compensating control §10 leans on when it declines `core/egress`. Notifiers
already take this route for the one channel where the same argument bites —
email, `notify.ErrTestRequiresSave` (`handlers_notifiers.go:310-314`).

**Deleting** states its blast radius per ui-principles §2 — *"Stops shipping
immediately. Lines already sent are not recalled."* — but takes no name-typing:
a drain holds no data of its own, and that bar is for things that do.

**The screen is a variant of a shell that already exists.**
`web/src/components/connection-dialog.tsx` was written generic for this feature
by name — its header calls out "log drains (6f/13f)" and says the shell exists
"so the next connection type adds fields and a test call rather than a second
dialog". Landing drains adds fields and a test call there, and fixes that
header in the same PR (rule 33): it still says registries and drains "have no
API yet", already false for registries, and still claims "the panel has no
endpoint that tests an unsaved config", which its own imports contradict.

**Audit.** `log_drain.created`, `.updated`, `.deleted` on the `log_drain`
resource kind. The update detail records `credential_rotated: true|false` and a
scope change from and to, and nothing else about the config.

## 10. Egress: where a drain may connect

The shipping path is **deliberately not** guarded by `core/egress`, which is the
least comfortable decision here, so it is explicit. That guard refuses addresses
that are not publicly routable, and it exists because the panel dials hosts
chosen in a request body (threat-model §5.11, §5.14); a drain is that shape. But
the most common deployment of the most-requested type is a Loki container on the
panel's own private network, and a feature that cannot ship to
`http://loki:3100` is a feature nobody asked for. notifications.md recorded this
posture for the analogous case: *"A saved notifier … may still point at an
internal host over plain HTTP — which is what makes a self-hosted
Slack-compatible receiver work."*

What makes it acceptable, and what does not:

- **Nothing comes back.** A drain is write-only in the strong sense: a Loki push
  returns `204`, a syslog write returns nothing, an S3 PUT returns an ETag. No
  response body is stored, rendered or returned by any route, so the classic
  SSRF read primitive is absent.
- **The rank is the control.** §9's owner requirement is the compensating
  control, not an accident: the principal who can aim a drain already reads
  everything the panel holds, and aiming one leaves a row and an audit event
  with a name on it.
- **The residual risk is real and named.** `POST /log-drains/{id}/test` returns
  the far end's own words, which is an owner-driven connectivity oracle inside
  the panel's network; `detail` truncates at 200 bytes and carries no response
  body beyond that. It belongs in the threat model as a new §5.16 beside the
  registry probe, and the feature is not done until that is written.

## 11. Configuration, and what this costs

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_DRAIN_BATCH_LINES` | `500` | Lines per push, per drain |
| `CYPHERD_DRAIN_BATCH_INTERVAL` | `2s` | Flush interval when the batch is not full |
| `CYPHERD_DRAIN_MAX_BACKOFF` | `60s` | Cap on retry backoff (§6) |
| `CYPHERD_DRAIN_SPOOL_MAX_BYTES` | `268435456` (256 MiB) | Compressed spool cap per S3 drain |
| `CYPHERD_DRAIN_MAX` | `8` | Enabled drains allowed; a ninth is a `409` |

**Throughput** is capped by the stream: 512 MiB over 24 h is ~6 KiB/s sustained,
and a drain reads it once — three drains read it three times. That is the number
to hold against < 300 MB RSS and a $5 VPS, and it is small only because bounded
retention already made it small. **Memory:** one batch buffer per drain plus a
gzip window for an S3 drain — single-digit MiB at `CYPHERD_DRAIN_MAX`. **Disk:**
zero for Loki and syslog; up to `2 × SPOOL_MAX_BYTES` per S3 drain under
`CYPHERD_DATA_DIR` — 4 GiB if every one of the eight is S3, which is why the
default is 8 and not 64. Those are bytes nothing watches at runtime (§8), and
bytes outside JetStream's own budget: `JetStreamMaxStore` is sized as
`workMaxBytes + runtimeLogsMaxBytes` (`core/bus/bus.go:209`), and the spool is
neither, so it sits on the same filesystem outside the cap that bounds the
stream.

## 12. Acceptance (testable)

1. A Loki drain against a stub receiver: the line arrives with its app, project,
   environment and server labels and a timestamp within seconds of receive time.
2. A project-scoped drain receives that project's lines and no others, including
   for an application created *after* the drain (the resolver refresh).
3. Stop the receiver: deploys continue at unchanged latency, no new table or
   file grows, and the drain reports `retrying` then `failing`. Restart it and
   everything buffered arrives, in order, back to `shipping`.
4. Restart `cypherd` mid-backlog: shipping resumes at the ack floor — no
   re-shipped day, no skipped one. Push the stream past its retention bound
   while a drain is down: `dropped_lines` is non-zero, the drain resumes at the
   oldest retained message, nothing crashes.
5. A syslog drain produces RFC 5424 frames a stock `rsyslogd` accepts; a UDP
   endpoint is refused at validation with the TCP remedy in the message.
6. An S3 drain spools continuously and uploads one gzip object at the configured
   hour, the spool gone afterwards; reaching the cap uploads early rather than
   growing.
7. No route returns a credential in any field; a panel admin reads the list and
   gets `403` on create; deleting a Backup Target a drain uses is a `409` naming
   the drain; deleting a drain stops its goroutine, deletes its consumer and
   removes its spool.

## 13. Deliberately out of scope

- **Draining anything but application runtime logs.** Build logs are deployment
  artifacts on a transient stream; managed-database and Compose Stack output is
  not on `RUNTIME_LOGS` at all (§4). When it is, it drains for free — a change
  in those features, not here.
- **More sink types.** Axiom, CloudWatch, Datadog, Elasticsearch and a generic
  "POST my JSON here" are each an integration to keep working across someone
  else's version changes. Three is what this panel can promise; a fourth needs a
  better reason than a logo.
- **Team-owned drains.** The tenancy-correct design, and the one that lets a
  tenant spend the panel's CPU, egress and stream reads. It needs per-team
  quotas on drains and on lines per second first.
- **Search, indexing, or a longer window in the panel.** §1, §11.
- **Redaction, sampling, or level filters.** Redaction is a promise the panel
  cannot keep (§4); the other two are cheap to add and expensive to get wrong on
  a line format we never parse. The sink filters.
- **Exactly-once or an end-to-end delivery guarantee.** §7 says what it would
  take and why it is a different project.
- **Draining the panel's own log.** `GET /panel/logs` tails an in-memory ring
  (control-plane-hardening.md §4), not a stream; that is a different source with
  a different retention story.

## Decisions taken (orchestrator, not the spec author)

Three questions the spec left open are settled here so implementation has no ambiguity.

**Mutations are session-only, not `adminRoutes` plus a handler rank check.** The
API-token ability system has no owner tier, so a rank check in the handler would
be the only thing standing between a leaked project-scoped token and a drain
that reads every project's logs. That is the same shape as deploy protection's
policy `PUT` and the access-request grant/deny — a control that can be switched
off wholesale — and those are session-only for exactly this reason. Reads stay
token-reachable. `/api/v1/log-drains` is added to `panelScopePrefixes` in
`core/api/rest/rest.go` regardless, so a project-scoped token cannot reach it at
all.

**A blocked backup-target delete answers with `used-by`, not a widened message.**
`GET /backup-targets/{id}/used-by` follows the registries precedent, which
already names what is blocking a delete rather than encoding it into a 409
string another feature owns. `handleDeleteBackupTarget` keeps its existing
`ErrInUse` mapping.

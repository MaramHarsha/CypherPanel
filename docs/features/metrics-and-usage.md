# Feature spec: Metrics, request analytics and usage insights

> Three questions an operator asks that the panel currently cannot answer at
> all: *how hard is this application working*, *is anyone actually using it and
> how fast is it*, and *which project is responsible for the next server I have
> to buy*. They share one collection path, one storage discipline and one
> write budget, so they ship as one spec.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why these three together, and what the shape of the answer must be

The panel today reports **status** — running, degraded, error — and nothing
about magnitude. Every screen can say a container is up; none can say it has
been pinned at 190% of a core for two days, that its p95 went from 90 ms to
2.4 s after Tuesday's deploy, or that one of eleven projects is eating 38% of
the fleet. [disk-management.md](disk-management.md) §8 deferred "disk usage per
application" here by name. The feature matrix has *Server metrics /
monitoring* and *Metric threshold alerts* as V1.x rows, and
[roadmap.md](../roadmap.md) Phase 4 lists observability. This is that work,
minus the alerting (§14).

They are one feature because they are one pipeline. Container CPU and memory
come from the local Docker daemon; request counts and timings come from the
local Proxy's access log; per-project attribution is those two summed up the
ownership chain the plane already stores. Split, they would be three
collectors, three wire messages, three retention policies and three chances to
get the write volume wrong.

And the write volume is the whole design. [research/dokploy.md](../../research/dokploy.md)
records **22.76 GiB written on an idle fresh install** — no user applications
deployed — and draws the conclusion for us in the same table: *"continuous
metrics/heartbeat persistence must batch and bound its writes … or we inherit
the same churn."* A metrics feature that is merely *correct* still fails here
if it writes like the thing we measured and rejected. §2 is therefore not a
performance appendix; it is the constraint every other section is derived from.

**On ADR-005.** Collection is the *observation* half of the reconciliation
loop, not a new imperative path: the agent reads and reports, the plane never
asks a node to do anything. The one thing that could have been an order —
whether to collect at all, and at what resolution — is carried as state in
`DesiredState` (§5), exactly as the panel's ACME account is
([agent-identity-and-tls.md](agent-identity-and-tls.md) §4). The collector is
the first loop in the agent that mutates nothing whatsoever, so the
converge-twice test (ENGINEERING rule 13) is satisfied trivially and the
interesting idempotency lives on the plane's ingest instead (§4.6).

## 2. The write budget

Take a representative install: **3 servers, 20 containers, 8 routed resources,
100 requests/second aggregate.**

The naive design — sample every container every 10 s and store the sample; log
every request and store the row — produces:

| | rows/day |
|---|---|
| container samples (20 × 8 640) | 172 800 |
| request rows (100/s) | 8 640 000 |

That is the Dokploy shape, and on a $5 VPS it is the vision's "lightweight,
with numbers" non-negotiable failing in the field: sustained random writes plus
WAL plus index maintenance, forever, for data nobody reads at that resolution.

What this design stores instead:

| Table | grain | rows/day |
|---|---|---|
| `resource_metrics` | resource × 5 min | 5 760 |
| `resource_disk_usage` | resource × 1 h | 480 |
| `request_metrics` | routed resource × 5 min | 2 304 |
| `request_paths` | routed resource × 1 h × top-20 | 4 032 |
| `resource_usage_daily` | resource × UTC day | 20 |
| **total** | | **≈ 12 600** |

≈ 1.7 MB of tuples per day, under 10 MB/day once WAL and indexes are counted,
and a **steady state under ~150 MB** because retention holds it there (§7).
Roughly **700× fewer rows** than the naive design, from one decision: **nothing
is stored per sample and nothing is stored per request. The agent aggregates
into a fixed bucket and publishes the bucket.**

The batching claim, stated so it can be checked: **one NATS message and one
multi-row `INSERT` per server per bucket** — 288 messages per node per day,
carrying every resource on it, roughly 4 KB each. Not one write per resource,
and emphatically not one per sample.

Agent-side cost, so the < 50 MB RSS budget stays checkable: per resource a
small counter struct; per routed resource a 16-slot histogram and a path map
capped at 200 entries (~13 KB); plus the unsent backlog (§4.6). Under 1 MB for
the install above. CPU is one JSON decode per access-log line — about 1 µs, so
~1% of a core at 10 000 req/s, with the ceiling in §4.5 for anything past that.

## 3. Everything stored merges

The second decision, and the one that makes the first one safe: **every stored
column is mergeable.** Counters merge by sum, peaks by max, histograms by
element-wise sum. Consequently the answer for any window — an hour, a day, a
month — is *exact*, computed from whatever buckets it covers, and does not
depend on the bucket size we happened to pick.

This is why the schema stores none of the things a dashboard displays:

- **No averages.** We store `cpu_core_ms` (CPU consumed in the bucket) and
  `memory_byte_seconds`, both accumulators. Mean CPU and mean memory are
  derived by dividing by the covered time. Storing a mean instead would make
  a longer window's mean an average-of-averages, which is wrong whenever the
  buckets are not equally covered — and after an agent restart they are not.
- **No percentiles.** p95 does not average. Two 5-minute buckets with p95 =
  100 ms and p95 = 2 000 ms have an hour-p95 that is *neither* the mean nor
  the max of them, and no arithmetic on the two numbers recovers it. So each
  request bucket stores a **16-slot latency histogram** with fixed boundaries
  (1, 2, 5, 10, 25, 50, 100, 250, 500, 1 000, 2 500, 5 000, 10 000, 30 000,
  60 000 ms, +∞). Histograms add, so p50/p95/p99 over any window are computed
  once, from the summed histogram, accurate to the bucket boundary. A
  `histogram_version` column carries the boundary set so the constants can
  change later without silently reinterpreting old rows.
- **Peaks alongside means.** `cpu_percent_peak` and `memory_bytes_peak` are
  the highest single sample in the bucket. Without them a 5-minute mean hides
  exactly the spike an operator opened the page to find; a container that OOMs
  once every four minutes looks calm in the mean.
- **Coverage is stored, not assumed.** Every bucket carries `sample_count` and
  `covered_seconds`. An agent that restarted at 12:03 publishes a 12:00 bucket
  covering 120 seconds, and the chart draws it as partial rather than as a dip
  in traffic. This matters more than it sounds: without it every agent restart,
  every proxy recreate and every deploy reads as an outage on the graph.

Percentile sketches (t-digest, DDSketch) were considered for the latency
dimension and rejected. They merge too, and more precisely, but they are an
opaque binary blob in a Postgres column, a dependency, and untestable by
inspection. A fixed histogram is auditable with `SELECT`, exactly good enough
for "is p95 seconds or milliseconds", and its error is bounded and stated.

## 4. Collection on the agent

### 4.1 CPU and memory

A sampler in `agent/metrics/` reads each managed container's cumulative CPU
and current memory from the local daemon every `--metrics-sample` (default
15 s), keeps the previous cumulative CPU counter itself, and folds the delta
into the open bucket. It computes deltas rather than trusting the daemon's own
`precpu` window, so a slow or retried read shifts nothing.

Attribution needs no new mechanism, because the containers are already
labelled: `cypherpanel.app-id` for an Application
(`agent/driver/driver.go`), `cypherpanel.db-id` for a Managed Database
(`agent/driver/docker/database.go`), and Compose's own
`com.docker.compose.project` = `cypher-<stack id>` for a Compose Stack
(`agent/compose/compose.go`). A container carrying none of those is not ours
and is not sampled — the same rule disk management follows: *the panel measures
what it manages and nothing else*, so an operator's own containers on a shared
box never appear in a project's figures.

The collector lives behind a consumer-defined interface on the driver seam.
Nothing Docker-shaped leaves `agent/driver/docker` (ENGINEERING rule 11,
project-structure rule 2) — a Swarm or k8s driver implements the same
`Sample(ctx) ([]ResourceSample, error)` and the rest of the pipeline does not
change.

Memory is reported as bytes used together with the container's limit when it
has one (`AppSpec.memory_limit_mb`), so the UI can show "1.8 GB of 2 GB"
rather than a number with no scale.

### 4.2 Disk

Disk is the expensive one and is treated accordingly. The agent asks the daemon
for its disk usage at `--disk-usage-interval` (default **1 h**, `0` disables)
and attributes three components by the same labels: **image** bytes for the
revisions retained for that resource — a built image by its labels, a pulled
one by the managed reference our own pull created, since a pulled image cannot
carry our labels (disk-management.md §3) — the container's **writable layer**,
and its **named volumes**. The verbose form the daemon needs in order to report
volume sizes is what makes the call expensive — it walks the graph driver and
can take seconds on a host with many layers — hence hourly, disableable, and
never on the sampling path.

One honesty note that belongs in the UI copy and not only here: **per-resource
disk figures do not sum to the host's usage.** Image layers are shared, and a
base layer used by four applications is counted for each of them, because the
question the number answers is "what is this resource responsible for", not
"how would the host shrink if I deleted it". The fleet-level number an operator
should trust for capacity is the one already on the Server DTO
(`disk_total_bytes` / `disk_free_bytes`, disk-management.md §4).

### 4.3 The Proxy access log

Traefik knows every request, and it already knows which resource served it: the
fragment writer names each router after the resource id
(`agent/proxy/traefik.go` writes routers `<appID>` and `<appID>-http`; the
compose reconciler passes the stack id as the same key). So attribution is read
directly off `RouterName` — there is no Host-header-to-resource lookup table to
build, keep in sync, or get wrong.

**The log goes to the Proxy container's stdout, not to a file.** The static
config gains an access-log section with `format: json`, `fields.defaultMode:
drop` and an explicit keep-list, and the Proxy container gains a **capped
`json-file` log driver** (`max-size`, `max-file`) — an additive `LogConfig` on
`engine.RunConfig`, which today has no log configuration at all and therefore
inherits whatever the host's daemon defaults to. The agent consumes the
container's log stream through the same Docker path
`agent/stream` already uses for runtime logs, and **discards every line after
counting it**: access lines never reach `logs.*`, never reach the plane, and
never reach an operator's log pane.

Two alternatives, both worse. **A log file in the agent's Traefik directory**
is natural under ADR-004 — the agent owns that directory — but nothing rotates
it: truncating a file Traefik holds open leaves a sparse file and a stale write
offset, and doing it properly means renaming plus signalling the container to
reopen, a hand-rolled rotation state machine duplicating what the log driver
already does correctly. **A named pipe** writes nothing to disk and has a
catastrophic failure mode: when the reader stops, the buffer fills and Traefik
*blocks on write*, so the node stops serving requests because a metrics reader
died. Rejected outright — nothing here may ever be able to take a route down.

**Enabling this recreates `cypher-proxy` once**, on the upgrade that ships it,
because the access-log block changes the static config and the static hash is
part of the container's identity (`agent/proxy/ensure.go`). That is a few
seconds with no routing on that node. It is a real cost and the release note
must say so. It happens at upgrade rather than at a later toggle deliberately:
an operator upgrading an agent already expects the Proxy to restart, whereas an
opt-in switch moves the same outage to a surprising moment *and* leaves a hole
in the data exactly when someone first goes looking for it.

**What is kept**, per line: the timestamp, the router name, the request method,
the request path, the downstream status, the duration, and the response size.
**What is dropped before anything is counted**, and never written anywhere:
request and response bodies, all cookies, all headers, the client IP address,
the user agent, the referrer, TLS details, and **the entire query string** —
`?token=…`, `?api_key=…` and `?reset=…` are the ordinary way secrets end up in
a URL, and ENGINEERING rule 20 does not have an exception for "it was already
in a log". The keep-list is expressed in Traefik's own `fields` configuration
with `defaultMode: drop`, so the dropped fields are never serialised on the
node either — not merely ignored by our parser.

**Redirects are counted separately.** A routed application with HTTPS gets two
routers, and a visitor arriving over HTTP produces a 308 from `<appID>-http`
before the real request. Counting both would double every visit and drag p95
toward the 1 ms redirect. So a line whose router is the `-http` sibling
increments `redirect_count` on the same resource's bucket and is excluded from
the latency histogram and from the top-paths table.

**Requests that matched no router** — scanners, wrong Host headers, a domain
whose fragment was removed — carry an empty router name. They are counted in a
per-server bucket (`resource_kind = 'server'`) rather than dropped, because
"traffic is arriving at this node and hitting nothing" is a question worth
being able to answer, and it costs one row per node per bucket.

### 4.4 Paths, and the cardinality problem

The design screen's `TOP PATHS` table is the part that can destroy the
database, because `/api/contacts/8f3ac1d0` is a distinct path for every
contact. Two bounds apply, both on the agent:

1. **Normalisation.** The query string is already gone. The path is then
   truncated to its first `3` segments and any segment that looks like an
   identifier — all digits, a UUID, or eight or more characters that are all
   hexadecimal — is replaced with `:id`. `/api/contacts/8f3ac1d0/notes`
   becomes `/api/contacts/:id`.
2. **A hard cap.** At most 200 distinct normalised paths are tracked per
   routed resource per hour. Once the cap is reached, a path not already in the
   map folds into a single `(other)` row instead of creating an entry. Only the
   **top 20 by request count** are published, and everything below the cut is
   summed into `(other)` as well — so the row totals always reconcile with the
   `request_metrics` count for the same window.

Both are approximations and the spec will not pretend otherwise.
Normalisation is a heuristic: an application whose real route *is*
`/v1/2024/report` will see it rewritten. A router-aware breakdown would need
the application to declare its routes, which nothing in a container image lets
us ask for. The cap means a path that first appears after 200 others are
already tracked is invisible for that hour. A Space-Saving heavy-hitters
sketch would fix the second case more precisely; it was rejected because the
failure it prevents is "a rare path is missing from a top-20 list", which is
not a failure anyone is paged for, and it costs a data structure to maintain
and test.

### 4.5 Sampling under load

Every line is parsed up to a ceiling. Past `accessLogMaxRate` (default **5 000
lines/second**, an agent-side constant because the node is where the cost is
known), the agent switches the affected bucket to deterministic **1-in-N**
sampling: it counts every Nth line, records `sample_rate = N` on the bucket,
and the plane scales the counters back up on read.

The consequences are stated rather than hidden:

- Counters become **estimates**, and the API and the UI both say `sampled:
  true` with the rate. A number the panel is not sure of is labelled, per
  ui-principles §10 — it is never quietly presented as exact.
- Percentiles from a uniform 1-in-N sample of a high-rate stream are
  statistically sound; that is precisely the regime where sampling is safe.
- `(other)`-folding and the path cap interact with sampling in the obvious
  direction: rare paths get rarer. A sampled hour's top-paths table is
  directional, not an inventory.

The alternative — dropping lines at the Proxy with Traefik's own access-log
filters — was rejected because filtering by status code or duration destroys
the counts, and the counts are the point. Sampling preserves the shape;
filtering biases it.

### 4.6 Batching, the backlog, and idempotency

At each bucket boundary (wall-clock aligned, so every node in the fleet cuts
buckets at the same instants and a fleet chart lines up) the agent seals the
open bucket and publishes **one** `MetricsReport` on
`state.<server_id>.metrics` carrying every resource's counters, every routed
resource's request bucket, and — on the hour — the path rows and disk rows.

The plane inserts them in one multi-row statement per report, with
`ON CONFLICT DO NOTHING` on `(resource_kind, resource_id, bucket_start)`. **The
bucket identity is the idempotency key** (ENGINEERING rule 12), so JetStream
redelivery and the agent's own replay are both no-ops, and no separate key
needs inventing.

The agent keeps up to **12 sealed buckets** (one hour) in memory when it cannot
publish, and sends them on reconnect. Past that the oldest are dropped. This is
the honest bound: **metrics are lossy by design.** They ride the memory-backed
`STATE` stream (10-minute max age) alongside heartbeats — additively adding
`state.*.metrics` to its subject list, ENGINEERING rule 14 — and no file-backed
stream is created for them, because a durable stream for metrics would put the
write volume back on the plane's disk to protect data whose entire purpose is
to be approximately right. A control plane down for more than an hour has a
**gap** in its charts, drawn as a gap and never interpolated across (again
ui-principles §10). This is the sharpest way to state what this feature is:
[audit-log.md](audit-log.md) is a ledger and must not lose a row; this is a
gauge and may.

## 5. Desired state and the wire (additive)

`DesiredState` gains one field, in the free slot after `retain`:

```proto
// In DesiredState:
MetricsSettings metrics = 6;

message MetricsSettings {
  bool enabled = 1;            // container CPU / memory / disk collection
  bool request_analytics = 2;  // Proxy access-log aggregation
  uint32 bucket_seconds = 3;   // 0 = the default, 300; must divide an hour
}
```

Panel-wide, carried to every node the way `TLSSettings` is, and changed through
the existing `Resync` nudge so a settings change does not wait for a
reconnect. Three fields, not a knob per dimension: `enabled` and
`request_analytics` are the two an operator has a real reason to change (cost,
and privacy about paths), and `bucket_seconds` is load-bearing for the write
budget — an operator with 500 containers raises it to 900 and cuts the row
count by a factor of three. Sample interval, path cap and rate ceiling stay agent-side,
because they protect the *node's* memory and CPU and the node is where that is
known.

The report itself is one new message family and one new subject. **No new
verbs**: nothing here asks an agent to do anything.

```proto
message MetricsReport {
  string server_id = 1;
  google.protobuf.Timestamp bucket_start = 2;
  uint32 bucket_seconds = 3;
  repeated ResourceMetricBucket resources = 4;
  repeated RequestBucket requests = 5;
  repeated RequestPathBucket paths = 6;   // hourly only
  repeated ResourceDiskBucket disk = 7;   // hourly only
}
```

`buf breaking` stays clean: new messages, one new field number, nothing
renumbered or reused (ENGINEERING rule 18).

## 6. The resource model — migration `0040_metrics_and_usage.sql`

Highest existing migration is `0039_server_disk.sql`. Five tables, plus one
additive column on `deployments`.

```
resource_metrics       (resource_kind, resource_id, bucket_start) PK
  server_id, cpu_core_ms, cpu_percent_peak, memory_byte_seconds,
  memory_bytes_peak, memory_limit_bytes, sample_count, covered_seconds

resource_disk_usage    (resource_kind, resource_id, bucket_start) PK   -- hourly
  server_id, image_bytes, volume_bytes, container_bytes

request_metrics        (resource_kind, resource_id, bucket_start) PK
  server_id, requests, redirect_count, status_2xx, status_3xx, status_4xx,
  status_5xx, response_bytes, latency_buckets BIGINT[], histogram_version,
  sample_rate

request_paths          (resource_kind, resource_id, bucket_start, path) PK -- hourly
  requests, status_5xx, latency_buckets BIGINT[], histogram_version

resource_usage_daily   (resource_kind, resource_id, day) PK
  team_id, project_id, project_name, environment_id, environment_name,
  resource_name, cpu_core_seconds, memory_byte_seconds, memory_bytes_peak,
  disk_bytes, requests, status_5xx, deploy_count, deploy_seconds

deployments ADD COLUMN started_at TIMESTAMPTZ   -- when it left the queue
```

**No foreign keys on any of them.** Two reasons, the second of which
[audit-log.md](audit-log.md) §2 already reached from a different direction:

1. `resource_kind` is polymorphic — an Application, a Compose Stack, a Managed
   Database or a Server — so there is no single column a foreign key could
   point at.
2. More importantly, **a cascade would rewrite history.** Delete an application
   on the 28th and every FK-cascading row for that month vanishes, so a monthly
   report run on the 1st is smaller than the same report run on the 27th, with
   no record that anything was removed. The daily rows therefore **snapshot**
   the ownership chain — team, project name, environment name, resource name —
   taken when the row is written, exactly as an audit event snapshots what it
   names. The short-lived bucket tables carry no names at all; they are swept
   by retention (§7) and joined to live resources on read, so a bucket whose
   resource is gone is simply never selected.

The deletion path for any resource calls the daily rollup for its unrolled days
**before** the resource row disappears, so a preview environment that lived for
six hours still appears in the month it lived in. The rollup is an idempotent
upsert on `(resource_kind, resource_id, day)`, so calling it there and again
that night is harmless.

`deployments.started_at` exists so **deploy minutes mean build-and-rollout
time, not wall time since somebody clicked deploy**: without it the figure
would include queue time and, worse, the hours a deploy sat in
`awaiting_approval` ([deploy-protection.md](deploy-protection.md)) — a project
would be charged for its own change-management policy. Rows predating the
migration have a null `started_at` and fall back to `created_at`, which the CSV
documents as an upper bound.

## 7. Rollup and retention

Two tiers, one nightly job, the same bounded-batch discipline the audit
retention sweeper uses:

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_METRICS_RETENTION` | `14d` | 5-minute and hourly buckets. `0` = forever — which, said plainly, is how a busy fleet fills a disk. |
| `CYPHERD_USAGE_RETENTION` | `400d` | Daily usage rows. 400 rather than 365 so "the same month last year" is always still there. |

A single owned goroutine (ENGINEERING rule 7) rolls up **every UTC day that has
bucket rows and no complete daily row**, not merely yesterday — a plane that
was down for three days catches up on boot instead of leaving a permanent hole
— bounded to the metrics retention window, because beyond it the source rows
are gone and no amount of catching up invents them.

Retention is deliberately the same shape as
[bounded-log-retention.md](bounded-log-retention.md): a bounded window that the
operator can size, defaulting to something that costs little, with the honest
statement that history beyond the window does not exist rather than a promise
we would need a second store to keep.

## 8. Usage: per-project attribution, and where it stops

The Usage screen is `resource_usage_daily` summed over a month and grouped by
project, joined to the projects the viewer can already see. Each row carries
average memory, share of CPU, disk, and deploy minutes — the four the design
screen names.

**The CPU share needs a stated denominator, or it is a tenancy leak.** "38% of
fleet CPU" shown to a member of one team silently discloses the size of every
other team's load. So the denominator is **the total of what the viewer can
see**: for a panel admin that is genuinely the fleet; for anyone else it is
their teams' own total, and the page says which in words above the table. A
project the viewer cannot see contributes to neither numerator nor denominator.

**Monthly CSV** at `GET /api/v1/usage/export?month=YYYY-MM`, one row per
resource, with the ownership chain, the four measures, request count and 5xx
count. Same scope as the page: the export can never contain a row the caller
could not already read.

### The vision line this comes near, and how it is resolved

[vision.md](../vision.md) lists **"SaaS billing and metering"** as explicitly
out of scope — *"cloud concerns must never leak into core, the way Stripe code
is threaded through Coolify."* The design screen's own copy calls this "the
agency answer to *what do I bill client X*", so the tension is real and worth
resolving in writing rather than hoping nobody notices.

What is banned is the panel doing commerce: prices, currency, rate cards,
invoices, plans, payment integrations, and quota enforcement. **None of that is
here, and the boundary is enforceable by three rules:**

1. No monetary concept exists anywhere in the feature — no price, no rate, no
   currency field, in the schema, the API or the UI.
2. **Nothing in this feature ever refuses an action.** There is no quota, no
   cap, no soft limit. Usage is reported; it never gates a deploy. Metering
   with teeth is what "metering" means in that vision line, and this has none.
3. The CSV ends where a spreadsheet begins. The panel produces measurements;
   whatever an operator multiplies them by is theirs.

The primary framing is the capacity one the same design screen gives —
*"which project forces the next server"* — which is an operations question a
self-hoster with one team asks as often as an agency with twenty. If a future
feature wants price fields or quota enforcement, that is a change to the
vision's out-of-scope list and needs a recorded decision, not an extra column
on this table.

## 9. Security and privacy

- **No PII.** Client IP addresses, user agents and referrers are dropped at the
  Proxy, not merely unstored (§4.3). An operator's visitors are third parties,
  and a panel that retained their addresses would make every self-hoster a data
  controller for them by default, for a feature they did not ask for. It also
  makes the panel deliberately useless as a web-analytics product (§14).
- **Paths are application-authored data.** Normalisation removes id-shaped
  segments and truncation removes depth, which covers the common
  `/reset/<token>` shape — but not every shape. An operator for whom URLs are
  themselves sensitive turns `request_analytics` off, and the setting's help
  text says exactly that rather than implying the sanitiser is complete.
- **Authorization is the existing one.** Reading a resource's metrics is its
  `read` ability at member rank; a resource in another team answers the same
  "no such resource" it already does. Usage and the CSV are scoped per §8.
- **mTLS only** (rule 23): reports ride the same authenticated bus as every
  other `state.*` message, inside the publishing server's own subject scope, so
  the per-agent authorization added in
  [control-plane-hardening.md](control-plane-hardening.md) needs no new case.
- **A malformed report is dropped with a warning**, never partially applied —
  the same stance `status.Recorder` takes on a malformed heartbeat. One bad
  agent must not corrupt a month.
- **Old agents report nothing**, and nothing is `unknown`, never zero
  (ADR-010). A resource with no buckets shows "no data yet" with the reason,
  not a flat line at 0% that reads as an idle application.

## 10. API surface (under `/api/v1`)

Eight operations, taking the contract from 198 to 206.

| Route | Ability | Notes |
|---|---|---|
| `GET /applications/{id}/metrics?window=` | `read` | CPU/memory/disk series + summary |
| `GET /compose-stacks/{id}/metrics?window=` | `read` | same shape |
| `GET /databases/{id}/metrics?window=` | `read` | same shape |
| `GET /servers/{id}/metrics?window=` | `read` | the node's managed containers, summed |
| `GET /applications/{id}/traffic?window=` | `read` | summary + series + top paths, one call |
| `GET /compose-stacks/{id}/traffic?window=` | `read` | same |
| `GET /usage?month=YYYY-MM[&team_id=]` | `read` | per-project attribution |
| `GET /usage/export?month=YYYY-MM[&team_id=]` | `read` | `text/csv` |

`window` takes the same forms `?since=` already takes on the log streams
([deployment-control.md](deployment-control.md) §4) — a Go duration or an
RFC 3339 instant — and anything else is a `400` naming both, for the same
reason: a client that asked for the last hour and silently got a fortnight has
been answered confidently and wrongly. The response carries the resolution it
actually served (5-minute or daily buckets) and an `as_of`, because a chart
whose last point is four minutes old must be able to say so.

`/traffic` returns the whole screen in one response — summary numbers, the
series behind the sparkline, and the top-paths rows — because it is one screen
and three round trips for one card is how a panel starts feeling slow.

**Fixed endpoints, not a query language.** No arbitrary group-by, no filter DSL,
no PromQL. The endpoints answer the questions the screens ask; anything richer
is a reason to export, not a reason to build a query engine into a control
plane with a 300 MB budget.

## 11. Screens

Design screens are the source of truth for layout and copy.

**Application → Traffic** — a new tab at
`web/src/routes/_app/projects/$projectId/applications/$appId/traffic.tsx`,
breadcrumb `WEB / TRAFFIC`: the window
picker (`last 24h ▾`), four stat tiles — requests, p50, p95, 5xx% — with the
5xx tile in the alert colour when non-zero, the request sparkline, and the
`TOP PATHS` table with columns `REQS · P95 · 5XX`. The caption is the design's
own sentence: *"Aggregated from proxy access logs on each node — counts and
timings only, no bodies, no cookies, bounded retention like runtime logs."*
A sampled window adds one line saying so.

**Settings → Usage** (`web/src/routes/_app/settings/usage.tsx`, breadcrumb
`SETTINGS / USAGE`): the month picker and `export csv ↓`, then one bar per
project with `avg N GB mem · N% of fleet cpu · N GB disk`, the bar showing CPU
share, and the design's caption about attribution and invoicing. The
denominator sentence from §8 sits above the list.

**Application overview and Server detail** gain a compact CPU/memory panel from
the same endpoints — where most people will actually look.

All four states per ui-principles §1, including the one this feature invents:
**"collecting"** — the resource exists, the agent supports metrics, the first
bucket has not closed. An empty state with a reason and a time ("first data in
under five minutes"), not a spinner and not a zero. Charts load lazily against
the 300 KB bundle budget, and the `dataviz` skill is loaded before any chart
code is written (web-ui-design.md §9).

**Glossary additions** the implementation PR makes first (ui-principles §8):
*Metric Bucket*, *Request Analytics* — with the note that the application tab
is labelled **Traffic**, because §11 of ui-principles asks for plain language in
the headline and "request analytics" is jargon — and *Usage*.

## 12. Alternatives considered

- **Traefik's Prometheus metrics instead of the access log.** Genuinely
  cheaper for what it covers: native per-router counters and a native latency
  histogram, no parsing, no PII risk at all. Rejected for two reasons. It is
  router-level only, so the `TOP PATHS` table — half of the design screen —
  would be impossible, and keeping it *as well* would mean two collection paths
  for overlapping numbers that will disagree at the edges. And it needs a new
  listening socket on the Proxy, which is the same family of decision ADR-004
  settled when it rejected exposing Traefik's API. If a later version wants
  cheap counters on very large nodes, this is the first place to look, and it
  should arrive as a superseding note rather than a second silent source.
- **A time-series database (Prometheus, VictoriaMetrics, TimescaleDB).** The
  right tool for this data and the wrong one for this product: one binary and
  one database is a vision non-negotiable. A self-hoster who wants real
  observability runs it as an Application from the catalog; what we owe them is
  a metrics *drain* (§14), not a bundled TSDB.
- **Scraping from the control plane.** Rejected on ADR-002: the plane has no
  route to a node, and inventing one for metrics would be exactly the inbound
  path the architecture exists to avoid.
- **Storing raw samples and rolling up later.** The classic design, and the one
  that produced the 22.76 GiB. Rolling up later means writing everything first;
  the whole saving is in never writing it.
- **A single row per resource updated in place** (a running total). One row per
  resource forever, tiny — and it makes every `UPDATE` a hot-row contention
  point, destroys all history, and turns a redelivered message into
  double-counting because an in-place increment has no idempotency key.
- **Per-container network counters** for bandwidth. Rejected: they are
  unreliable for host-network containers, they count proxy-to-container traffic
  as well as internet traffic, and the number an operator actually wants —
  bytes served to the internet — is already in the access log's response size.

## 13. Acceptance (testable)

1. Two buckets close on a deployed application → the metrics endpoint returns
   two buckets with non-zero `cpu_core_ms` and `covered_seconds` of 300 each.
2. Kill the agent mid-bucket → that bucket's `covered_seconds` is short, the
   API reports it partial, and the chart draws it partial rather than as a dip.
3. Replay the same `MetricsReport` ten times → row counts unchanged.
4. Drive 1 000 requests, 10 of them slow → `/traffic` reports 1 000 requests, a
   p95 in the right order of magnitude, and the slow path in `TOP PATHS`.
5. Request `https://…` over HTTP → the 308 lands in `redirect_count` and in
   neither the latency histogram nor the paths table.
6. Request `?token=secret` and a UUID path segment → neither string appears
   anywhere in the database. Worth checking crudely, over a dump.
7. Exceed the line-rate ceiling → the bucket carries `sample_rate > 1`, the API
   marks it `sampled`, and the scaled count is within the expected error.
8. Delete an application mid-month → its days survive in
   `resource_usage_daily` with names snapshotted, and stay in the CSV.
9. Stop the plane for two days → the rollup materialises both missed days on
   boot, and the charts show a gap rather than a line across it.
10. A member of team A calls `GET /usage` → team B is absent from the rows
    *and* from the CPU-share denominator.
11. Run the reference install for 24 h → rows written are within the order of
    magnitude of §2's table. This is the acceptance test the feature exists to
    pass.

## 14. Deliberately out of scope

- **Metric threshold alerts.** The feature-matrix V1.x row, and the obvious
  next step — but this ships the numbers, not the policy. An alert needs
  thresholds, hysteresis, cooldowns, per-resource configuration and a mute
  story; `app.crashed` earned all of that discussion in
  [deployment-control.md](deployment-control.md) §5 for a single boolean
  transition. It gets its own spec, and it will be cheap once these buckets
  exist.
- **Live, per-second resource view.** A second collection path with different
  semantics, different transport and a different failure mode, to serve a
  question ("what is it doing *right now*") that the interactive terminal
  answers better. The 5-minute bucket is the product; the honest cost is that
  the newest point can be five minutes old, and the UI says so.
- **A Prometheus/OpenMetrics scrape endpoint on the panel.** A good idea and a
  different feature: it re-exposes what we store, which means an auth story for
  scrapers and a stable metric naming contract we would then owe forever.
- **A metric drain to an external system.** Runtime logs get one
  ([log-drains.md](log-drains.md)); metrics do not, yet. The bounded window
  here is deliberately small precisely because the answer to "I need 13 months
  at 15-second resolution" is a drain, not a bigger table in the control
  plane's database — and the drain is a second spec, not a paragraph in this
  one.
- **Acting on the numbers.** A controller that scales an Application from its
  own CPU buckets is designed in [app-scaling.md](app-scaling.md), on top of
  this. What this feature owes it is the measurement and the honesty about its
  resolution; the cooldowns, hysteresis and cost caps that make a controller
  safe are that spec's problem, and must not be improvised here.
- **Web analytics.** Unique visitors, geography, referrers, user agent and
  browser breakdowns, bot filtering, sessions, funnels, conversions. Not a
  near-miss — an entirely different product with an entirely different privacy
  posture. Umami is already an entry in the template catalog and Plausible and
  Matomo are exactly what that catalog exists to host; this feature
  deliberately drops the data all three of them need.
- **Per-request tracing, APM, or slow-request samples.** A captured slow
  request means capturing headers and probably a body, which §4.3 forbids.
- **Anything monetary** — prices, currency, invoices, plans, quotas,
  enforcement (§8, vision.md).
- **Per-container network counters** (§12).
- **Metrics for the control plane's own process.** `GET /panel/version` already
  carries the panel host's disk (disk-management.md §6); the panel's own CPU and
  memory belong to a self-monitoring story that does not exist yet, and half of
  one here would be worse than the gap.
- **A third rollup tier.** Two tiers answer both screens; a third arrives when
  someone can name the query it makes possible.

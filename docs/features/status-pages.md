# Feature spec: Public per-project status pages

> One switch on a Project publishes a page at `status.example.com` that says
> whether the things that project runs are working. It reuses the health checks
> the panel already runs, serves through the same Proxy and the same
> certificate machinery every Application uses, draws thirty days of uptime
> bars, and opens and closes incidents on its own from observed health.
>
> It is the first page this product serves to **people who are not signed in and
> never will be**, and that — not the bars — is what most of this spec is about.
> Everything the panel knows is interesting to someone; almost none of it is
> theirs to see.
>
> Written 2026-09-06, just before implementing (CLAUDE.md rule 7). Vocabulary
> per [glossary.md](../glossary.md), which gains **Status Page**, **Status Page
> Component** and **Incident** in the same PR.

## 1. The thing that must not go wrong

A status page is a small feature with one enormous failure mode: it publishes,
to the entire internet, information the panel gathered under the assumption
that only the team could read it. Everything else here — the bars, the
percentages, the incidents — is arithmetic. The disclosure decision is the
design.

Two concrete versions of getting it wrong, both of which the obvious
implementation produces:

- **The agency case.** P2 in [personas.md](../product/personas.md) runs "5–30
  client applications across a handful of servers", and their clients
  "occasionally get read access". Their Projects are named after clients and
  their Applications after client systems. A status page that
  publishes resource names by default publishes a customer list on the day
  somebody flips a switch, and the operator finds out from the customer.
- **The reconnaissance case.** Server names, host counts, revision ids, image
  references, health-check detail strings and route domains are, collectively,
  a map of an estate: how many machines, what is on them, which version of what
  is running, when it ships, and which internal hostname to try next. Each field
  looks harmless where it sits in the panel. Published together, unauthenticated,
  they are the first page of a penetration test.

So the rule this feature is built around, stated once and enforced structurally
in §2 and §5:

> **Nothing reaches the public page unless a person chose that exact string and
> saw it rendered before it was published.**

The design caption says "one toggle per project", and it stays one toggle — but
the toggle is preceded by a screen showing precisely what will become public,
with every published word editable. That is not friction added for its own sake;
it is the whole security model, and §9 explains why a preview is a better
control than a warning.

## 2. What is public, and what never is

Every field the panel holds about a resource falls in one of three columns. The
middle column is the operator's decision; the right-hand column is not offered
at any price.

| Public, always | Public, operator's choice | Never public |
|---|---|---|
| A page title | Which resources appear at all | Resource names (unless retyped as a label) |
| A per-component label | The label each one carries | Server names, ids, addresses, host counts |
| A public state word (§6) | The page's own domain | Revision ids, image references, digests, tags |
| 30 daily bars and an uptime figure | Whether the page exists | `status_detail` — the agent's own words |
| Incident start, end and duration | A one-line incident message | Route domains, ports, path prefixes |
| The time the page was last updated | | Deploy events, deployment ids, build output |
| | | Environment names, project name, team, slug |
| | | Anything about a preview environment |

The reasoning behind the right-hand column, because a list without reasons gets
edited by the next person:

- **Server identity, in any form.** Not the name, not the address, not the
  count, and not implicitly: components are never grouped by server and the page
  never says "3 of 4 nodes healthy". Which workload sits on which host is the
  single most useful fact an attacker can be handed for free, and it is of no
  use whatsoever to the customer the page is for.
- **Revision ids and image references.** A revision id plus a public repository
  is a direct index into the exact code running, including the dependency
  versions in it. It also publishes release cadence. The page reports
  availability, not inventory.
- **Deploy activity.** A deploy is not an event on a status page. Where the
  rollout is zero-downtime — which is non-negotiable 4 — the service was
  available throughout, so there is nothing to report; where it was not, the
  outage is reported as an outage. Publishing "deploying" would tell the world
  when to watch, and would tell the reader nothing they can act on.
- **`status_detail`.** It is free text written by the agent from the daemon,
  from compose, or from a failing health probe: `exit code 137`, a refused
  connection on a container port, an image reference, a file path. It is
  exactly the field ENGINEERING rule 20's discipline exists around, and it is
  never rendered, never returned by the public JSON, and never used to derive
  public copy beyond the state word.
- **Component URLs.** Tempting and refused: an Application's route domain may
  be an internal-only hostname, and a list of the domains a project serves is
  an attack-surface inventory. A component is a name and a state.
- **Preview environments, structurally.** A Preview Environment is created by a
  machine from an outsider's pull request. A page that could include one would
  publish branch names and PR titles written by people who do not work here.
  The API refuses a component whose resource lives in a preview environment;
  it is not a default, it is a rejection.

**The public payload is its own type.** The JSON in §8 is assembled by a
dedicated mapper into a struct that has no resource ids, no server fields and no
detail string to omit. It does not narrow a DTO the panel already returns.
Reusing an internal DTO and deleting fields is how the third field added next
year becomes public by accident; a separate type makes the leak a compile-time
addition somebody has to write on purpose.

**One field of the panel's own is deliberately withheld too:** the page carries
no branding, no "powered by" line and no link back to the panel. An install
should not announce what software it runs on a page it invites the public to,
and a customer's status page is not an advertising surface.

## 3. Who serves the page

The page is rendered and served by the **control plane**, and fronted at its
domain by the ordinary per-node **Proxy** (§4).

That deserves an argument, because it is the first time public traffic reaches
`cypherd`. Three candidates were considered:

1. **A container on a node holding a rendered snapshot.** Survives a plane
   outage, which is a genuine advantage. Rejected: it needs an image, a
   reconciler, a re-render-and-redistribute path on every state change, and a
   container per page — a large machine to make a 4 KB document, and one whose
   content is stale exactly when it changes. It also puts panel-authored
   content into the workload plane, which is a boundary worth not smudging.
2. **A route in the panel's React app.** Rejected on two counts: a public
   visitor would download the entire admin bundle to read four lines, and the
   page would be blank until JavaScript ran. §3.1 explains why that second point
   is disqualifying for this page specifically.
3. **The plane renders it.** Chosen. The plane already holds every input, has
   no round trip to make, and answers in one request.

**Vision non-negotiable 5** — *the control plane never runs user workloads or
builds* — is the line to check this against, and it holds: the page is the
panel's own content about the operator's resources, exactly like the panel UI,
and no request from a status-page visitor reaches any user container, any
build, or any code the panel did not ship. What is genuinely new is that the
plane is now in a public request path, which strains threat-model §5.9
(availability, self-DoS). That is paid for in §10, not waved away.

### 3.1 Server-rendered, and running no JavaScript at all

The page is one HTML document produced by one `html/template` compiled at init
from an embedded file. It carries inline CSS, inline SVG bars, and **no script**.
It refreshes with `<meta http-equiv="refresh" content="60">`.

This is the first server-rendered HTML in `cypherd`, and threat-model §5.8
specifically banks on there not being any: *"No server-side web framework to
CVE. The UI is static assets served from the Go binary (ADR-001)."* The
exception is deliberate and is bounded so that the property that sentence is
really claiming still holds:

- One template, embedded, parsed once at startup. No template is ever built
  from data, and no template is ever parsed at request time.
- `html/template` contextual auto-escaping. Every interpolated value is
  operator-authored text landing in a text node or a plain attribute — never in
  a URL, script, style or `srcset` context. The page's canonical domain is
  rendered as text, not as a link.
- **No request data reaches the template.** Not the query string, not a header,
  not the user agent. The only request-derived value is the slug, which is used
  to look the page up and is never rendered.
- Response headers: `Content-Security-Policy: default-src 'none'; style-src
  'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`,
  plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

A page that ships no script and forbids script cannot be made to run one, which
is most of what the "no SSR" property was buying. The rest — no framework, no
router, no server-side component tree — is unchanged.

The reason for no-JavaScript is not purity. **This page is read when things are
broken.** A document that needs a bundle, a fetch and a parse has three ways to
fail where one response has one; it also has to work in a text browser, in a
link preview, behind a corporate proxy that strips scripts, and on a phone on a
train. The one thing lost is live updating without a reload, and a 60-second
meta refresh is an honest substitute for a page whose underlying data cannot
move faster than that anyway (§6).

## 4. Routing: the same Proxy, the same TLS

A Status Page with a domain is routed by exactly the mechanism an Application's
domain is routed by — [ADR-004](../adrs/ADR-004-traefik-file-provider.md)'s file
provider, one fragment per route, written atomically by the local agent — with
one difference: its upstream is the panel rather than a container.

`DesiredState` gains one additive field (number 6, the next free one):

```proto
// StaticRouteSpec routes a domain to an upstream that is not a container.
// Today that is exactly one thing: a Status Page served by the control plane
// (status-pages.md §4). It carries no command and no credential — a hostname,
// an absolute URL and a path prefix — and absence means remove, like specs.
//
// upstream_url is filled by the PLANE from its own configuration and is never
// operator input, which is what keeps this from being a way to aim a node's
// Proxy at an address someone typed.
message StaticRouteSpec {
  string route_id = 1;      // fragment identity on the node (sp_…)
  RouteSpec route = 2;      // domain + https, as an Application's route
  string upstream_url = 3;  // absolute http(s) URL of the control plane
  string add_prefix = 4;    // prepended to the request path before forwarding
}
```

`buf breaking` stays clean (rule 18): one new message, one new field, nothing
renumbered. No new subject, no new verb, and nothing here asks an agent to *do*
anything — the node converges one more fragment, which is what
[ADR-005](../adrs/ADR-005-desired-state-reconciliation.md) already asks of it.
A route id absent from `static_routes` has its fragment removed, the same
absence-means-remove contract Applications follow.

The Proxy driver gains two small capabilities behind its existing seam: an
upstream given as an **absolute URL** (an Application's upstream is a bare
`host:port` the writer prefixes with `http://`; a plane behind a TLS terminator
must be expressible as `https://…`), and the `addPrefix` middleware. The
prefix is why the page needs no Host-header dispatch: Traefik rewrites `/` to
`/status/<slug>`, so the plane serves one ordinary path and the same page works
under any number of domains, with `passHostHeader` left at its default.

Everything else is inherited rather than rebuilt:

- **Certificates.** The `le` resolver on the serving node, from the panel-wide
  ACME account in `DesiredState`. If the node has no resolver, the status route
  is written on `web` only with no redirect and no `certResolver` — the same
  rule, for the same reason, as
  [routing-and-tls.md](routing-and-tls.md) §7.
- **Ownership.** When a DNS Provider is connected, the status domain is
  resolved against the zone list exactly as an Application's is
  ([dns-automation.md](dns-automation.md) §4.1). A domain outside every zone is
  stored, reported unverified, and **not routed** — otherwise this feature would
  be a way around the one ownership check the panel has.
- **Diagnosis.** `GET /api/v1/status-pages/{id}/domain-check` is the existing
  prober against the page's domain, with the same verdicts and the same
  remedies (`no_dns`, `unreachable`, `served_by_other`, `ok`).

Two things this does *not* inherit. **A DNS Record is not created for the status
domain in this slice**: `dns_records.application_id` is the FK the whole reaper
rule rests on (*"a record with no application has no reason to exist"*), and
making that owner polymorphic changes a shipped feature's central invariant —
that belongs in a PR of its own, not smuggled in here. And **the page's domain
is checked for collision** with Application and Compose Stack route domains on
write, answering `409` naming the resource that already claims it: two fragments
with the same `Host` rule on one Proxy is a coin flip. That check is on write
only, and general panel-wide domain uniqueness remains unenforced, which is the
honest description of what ships.

The page names the **Server** whose Proxy fronts it (defaulting to the server
the first component runs on). On the single-node install that is the only
choice and the default is right; on a fleet the operator points DNS at that
server, and the UI says which one in plain words.

## 5. The resource model

**Migration `0041_status_pages.sql`.** The highest on disk is
`0039_server_disk.sql`, and `0040`/`0041` are contested by sibling specs written
the same week, so the PR takes the next free number. Reversible (rule 16).

```
status_pages
  id              TEXT PK (sp_…)
  project_id      TEXT NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE
  slug            TEXT NOT NULL UNIQUE     -- public handle, [a-z0-9-]{3,40}
  title           TEXT NOT NULL            -- the public display name
  enabled         BOOLEAN NOT NULL DEFAULT false
  domain          TEXT NOT NULL DEFAULT '' -- '' = panel origin only
  https           BOOLEAN NOT NULL DEFAULT true
  route_server_id TEXT REFERENCES servers(id) ON DELETE SET NULL
  created_at, updated_at

status_page_components
  id              TEXT PK (spc_…)
  status_page_id  TEXT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE
  resource_kind   TEXT NOT NULL   -- application | compose_stack | database
  resource_id     TEXT NOT NULL   -- no FK: polymorphic (metrics-and-usage §6)
  label           TEXT NOT NULL   -- the ONLY name that becomes public
  position        INTEGER NOT NULL
  tracking_since  TIMESTAMPTZ NOT NULL
  UNIQUE (status_page_id, resource_kind, resource_id)

status_evaluations
  id              TEXT PK  -- exactly one row: the evaluator's last tick (§6.3)
  evaluated_at    TIMESTAMPTZ NOT NULL

status_intervals
  id              TEXT PK (si_…)
  component_id    TEXT NOT NULL REFERENCES status_page_components(id) ON DELETE CASCADE
  state           TEXT NOT NULL   -- operational | degraded | down | unknown
  started_at      TIMESTAMPTZ NOT NULL
  ended_at        TIMESTAMPTZ     -- NULL = current
  message         TEXT NOT NULL DEFAULT ''   -- operator's one line (§8)
  INDEX (component_id, started_at DESC)
```

`UNIQUE (project_id)` is the design caption enforced in the schema: **one page
per project**, so "which of our four status pages is the real one" is not a
question this product can be asked.

**Components are polymorphic and carry no foreign key**, following
[metrics-and-usage.md](metrics-and-usage.md) §6: the target is an Application, a
Compose Stack or a Managed Database — the three Resources in the glossary, and
the three the design screen shows ("Web app", "API", "Database"). All three
already report observed status in the same six-word vocabulary
([ui-principles.md](../product/ui-principles.md) §5), which is what makes one
component type possible at all. A component whose resource no longer exists is
not rendered — the read is a join — and is swept.

**`status_intervals` is the whole time series, and it is written per change,
not per tick.** A healthy component has exactly one open row, forever. This is
the [metrics-and-usage.md](metrics-and-usage.md) §2 write-budget discipline
applied to a much easier problem: 30 daily bars, an uptime percentage and the
incident list are all queries over the same rows, so there is no samples table,
no rollup job, and nothing whose cost scales with time rather than with events.

## 6. From an observation to an interval to an incident

### 6.1 The public state of a component

Internal status is mapped down to four public words. The mapping is where the
honesty lives, so each line has a reason:

| Observed | Public | Why |
|---|---|---|
| `running` | operational | |
| `degraded` | degraded | "Serving, with something wrong" is a true thing to tell a customer. |
| `error` | down | |
| `stopped` (after it has run) | down | The page describes what a visitor gets, not what the operator intended. A stopped Application answers nothing; calling that operational is a lie the reader can check. |
| `stopped` (never yet run) | unknown | The birth state. A component added before its resource has ever reported `running` is not tracked yet, so adding one does not open an instant permanent incident. |
| `deploying` | *the state already recorded* | Zero-downtime deploys mean the previous revision is serving. A deploy is not news, and it is not the public's business (§2). |
| `unknown`, **or the component's Server has no recent heartbeat** | unknown | ui-principles §10: never show state you cannot currently verify. A silent agent means we do not know, and the page says so rather than freezing a green light. |

The last row matters more than it reads. An Application's stored status is the
last thing an agent said; the stale-heartbeat sweep marks the **Server**
unknown, not the workloads on it. Without that join a page would confidently
report "operational" for a host that fell off the internet an hour ago — the
precise failure ui-principles §10 exists to forbid, made public.

### 6.2 The evaluator, and why there is a dwell

One owned goroutine (rule 7) ticks every 30 s over enabled pages' components,
reads each one's current public state, and appends to `status_intervals` when —
and only when — the observed state has differed from the recorded one for at
least `CYPHERD_STATUS_DWELL` (default 60 s). Injected clock (rule 9).

The dwell does three jobs with one rule:

- **It suppresses blips.** A container that restarts and recovers inside a
  minute is not an outage anyone should be told about, and a page that reports
  it is a page nobody reads by the second week. This is the same argument
  [disk-management.md](disk-management.md) §5 makes for firing on transitions
  only and [threshold-alerts.md](threshold-alerts.md) §4 makes for "for this
  long".
- **It bounds writes.** A crash-looping application produces one continuous
  `down` interval rather than three hundred rows.
- **It matches the resolution of the input.** The agent reports on change and
  on its 60-second drift pass, so an observation is up to a minute old by
  construction. A dwell shorter than that would claim precision the data does
  not have. The page therefore states its own resolution out loud rather than
  implying a stopwatch.

The cost is stated rather than hidden: **an outage shorter than the dwell is
invisible to this page**, and the uptime figure does not include it.

An **Incident** is not a separate record. It is a `status_intervals` row whose
state is `down` — opened by the evaluator when the dwell is satisfied, closed
when recovery satisfies it. That is what "incidents open and close
automatically" means here, literally. `degraded` intervals are stored and
colour the bars but do **not** open an incident: an incident is a record that a
service was *unavailable*, and a page that files one whenever something is
imperfect is a page that gets muted — the same conclusion deployment-control
reached when it excluded `degraded` from `app.crashed`.

### 6.3 Being honest about the plane's own absence

Uptime derived only from recorded intervals has one systematic lie in it: while
the control plane is not running, it observes nothing, so an open `operational`
interval silently spans the gap and the page later claims a perfect day.

So a single panel-wide marker row is stamped by the evaluator on every tick,
and on boot a gap wider than two ticks opens and immediately closes an
`unknown` interval covering it on every tracked component. Time the plane could
not see is drawn as a grey gap and excluded from the uptime denominator — not
counted as up, and not counted as down.

This composes neatly with §10's central limitation rather than fighting it:
while the plane was down the page was returning `502`, and afterwards it shows
grey for exactly that period. The page cannot report its own absence in real
time, but it never pretends the absence did not happen.

## 7. The bars and the number

For each of the last **30 UTC days**, per component, over the intervals
overlapping that day:

- `covered` = seconds tracked and not `unknown`; `down` and `degraded` = seconds
  in those states.
- Colour: grey when `covered = 0`; red when `down > 0`; amber when
  `degraded > 0`; green otherwise. A day is not green because the downtime was
  brief — it is green because there was none.
- The bar's `title` gives the date and the plain-language figure ("13 Aug —
  down 4m 12s"), which works without JavaScript.

Uptime over the window is `1 − Σdown ⁄ Σcovered`, to two decimals, matching the
design screen's `99.97%`. Two rules keep the number honest:

- **Uptime is over observed time.** Grey periods are in neither the numerator
  nor the denominator, so a fleet-wide blackout cannot round itself up to 100%.
- **A component with no coverage shows `—`, never `100%`.** Printing a perfect
  score for a page that has measured nothing is the most common dishonesty in
  this product category, and it is one `if`.

The banner ranks the components — `down` beats `degraded` beats `unknown` beats
`operational` — so the copy is one of: **All systems operational** ·
Some systems are degraded · Some systems are down · All systems are down ·
Some systems are not reporting. `unknown` deliberately outranks `operational`:
"All systems operational" while a component is unreporting is the same lie in
sentence form.

## 8. API and authorization

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/projects/{id}/status-page` | member | `404` when the project has none |
| `PUT /api/v1/projects/{id}/status-page` | **team admin** | creates or replaces title, slug, domain, server, `enabled` |
| `DELETE /api/v1/projects/{id}/status-page` | **team admin** | removes the page and its history |
| `GET /api/v1/status-pages/{id}/components` | member | |
| `PUT /api/v1/status-pages/{id}/components` | **team admin** | the whole ordered list, labels included |
| `GET /api/v1/status-pages/{id}/domain-check` | member | the existing prober (§4) |
| `GET /api/v1/status-pages/{id}/preview` | member | the public payload for a page that is not enabled yet |
| `PATCH /api/v1/status-pages/{id}/incidents/{iid}` | member | one line of public text, ≤ 280 chars |
| `GET /status/{slug}` | **public** | the HTML document |
| `GET /status/{slug}/data.json` | **public** | the same content as JSON |

**Enabling a page is team admin**, and the reason is the one Compose Stacks
used for its file: this is a capability an ordinary deploy does not confer.
Publishing a project's health, under names of somebody's choosing, at a
hostname customers will read, is a disclosure decision — so it sits at the rank
that already decides who joins the team, not at the rank that ships code.

**Annotating a live incident is a member**, and that is a deliberate softening.
The person who notices at 02:00 is on call, not an admin; making them find one
before they can write "we are aware and investigating" is how a status page
stops being used. The blast radius is bounded to 280 characters of plain text on
a page that is already public, it is audited with the author's name, and it is
never linkified — operator text is escaped and rendered as text, so the page
cannot become a redirect on a hostname the customer trusts.

Audited actions, closed vocabulary: `status_page.created`, `.updated`,
`.deleted`, `.published`, `.unpublished`, `.incident_annotated`. Publishing and
unpublishing are their own actions rather than details on `.updated` because
"we made this project's health public, on this date, and this person did it" is
precisely the fact an audit read exists to find.

The **public routes** are the third route family this panel opens to people
outside it — after the inbound GitHub webhook and the invitation links — and the
first meant for an anonymous audience rather than for someone holding a secret. They are `security: []` in `openapi.yaml`, read-only, take
no body and no query parameters, and answer one undifferentiated `404` for an
unknown slug, a disabled page and a deleted page alike. They are **not** rate
limited by client address, unlike sign-in and the invitation routes: a status
page is read hardest exactly when it matters, and throttling the audience during
an incident would be a self-inflicted outage of the outage page. The protection
is §10's cache instead.

## 9. Screens

**Managing it** is a new tab beside the existing project settings
(`/projects/{id}/settings/status-page`), so top-level navigation stays at four
items (ui-principles §4).

The tab is, top to bottom: the switch; the public address (the panel-origin URL
always, the custom domain when set, each with a copy button); the page title and
slug; the ordered component list, each row a resource picker and the **public
label** in an editable field; and a live preview rendering the real page from
`/preview`.

**The preview is the disclosure control**, and it is a better one than a
warning. A confirmation dialog that says "this will be public" is read by
nobody; a rendered page with the customer's name on it is read by everybody,
because it looks like the thing it is. Turning the switch on shows that preview
with the heading **"This page will be public at status.example.com"** and the
component labels one last time. The switch is still one switch.

The four-state page contract (ui-principles §1) applies to the settings tab as
usual, and — less usually — **to the public page too**:

- **Empty** — an enabled page with no components says "No systems are being
  reported here yet." It does not render a green banner over nothing.
- **Error** — if the data cannot be read, the response is `503` with one plain
  sentence. A status page that fails must not fail cheerful, and must never
  serve a stale cached green page in place of an error.
- **Content** — the design screen: title left, canonical domain right in mono,
  the status dot and sentence, then one row per component (label · 30-day bar ·
  state word · uptime). Under the bars, "30 days ago" and "Today", because
  thirty anonymous rectangles fail the "explain it cold" test (§11) without
  them. In the footer, "Updated 4m ago" with the absolute UTC time in the
  `title`, and one line saying what is measured.

Light and dark both ship (ui-principles §9). The public page follows
`prefers-color-scheme` rather than a stored preference, because it has no
account to remember one against; the design screen is the dark rendering.

## 10. Cost, risk, and what this page cannot tell you

The four things an operator should know before they point a customer at it.

1. **It is served by the panel, so it dies with the panel.** If `cypherd` is
   down or the panel host is gone, the status page answers `502`. It reports on
   your applications; it cannot report on itself, and it is not a substitute for
   a status page hosted somewhere else entirely. No design on the table changes
   this — a page whose data comes from the plane cannot outlive it — and §6.3
   at least makes the resulting gap visible after the fact instead of green.
2. **It measures health checks, not what a user experiences.** The probe is
   internal, agent → container, and independent of any public route (that is
   `work.proto`'s own wording). So a container that is perfectly healthy behind
   a certificate that failed to issue, a DNS record that moved, or a Proxy that
   is not running will read as **operational** while nobody can reach it. This
   is the sharpest limitation in the feature. It is not hidden: the footer says
   what is measured, and §14 records what fixing it would take.
3. **Its resolution is one minute** (§6.2). Sub-minute outages do not appear in
   the bars or the percentage.
4. **The plane is now in a public request path.** Mitigated, not eliminated: a
   rendered page is cached in memory for `CYPHERD_STATUS_CACHE_TTL` (15 s) and
   served with a matching `Cache-Control` and an `ETag`, so a link that goes
   viral costs one render per page per 15 s and zero database work in between;
   the enabled-slug set is held in the same refreshed snapshot, so an
   enumeration flood is answered from memory without touching Postgres. What
   remains is that anonymous traffic can consume the plane's HTTP capacity —
   true of `/healthz` and the console today, unchanged in kind, larger in
   likelihood. Accepted, recorded here and in threat-model §5.9, and bounded by
   the operator's own front door if they run one.

Footprint (non-negotiable 1): one goroutine, one timer, one cached document per
enabled page — a few KB each — and no per-tick writes. Nothing here moves the
plane's 300 MB idle budget.

## 11. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_STATUS_DWELL` | `60s` | How long a component must hold a state before the page records it. Also the page's resolution. |
| `CYPHERD_STATUS_CACHE_TTL` | `15s` | How long a rendered page and its JSON are cached in memory. |
| `CYPHERD_STATUS_RETENTION` | `90d` | How long `status_intervals` rows are kept. Longer than the 30-day window so a longer window is possible later with no hole. `0` keeps them forever. |

Swept in bounded batches by the existing retention discipline
([audit-log.md](audit-log.md) §8). The upstream the Proxy points at is the
panel's own base URL — `CYPHERD_PUBLIC_URL` when set, otherwise the derived
`http://<CYPHERD_PUBLIC_HOST>:<http port>` — the same value every self-link the
panel writes is already built from. There is no new address to configure, and
none of it is operator-supplied (§4).

## 12. Alternatives considered and rejected

- **Deploy Uptime Kuma from the catalog.** It is already a bundled template and
  it is a good program. Rejected as the answer because it is a second system to
  operate, with its own probes to configure by hand, duplicating health-check
  configuration the panel already holds — and it goes stale the moment an
  application is renamed or moved. The panel's advantage here is that it already
  knows; a feature that makes the operator retype what the panel knows throws
  that away. An operator who wants an independent prober should still run one,
  and §10.1 says so.
- **Uptime from request metrics (5xx rate) rather than health checks.** Rejected
  three times over: it depends on [metrics-and-usage.md](metrics-and-usage.md),
  which has not shipped; a page with no traffic would have no data, so a 03:00
  outage nobody hit would be invisible; and an application returning 500 to one
  user is not the service being down — a page that goes red for that is a page
  nobody trusts. It is a good *second* signal later (§14).
- **A samples table, one row per component per interval.** Rejected on the write
  budget metrics-and-usage §2 already argued: 8 640 rows per component per month
  to answer questions that interval rows answer exactly.
- **Point the status domain at the panel and let the operator sort out TLS.**
  The zero-code option. Rejected because the panel's own TLS is usually
  somebody's external reverse proxy, so the operator would be obtaining a
  certificate by hand for a subdomain — exactly the friction this product
  exists to remove, and the opposite of "the same proxy and TLS".
- **A new subscribable event (`status_page.incident_opened`).** Rejected as a
  duplicate: `app.crashed` already fires on the transition this page draws, and
  a second event for the same fact means two messages per incident and one more
  channel to mute.
- **Operator-written incident timelines, subscribers, scheduled maintenance.**
  Rejected as a different product (§14) — but a *single* line of operator text
  per incident is kept, because the gap between "it is down" and "we know" is
  the only thing a reader needs that automation cannot supply.

## 13. Acceptance (testable)

1. Enabling a page and pointing a domain at the serving node makes
   `https://status.<domain>/` answer the rendered page through the Proxy, with a
   certificate from the panel's ACME account — and every other path on that host
   answers `404`, never the panel console.
2. **Nothing from the right-hand column of §2 appears in either public
   response.** Asserted directly: a fixture with a distinctive server name,
   revision id, image reference, route domain and `status_detail` is fetched
   through both public routes and every one of those strings is absent.
3. A component driven `running → error` and held there past the dwell opens an
   incident and turns the day's bar red; returning to `running` closes it. A
   flap shorter than the dwell does neither.
4. A component whose Server stops heart-beating reads `unknown`, the banner says
   "Some systems are not reporting", and the uptime denominator excludes that
   period.
5. A page with no coverage renders `—`, not `100%`.
6. Disabling the page makes both public routes `404` within the cache TTL and
   removes the route fragment from the node.
7. A component naming a resource in a preview environment is refused with a
   reason.
8. The public routes are reachable with no credentials, and reachable with an
   *invalid* one, without a `401`.

## 14. Deliberately out of scope

- **External probing from the plane.** The fix for §10.2, and a feature of its
  own: it needs a scheduler, an `egress`-guarded client (threat-model §5.11's
  discipline), per-component probe configuration, and its own threat-model
  scenario for "the panel makes recurring requests to an address an operator
  typed". Naming it here is the honest alternative to pretending the internal
  probe measures reachability.
- **Incident timelines, updates and subscribers.** Email or RSS subscription,
  multiple updates per incident, incident severities and postmortems. That is a
  communications product; this is a health display with one line of annotation.
- **Scheduled maintenance windows.** They interact with Freeze Windows and
  Deploy Protection in ways worth designing once, deliberately, rather than
  bolting a second calendar onto a page.
- **Custom branding, logos, CSS.** Uploaded assets on an unauthenticated page
  are a content-hosting feature and a fresh XSS surface. The page is typography
  and two colours; that is a choice, not a placeholder.
- **More than one page per project, or a page spanning projects.** The schema
  refuses both (§5). A page that can span tenancy boundaries is a page that can
  leak across them.
- **Uptime windows other than 30 days**, and per-component history pages.
  Retention keeps 90 days so this stays available; the API and the page show 30.
- **A DNS Record for the status domain** (§4), and panel-wide domain uniqueness.
  Both are changes to shipped features and belong in their own PRs.

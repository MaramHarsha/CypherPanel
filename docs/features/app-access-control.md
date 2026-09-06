# Feature spec: Preview passwords, IP allowlists and maintenance mode

> Three answers to one question — *who is allowed to reach this application right
> now?* — grouped on one card in the application's Settings, because an operator
> asking that question does not know in advance which of the three is the answer.
> A passphrase in front of every preview, a list of CIDRs in front of an admin
> panel, and a deliberate 503 while a migration runs.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md); the Proxy and its fragments per
> [routing-and-tls.md](routing-and-tls.md) and
> [ADR-004](../adrs/ADR-004-traefik-file-provider.md).

## 1. What the design asks for, and the one claim in it that is wrong

The Access card carries three toggles and this copy, which ships verbatim:

- **Password-protect previews** — "Every pr-* environment asks for this
  passphrase before serving — clients see staging, the internet doesn't."
- **IP allowlist** — "Only these CIDRs reach the app — for admin panels and
  internal tools."
- **Maintenance mode** — "Serve a branded 503 page while you migrate; deploys
  still work underneath."

The card's footnote reads "All three are proxy middlewares — no app code
changes." Half of that is true and the important half is not. Traefik v3 has an
`ipAllowList` middleware and a `basicAuth` middleware; it has **no middleware
that returns a fixed response**. There is no way to make Traefik say 503 with a
page of our own from middleware alone, and building the feature on the
assumption that there is would be discovered in the implementation rather than
here. So the footnote ships as **"All three are handled by the Proxy — no app
code changes"** — which is what the line was actually promising the reader — and
§7 pays for the difference.

The claim the copy makes about deploys is true and load-bearing: the health
probe is internal (agent → container) and nothing here sits in the deploy path.
Maintenance mode changes who is served, never what is running.

## 2. Three capabilities, not custom middlewares

[routing-and-tls.md §10](routing-and-tls.md) lists "custom redirects,
middlewares, rate-limiting, auth middleware" as non-goals, and the feature
matrix keeps *Redirects & middleware* at V1.x. The design says this feature is
the case for promoting them. It is the case for promoting **three named
capabilities**, and the distinction is the whole argument:

A named capability is desired state. The plane can validate a CIDR, refuse an
empty allowlist, hash a passphrase, audit the change, tell the UI what is on and
since when, and render the result honestly. A pass-through middleware block is
an escape hatch whose effect the panel cannot describe. It is the same line
[ADR-004](../adrs/ADR-004-traefik-file-provider.md) draws when it refuses the
docker provider, and the same one [compose-stacks.md §5](compose-stacks.md)
draws when it refuses a compose file's own Traefik labels: **the panel emits the
routing.** Once an operator can inject arbitrary middleware into a fragment, the
writer can no longer guarantee the entrypoint pinning, the `certResolver`
logic that [agent-identity-and-tls.md](agent-identity-and-tls.md) §5 existed to
get right, or the `X-Served-By` marker — and the panel's answer to "why is my
site down" becomes "read your own YAML".

So this spec promotes three, not the category. *Redirects & middleware* stays
V1.x, and stays a different feature.

## 3. Where the state lives

All three are **current application state, not a per-revision snapshot** —
`0040_app_access_control.sql`, additive (rule 16):

```
applications
  ip_allowlist_enabled     BOOLEAN NOT NULL DEFAULT false
  ip_allowlist             JSONB   NOT NULL DEFAULT '[]'   -- normalized CIDR strings
  maintenance_mode         BOOLEAN NOT NULL DEFAULT false
  maintenance_since        TIMESTAMPTZ
  preview_password_enabled BOOLEAN NOT NULL DEFAULT false
  preview_password_hash    TEXT    NOT NULL DEFAULT ''     -- bcrypt; '' = never set
  preview_password_set_at  TIMESTAMPTZ
```

Current state is the only defensible choice, and the precedent is already here:
`restart_token`, CPU/memory limits, volumes and ports are all read from the
application row at spec-build time while the route's domain comes from the
revision's config snapshot ([scheduler.buildSpec](../../core/scheduler/scheduler.go)).
If access control were snapshotted, a rollback would silently lift a
maintenance page or restore a deleted allowlist entry — a security control that
changes because someone re-pointed a revision is not a control. The cost is one
mixed-provenance `RouteSpec`: domain, `https` and `path_prefix` from the
snapshot, access from the row. That asymmetry is deliberate and is worth a
comment at the assembly site.

The passphrase itself is **never stored**. The plane bcrypt-hashes it
(`core/auth.HashPassword` already exists, `bcrypt.DefaultCost`) and keeps only
the hash, so the plaintext exists in the operator's clipboard and nowhere else.
It is returned exactly once, from the call that set it, and never again —
precisely the contract `POST /databases/{id}/reset-password` already has, for
the same reason: a credential the API will re-read is a credential the API will
eventually leak (rule 20).

## 4. On the wire, and in the fragment

`RouteSpec` is the right home: it is what the proxy driver consumes, and it is
the one message both an Application's route and a Compose Stack's route already
converge on. Additive, `buf breaking` clean (rule 18):

```proto
message RouteSpec {
  string domain = 1;
  bool https = 2;
  string path_prefix = 3;
  AccessSpec access = 4;              // new
}

message AccessSpec {
  repeated string allow_cidrs = 1;    // empty = no allowlist middleware
  repeated string basic_auth_users = 2; // "user:<bcrypt>" htpasswd lines, never a passphrase
  bool maintenance = 3;               // serve the 503 responder instead of the app
}
```

`basic_auth_users` is repeated although exactly one line is ever emitted today:
it is Traefik's own shape, and widening a singular string later is the kind of
change `buf breaking` refuses.

The fragment gains two middlewares — `<app>-allow` (`ipAllowList.sourceRange`)
and `<app>-auth` (`basicAuth`, with `removeHeader: true` so the app never sees
the credential it might log) — appended after the existing `<app>-mark`. **Mark
stays first**, deliberately: middlewares wrap in list order, so a visitor the
allowlist rejects still gets `X-Served-By: cypherpanel` on their 403, and an
operator locked out from a café learns that the panel is the thing refusing
them rather than guessing at a DNS problem.

Fragments that carry a credential hash are written **0600** instead of 0644 —
and so is every other fragment, because a file mode that varies with content is
a mode nobody can reason about. This follows the node's existing rule for
credential material (`acme.json`, and the 0600 env file compose-stacks writes),
and it works because the agent and the Proxy's Traefik both run as root on the
node. That is a stated dependency: if Traefik is ever run as non-root this
becomes a `usersFile` with matched ownership, and the existing routing
integration test is what catches it.

## 5. IP allowlist

`ipAllowList` with `sourceRange`, matched against the connection's source
address. Validation on the plane, not discovery on the node:

- Each entry is parsed as a prefix; a **bare address is accepted and normalized**
  to `/32` or `/128` and echoed back, because an operator pastes what
  whatismyip gave them, not a CIDR (ui-principles §11).
- `0.0.0.0/0` and `::/0` are refused: they mean "everyone", which is what the
  toggle already expresses, and a list that reads like a restriction but is not
  is worse than no list.
- **Enabled with an empty list is a 400**, and the message names maintenance
  mode — "an allowlist that allows nobody is a maintenance page with worse
  copy" (§1 of ui-principles: an error offers its remedy).
- Capped at 64 entries so a fragment stays a fragment.

Two honest limits, both surfaced in the UI rather than buried here:

**It protects the domain, not the machine.** An application with published
`ports` is reached directly on the host, entirely outside the Proxy. When
`ports` is non-empty the card says so next to the toggle. Pretending otherwise
would be the most dangerous sentence in this feature.

**Behind a CDN it matches the CDN.** The source address Traefik sees is the last
hop. We do not guess: no `ipStrategy` depth is configured, and the card says
that a proxied domain needs the forwarded-header work that the feature matrix
tracks as *Cloudflare CDN/proxy mode* (V1.x). The panel's own login throttle
solved this problem with an explicit trusted-proxy setting
([control-plane-hardening.md](control-plane-hardening.md)); the Proxy's version
of that decision belongs to that row, not to this card.

A list of IPv4 prefixes on a dual-stack host blocks every IPv6 visitor. The card
says that too, when the list has no v6 entry.

## 6. Preview password

Scope: the toggle lives on the **source** Application and governs the
Applications in the preview Environments it spawns — not the source application
itself. That asymmetry is the design's, and it is right. HTTP Basic in front of
production traffic trains people to type a shared credential into a browser
dialog, and it is verified per request (there is no session), so every asset
load pays a bcrypt comparison. In front of a `pr-*` environment that is a fine
trade; in front of production it is a bad one wearing the clothes of a good one.

The hash is read from the source application at spec-build time via the
`Preview` row, not copied into the clone at creation. Rotating the passphrase
therefore applies to **every live preview** on the next converge, which is what
"every pr-* environment asks for this passphrase" has to mean. Username is a
fixed `preview` (a dialog demands two fields; inventing a second unique string
helps nobody), realm is a constant, and both are shown beside the passphrase.

What this does and does not do, since it sits next to threat-model §5.6: it
keeps a client's staging build off search engines and away from a stranger who
guessed a PR number. It does **not** contain fork-PR code — that is the
environment-scoped secret boundary §5.6 already specifies, and a passphrase in
front of the door does nothing about what is already inside the room.

One refusal is deliberate: on a panel with no ACME account a preview is served
over plain HTTP ([routing-and-tls.md §7](routing-and-tls.md)), and Basic auth
over plain HTTP puts the passphrase on the wire in base64. We still emit it, and
the UI says so plainly. Silently declining to protect an environment the
operator believes is protected is the one option that is not acceptable.

## 7. Maintenance mode is a service, not a middleware

Traefik cannot serve a body of ours. The options were:

- **Point the route at a dead upstream.** Free, and wrong: Traefik answers 502
  with its own error page. 502 tells a crawler the app is broken; 503 tells it
  to come back. An unbranded gateway error is indistinguishable from the outage
  this feature exists to *replace* with a sentence.
- **The `errors` middleware.** It needs a responder service anyway and it
  preserves the upstream's status code, so it buys nothing.
- **Serve the page from the control plane.** Rejected outright: the plane is not
  in the request path (routing-and-tls §9), and putting it there would make a
  panel outage a fleet outage. Serving it from the agent process is rejected for
  its neighbour reason — that process holds the node's mTLS identity, and a
  public listener in it is the wrong blast radius.
- **A Traefik plugin.** Rejected: Yaegi plugins are fetched at start, a network
  dependency in the one component that must come up inert (ADR-004).

So the agent ensures a second managed container, `cypher-maintenance`, exactly
the way it ensures `cypher-proxy`: a pinned image, management labels,
`unless-stopped`, joined to a dedicated `cypher-maintenance` network the Proxy
also joins — one network rather than every environment network, because the
responder has no business reaching applications. It answers every request with
**503**, `Retry-After: 300`, `Cache-Control: no-store` and a panel-styled page.
It is ensured on demand and removed when the last resource on the node leaves
maintenance, so a node that never uses this pays nothing.

Maintenance is then a **service swap** in the fragment the writer already owns:
the router, its rule, its TLS, its allowlist and its basicAuth are unchanged;
only the load-balancer server points at the responder. The app container keeps
running, keeps passing its health gate, keeps its observed `running` status, and
a deploy underneath still builds, health-gates and completes — it simply flips a
route that currently points elsewhere. When maintenance lifts, the fragment
points at whatever revision is current by then.

The page is **generic**. It names no application, no revision and no version:
its whole audience is the public internet, and telling a stranger which product
is down here is a fingerprint we are not obliged to hand out. Per-application
copy is out of scope (§11) for that reason and not merely for effort.

The trade this makes: while maintenance is on, **nobody** reaches the app
through the front door, including an allowlisted operator. An
"allowlist bypasses maintenance" rule was considered and rejected — it would
make maintenance mode's meaning depend on a second toggle's contents, and an
operator who allowlisted the office would never see the page they are showing
the world, then lift it believing it had never applied.

## 8. Convergence, propagation and failure

The plane writes the row, then calls the existing `Scheduler.ConvergeApp` —
`ConvergeWork` carries a spec, never a verb (ADR-005), so nothing new crosses
the boundary. The agent's reconciler already writes the route fragment on
**every** cycle rather than only when the upstream moves — the comment in
`convergedApp` records why: "a middleware added to the template stayed invisible
until something unrelated moved the container's IP." That existing behaviour is
what makes this feature converge at all, and it means a lost converge message
self-heals within the 60 s drift pass instead of leaving a maintenance page up
forever. `SetRoute` skips a byte-identical write, so converging twice still
mutates nothing (rule 13).

Two implementation consequences to get right, both of them cheap and both of
them silent bugs if missed:

1. `convergedApp` compares the **applied** upstream with the app's upstream to
   decide whether to re-probe. Under maintenance the applied upstream is the
   responder's, so the comparison must account for maintenance or the agent
   health-probes the app once per cycle for the duration of the outage window.
2. The shared responder service is defined **once**, in a reserved fragment the
   orphan GC must skip — not in each app's fragment, which the file provider
   would read as duplicate service definitions.

Failure has one honest path. If the responder cannot be ensured (an image pull
fails on a node with no registry access), the agent **does not touch the
route**: the application keeps serving, and it reports `degraded` with the pull
error in `status_detail` — `degraded` is precisely "serving, with something
wrong" ([deployment-control.md §5](deployment-control.md)), and because
`app.crashed` fires only on `running → error`, nobody is paged for a
maintenance page that did not appear. The panel says the maintenance page could
not start and why. It never takes an application down as a side effect of
failing to take it down politely.

This is the feature's one new external dependency: a second image pull per node,
alongside the Proxy's. It is stated, not hidden — an air-gapped node gets the
other two capabilities and a refusal on the third.

## 9. API, authorization and audit

| Route | Rank | Notes |
|---|---|---|
| `PUT /api/v1/applications/{id}/access` | member | allowlist + `preview_password_enabled`, wholesale |
| `POST /api/v1/applications/{id}/access/preview-password` | member | sets or rotates; the passphrase is in the response **once** |
| `DELETE /api/v1/applications/{id}/access/preview-password` | member | forgets the hash |
| `PUT /api/v1/applications/{id}/maintenance` | member | on; idempotent |
| `DELETE /api/v1/applications/{id}/maintenance` | member | off; idempotent |

Maintenance is its own sub-resource rather than a field in the `PUT /access`
body for a concrete reason: the caller that most wants it is a migration script,
and a script that has to read-modify-write the allowlist in order to put up a
holding page will eventually clobber the allowlist. `Application` gains a
read-only `access` object (`ip_allowlist_enabled`, `ip_allowlist`,
`maintenance_mode`, `maintenance_since`, `preview_password_enabled`,
`preview_password_set_at`) — never the hash, never the passphrase.

**Member, like every other application mutation**, including env vars. Admin was
considered and rejected: a principal who can already rewrite the application's
`DATABASE_URL` and delete it outright, but cannot change its allowlist, is not a
coherent boundary — it is a second rank invented for one card. These are also
deliberately **not** session-only (unlike deploy-protection's approvals): a CI
token raising a maintenance page around a migration is the intended use, and the
audit log is where the accountability lives.

Four new actions in the closed vocabulary — `application.access_updated`,
`application.preview_password_set`, `application.maintenance_started`,
`application.maintenance_ended`. Details carry the CIDRs and the fact of a
rotation; never the passphrase, never the hash (audit-log.md §6).

No new event types. Maintenance mode is a person's decision, not an
infrastructure failure — the same reason
[deployment-control.md §2](deployment-control.md) gave for announcing nothing on
a cancelled deploy — and a channel that pages on deliberate actions gets muted,
taking the next real alert with it.

## 10. UI, and the failure mode that actually matters

One `Access` card in the application's existing **Settings** tab — the design's
own breadcrumb (`WEB / SETTINGS / ACCESS`), and not an eighth tab: the strip
already carries seven, and ui-principles §4 makes a new one cost a recorded
decision. Three rows, three toggles, the design's copy verbatim, the CIDR list
inline with `+ add`, and the caveats from §5 shown *only* when they apply.

The failure mode of this feature is not a bug. It is **maintenance mode left
on** — a Friday migration and a Monday of silence. So `maintenance_since` is
displayed wherever the application appears ("Maintenance mode · 3h"), as a
badge, not a status word: the status vocabulary is closed (ui-principles §5) and
`redeploy_pending` already set the precedent for a fact that is not a status.

An auto-expiring maintenance window was considered and rejected. A holding page
that lifts itself while the migration is still running publishes a half-migrated
application to the internet, which is a worse Monday than the one it prevents.
The panel's job is to make the on-state impossible to miss, not to guess when
the operator is finished.

This is also where the feature touches a vision non-negotiable and should say
so: *"Zero-downtime deploys by default. Opting into downtime is allowed;
getting it by surprise is not."* Maintenance mode is downtime on purpose. The
toggle is the opt-in, the badge is what keeps it from becoming a surprise, and
the deploy path is untouched — so the non-negotiable holds in both halves.

The glossary gains **Maintenance Mode** and **Preview Password** before this
ships (ui-principles §8).

## 11. Deliberately out of scope

- **Custom middlewares, redirects, rate limiting, forward-auth.** §2. Three
  named capabilities, not an escape hatch. *Redirects & middleware* stays a
  V1.x row of its own.
- **Access control on Compose Stacks.** The wire already carries it — a stack's
  route is a `RouteSpec` through the same writer — so this is API surface and
  UI, not architecture. It waits for someone to ask, because a stack's route is
  already deliberately one service and one port.
- **Environment-wide maintenance mode.** The obviously right shape for "while
  you migrate", and the same mechanism fanned out over N applications. It is
  deferred rather than free: it needs a rule for what happens to an application
  added to an environment that is already in maintenance, and that is a design
  question, not a loop.
- **Per-application maintenance copy.** §7 — the page's audience is the public
  internet and it should not name what is behind it. A panel-wide custom page is
  a defensible later setting; a per-app one is a leak with a nice UI.
- **Basic auth on production applications.** §6. The mechanism generalizes in
  one line; the reason not to is not a mechanism.
- **Multiple preview credentials, or per-user accounts.** One passphrase per
  source application. The moment there are user accounts in front of a route,
  the answer is real authentication and it belongs behind its own ADR.
- **`X-Forwarded-For` depth for the allowlist.** §5. It belongs to the
  Cloudflare proxy-mode row, with the trusted-proxy decision made once for the
  Proxy rather than twice.
- **Protecting published host ports.** §5. The allowlist is a property of the
  route; a raw port is not routed. Filtering the host's own ports is a firewall,
  and CypherPanel does not manage the firewall.

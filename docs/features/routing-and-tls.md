# Feature spec: Routing & TLS (the Proxy)

> How a deployed Application becomes reachable at its domain over HTTPS. The
> per-node Traefik **Proxy** ([glossary](../glossary.md)) is owned by the local
> `cypher-agent` via Traefik's file provider ([ADR-004](../adrs/ADR-004-traefik-file-provider.md)):
> the agent ensures Traefik is running, keeps its dynamic config converged with
> desired state, and obtains Let's Encrypt certificates on the serving node.
>
> Written 2026-07-19, just before implementing the Proxy lifecycle. ADR-004
> already decided *Traefik v3, file provider, behind a driver interface*; this
> spec pins down the parts it left open — running Traefik, the upstream
> networking model, and the TLS flow. Vocabulary per [glossary.md](../glossary.md).

## 1. Scope

Phase 2's routing slice, completing the deploy pipeline's ROUTE stage
([application-deploy.md §2–3](application-deploy.md)):

- The agent **ensures a Traefik v3 Proxy** runs on every server that hosts
  Applications, with a static config it fully controls.
- The agent reaches Application upstreams over Docker networks without
  publishing app container ports, preserving per-environment isolation.
- **HTTP → HTTPS** with **Let's Encrypt HTTP-01** certificates obtained and
  stored on the serving node (ADR-004), auto-renewed.
- The route flip is **zero-downtime**: the old revision keeps serving through
  the Proxy until the new one is healthy and the fragment is atomically
  swapped (acceptance gate 1, [roadmap](../roadmap.md)).
- Everything behind the `proxy.Driver` interface (Caddy is a later driver).

Out of scope this slice → §10.

## 2. The Proxy model (recap)

Per [ADR-004](../adrs/ADR-004-traefik-file-provider.md): **Traefik v3 runs on
every worker node; the local agent owns its dynamic configuration** via the
file provider — atomic `write-temp + rename` of one config fragment per
Application. Traefik's own API is never exposed (a known attack surface); the
file provider achieves dynamic config inertly. The generation logic lives
behind a driver interface in `agent/proxy/`, exactly like the orchestrator
drivers in `agent/driver/`.

Already built (Phase 2 to date): `agent/proxy.TraefikWriter` implements the
`docker.Router` seam — `SetRoute` / `RemoveRoute` / `Route` — writing and
observing per-app fragments with injection guards (no backticks in domain or
path, no path traversal in the app id) and atomic activation. What is missing,
and is this spec's subject: **who runs Traefik, how Traefik reaches upstreams,
and how certificates are obtained.**

## 3. Proxy lifecycle — the agent ensures Traefik

The agent treats Traefik as a **managed resource on the node**, reconciled the
same way Applications are (the reconciler-development rules apply — the Proxy
is a reconciler). On startup and on every reconcile the docker-role agent
**ensures the Proxy**:

- A container named `cypher-proxy` from a pinned `traefik:v3` image, carrying
  the management labels (`cypherpanel.managed=proxy`) so it is discoverable and
  never confused with an Application.
- `RestartPolicy: unless-stopped` — a node reboot restores routing without the
  plane, matching the Application containers (ADR-005 desired-state survives
  restarts).
- The image tag is pinned and updated only by an agent release, never
  auto-latest (a surprise Traefik major is a fleet-wide outage risk).
- Ensuring is idempotent (rule 13): a `cypher-proxy` already running the
  desired image + static config is left untouched; a drifted or absent one is
  (re)created. Static-config changes are applied by recreating the container
  (Traefik reads static config only at start; dynamic config hot-reloads).

The agent binary stays a single static binary (vision.md non-negotiable 1);
the Proxy is a container it pulls and supervises, **not** linked into the agent
process — so it does not count against the agent's < 50 MB RSS budget, though
it is a documented additional per-node cost (~one small container).

## 4. Networking & isolation model (decision)

The docker driver already places each Application on a per-environment network
`cypher-<environment_id>` and never publishes container ports; the route
fragment's upstream is the container's IP on that network. For a single Proxy
to reach those upstreams it must share each such network.

**Decision: the agent attaches `cypher-proxy` to every environment network it
serves an Application on.** When the docker driver ensures `cypher-<env>` for a
rollout, the Proxy is (idempotently) connected to it; when an environment no
longer has any routed Application on the node, the Proxy is disconnected from
that network during GC. Upstreams stay bare `IP:port` on `cypher-<env>` — no
port publishing, no host-port management.

Rationale: this keeps **cross-environment network isolation** — Applications
in one environment cannot reach another environment's containers at the
network layer — while making the Proxy the single, deliberate exception that
spans environments (it must reach every app it fronts anyway). This is a
security-relevant property; it is documented here and referenced from the
threat model rather than left implicit.

Alternatives rejected:
- **One shared `cypher-ingress` network for all app containers + Traefik** —
  simplest (Traefik on one network) but every Application shares a network,
  erasing cross-environment isolation. Rejected for the isolation regression.
- **Publish app ports to the host, route Traefik to `host:port`** — rejected:
  host-port allocation/collision management, and it reopens inbound app ports
  the driver deliberately avoids.

> If the cross-environment isolation guarantee is later hardened into a stated
> security boundary (e.g. enforced by network policy, not just topology),
> promote this decision to an ADR that supersedes the relevant part of ADR-004.

## 5. Static configuration

The agent renders Traefik's static config (`traefik.yml`, mounted read-only)
and owns it:

- **Entrypoints:** `web` on `:80`, `websecure` on `:443`. `web` serves the ACME
  HTTP-01 challenge, and — per Application, not globally (§6) — a permanent
  redirect to `websecure` for routes that actually get a certificate. These are
  **public ingress ports** — orthogonal to
  [ADR-002](../adrs/ADR-002-agent-dial-home-no-ssh.md), which forbids inbound
  *control* ports on the agent; user traffic to Applications is a different
  boundary (threat-model TB below §9) and must be served somewhere.
- **Providers:** the `file` provider with `directory:
  /etc/cypherpanel/traefik/apps`, `watch: true`. No `docker` provider, no API,
  no dashboard — nothing that reads intent from anywhere but the agent's
  fragments (ADR-004).
- **Certificate resolver `le`:** ACME with the HTTP-01 challenge on the `web`
  entrypoint, storage at `/etc/cypherpanel/traefik/acme.json`. It is configured
  **only when the node has an ACME account** — carried in desired state from the
  panel's TLS settings, or overridden per host with `CYPHER_ACME_EMAIL`
  ([agent-identity-and-tls.md §4](agent-identity-and-tls.md)). With no account
  there is no resolver, and the fragment writer agrees with the static config
  rather than naming one that does not exist (§7).
- The static config and the `apps/` fragment directory and `acme.json` live on
  a host path bind-mounted into `cypher-proxy`, so certificates and routes
  survive a Proxy container recreation.

Config hygiene (ADR-004): fragments are written temp-then-renamed (atomic), and
the writer validates structure before the rename so Traefik never watches a
half-written file.

## 6. Dynamic configuration (route fragments)

Unchanged from what the driver already produces, restated for completeness. For
each routed Application the agent writes `apps/<app_id>.yml`:

- a **router** keyed by `app_id`: rule `Host(\`<domain>\`)`
  (`&& PathPrefix(\`<prefix>\`)` when set), service `<app_id>`, and
  `tls.certResolver: le` when the route is HTTPS **and this node has a resolver**
  (§7). Its `entryPoints` are always written explicitly — `websecure` for a TLS
  router, `web` for every router without a `certResolver` — never left to
  Traefik's "attach to every entrypoint" default, which would answer `https://`
  for a plain-HTTP route with the self-signed default certificate;
- for an HTTPS route with a resolver, a sibling router `<app_id>-http` on `web`
  that permanently redirects to HTTPS;
- a **service** `<app_id>` whose single load-balancer server is the current
  revision's upstream (`http://<container-ip>:<port>` on `cypher-<env>`).

The fragment is removed when the Application is deleted or descheduled from the
node (desired absence). Orphaned fragments — a `<app_id>.yml` with no
corresponding desired Application — are cleaned during reconcile (desired-state
GC, like managed containers/images).

## 7. TLS / Let's Encrypt

- **HTTP-01 by default** ([ADR-004](../adrs/ADR-004-traefik-file-provider.md)):
  Traefik answers the ACME challenge on the `web` entrypoint for the routed
  domain; the operator's DNS must already point the domain at the server. No
  DNS-01, no wildcards this slice (§10).
- **Certificates live on the serving node**, in `acme.json` (bind-mounted,
  `0600`). The control plane holds only route metadata, never certificate
  private keys (ADR-004). Renewal is Traefik's own responsibility once the
  resolver is configured; the agent does not proxy ACME.
- **The ACME account is the panel's, not the host's.** The account email (and an
  optional directory URL — Let's Encrypt staging vs production) is one panel-wide
  setting under `PUT /api/v1/panel/tls`, carried to every node in `DesiredState`
  ([agent-identity-and-tls.md §4](agent-identity-and-tls.md)). Nothing has to be
  set per host; `CYPHER_ACME_EMAIL` / `CYPHER_ACME_CASERVER` remain per-host
  overrides, which is also what keeps the integration test hermetic and off
  Let's Encrypt's rate limits (§9).
- **No account ⇒ no resolver ⇒ no HTTPS promise.** With no ACME account the node
  configures no resolver, and an `https` route is written as a plain HTTP router
  on `web` **only**: no `certResolver`, **no HTTP→HTTPS redirect**, and no
  binding to `websecure`. Emitting the redirect anyway sent every visitor to
  `:443` to be answered by Traefik's self-signed default certificate — a browser
  warning instead of the app, which is worse than plain HTTP; leaving the router
  bound to every entrypoint reached the same warning without the redirect, for
  anyone who typed `https://`. `:443` answers such a host with a 404 instead.
  The same pinning applies to a route the operator declared HTTP-only. The plane
  reports this as
  `Application.tls_state: http_only_no_resolver`, so the UI says "serving over
  HTTP meanwhile" rather than claiming a certificate.
- A domain that fails issuance (DNS not pointed, rate-limited) does not break
  the rollout: the Application still serves over `web`/HTTP and its status
  reflects the running revision; the missing cert is a route-level condition,
  surfaced separately, not a deploy failure. (Reporting *issuance* state — as
  opposed to whether a resolver is configured at all — from the node to the
  plane is still a later refinement; §10.)

## 8. The `proxy.Driver` interface & reconciliation

Formalize the existing `docker.Router` seam as the driver contract in
`agent/proxy` (mirroring `agent/driver.Reconciler`):

```
type Driver interface {
    Name() string                                   // "traefik" (later "caddy")
    EnsureProxy(ctx) error                           // §3: Traefik running + static config
    SetRoute(ctx, appID, route, upstream) error      // write+activate a fragment (§6)
    RemoveRoute(ctx, appID) error                    // remove a fragment
    Route(ctx, appID) (upstream string, ok bool, err) // observe applied route (§6)
    AttachNetwork(ctx, network string) error          // §4: join cypher-<env>
    Reconcile(ctx, desired []RouteSpec) error         // ensure fragments == desired; GC orphans
}
```

The Proxy is reconciled from the same desired set the orchestrator driver
converges: after a rollout the agent has the desired routes; `Reconcile`
ensures Traefik is up (§3), attached to the needed networks (§4), fragments
match, and orphaned fragments are removed. Idempotent and observation-based
(rules 12–13): converging twice makes no changes, and a fresh agent over an
existing node converges without in-memory state (fragments and container
labels are the truth). Nothing Traefik-shaped leaks outside `agent/proxy`
(project-structure rule 2).

## 9. Zero-downtime flip & security

**Zero-downtime (acceptance gate 1).** During a rollout the sequence is: start
the new revision → health-gate it → **atomically rename the app's fragment to
point its service at the new upstream** → Traefik hot-reloads the fragment →
drain and stop the old container. The old upstream keeps serving every request
until the file-provider swap; Traefik finishes in-flight requests against the
old server as it reloads. "Zero dropped requests" is asserted by a request loop
through the Proxy across a rev A→B flip (§ acceptance).

**Threat model.** The Proxy adds one boundary — **public user traffic →
`:80/:443` → Application** — which is expected and distinct from the agent↔plane
control boundary (ADR-002, threat-model TB4/TB6): no inbound *control* ports are
opened, agent identity is unchanged, and the plane is not in the request path.
Traefik's API/dashboard stay disabled (config comes only from agent fragments,
ADR-004). Fragment inputs are guarded against router-rule and path injection
(already in the writer). Certificate keys never leave the node. Cross-env
network isolation holds except for the Proxy's deliberate spanning role (§4).

## 10. Non-goals for this slice

DNS-01 / wildcard certificates · user-supplied (BYO) certificates · custom
redirects, middlewares, rate-limiting, auth middleware · Caddy as a second
proxy driver (bounded follow-on: implement `proxy.Driver`) · TCP/UDP routers ·
multiple Proxy replicas / external load balancers (the scale-out story,
[roadmap](../roadmap.md) Later) · reporting per-domain certificate **issuance**
status (reading `acme.json` on the node) to the plane UI — whether a *resolver
exists* is now reported, via `Application.tls_state`
([agent-identity-and-tls.md §5](agent-identity-and-tls.md)), but whether a
certificate was actually obtained is not · sticky sessions.

## Acceptance (testable)

1. A deployed Application is reachable at its `Host(...)` rule **through
   Traefik** on `:80` (the integration test drives the real Proxy, not the
   container directly).
2. **Zero dropped requests across a flip:** a request loop through the Proxy
   during a rev A→B rollout sees only 2xx and observes the body change from A
   to B exactly once — no gap, no error.
3. Deleting the Application removes its fragment and the route stops answering;
   the Proxy keeps serving other Applications.
4. The Proxy survives its own container restart (routes + `acme.json` persist)
   and a node reboot (`unless-stopped`).
5. HTTPS path exercised against an ACME stub (hermetic); production HTTP-01 is
   validated on a real domain in the verify skill, not CI.

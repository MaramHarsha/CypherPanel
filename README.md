<p align="center">
  <img src="docs/assets/cypherpanel-lockup.svg" alt="CypherPanel" width="360">
</p>

<p align="center"><strong>An open-source Heroku alternative that leaves your VPS enough RAM to actually deploy something.</strong></p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#what-works-today">What works today</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#the-api">API</a> ·
  <a href="#security-model">Security</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

---

CypherPanel runs applications, databases and compose stacks on servers you own — from a git push, a container image, or a one-click template. Builds happen on your servers and never on the panel, every rollout is health-gated so the old version keeps serving until the new one is provably healthy, and anything you give a domain is routed through a managed Traefik proxy (with Let's Encrypt certificates once you have added an ACME account).

It takes the breadth of **Coolify** (one-click templates, every database) and the polish of **Dokploy** (rollbacks, backups, a clean data model), and puts them on an architecture neither has: a Go control plane commanding **dial-home agents** — no SSH keys stored anywhere, no builds on the panel, desired-state reconciliation throughout.

The tagline is a measurement, not a boast:

| | Coolify | Dokploy | CypherPanel |
|---|---|---|---|
| **Platform RAM, idle** | ~2 GB stack | **~1 GiB, measured on a fresh VPS** | **34 MB** control plane · **17 MB** per agent |
| **Platform disk before your first app** | — | **3.84 GB** of images and volumes | One binary + Postgres |
| **On a 1 GB VPS** | No | **Cannot run at all** — the baseline exceeds total RAM | The target this was designed against&nbsp;<sup>[1]</sup> |
| What you install | Laravel, Redis, Horizon, Soketi | Node SSR, Redis, BullMQ, Postgres | One Go binary + Postgres |
| Orchestration | SSH from the panel — keys stored, fleet-wide liability | Docker Swarm lock-in | Outbound-only mTLS agents, no stored credentials |
| Where builds run | Ad hoc over SSH | On the panel's own node | On worker agents — **never** on the control plane |
| Deploys | Imperative scripts | Imperative scripts | Desired-state reconciliation — drift repair and crash recovery by construction |

<sup>[1]</sup> Where each number comes from, because a table like this is worthless if you cannot check it. **CypherPanel:** measured RSS for the `cypherd` and `cypher-agent` processes at idle, against budgets of 300 MB and 50 MB ([vision.md](docs/vision.md)). **Dokploy:** a whole-box measurement on a fresh Ubuntu VPS, recorded with its method in [research/dokploy.md](research/dokploy.md) — the "cannot run at all" is that document's own conclusion. **Coolify:** derived from the components its stack runs, not measured whole-box. The last row is the design goal stated in [vision.md](docs/vision.md) ("a $5 VPS runs the control plane *and* two deployed apps comfortably"); a like-for-like whole-box benchmark of CypherPanel is still owed, so read it as the target it is rather than as a number we have published.

> **Status.** Phases 1–3 are complete and Phase 4 is nearly closed. The deploy pipeline is proven end to end in CI against real Docker and real Traefik; the state model, the security model, the template catalog and the web UI are all in. See [What works today](#what-works-today) for the honest checklist — including what is CI-proven versus verified by hand — and [docs/roadmap.md](docs/roadmap.md) for the phase gates.
>
> **There is no published release yet.** The one-line installer below expects one, so today you build the two binaries yourself ([Build from source](#build-from-source)).

## How it works

Two programs and a database. That is the whole install.

```
┌───────────────── CONTROL PLANE — cypherd (one binary) ─────────────────┐
│  REST API + embedded web UI ──► PostgreSQL (the only state of record)  │
│  Scheduler: Deployment → durable work items → observed outcome         │
│  Embedded NATS JetStream bus (mTLS, per-agent authorization):          │
│     work.*  → commands to agents      (file-backed, survives restarts) │
│     state.* → agent observations      (heartbeats, statuses, events)   │
│     logs.*  → build/runtime log lines (bounded retention, SSE replay)  │
└─────────────────────────────▲──────────────────────────────────────────┘
                       mTLS,  │  outbound-only (works behind NAT)
┌─────────────────────────────┴─────────── cypher-agent (per server) ────┐
│  docker driver: reconciles containers against desired state            │
│  builder: git clone → image build (stays local — no registry required) │
│  proxy: runs and configures Traefik v3 (file provider, Let's Encrypt)  │
│  streams logs, reports what is ACTUALLY running                        │
└────────────────────────────────────────────────────────────────────────┘
```

**A deploy, end to end.** A `git push` fires your app's HMAC-verified webhook (or you `POST /deploy`) → the scheduler records a **Deployment** pointing at an immutable **Revision** and publishes a build work item to that server's durable queue → the agent clones, builds the image locally, and streams the build log (an application sourced from an image skips straight past this) → the docker driver starts the new container beside the old one, **health-checks it, atomically flips the Traefik route, and only then drains the old one** — the old revision never stops serving until the new one is provably healthy → the agent reports the *observed* state, and only that observation marks the deployment succeeded. Rollback is the same pipeline aimed at an earlier revision with the build skipped: seconds, not minutes.

Because everything is desired state, failure is boring. Kill the agent mid-deploy and the work item waits in its durable queue; on restart the agent converges with no manual step. Kill the control plane and your apps keep serving; work replays when it comes back. CI asserts both on every push.

**Three principles fall out of the design** ([architecture.md](docs/architecture.md)):

1. **The control plane and the data plane are separate programs.** The panel never reaches into a server; it publishes desired state and agents reconcile it.
2. **Desired state, not imperative scripts.** A deployment is a row in Postgres, and the agent's whole job is making reality match it.
3. **The API is the product.** Every feature is a documented REST call first — the web UI, the webhooks and your CI are all just clients of it.

## Core concepts

Full vocabulary in [docs/glossary.md](docs/glossary.md).

- **Team → Project → Environment** — the organisational spine and the tenancy boundary. A team owns projects; people belong to teams with a ranked role (member < admin < owner). Every project gets a `production` environment; staging and previews are simply more environments.
- **Server** — a host running `cypher-agent`, identified by its mTLS certificate and never by a stored credential.
- **Resource** — anything deployable inside an environment. There are three: an **Application** (built from a git repository or an image), a **Managed Database** (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey), and a **Compose Stack** (your own compose file, run as written).
- **Revision** — an immutable snapshot of image plus config that a deployment points at, and what a rollback returns to.
- **Deployment** — a recorded transition from one desired revision to another, with its full pipeline history.
- **Preview** — an ordinary child environment holding a cloned application, created and destroyed by a pull request's own lifecycle with a TTL backstop. Not a special case.
- **Driver** — an orchestrator backend inside the agent. `docker` is the launch driver ([ADR-006](docs/adrs/ADR-006-docker-only-at-launch.md)); Swarm and Kubernetes are planned behind the same interface. The proxy is a driver too.

## What works today

**Proven by [integration CI](.github/workflows/integration.yml) on every push**, against real Postgres, real Docker and real Traefik:

- **Server onboarding** — create a server, paste one `curl | sh` line on a fresh Ubuntu box, and it joins in under 60 seconds: mTLS certificate issued against a pinned CA, single-use join token, no SSH at any point.
- **The deploy pipeline** — git repository → image build on the agent → health-gated zero-downtime rollout → reachable at its domain through the managed Traefik proxy.
- **Zero dropped requests** across a rolling update — a request hammer through the real proxy during the A→B flip sees only 200s.
- **Rollback in seconds** without a rebuild, **GitHub push-to-deploy** with constant-time HMAC verification, and `PATCH` config shaping the next revision.
- **Sealed secrets** — env vars and webhook secrets are AES-256-GCM encrypted at rest, masked in every response, and decrypted only onto the mTLS wire to the agent.
- **Live and replayed logs** — build and runtime output streams over SSE, and a client joining mid-build replays what it missed from a bounded retention window.
- **Crash recovery** — plane killed for 45 s and agents reconverge; agent killed with a deploy pending and the durable queue delivers it on restart, unaided.
- **Revocation** — deleting a server severs its live connection and refuses its still-valid certificate.
- **Footprints inside budget** — 34 MB plane / 17 MB agent RSS at idle, against budgets of 300 and 50.

**The state model** (Phase 3) — plane services and agent reconcilers with unit coverage and real-Postgres store tests in CI, each additionally verified end to end by hand:

- **Managed databases** across six engines, provisioned and reconciled by the agent, with sealed root credentials, start/stop/reset and connection info.
- **Scheduled backups and restore** to any S3-compatible target (SigV4, MinIO-tested) — engine-derived dump commands moved over the Docker archive API, never a shell string on the wire. A restore reports its real progress: the step it has reached and the bytes applied.
- **Preview environments** from pull requests, torn down on close or by a TTL sweeper, with fork-PR secret safety by construction.
- **Notifications** — Email, Discord, Slack and Telegram, project-scoped, fired on observed outcomes, best-effort and never blocking a deploy.
- **Scheduled tasks** — cron as declarative desired state, run by the agent inside the app's own unprivileged container ([ADR-011](docs/adrs/ADR-011-in-container-scheduled-tasks.md)).
- **Teams and roles** — ranked roles enforced on every project route, where a non-member gets 404 rather than 403 so existence never leaks across tenants.

**Breadth and hardening** (Phase 4):

- **A catalog of 158 one-click templates** — 7 hand-curated plus 151 translated from Coolify's compose library by a build-time importer, every image digest-pinned. What the importer refused, and why, is recorded per template ([docs/dev/template-import-report.md](docs/dev/template-import-report.md)).
- **Compose stacks** — bring your own compose file and the agent converges to it. The file *is* the desired state, so the revision list is the history and rollback re-points it ([docs/features/compose-stacks.md](docs/features/compose-stacks.md)).
- **Deploy protection** — per environment, who must approve a deploy and when deploys are refused outright. Freeze windows are weekly and zone-aware; break glass is a 30-minute recorded owner override; approvals are session-only, so a CI token can neither open its own gate nor delete it.
- **An immutable audit log** — one row per sensitive action: who did what to which resource, from where, and whether it worked. Scope *is* the authorization, so it needs no role gate.
- **Team invitations and access requests** — a single-use seven-day link, and the mirror image the 403 screen opens.
- **Container registries** — optional by construction ([ADR-008](docs/adrs/ADR-008-no-registry-required.md)): a sealed credential to pull a private base image through, or push a build to. No registry is ever required.
- **Deployment control** — cancel a deploy that is going nowhere, restart an application as desired state (zero-downtime, and never a silent redeploy of stale code), and a `?since=` replay window on both log streams.
- **Pack builds** — Nixpacks and Railpack, where a builder has opted in ([docs/features/pack-builds.md](docs/features/pack-builds.md)).
- **Proactive disk management** — the agent converges to a retain set rather than running a periodic prune, because a prune cannot tell what is still wanted. Crossing the disk threshold notifies the panel's owners once, on the transition.
- **Agent identity and TLS** — certificates renew themselves over the mTLS channel at two thirds of their life with a fresh key; one panel-wide ACME account reaches every node.
- **The web UI** — React, embedded in the binary, and at parity with the API: every mutating capability the plane exposes is reachable from the panel.

**Not built yet**, tracked in [docs/roadmap.md](docs/roadmap.md): named application databases, a dashboard, an interactive terminal, metrics and observability, a published design system, a CLI, and the implementation of agent auto-update ([ADR-010](docs/adrs/ADR-010-agent-auto-update.md), which lands with the release pipeline). Granular RBAC is deliberately deferred to V1.x behind its own ADR.

## Install

> Once a release is published, one command on a fresh Linux VPS (amd64 or arm64, systemd, root) is the whole install:

```sh
curl -fsSL https://raw.githubusercontent.com/MaramHarsha/CypherPanel/main/install/install.sh | sh
```

It installs Docker, starts PostgreSQL on loopback, installs the `cypherd` binary, generates a master key, and enables a systemd unit that survives reboots. Then you open the panel and create the owner account in the browser — no password is ever printed or defaulted.

Re-running is safe: an existing master key is never regenerated (that would make every sealed secret unrecoverable) and an existing database is left alone. Point it at your own build with `CYPHERD_URL=file:///path/to/cypherd` until releases exist. Options are documented in the [installer's own header](install/install.sh).

Servers are joined afterwards from the panel's copy-paste command, one per host.

## Build from source

Prerequisites: Go 1.25+, Docker, and PostgreSQL (or `make dev-up` for a throwaway one).

**1. Run the control plane**

```sh
(cd core  && go build -o ../bin/cypherd ./cmd/cypherd)
(cd agent && CGO_ENABLED=0 go build -o ../bin/cypher-agent ./cmd/cypher-agent)

export CYPHERD_DATABASE_URL="postgres://cypherpanel:devpassword@localhost:5432/cypherpanel?sslmode=disable"
export CYPHERD_MASTER_KEY=$(head -c32 /dev/urandom | base64)   # keep this — it seals every secret
export CYPHERD_ADMIN_EMAIL=you@example.com
export CYPHERD_ADMIN_PASSWORD=choose-a-password
export CYPHERD_PUBLIC_HOST=<hostname-agents-can-reach>         # default: localhost
./bin/cypherd    # or, against the make dev-up Postgres: make run-plane
```

`cypherd` migrates its own schema, bootstraps the admin account, and serves the API and web UI on `:8080`, agent enrollment on `:8443`, and the mTLS bus on `:4222`.

**2. Join a server**

```sh
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -d '{"email":"you@example.com","password":"choose-a-password"}' | jq -r .token)
curl -s -X POST localhost:8080/api/v1/servers \
  -H "Authorization: Bearer $TOKEN" -d '{"name":"my-vps"}' | jq -r .join.install_command
```

Paste the printed `curl | sh` line on the target server as root. It is self-sufficient on a fresh box: it installs Docker if there is none, downloads the agent, pins the plane's CA against the fingerprint carried in the command, enrolls, and installs a systemd unit. The agent dials home over mTLS and the server reads `running` within seconds, running and configuring its own Traefik.

**3. Deploy an application**

```sh
EID=$(curl -s -X POST localhost:8080/api/v1/projects -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"my-project"}' | jq -r .default_environment.id)

curl -s -X POST localhost:8080/api/v1/environments/$EID/applications \
  -H "Authorization: Bearer $TOKEN" -d '{
    "name": "web",
    "source": {"kind": "git_url", "repo": "https://github.com/you/your-app.git"},
    "runtime": {"server_id": "<server-id>", "port": 3000},
    "route": {"domain": "app.example.com"},
    "env_vars": {"DATABASE_URL": "..."}
  }'
# the response carries the app id and a webhook URL + secret, shown exactly once

curl -s -X POST localhost:8080/api/v1/applications/<app-id>/deploy \
  -H "Authorization: Bearer $TOKEN" -d '{}'
```

Point the domain's DNS at the server and the app is live. For HTTPS, give the panel an ACME account once — `curl -X PUT .../api/v1/panel/tls -d '{"acme_email":"ops@example.com"}'` — and every server obtains Let's Encrypt certificates for the domains routed to it. Until you do, routed apps are served over plain HTTP and the API says so (`tls_state: http_only_no_resolver`) rather than claiming a certificate nobody issued.

## The API

**198 operations under `/api/v1`**, bearer-token authenticated — except the GitHub webhook, which authenticates by per-application HMAC. The complete OpenAPI spec ships inside the binary at `GET /api/v1/openapi.yaml`, and it is the contract: CI fails on drift between it and the handlers.

| Area | What it covers |
|---|---|
| Auth & sessions | login/logout, profile, TOTP 2FA with recovery codes, live sessions, email change, API tokens |
| Teams, users & access | teams and ranked membership, panel accounts, invitations, access requests |
| Servers | enrollment, the join command, public address, disk, revocation |
| Projects | projects, environments, transfer, shared variables |
| Applications | create/patch/delete, env vars (write-only), volumes, ports and limits, restart, DNS and domain checks, runtime logs (SSE) |
| Deployments | deploy, list, rollback, cancel, build logs (SSE) |
| Compose stacks | create/edit, deploy, revisions, rollback, variables, logs (SSE) |
| Databases & backups | six engines, start/stop/reset, backup targets, schedules, history, restore |
| Previews | list and delete PR-spawned environments |
| Registries | credentials, connection test, and what uses each one |
| Deploy protection | policy, freeze windows, approvals, break glass |
| Notifiers & webhooks | channels, tests, outbound endpoints and delivery history |
| Scheduled tasks | cron entries and their run history |
| Templates | the 158-entry catalog and one-click instantiation |
| Audit & inbox | the immutable log, and per-user notifications |
| Panel | ACME account, mail, DNS, version, log tail |

## Security model

The [threat model](docs/security/threat-model.md) was written before the first line of agent code, and its requirements are enforced by tests rather than by review.

- **No SSH, ever.** Agents dial home outbound-only over mTLS against a pinned CA, so they work behind NAT and a compromised control plane yields shell access to nothing ([ADR-002](docs/adrs/ADR-002-agent-dial-home-no-ssh.md)).
- **Join tokens are single-use and short-lived.** After that an agent's identity is its 90-day certificate, renewed over the authenticated channel at two thirds of its life with a fresh key. Deleting a server cuts the live connection *and* refuses renewal, so a revoked identity expires rather than lingering.
- **Per-agent authorization on the bus.** Each agent may publish only its own `state.*` and `logs.*` and read only its own work queue, so a compromised agent's blast radius is its own server — asserted down to the JetStream API grants.
- **No arbitrary-command verb on the wire.** Work items describe state to converge on; there is no "run this on the host" message. A scheduled task's command is the one command-bearing field, and it runs only inside the app's own unprivileged container — no more privilege than deploying an image already grants.
- **Team-scoped authorization** on every project route, where a non-member gets 404 rather than 403 so resource existence never leaks. Rank is checked when a permission is created, never when it is spent.
- **Secrets sealed at rest** under AES-256-GCM, masked in every response, never logged; certificate private keys never leave the node that serves them.
- **Constant-time comparison** for tokens and HMACs, two-dimension login throttling, and one immutable audit row per sensitive action.

## Repository layout

```
core/      control plane (cypherd): REST + embedded web UI, scheduler, NATS bus,
           sqlc store + migrations, auth, enrollment CA, template catalog
agent/     data plane (cypher-agent): docker driver, builder, Traefik proxy driver,
           work consumer, health prober, log streamer
web/       the React UI (TanStack Router, generated API client) — embedded into cypherd
pkg/       shared: NATS subject contracts, generated proto, PKI, IDs
proto/     the wire contract (buf-managed; additive-only, no arbitrary-command verbs)
docs/      vision, architecture, 11 ADRs, 32 feature specs, threat model, roadmap
research/  extraction maps and measured baselines from the reference codebases
install/   the curl|sh installers for the plane and the agent
```

Placement rules: [docs/project-structure.md](docs/project-structure.md).

## Development

```sh
make check        # fast pre-commit gate: proto + build + vet + test
make test-race    # what CI runs
make test-store   # the store layer against a real throwaway Postgres
make lint         # golangci-lint, per module
make dev-up       # local Postgres via docker compose
```

Every push runs format, lint, race tests, an arm64 cross-build, proto compatibility and generated-code drift ([ci.yml](.github/workflows/ci.yml)), plus five integration jobs against real infrastructure: handshake and revocation, the 60-second installer gate, real-Postgres store tests, the full deploy slice including the zero-dropped-requests check through Traefik, and agent-outage resilience ([integration.yml](.github/workflows/integration.yml)).

Working on the code? [CLAUDE.md](CLAUDE.md) is the router and [ENGINEERING.md](ENGINEERING.md) is the law — binding rules rather than suggestions: consumer-defined interfaces, idempotent reconcilers with required test patterns, additive-only wire and schema changes, and secrets never in logs.

## Reading order

1. [docs/vision.md](docs/vision.md) — why this exists, who it serves, and the non-negotiables, with numbers
2. [docs/architecture.md](docs/architecture.md) — the system design
3. [docs/adrs/](docs/adrs/) — the eleven decisions everything else rests on
4. [docs/features/](docs/features/) — thirty-two implemented feature specs, each written before its code
5. [docs/product/feature-matrix.md](docs/product/feature-matrix.md) — the v1 scope contract, extracted from both reference codebases
6. [docs/roadmap.md](docs/roadmap.md) — phases with acceptance gates
7. [research/](research/) — extraction maps, measured footprints, and evidence-linked community pain points

## License

[Apache-2.0](LICENSE) — the whole repository, with no open-core split ([ADR-009](docs/adrs/ADR-009-apache-2-license.md)).

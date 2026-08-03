<p align="center">
  <img src="docs/assets/cypherpanel-lockup.svg" alt="CypherPanel" width="360">
</p>

<p align="center"><strong>A self-hosted PaaS: push code, get a URL, sleep well.</strong></p>

<p align="center">
  <a href="#quickstart-current-dev-state">Quickstart</a> ·
  <a href="#what-works-today">What works today</a> ·
  <a href="docs/architecture.md">Architecture</a> ·
  <a href="#the-api">API</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

CypherPanel deploys your applications from git to a live, TLS-terminated URL on your own servers. It unifies the best of **Coolify** (breadth: one-click templates, every database) and **Dokploy** (polish: rollbacks, backups, a clean data model) on an architecture neither has: a lightweight Go control plane commanding **dial-home agents** — no SSH keys stored anywhere, no builds on the panel, desired-state reconciliation throughout.

> **Status: Phases 1–3 are complete.** The deploy vertical slice is proven end-to-end in CI — git → image build → health-gated zero-downtime rollout → routed at its domain through a managed Traefik proxy, plus webhook deploys, live log streaming, rollback, and crash recovery. Phase 3 adds the state-model breadth on top: managed databases, S3 backups/restore, preview environments from PRs, notifications, cron-in-container scheduled tasks, and teams + roles. See [What works today](#what-works-today) for the honest checklist of what is CI-proven vs. end-to-end-verified, and [docs/roadmap.md](docs/roadmap.md) for the phase gates.

---

## Why another PaaS?

Coolify and Dokploy fail in mirror-image ways with the same root cause — the control panel does the heavy lifting itself:

| | Coolify | Dokploy | CypherPanel |
|---|---|---|---|
| Orchestration | SSH from the panel (keys stored — fleet-wide liability) | Docker Swarm lock-in | Outbound-only mTLS agents, no stored credentials |
| Footprint | ~2 GB stack (Laravel, Redis, Horizon, Soketi) | ~1 GB measured idle (Node SSR, Redis, BullMQ) | One Go binary + Postgres — **measured 34 MB plane / 17 MB agent RSS idle** (budgets: 300/50) |
| Builds | Ad-hoc over SSH | On the panel's own node | On worker agents; **never on the control plane** |
| Deploys | Imperative scripts | Imperative scripts | Desired-state reconciliation — drift repair and crash recovery by construction |

Full analysis: [docs/architecture.md](docs/architecture.md) · measured baselines and extraction maps: [research/](research/)

## How it works

Two programs and a database. That is the whole install.

```
┌───────────────── CONTROL PLANE — cypherd (one binary) ─────────────────┐
│  REST API + console ──► PostgreSQL (the only state of record)          │
│  Scheduler: Deployment → durable work items → observed-outcome         │
│  Embedded NATS JetStream bus (mTLS, per-agent authorization):          │
│     work.*  → commands to agents      (file-backed, survives restarts) │
│     state.* → agent observations      (heartbeats, statuses, events)   │
│     logs.*  → build/runtime log lines (bounded retention, SSE replay)  │
└─────────────────────────────▲──────────────────────────────────────────┘
                       mTLS,  │  outbound-only (works behind NAT)
┌─────────────────────────────┴─────────── cypher-agent (per server) ────┐
│  docker driver: reconciles containers against desired state            │
│  builder: git clone → docker build (image stays local, no registry)    │
│  proxy: runs + configures Traefik v3 (file provider, Let's Encrypt)    │
│  streams logs, reports what is ACTUALLY running                        │
└────────────────────────────────────────────────────────────────────────┘
```

**The deploy flow**, end to end: a `git push` fires your app's HMAC-verified webhook (or you `POST /deploy`) → the scheduler records a **Deployment** pointing at an immutable **Revision** and publishes a build work item to the target server's durable queue → the agent clones the repo, builds the Dockerfile image locally (no registry needed — [ADR-008](docs/adrs/ADR-008-no-registry-required.md)), and streams build logs → the docker driver starts the new container next to the old one, **health-checks it, atomically flips the Traefik route, then drains the old container** — the old revision never stops serving until the new one is provably healthy → the agent reports the *observed* state, and only that observation marks the deployment succeeded ([ADR-005](docs/adrs/ADR-005-desired-state-reconciliation.md)). Rollback is the same pipeline pointed at a previous revision, build skipped — seconds, not minutes.

Because everything is desired state, failure is boring: kill the agent mid-deploy and the work item waits in the durable queue; on restart the agent converges with no manual step. Kill the plane and agents keep serving; work replays when it returns. All of this is asserted by CI on every push.

**Three principles fall out of the design** ([architecture.md](docs/architecture.md)):
1. **Control plane and data plane are separate programs.** The panel never reaches into servers; it publishes desired state, agents reconcile it.
2. **Desired-state reconciliation, not imperative scripts.** A deployment is a row in Postgres; the agent's job is to make reality match it.
3. **The API is the product.** Every feature is a documented REST call first — the UI, CLI, and CI integrations are just clients.

## Core concepts

Full vocabulary in [docs/glossary.md](docs/glossary.md):

- **Team → Project → Environment** — the organizational spine and tenancy boundary. A team owns projects; users belong to teams with a ranked role (member < admin < owner). Every project gets a `production` environment; previews and staging are just more environments.
- **Server** — a host running `cypher-agent`, identified by its mTLS certificate (never by stored credentials).
- **Application** — a resource built from a git repository and owned end to end: source, build, runtime, route, health checks, sealed env vars.
- **Managed Database** — a first-class resource (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey) the agent provisions and reconciles, with scheduled backups to any S3-compatible target and restore.
- **Revision** — an immutable record (image + config snapshot) that a deployment points at; what rollback restores.
- **Deployment** — a recorded transition of an application from one desired revision to another, with its full pipeline history.
- **Preview** — an ordinary child environment holding a cloned application, created and destroyed automatically from PR lifecycle events (with a TTL backstop) — not a special case.
- **Notifier / Scheduled task** — a project-scoped channel (Email/Discord/Slack/Telegram) that fires on observed outcomes; and a cron entry the agent runs inside an app's own container ([ADR-011](docs/adrs/ADR-011-in-container-scheduled-tasks.md)).
- **Driver** — an orchestrator backend inside the agent. `docker` is the launch driver ([ADR-006](docs/adrs/ADR-006-docker-only-at-launch.md)); Swarm and k8s are planned behind the same interface. The proxy (Traefik, later Caddy) is a driver too.

## What works today

Everything below is exercised by the [integration CI](.github/workflows/integration.yml) on every push — real Postgres, real Docker, real Traefik:

- ✅ **Server onboarding**: create a server in the API, paste one `curl | sh` line on a fresh Ubuntu box, it joins in under 60 seconds — mTLS cert issued against a pinned CA, single-use join token, no SSH ever.
- ✅ **Deploy pipeline**: git repo → Dockerfile build on the agent → health-gated zero-downtime rollout → **reachable at its domain through the managed Traefik proxy**.
- ✅ **Zero dropped requests** across a rolling update — a request hammer through the real proxy during the A→B flip sees only 200s.
- ✅ **Rollback in seconds** (no rebuild), **GitHub push-to-deploy** (constant-time HMAC verification, branch-filtered), **PATCH config** shaping the next revision.
- ✅ **Sealed secrets**: env vars and webhook secrets are AES-256-GCM encrypted at rest, masked in every API response, decrypted only on the mTLS wire to the agent.
- ✅ **Live + replayed logs**: build and runtime logs stream over SSE; a client connecting mid-build replays what it missed from a bounded retention window.
- ✅ **Crash recovery, proven**: plane killed for 45 s → agents reconverge; agent killed with a deploy pending → the durable work queue delivers it on restart and the new revision goes live unaided.
- ✅ **Revocation**: deleting a server severs its live agent connection and refuses its still-valid certificate.
- ✅ **Footprints inside budget**: 34 MB plane / 17 MB agent RSS measured idle (budgets 300/50, [vision.md](docs/vision.md)).

Also shipped in Phase 2: deploy-key private repos, bounded runtime-log retention, the `--role=builder` split with multi-server image relay (proven live across two Docker daemons, [ADR-008](docs/adrs/ADR-008-no-registry-required.md)), and production Let's Encrypt validated on a real domain.

**Phase 3 — state-model breadth — is complete.** These land as plane services + agent reconcilers with unit coverage and **real-Postgres store tests in integration CI**; each was additionally **verified end-to-end by hand** (a real deploy/boot, driven through the API) rather than yet having its own dedicated end-to-end CI job like the deploy slice:

- ✅ **Managed databases** — PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey: provisioned and reconciled by the agent, sealed root credentials, start/stop/reset, connection info.
- ✅ **Scheduled backups & restore** to any S3-compatible target (SigV4, MinIO-tested) — engine-derived dump commands moved via the Docker archive API, never a shell string on the wire.
- ✅ **Preview environments** from PRs — a signed `pull_request` webhook clones the app into a child environment at `pr-<n>.<base>`; close (or a TTL sweeper) tears it all down; fork-PR secret safety by construction.
- ✅ **Notifications** — Email (SMTP), Discord/Slack/Telegram (webhooks): project-scoped, fired on observed deploy/backup outcomes, best-effort and never blocking the pipeline; sealed config, masked responses.
- ✅ **Scheduled tasks** (cron-in-container) — declarative desired state, run by the agent inside the app's own unprivileged container ([ADR-011](docs/adrs/ADR-011-in-container-scheduled-tasks.md)); **live-verified firing into a running container**.
- ✅ **Teams + roles** — teams own projects; ranked roles (member < admin < owner) enforced on every project-scoped route (non-member → 404, low rank → 403), plus panel roles gating shared infrastructure.

**Not yet built** (tracked in [docs/roadmap.md](docs/roadmap.md)): template catalog, Compose stacks, metrics/observability, interactive terminal, CLI (Phase 4) · granular RBAC (V1.x) · agent auto-update implementation ([ADR-010](docs/adrs/ADR-010-agent-auto-update.md), lands with the release pipeline). Since Phase 3 closed, TOTP 2FA (with recovery codes), backup-cron auto-scheduling with S3 retention pruning, and the web UI (slices 1–4 of [docs/product/web-ui-design.md](docs/product/web-ui-design.md)) have all landed.

## Install

One command on a fresh Linux VPS (amd64 or arm64, systemd, root):

```sh
curl -fsSL https://raw.githubusercontent.com/MaramHarsha/CypherPanel/main/install/install.sh | sh
```

It installs Docker, starts PostgreSQL on loopback, installs the `cypherd`
binary, generates a master key, and enables a systemd service that survives
reboots. Then open the panel and create the owner account in the browser — no
password is ever printed or defaulted.

Re-running is safe: an existing master key is never regenerated (that would
make every sealed secret unrecoverable) and an existing database is left alone.

Servers are joined afterwards from the panel's own copy-paste command, one per
host. Details and options: [install/install.sh](install/install.sh) header.

## Quickstart (current dev state)

Prerequisites: Go 1.25+, Docker, PostgreSQL (or `make dev-up` for a local one). There are no hosted releases yet — you build the two binaries yourself.

**1. Run the control plane**

```sh
(cd core  && go build -o ../bin/cypherd ./cmd/cypherd)
(cd agent && CGO_ENABLED=0 go build -o ../bin/cypher-agent ./cmd/cypher-agent)

export CYPHERD_DATABASE_URL="postgres://cypherpanel:devpassword@localhost:5432/cypherpanel?sslmode=disable"
export CYPHERD_MASTER_KEY=$(head -c32 /dev/urandom | base64)   # keep this safe — it seals every secret
export CYPHERD_ADMIN_EMAIL=you@example.com
export CYPHERD_ADMIN_PASSWORD=choose-a-password
export CYPHERD_PUBLIC_HOST=<hostname-agents-can-reach>          # default: localhost
./bin/cypherd    # or, against the make dev-up Postgres: make run-plane
```

`cypherd` migrates its schema, bootstraps your admin account, and serves the API + console on `:8080`, agent enrollment on `:8443`, and the mTLS bus on `:4222`.

**2. Join a server**

```sh
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -d '{"email":"you@example.com","password":"choose-a-password"}' | jq -r .token)
curl -s -X POST localhost:8080/api/v1/servers \
  -H "Authorization: Bearer $TOKEN" -d '{"name":"my-vps"}' | jq -r .join.install_command
```

Paste the printed `curl | sh` line on the target server. The agent enrolls, dials home over mTLS, and the server shows `running` within seconds. The agent needs Docker and git installed; it runs and configures its own Traefik.

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
# response includes the app id and a webhook URL + secret (shown exactly once)

curl -s -X POST localhost:8080/api/v1/applications/<app-id>/deploy \
  -H "Authorization: Bearer $TOKEN" -d '{}'
```

Point your domain's DNS at the server and the app is live at its URL (set `CYPHER_ACME_EMAIL` on the agent to enable Let's Encrypt). Watch progress with `GET /api/v1/deployments/<id>` and stream logs from `/api/v1/deployments/<id>/logs` (SSE). Add the webhook to GitHub and every push to the configured branch deploys itself.

## The API

Everything under `/api/v1`, bearer-token authenticated (the GitHub webhook authenticates by per-app HMAC instead). The complete OpenAPI spec ships inside the binary at `GET /api/v1/openapi.yaml` — it is the contract, enforced by CI.

| Area | Endpoints |
|---|---|
| Auth | `POST /auth/login` · `POST /auth/logout` · `GET /auth/me` |
| Teams & users | `GET·POST /teams` · `GET·PATCH·DELETE /teams/{id}` · `GET·POST·PATCH·DELETE /teams/{id}/members[/{uid}]` · `GET·POST /users` · `PATCH·DELETE /users/{id}` |
| Servers | `GET·POST /servers` · `GET·DELETE /servers/{id}` |
| Projects | `GET·POST /projects` · `GET·DELETE /projects/{id}` · `GET·POST /projects/{id}/environments` |
| Applications | `GET·POST /environments/{id}/applications` · `GET·PATCH·DELETE /applications/{id}` · env vars (write-only values) · `GET /applications/{id}/logs` (SSE) |
| Deployments | `POST /applications/{id}/deploy` · `GET /applications/{id}/deployments` · `GET /deployments/{id}` · `POST /deployments/{id}/rollback` · `GET /deployments/{id}/logs` (SSE) |
| Databases | `GET·POST /environments/{id}/databases` · `GET·PATCH·DELETE /databases/{id}` · start/stop/reset-password · `GET /databases/{id}/connection-info` |
| Backups | `GET·POST /backup-targets` · `GET·POST·DELETE /databases/{id}/backups[/{bak}]` · `POST …/run` · `GET …/history` · `POST /databases/{id}/restore` |
| Previews | `GET /applications/{id}/previews` · `GET·DELETE /previews/{id}` |
| Notifiers | `GET·POST /projects/{id}/notifiers` · `GET·PATCH·DELETE /notifiers/{id}` · `POST /notifiers/{id}/test` |
| Scheduled tasks | `GET·POST /applications/{id}/scheduled-tasks` · `GET·PATCH·DELETE /scheduled-tasks/{id}` · `GET …/runs` |
| Webhooks | `POST /webhooks/github/{webhook_id}` (push → deploy, pull_request → preview) |

## Security model

The full threat model lives in [docs/security/threat-model.md](docs/security/threat-model.md); it was written before the first line of agent code, and its requirements are enforced by tests. The highlights:

- **No SSH, ever.** Agents dial home outbound-only over mTLS against a pinned CA. A compromised control plane yields shell access to nothing ([ADR-002](docs/adrs/ADR-002-agent-dial-home-no-ssh.md)).
- **Join tokens are single-use and short-lived**; thereafter the agent's identity is its short-lived, revocable certificate. Deleting a server cuts its live connection and refuses reconnection.
- **Per-agent authorization on the bus**: each agent can publish only its own `state.*`/`logs.*` and read only its own work queue — a compromised agent's blast radius is its own server, verified by tests down to the JetStream API grants.
- **No arbitrary-command *verb* on the wire.** Work items describe state to converge on; there is no "run this command on the host" message. A scheduled-task command is the one command-bearing field, and it is deliberately scoped: declarative workload config that the agent runs only inside the app's *own* unprivileged container — no more privilege than deploying an image already grants ([ADR-011](docs/adrs/ADR-011-in-container-scheduled-tasks.md), refining threat-model §8 req 4).
- **Team-scoped authorization** on every project route: the request resolves to its owning team, the caller's ranked role is checked, and a non-member gets 404 (not 403) so resource existence never leaks across tenants. Grants require strictly sufficient rank — no self-service escalation.
- **Secrets sealed at rest** (AES-256-GCM under the master key), masked in all responses, never logged; certificate private keys never leave the node that serves them.
- **Constant-time comparisons** for tokens and webhook HMACs; login rate-limiting; the plane guards its own disk headroom at boot.

## Repository layout

```
core/    control plane (cypherd): REST+console, scheduler, embedded NATS bus,
         sqlc store + migrations, auth, enrollment CA
agent/   data plane (cypher-agent): docker driver + Engine API client, builder,
         Traefik proxy driver, work consumer, health prober, log streamer
pkg/     shared: NATS subject contracts, generated proto, PKI, IDs
proto/   the wire contract (buf-managed; additive-only, no arbitrary-command verbs)
docs/    vision, architecture, ADRs, feature specs, threat model, roadmap
research/ extraction maps + measured baselines from the reference codebases
install/ the curl|sh agent installer (served by the plane)
```

Details and placement rules: [docs/project-structure.md](docs/project-structure.md).

## Development

```sh
make check        # fast pre-commit gate: proto + build + vet + test
make test-race    # what CI runs
make test-store   # store layer against a real throwaway Postgres
make lint         # golangci-lint, per module
make dev-up       # local Postgres via docker compose
```

CI runs on every push: format/lint/race/arm64-cross/proto-compat/generated-drift ([ci.yml](.github/workflows/ci.yml)) plus five integration jobs against real infrastructure — handshake & revocation, the 60-second installer gate, real-Postgres store tests, the full deploy slice (including the zero-dropped-requests check through real Traefik), and agent-outage resilience ([integration.yml](.github/workflows/integration.yml), inventory in [docs/dev/ci.md](docs/dev/ci.md)).

Working on the code? [CLAUDE.md](CLAUDE.md) is the router; [ENGINEERING.md](ENGINEERING.md) is the law — binding rules, not suggestions (consumer-defined interfaces, idempotent reconcilers with required test patterns, additive-only wire/schema changes, secrets never in logs).

## Reading order (the deep dive)

1. [docs/vision.md](docs/vision.md) — why this exists, who it serves, the non-negotiables (with numbers)
2. [docs/architecture.md](docs/architecture.md) — the system design
3. [docs/adrs/](docs/adrs/) — the decisions everything rests on: Go single binary · dial-home agents · embedded NATS · Traefik file provider · desired-state reconciliation · docker-only at launch · no registry required · Apache-2.0 license · agent auto-update · in-container scheduled tasks
4. [docs/features/](docs/features/) — implemented feature specs: [application-deploy](docs/features/application-deploy.md), [routing-and-tls](docs/features/routing-and-tls.md), [managed-databases](docs/features/managed-databases.md), [preview-environments](docs/features/preview-environments.md), [notifications](docs/features/notifications.md), [scheduled-tasks](docs/features/scheduled-tasks.md), [teams-and-roles](docs/features/teams-and-roles.md)
5. [docs/product/feature-matrix.md](docs/product/feature-matrix.md) — the v1 scope contract, extracted from both reference codebases
6. [docs/roadmap.md](docs/roadmap.md) — phases with acceptance gates, open decisions
7. [research/](research/) — extraction maps into the reference sources, measured footprints, community pain points

## License

[Apache-2.0](LICENSE) — the whole repository, no open-core split ([ADR-009](docs/adrs/ADR-009-apache-2-license.md)).

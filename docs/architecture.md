# CypherPanel — Architecture

> Canonical architecture document. Decisions referenced here are recorded in [adrs/](adrs/); terms are defined in [glossary.md](glossary.md); the code layout is specified in [project-structure.md](project-structure.md).

CypherPanel unifies the best features of Coolify and Dokploy while eliminating their architectural gaps: SSH-driven orchestration, heavyweight control planes, orchestrator lock-in, and builds that starve the manager node.

---

## 1. Design Thesis

Coolify and Dokploy fail in mirror-image ways, and the architecture is built around eliminating both failure modes:

| Gap | Coolify | Dokploy | CypherPanel answer |
|---|---|---|---|
| Orchestration coupling | Everything over SSH from a PHP monolith — chatty, fragile, slow at scale | Hard-wired to Docker Swarm — can't do plain Docker cleanly | **Pluggable orchestrator drivers** behind one interface ([ADR-005](adrs/ADR-005-desired-state-reconciliation.md)) |
| Runtime weight | Laravel + Horizon + Redis + Postgres on every install (~2GB+) | Full Next.js server + deployments run *on* the control node | **Thin Go agent on workers; control plane runs nothing user-facing** ([ADR-001](adrs/ADR-001-go-single-binary-control-plane.md)) |
| Build system | Nixpacks/Dockerfile invoked ad-hoc over SSH | Builds executed on the manager node, starving it | **Dedicated build workers with BuildKit, queue-scheduled** |
| State visibility | Polling + SSH scraping | Swarm events only | **Agent-pushed event stream; control plane is a passive subscriber** ([ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md)) |
| Extensibility | Blade/Livewire templates, hard to extend | tRPC monolith, no plugin seam | **Everything internal goes through the same public gRPC/REST API** |

Three principles fall out of this:

1. **Control plane and data plane are separate programs.** The panel never SSHes into servers to do work; it publishes desired state, agents reconcile it. (This is the Kubernetes lesson applied at Docker scale.)
2. **Desired-state reconciliation, not imperative scripts.** A deployment is a record in Postgres; the agent's job is to make reality match it. Retries, drift detection, and crash recovery come free.
3. **The API is the product.** The web UI is just the first API client. No feature exists that isn't reachable via the API — this is what makes CLI, Terraform provider, and CI integrations trivial later.

---

## 2. System Architecture

```
┌────────────────────────── CONTROL PLANE (1 node) ──────────────────────────┐
│                                                                            │
│  ┌──────────┐   ┌───────────────┐   ┌────────────┐   ┌─────────────────┐   │
│  │  Web UI  │──▶│   Core API    │──▶│ PostgreSQL │   │  Job Scheduler  │   │
│  │ (static) │   │ (REST + gRPC) │   │ (state of  │◀──│  (queue, cron,  │   │
│  └──────────┘   │  authn/authz  │   │  record)   │   │  reconciler)    │   │
│                 └───────┬───────┘   └────────────┘   └────────┬────────┘   │
│                         │                                     │            │
│                 ┌───────▼─────────────────────────────────────▼────────┐   │
│                 │        Event Bus (NATS JetStream, embedded)          │   │
│                 └───────▲──────────────────▲──────────────────▲────────┘   │
└─────────────────────────┼──────────────────┼──────────────────┼────────────┘
                   mTLS   │                  │                  │
             ┌────────────┴───┐    ┌─────────┴──────┐   ┌───────┴────────┐
             │  cypher-agent  │    │  cypher-agent  │   │ cypher-builder │
             │  (worker node) │    │  (worker node) │   │ (BuildKit node)│
             │  Docker API    │    │  Swarm/K8s API │   │ image registry │
             │  Traefik cfg   │    │  Traefik cfg   │   │ push           │
             │  logs/metrics  │    │  logs/metrics  │   └────────────────┘
             └────────────────┘    └────────────────┘
```

### Components

**Core API** — the only writer to Postgres. Owns auth (sessions + API tokens + OIDC), RBAC (teams → projects → environments), and the resource model. Exposes REST for humans/UI and gRPC for agents. Validates and persists *desired state*; never talks to Docker directly.

**Job Scheduler / Reconciler** — watches for diffs between desired and observed state, emits work items (build, deploy, backup, cert-renew) onto the event bus with idempotency keys. Handles cron (scheduled backups, health sweeps) and retry policy with backoff.

**Event Bus** — NATS JetStream, embedded in the control-plane binary ([ADR-003](adrs/ADR-003-embedded-nats-jetstream.md)). Three subject families: `work.*` (commands to agents), `state.*` (agent status reports), `logs.*` (streamed build/runtime logs). Durable consumers give at-least-once delivery so an agent that was offline replays what it missed.

**cypher-agent** — a single static Go binary (~20MB) per server. Outbound-only connection to the control plane (mTLS over gRPC/NATS) — meaning **no inbound ports, no SSH keys stored on the panel**, works behind NAT ([ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md)). Responsibilities: reconcile containers against desired state via the local Docker socket, write Traefik dynamic config, stream logs/metrics/events upward, run health checks locally. The agent embeds orchestrator *drivers*: `docker` (standalone), `swarm`, and a future `k8s` — same desired-state schema, different reconcilers.

**cypher-builder** — a role flag on the agent, not a separate program (`cypher-agent --role=builder`; in the Phase 2 slice every docker agent builds its own apps — builder = target, the ADR-008 no-relay case — and the role split lands with multi-server distribution). Dockerfile builds run through the local Docker daemon's build API; Railpack/Nixpacks auto-detected builds and Compose parsing are later matrix rows. Image distribution needs no registry ([ADR-008](adrs/ADR-008-no-registry-required.md)): local when the builder is the target, streamed via mTLS relay through the plane for multi-server, external registries optional. Builds never run on the control plane and never on production workers unless the user opts in — this fixes Dokploy's manager-starvation problem.

**Proxy layer** — Traefik per node, but the agent owns its dynamic configuration through the file provider ([ADR-004](adrs/ADR-004-traefik-file-provider.md)). TLS via Let's Encrypt with DNS-01 support for wildcards. Proxy config generation is a driver too, so Caddy can be offered later (Coolify parity).

### Key Flows

**Deploy:** git webhook → Core API resolves app + branch rules → Scheduler enqueues `work.<builder>.build` → builder clones, builds, streams logs over `logs.*.build.*` → on success Scheduler flips desired state to the new revision and enqueues the rollout; the image is already local when builder = target, otherwise it is relayed over mTLS per [ADR-008](adrs/ADR-008-no-registry-required.md) — no registry push/pull in the path → target agent starts the new container, health-checks it, rewrites Traefik config, drains the old container (zero-downtime by default) → agent reports the observed outcome on `state.<server>.deploy`, and the plane marks the deployment succeeded only from the observed `AppStatus` ([ADR-005](adrs/ADR-005-desired-state-reconciliation.md)).

**Preview environments:** a PR webhook instantiates a templated child of the app's config (subdomain pattern, scaled-down resources, TTL for auto-destroy). Because environments are first-class rows, previews are not a special case.

**Server onboarding:** one `curl | sh` that installs the agent with a join token; agent dials home, presents token, receives mTLS cert. No SSH credential ever stored — this closes Coolify's biggest security liability.

---

## 3. Decision Log

Every load-bearing choice above has an ADR. Do not re-litigate these in implementation discussions — read the ADR; if circumstances changed, write a superseding ADR.

| ADR | Decision |
|---|---|
| [ADR-001](adrs/ADR-001-go-single-binary-control-plane.md) | Go, single-binary control plane |
| [ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md) | Dial-home agent over mTLS; no SSH orchestration |
| [ADR-003](adrs/ADR-003-embedded-nats-jetstream.md) | Embedded NATS JetStream as queue + event bus |
| [ADR-004](adrs/ADR-004-traefik-file-provider.md) | Traefik v3 via file provider, proxy behind driver interface |
| [ADR-005](adrs/ADR-005-desired-state-reconciliation.md) | Desired-state reconciliation, not imperative scripts |
| [ADR-006](adrs/ADR-006-docker-only-at-launch.md) | Standalone `docker` driver only at launch; Swarm fast-follows (V1.x) |
| [ADR-008](adrs/ADR-008-no-registry-required.md) | No registry required: local image / mTLS relay / optional external |

Open questions (candidate ADRs) are tracked in [roadmap.md](roadmap.md).

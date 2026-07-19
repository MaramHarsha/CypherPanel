# CypherPanel

A self-hosted PaaS that unifies the best of **Coolify** (breadth: 361 one-click templates, 8 database engines) and **Dokploy** (polish: rollbacks, volume backups, clean data model) on an architecture neither has: a lightweight Go control plane commanding dial-home agents — **no SSH keys stored, no builds on the panel, desired-state reconciliation throughout.**

> **Status: Phase 2 (deploy vertical slice) in progress.** Phase 1 — the control-plane ↔ agent handshake — is complete: `cypherd` (embedded NATS bus with per-agent mTLS authorization, CA + enrollment, servers API, `curl | sh` installer, interim console) and `cypher-agent` (enroll, dial home, heartbeat, exit-on-revocation) are live, with the handshake, outage reconvergence, revocation, and the 60-second join gate asserted by CI on every push. Phase 2 builds on it: projects/environments/applications with sealed secrets, a desired-state deploy pipeline (scheduler → durable work items → docker driver rollout → Traefik route), git-to-image builds, rollback, GitHub webhooks, and live log streaming — see [docs/features/application-deploy.md](docs/features/application-deploy.md) and [docs/roadmap.md](docs/roadmap.md).

## Why another PaaS?

Coolify and Dokploy fail in mirror-image ways with the same root cause — the control panel does the heavy lifting itself:

| | Coolify | Dokploy | CypherPanel |
|---|---|---|---|
| Orchestration | SSH from the panel (keys stored — fleet-wide liability) | Docker Swarm lock-in | Outbound-only mTLS agents, no stored credentials |
| Footprint | ~2 GB stack (Laravel, Redis, Horizon, Soketi) | ~1 GB measured idle (Node SSR, Redis, BullMQ) | One Go binary + Postgres; < 300 MB budgeted |
| Builds | Ad-hoc over SSH | On the panel's own node | Dedicated builder agents only |
| Deploys | Imperative scripts | Imperative scripts | Desired-state reconciliation — drift repair and rollback by construction |

Full analysis: [docs/architecture.md](docs/architecture.md) · measured baselines: [research/](research/)

## Reading order

1. [docs/vision.md](docs/vision.md) — why this exists, who it serves, the non-negotiables (with numbers)
2. [docs/architecture.md](docs/architecture.md) — the system design
3. [docs/adrs/](docs/adrs/) — the five decisions everything rests on (Go single binary · dial-home agents · embedded NATS · Traefik file provider · desired-state reconciliation)
4. [docs/product/feature-matrix.md](docs/product/feature-matrix.md) — the v1 scope contract, extracted from both reference codebases
5. [docs/roadmap.md](docs/roadmap.md) — phases with acceptance gates, open decisions
6. [research/](research/) — extraction maps into the reference sources, measured footprints, community pain points

Working on the code? [CLAUDE.md](CLAUDE.md) is the router; [ENGINEERING.md](ENGINEERING.md) is the law.

## License

Not yet decided ([ADR-009 pending](docs/roadmap.md)) — all rights reserved until it is.

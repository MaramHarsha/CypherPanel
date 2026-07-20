# CypherPanel

Self-hosted PaaS unifying the best of Coolify and Dokploy on a desired-state, agent-based architecture. Go control plane (`cypherd`) + Go agent (`cypher-agent`) + PostgreSQL; React UI embedded as static assets.

**Current status:** Phase 1 (control-plane ↔ agent handshake) complete — see the evidence note in `docs/roadmap.md`. Phase 2 (deploy vertical slice) is built and proven end-to-end against real Docker + Traefik (integration CI: `deploy`, `deploy-resilience`): git → build → health-gated rollout → routed at its domain through the managed Proxy (`docs/features/routing-and-tls.md`), webhook deploys, SSE logs, rollback, agent-outage reconvergence, revocation — with footprints well inside the vision budgets. Deploy-key private repos (`docs/features/deploy-key-private-repos.md`), bounded runtime-log retention (`docs/features/bounded-log-retention.md`), and the `--role=builder` split + multi-server image relay (`docs/features/builder-role-and-relay.md`, ADR-008 — proven live across two Docker daemons) are implemented. Production Let's Encrypt is validated on a real domain (see the Phase 2 evidence note in `docs/roadmap.md`). With ADR-009 (Apache-2.0 license) and ADR-010 (agent auto-update mechanism) decided 2026-07-20, **Phase 2 is complete**; next up is Phase 3 (state-model breadth) per `docs/roadmap.md`, with ADR-010's implementation landing alongside the release pipeline.

## Hard rules

1. `../coolify` and `../dokploy` are **read-only references**. Port logic and schemas via the extraction maps in `research/` — never copy code. Dokploy is partly proprietary-licensed: check per directory before extracting.
2. Architectural decisions live in `docs/adrs/` and are **not re-litigated** in implementation work. Changed circumstances → write a superseding ADR.
3. Every feature must be expressible as desired state (ADR-005). No imperative "poke the server" paths.
4. No feature ships UI-only — API first, always.
5. Use `docs/glossary.md` vocabulary in code identifiers, API names, and UI copy. "Destination" is banned.
6. Follow `ENGINEERING.md` (code rules) and `docs/product/ui-principles.md` (UI rules) — they are binding, not advisory.
7. Docs: no stub files; one topic, one home; feature specs (`docs/features/`) are written just before implementing, 3–8 pages.

## Read-first map

| Before you… | Read |
|---|---|
| Touch anything architectural | `docs/architecture.md`, then the relevant ADR |
| Question why Go / no SSH / NATS / Traefik / reconciliation | `docs/adrs/ADR-001…005` |
| Implement any feature | Its spec in `docs/features/` (write it first if missing), `docs/product/feature-matrix.md` for scope |
| Port anything from the references | `research/coolify.md` / `research/dokploy.md` extraction maps |
| Weigh feature demand or priority | `research/community-pain-points.md` (evidence-linked community votes) |
| Name anything | `docs/glossary.md` |
| Build UI | `docs/product/ui-principles.md`, `docs/product/personas.md` |
| Decide what's in scope | `docs/vision.md` (non-negotiables incl. footprint budgets), `docs/roadmap.md` |
| Add files/directories | `docs/project-structure.md` |

## Open decisions — do not preempt

ADR-007 (template format) — tracked in `docs/roadmap.md`. Work that would force it must surface the decision instead of assuming it. (Decided: ADR-006 docker-only at launch and ADR-008 no registry required, 2026-07-18; ADR-009 Apache-2.0 and ADR-010 agent auto-update, 2026-07-20; ADR-011 in-container scheduled-task commands as declarative desired state, 2026-07-20.)

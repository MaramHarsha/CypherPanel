# CypherPanel

Self-hosted PaaS unifying the best of Coolify and Dokploy on a desired-state, agent-based architecture. Go control plane (`cypherd`) + Go agent (`cypher-agent`) + PostgreSQL; React UI embedded as static assets.

**Current status:** Phase 1 (control-plane ↔ agent handshake) complete — see the evidence note in `docs/roadmap.md`. Phase 2 (deploy vertical slice) is in progress against `docs/features/application-deploy.md`: the plane pipeline (scheduler, file-backed WORK stream, deploy/rollback/webhook + SSE-log API) and the agent runtime (docker driver + Engine API client, health prober, Traefik writer, builder, log streaming) are built; ADR-006 (docker-only launch) and ADR-008 (no registry) are decided. Remaining before the phase closes: end-to-end runtime proof against a real Docker daemon and the Phase 2 integration-CI job. Every Phase 2 feature still needs its `docs/features/` spec written first.

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

ADR-007 (template format), ADR-009 (license), ADR-010 (agent auto-update mechanism) — tracked in `docs/roadmap.md`. Work that would force one of these must surface the decision instead of assuming it. (ADR-006 and ADR-008 were decided 2026-07-18: docker-only at launch; no registry required.)

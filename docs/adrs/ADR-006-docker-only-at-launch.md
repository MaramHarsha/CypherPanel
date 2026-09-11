# ADR-006: Standalone Docker driver only at v1 launch; Swarm fast-follows

- **Status:** Accepted
- **Date:** 2026-07-18

## Context

The agent's orchestrator drivers (`agent/driver/`, [project-structure.md](../project-structure.md)) are the seam that eliminates the references' orchestration coupling ([architecture.md](../architecture.md)). The open question tracked since Phase 0 ([roadmap.md](../roadmap.md)): does the **Swarm** driver ship at v1 launch alongside `docker`, or fast-follow?

What weighs on it:

- Every Phase 2–4 feature — deploy, health-gated rollout, logs, rollback, scheduled tasks, backups of attached volumes — must be implemented and tested **per driver**. Two drivers at launch roughly doubles the launch-blocking test surface.
- The personas ([product/personas.md](../product/personas.md)) run standalone Docker almost universally: P1 (one or two $5 VPSes), P2 (a handful of servers, per-client isolation), P4 (one home server). Swarm serves a slice of P3 at most.
- Dokploy is Swarm-native — it is simultaneously their headline capability and the lock-in this project exists to avoid ([research/dokploy.md](../../research/dokploy.md) lesson 3: "Swarm assumptions creep").
- The driver interface must not calcify around one implementation. But two other forces already keep it honest: the **proxy** driver seam (ADR-004, Traefik now / Caddy later) and the sanctioned post-v1 **k8s** driver ([vision.md](../vision.md)).

## Decision

**v1 launches with the standalone `docker` driver only.** The Swarm driver is a committed **V1.x fast-follow**, not a cut.

The driver interface is still designed as a multi-driver contract from day one: the `Reconciler` contract in `agent/driver/driver.go` is written against the desired-state schema, never against Docker's API shapes, and project-structure rule 2 (orchestrator-specific logic only inside a `driver/` implementation; a feature needing `if swarm` is a design error) is enforced in review from the first Phase 2 PR.

## Alternatives considered

- **Swarm at launch.** Day-one parity with Dokploy and native replica scaling. Rejected: it doubles the launch test matrix to chase parity on the competitor's turf, when the winning move is being unmatched at what the personas actually run. The feature matrix keeps its honest ⚠️/✅ comparison; parity arrives in V1.x.
- **Drop Swarm entirely.** Rejected: the post-v1 scale-out ladder ([roadmap.md](../roadmap.md)) wants an orchestrator step, Swarm is the community-voted bridge below k8s, and the Dokploy migration path ([feature-matrix](../product/feature-matrix.md) importer row) is materially weaker without it.

## Consequences

- Phase 2–3 build and test once; launch comes sooner and smaller.
- Replica scaling before the Swarm driver lands is limited to what standalone Docker can express (N containers behind the local proxy); the full story waits for V1.x — consistent with the matrix's **Later** row for horizontal scaling.
- The V1.x Swarm work is bounded by construction: implement the `Reconciler` interface (converge + report + drain semantics); the capability inventory to port is already mapped in [research/dokploy.md](../../research/dokploy.md) (`utils/docker/`, `utils/cluster/`).
- Risk accepted: the interface may bias toward Docker despite the rules. Mitigations: the `reconciler-development` skill documents the driver contract as it is built (Phase 2 deliverable), and the k8s driver design review is the second-implementation check before the interface is declared stable.

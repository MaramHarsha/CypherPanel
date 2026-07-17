# ADR-005: Desired-state reconciliation, not imperative scripts

- **Status:** Accepted
- **Date:** 2026-07-17

## Context

Both references deploy imperatively. Coolify's `ApplicationDeploymentJob.php` is a monolithic job that executes a long command sequence over SSH; if it dies mid-way, the server is left in an undefined state that no component is responsible for noticing. Dokploy's deployment services behave similarly on the manager node. Neither system can answer "does reality currently match what the user asked for?" — they can only answer "did the last script exit 0?"

## Decision

CypherPanel is a **desired-state system**. PostgreSQL stores what should be true (app X at image Y with config Z on server S). Agents continuously reconcile their local reality against the desired state relevant to them; the control-plane scheduler diffs desired vs. observed (as reported over `state.*`) and emits idempotent work items for anything an agent can't converge alone (builds, cross-server moves). A "deployment" is a recorded transition between desired revisions, not a script run.

Every reconciler implements the same contract (`agent/driver/driver.go`): given desired state, converge and report. Orchestrator differences (standalone Docker, Swarm, later k8s) live entirely inside driver implementations.

## Alternatives considered

- **Imperative job pipelines (both references).** Familiar and simpler to write initially. Rejected: crash recovery, drift detection, and retries all have to be hand-built per feature, and the failure modes are exactly the ones users report most in both upstream issue trackers.
- **Adopting Kubernetes as the reconciliation engine.** It *is* this model, proven. Rejected: violates the lightweight non-negotiable and the anti-persona in vision.md; a k8s *driver* remains possible later.

## Consequences

- **Every feature must define its desired-state schema before implementation.** A feature that can't express itself as state the agent converges on needs design review first. (Enforced via docs/features specs.)
- Reconcilers must be idempotent — converging twice equals converging once. This is testable and ENGINEERING.md requires those tests.
- Drift (manually stopped container, OOM-killed process) is detected and repaired by the same loop that does deploys — no separate healing subsystem.
- Rollback is cheap by construction: point desired state at the previous revision.
- Discipline cost: "quick fixes" that imperatively poke a server are architecturally forbidden, even when tempting.

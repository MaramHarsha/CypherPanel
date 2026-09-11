---
name: reconciler-development
description: Rules and test patterns for writing or changing any reconciler (agent drivers, scheduler diff loops) — read before touching agent/driver/*, agent/proxy/*, or core/scheduler reconciliation code.
---

# Writing reconcilers

A reconciler converges reality toward desired state and reports what is
actually true (ADR-005). Every reconciler in this codebase — orchestrator
drivers (`agent/driver/*`), the proxy fragment writer (`agent/proxy`), the
scheduler's diff loop — follows the same rules. The driver contract itself is
documented in `agent/driver/driver.go`; the deploy pipeline it serves is
specified in `docs/features/application-deploy.md`.

## The contract

- **Input is the full desired set, not a delta.** Absence means remove. Never
  design a reconciler that needs to be told what changed — it must be able to
  converge a host it has never seen (crash recovery is the same code path as
  a normal deploy).
- **Discover your managed set from labels** (`driver.LabelManaged`,
  `LabelAppID`, `LabelRevisionID`), never from memory. In-memory state is a
  cache, not truth.
- **Report observations, not intentions.** Status comes from inspecting
  reality after convergence (`AppStatus`); "it should be running now" is not
  a reportable state. The plane asserts success only from observations —
  this is what makes Dokploy's stale-container bug impossible here.
- **Idempotent by construction** (ENGINEERING rules 12–13): converging twice
  equals converging once, and redelivery of the same work item is a no-op
  re-convergence. Idempotency keys ride on the work item (`deployment_id`).
- **Partial failure converges the rest.** One broken app must not block the
  others; report per-app `error` state with a secret-free detail string and
  keep going. The returned error is reserved for "orchestrator unreachable".
- **No imperative escape hatches.** If a feature seems to need "just run this
  command on the server", the design is wrong — redesign the desired state
  (CLAUDE.md rule 3; threat-model §5.1 forbids exec verbs on the wire).
- **Zero-downtime is the default sequence**: start new → health-gate → flip
  route (atomic fragment rename) → drain old → stop old. The old revision
  never stops serving until the new one has passed health checks.
- **Deterministic names everywhere.** Networks are `cypher-<environment_id>`,
  containers/fragments derive from app id — never orchestrator auto-naming
  (research/coolify.md lesson 5: the CORS-storm bug).
- **GC is desired-state GC** (threat-model §5.9): prunable = carries our
  labels AND referenced by no current spec or rollback-window revision.
  Never `docker system prune`-style heuristics.

## Required tests (blocking, ENGINEERING rule 13)

Every reconciler ships at minimum, table-driven where variants exist:

1. **Converge-twice** — `Reconcile(desired)` twice; the second call makes zero
   mutating calls (assert via a recording fake of the orchestrator API).
2. **Crash-resume** — converge, discard the reconciler instance, construct a
   fresh one over the same fake state, converge again: no-op.
3. **Absence-removes** — app present, then absent from desired: resources
   removed, labels respected (an unlabeled look-alike container is untouched).
4. **Failed health gate** — new revision never healthy: old revision still
   serving, app reports `error`, new container cleaned up.
5. **Partial failure** — one app's convergence fails: others still converge,
   per-app states correct.

Unit tests run against a fake orchestrator API (consumer-defined interface —
rule 6 — so the fake is cheap). The real-daemon path is integration.yml's job.

## Wire rules

Specs and statuses are the generated protos (`pkg/proto/.../work.pb.go`) —
never parallel Go structs. Changing them: additive only, field numbers are
forever (rule 18, `buf breaking` enforces). New state to report → new field,
never a repurposed one.

# ADR-010: Agent auto-update — desired version, signed artifacts, two-slot swap

- **Status:** Accepted
- **Date:** 2026-07-20

## Context

A fleet of outdated agents is a support nightmare ([roadmap](../roadmap.md)):
every plane feature that needs a newer agent stalls on hosts nobody remembers
to SSH into — and ADR-002 means there is no SSH to reach them with. The
mechanism must fit the existing architecture: agents are outbound-only
(ADR-002), everything is desired state (ADR-005, rule 3), the plane runs no
user workloads and should not become a blob store (ADR-008's discipline), and
a compromised plane must not become fleet-wide code execution (threat-model
§5.1 — the same reason the wire carries no exec verbs).

## Decision

Agent updates are **desired state, converged by the agent itself**:

1. **The plane declares, per server:** desired agent version + checksum, on a
   **channel** (`stable` default, `canary` opt-in per server). Version state
   rides the existing sync/heartbeat plumbing — agents already report their
   running version in every heartbeat, so drift is observable today.
2. **The agent downloads the artifact itself** over outbound HTTPS from the
   release source (GitHub Releases by default; a configurable artifact URL
   for air-gapped fleets). The plane names versions; it never stores or
   relays agent binaries.
3. **Signature over trust-the-plane:** artifacts are signed with an offline
   release key; the agent verifies against a public key baked into its
   binary before swapping. The agent also refuses versions below its
   currently running one unless the desired state explicitly flags a
   rollback. A compromised plane can therefore only choose among genuine
   releases — it cannot inject arbitrary code onto the fleet.
4. **Two-slot swap:** download to a staging path → verify checksum +
   signature → atomically rename the current binary to `.prev` and the new
   one into place → exit 0 and let the init system (systemd unit from the
   installer, `Restart=always`) start the new version.
5. **Self-rollback:** the new process must reach its first successful
   heartbeat to clear a boot marker; a binary that repeatedly fails before
   that point re-execs `.prev` and reports `degraded` with the failed
   version in the detail — an update can strand a host at the old version,
   never off the bus.
6. **Rollout = channel order:** `canary` servers converge first; `stable`
   follows when the operator promotes the release. Percentage-based staged
   rollout is a Later refinement on the same primitive (per-server desired
   version already permits it). **The plane never updates itself** —
   operator-driven, release notes in hand.

Implementation lands with the release pipeline (goreleaser artifacts +
signing), not before it — this ADR fixes the mechanism so the installer's
systemd unit and the state layout are built update-ready from the start.

## Alternatives considered

- **OS package managers (apt/yum).** No fleet coordination, no channels, no
  observed-version feedback, and a second packaging matrix to maintain.
- **Plane pushes the binary over the bus.** Blob transport over NATS (the
  exact failure ADR-008 rejected for images), and it makes the plane a
  binary distributor — the §5.1 blast radius this design refuses.
- **Watchtower-style container image updates.** The agent is a host binary
  by design (it manages the container runtime; it cannot live inside it).
- **No auto-update (manual re-run of the installer).** The status quo that
  produced Coolify/Dokploy's version-skew support burden; rejected by the
  roadmap's own framing.

## Consequences

- `Heartbeat.agent_version` (already shipped) is the observation side;
  desired version/channel become plane state with an API surface when
  implemented.
- The installer's systemd unit and agent state dir are shaped for the
  two-slot layout from day one, so enabling updates later is additive.
- Release engineering must produce signed, checksummed artifacts per
  arch (amd64/arm64) — joins the release checklist alongside ADR-009's
  dependency audit.
- The baked-in release public key rotates via a normal signed update that
  carries the successor key — key rotation needs no side channel.

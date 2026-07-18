# ADR-008: No image registry required at v1

- **Status:** Accepted
- **Date:** 2026-07-18

## Context

Builds run only on builder-role agents ([architecture.md](../architecture.md); vision non-negotiable 5), so built images must somehow reach the servers that run them. The open question ([roadmap.md](../roadmap.md)): ship a built-in registry, require an external one, or neither?

What weighs on it:

- **The dominant case needs no transfer at all.** P1/P4 run one server: the builder role and the target are the same machine, and the built image is already in the local Docker daemon. Requiring a registry here is pure onboarding tax against the "fresh VPS to deployed app < 10 minutes" non-negotiable.
- **The plane must not accumulate user data.** Image blobs on the control plane's disk feed the exact failure class this project is designed against — silent disk exhaustion, the #1 production killer in both references ([threat-model §5.9](../security/threat-model.md), [research/community-pain-points.md](../../research/community-pain-points.md)) — and strain "the control plane never runs user workloads."
- **Agents cannot serve each other.** They are outbound-only with no inbound ports (ADR-002), so a "registry on the builder" is structurally impossible — no other agent could pull from it.
- Reference behavior: Coolify predominantly builds on the server that runs the app; Dokploy requires configuring a registry for multi-node Swarm — a known onboarding cliff.

## Decision

**v1 requires no registry.** Image distribution has three paths, in order of preference:

1. **Local (default, single-server):** builder and target are the same server; the image never leaves the local daemon. Zero transfer, zero config.
2. **Relay (multi-server):** the image is streamed builder → control plane → target over the existing mTLS gRPC channel as a **transient relay** — bounded buffers, never written to control-plane disk.
3. **External registry (optional, day one):** users may configure a registry (GHCR, Docker Hub, Harbor, …); builders push after build, targets pull. This path is required anyway for private base images and for users with existing registry workflows — it is the feature-matrix "Private registries: V1" row, now as an *option* rather than a prerequisite.

A **built-in registry stays a Later candidate**, revisited via a superseding ADR only if the relay path proves insufficient at real fleet sizes.

## Alternatives considered

- **Built-in registry embedded in `cypherd`.** Zero-config multi-server. Rejected: blobs land on the plane's disk (§5.9), the plane grows a high-value attack surface (image tampering = fleet code execution), and it bends "the plane runs no user workloads."
- **Require an external registry.** Simplest to build. Rejected as a *requirement*: it breaks the 10-minute promise with an account signup and a credential to manage before the first multi-server deploy; kept as the optional path 3.
- **Registry served by the builder agent.** Rejected as structurally impossible under ADR-002 (no inbound ports on agents).

## Consequences

- The Phase 2 deploy spec must define the relay stream contract (chunked image transfer over gRPC, resumable on reconnect — same channel discipline as `logs.*`).
- Multi-server transfer cost is plane **bandwidth**, transient and disk-free; acceptable at v1 fleet sizes (one plane, tens of workers), and the external-registry path is the documented relief valve beyond that.
- The single-server path keeps the P1 demo at zero transfer overhead — deploys are as fast as the build.
- Image lifecycle on targets is owned by desired-state GC (§5.9): anything no revision references is prunable — the relay path adds no new cleanup category.

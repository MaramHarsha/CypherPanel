# CypherPanel — Roadmap

> Phases are gates, not dates: a phase is done when its acceptance criteria pass, and the next phase doesn't start early. The v1 feature cut is the "V1" column of [product/feature-matrix.md](product/feature-matrix.md).

## Phase 0 — Knowledge base ✅ (2026-07-17)

Architecture, ADRs 001–005, vision, glossary, personas, feature matrix, engineering handbook, research extraction maps. This documentation set is the ground truth for everything below.

## Phase 1 — Skeleton (control plane ↔ agent handshake) ✅ (2026-07-18)

Proves the hardest architectural claims (ADR-002, ADR-003) before any product feature exists.

Scope: `cypherd` boots (config, migrations, embedded NATS, REST skeleton, auth with a single admin user); `cypher-agent` enrolls via single-use join token, receives mTLS cert, maintains heartbeat; servers visible with live status via API and a minimal UI page.

Deliverables alongside code: `docs/security/threat-model.md` written **before** the first line of agent code (panel compromise, agent compromise blast radius, join-token leak, malicious templates, fork-PR preview secrets); the multi-node CI integration harness ([dev/ci.md](dev/ci.md)); the `reconciler-development` skill in `.claude/skills/` once the driver interface exists *(carried into Phase 2 — the driver interface is a Phase 2 artifact)*.

**Acceptance:** fresh Ubuntu VM joins via `curl | sh` in under 60 seconds. Kill `cypherd` for 5 minutes → agent reconnects, replays missed work, status converges with no manual step. Footprint budgets from [vision.md](vision.md) hold.

**Evidence at closeout:** installer join measured ~1 s wall on fresh `ubuntu:24.04`, tampered CA fingerprint refused (CI job on every push + live run); outage reconvergence + revocation-kick asserted by `integration.yml` on Linux each push (outage compressed to 45 s for CI latency — run the full 5-minute variant on the first real VPS deployment); footprints measured idle at ~31 MB (plane) / ~14 MB (agent) working set against 300/50 MB budgets.

## Phase 2 — Deploy vertical slice

One application, end to end, done properly rather than many features shallowly.

Scope: GitHub repo (public + deploy-key private) → Dockerfile build on a builder-role agent → image distributed per [ADR-008](adrs/ADR-008-no-registry-required.md) (local when builder = target; mTLS relay otherwise; external registry optional) → `docker` driver rollout with health check ([ADR-006](adrs/ADR-006-docker-only-at-launch.md): `docker` is the only launch driver) → Traefik route + Let's Encrypt cert → build/runtime logs streamed to the UI. Push-to-deploy webhook. Rollback to previous revision.

**Acceptance:** git push → new version live with zero dropped requests; kill the agent mid-deploy → reconciler converges on restart; rollback restores the previous revision in seconds; deploy fully drivable via REST API alone.

## Phase 3 — State model breadth

Scope: managed databases (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey); env vars & secrets; scheduled backups to S3-compatible targets with restore; preview environments from PRs (TTL auto-destroy); notifications (Email, Discord, Slack, Telegram); teams + roles; scheduled tasks (cron).

**Acceptance:** each resource type deploys, backs up, restores, and deletes cleanly via API; preview environments create and destroy themselves from PR lifecycle events without manual action.

## Phase 4 — Catalog and polish

Scope: template catalog (port Coolify's 361 compose templates + Dokploy's registry mechanism — see research docs); Compose Stack resources; dashboard; interactive terminal; observability — metrics (port concepts from Dokploy's Go monitoring app), bounded persistent log retention, log drains to external systems, metric threshold alerts; design system documented from the real components (`docs/product/design-system.md` gets written *now*, not before); CLI.

**Acceptance:** a Coolify or Dokploy user can self-migrate a typical workload without losing a capability they used (checked against the feature matrix).

## Post-v1 directions (recorded, not scheduled)

Deliberate **Later** items from the [feature matrix](product/feature-matrix.md), captured so v1 work doesn't preempt or accidentally foreclose them:

- **Scale-out story**, in three steps that each deliver value alone:
  1. Manual replica scaling — run a stateless Application as N replicas across existing servers with load-balanced routing (needs an ingress strategy: cloud LB, DNS, or designated ingress node).
  2. Cloud provider integration — customer connects AWS/GCP/Azure/Hetzner credentials; CypherPanel provisions servers via cloud-init carrying the agent join command. [ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md) makes this nearly free: no SSH key injection, the new server simply dials home. (Coolify has a partial start here: `coolify/app/Services/HetznerService.php`.)
  3. Metric-triggered autoscaling — a controller that edits desired state ([ADR-005](adrs/ADR-005-desired-state-reconciliation.md)) from agent metrics (`state.*`). Hard requirements from day one: scale-in draining, trigger cooldowns/hysteresis to prevent flapping, and a hard cost cap (max servers) since it spends customer money automatically.
- **`k8s` driver targeting existing clusters** — sanctioned by [vision.md](vision.md) (we deploy *onto* Kubernetes, never install or operate it).

## Open decisions (candidate ADRs)

| # | Question | Decide by |
|---|---|---|
| ADR-007 | Template format: extend Coolify's compose-YAML + magic envs, Dokploy's remote registry, or a merged schema | Before Phase 4 starts |
| ADR-009 | License (Apache/MIT vs AGPL vs open-core) — shapes community and monetization from day one; must be cleaner than Dokploy's mixed model | Before the repo goes public |
| ADR-010 | Agent auto-update mechanism (channel, rollout, rollback) — a fleet of outdated agents is a support nightmare | Before first public release (end of Phase 2) |

Decided 2026-07-18, unblocking Phase 2: [ADR-006](adrs/ADR-006-docker-only-at-launch.md) (standalone `docker` driver only at launch; Swarm fast-follows in V1.x) and [ADR-008](adrs/ADR-008-no-registry-required.md) (no registry required: local image on single-server, mTLS relay for multi-server, external registries optional).

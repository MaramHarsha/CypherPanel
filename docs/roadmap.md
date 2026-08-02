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

## Phase 2 — Deploy vertical slice ✅ complete (2026-07-20)

One application, end to end, done properly rather than many features shallowly.

Scope: GitHub repo (public + deploy-key private) → Dockerfile build on a builder-role agent → image distributed per [ADR-008](adrs/ADR-008-no-registry-required.md) (local when builder = target; mTLS relay otherwise; external registry optional) → `docker` driver rollout with health check ([ADR-006](adrs/ADR-006-docker-only-at-launch.md): `docker` is the only launch driver) → Traefik route + Let's Encrypt cert → build/runtime logs streamed to the UI. Push-to-deploy webhook. Rollback to previous revision.

**Acceptance:** git push → new version live with zero dropped requests; kill the agent mid-deploy → reconciler converges on restart; rollback restores the previous revision in seconds; deploy fully drivable via REST API alone.

**Evidence at closeout:** zero-drop rollout, mid-deploy agent kill → reconverge, rollback, and REST-only driving asserted by `integration.yml` (`deploy`, `deploy-resilience`) on every push. Multi-server builder split + relay proven live across two real Docker daemons (target in dind): image built on the builder, relayed through the plane (bounded memory, nothing on plane disk), health-gated and routed on the target; target killed mid-distribute reconverged seconds after restart. Production Let's Encrypt validated 2026-07-20 on a real domain (`cypherpanel.in` → HTTP-01 → `SSL certificate verify ok`, HTTP→HTTPS 301, cert in node-local `acme.json` 0600), private-repo deploy-key clone in the same pipeline run.

## Phase 3 — State model breadth ✅ complete (2026-07-21)

Scope: managed databases (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey); env vars & secrets; scheduled backups to S3-compatible targets with restore; preview environments from PRs (TTL auto-destroy); notifications (Email, Discord, Slack, Telegram); teams + roles; scheduled tasks (cron).

**Acceptance:** each resource type deploys, backs up, restores, and deletes cleanly via API; preview environments create and destroy themselves from PR lifecycle events without manual action. **Met:** managed databases + S3 backups/restore (`managed-databases.md`), preview environments (`preview-environments.md`), notifications (`notifications.md`), scheduled tasks (`scheduled-tasks.md`, ADR-011 — live-verified firing in-container), and teams + roles (`teams-and-roles.md`) all landed, each with real-Postgres store tests and end-to-end verification. Deferred within scope to V1.x: TOTP 2FA, granular RBAC, backup-cron auto-scheduling + S3-object pruning (all recorded on their feature-matrix rows).

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

None open.

Decided 2026-08-02: [ADR-007](adrs/ADR-007-template-format.md) — a native
declarative template schema resolving to ordinary Applications and Managed
Databases, with Coolify's compose+magic-env library reached through an importer
rather than adopted as the native format, and the catalog bundled in the
release rather than fetched at runtime. This unblocks Phase 4's catalog.

Decided 2026-07-18, unblocking Phase 2: [ADR-006](adrs/ADR-006-docker-only-at-launch.md) (standalone `docker` driver only at launch; Swarm fast-follows in V1.x) and [ADR-008](adrs/ADR-008-no-registry-required.md) (no registry required: local image on single-server, mTLS relay for multi-server, external registries optional).

Decided 2026-07-20, closing Phase 2's decision gates:
[ADR-009](adrs/ADR-009-apache-2-license.md) (Apache-2.0 for the whole
repository — no open-core split; trademark is the brand lever) and
[ADR-010](adrs/ADR-010-agent-auto-update.md) (agent auto-update as desired
state: plane declares version+channel, agent pulls the signed artifact,
two-slot swap with self-rollback; implementation lands with the release
pipeline).

[ADR-011](adrs/ADR-011-in-container-scheduled-tasks.md) (in-container
scheduled-task commands are declarative desired state, executed only in the
app's own sandbox — refining threat-model §8 req 4 from "no shell strings" to
"no verb that can execute outside a workload's own sandbox"; unblocks the
Phase 3 scheduled-tasks feature).

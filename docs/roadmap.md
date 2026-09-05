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

**Acceptance:** each resource type deploys, backs up, restores, and deletes cleanly via API; preview environments create and destroy themselves from PR lifecycle events without manual action. **Met:** managed databases + S3 backups/restore (`managed-databases.md`), preview environments (`preview-environments.md`), notifications (`notifications.md`), scheduled tasks (`scheduled-tasks.md`, ADR-011 — live-verified firing in-container), and teams + roles (`teams-and-roles.md`) all landed, each with real-Postgres store tests and end-to-end verification. Deferred within scope to V1.x: granular RBAC (recorded on its feature-matrix row). TOTP 2FA and backup-cron auto-scheduling + S3-object pruning were also deferred at closeout but have since landed (`core/auth/totp*.go`, `core/scheduler/backups.go`).

## Phase 4 — Catalog and polish

Scope: template catalog (a native template schema with a Coolify importer, bundled in the release — see [ADR-007](adrs/ADR-007-template-format.md)); Compose Stack resources; dashboard; interactive terminal; observability — metrics (port concepts from Dokploy's Go monitoring app), bounded persistent log retention, log drains to external systems, metric threshold alerts; design system documented from the real components (`docs/product/design-system.md` gets written *now*, not before); CLI.

**Acceptance:** a Coolify or Dokploy user can self-migrate a typical workload without losing a capability they used (checked against the feature matrix).

**Progress.** The template catalog and its Coolify importer have landed
([template-catalog.md](features/template-catalog.md),
[dev/template-import.md](dev/template-import.md)): 137 bundled templates — 7
hand-curated plus 130 machine-translated from Coolify's compose library, every
image pinned to a digest. The importer refuses 235 of the 365 upstream
templates and records why for each ([the report](dev/template-import-report.md)).
Two blockers there are ours to remove and are the highest-leverage catalog work
left: a **named application database** on Managed Databases (43 templates,
including the WordPress/Ghost/Nextcloud class) and **Compose Stack resources**,
already in this phase's scope, for the ~130 that need a command override, a
host mount, or privileged access.

**Control-plane hardening** has landed alongside it
([control-plane-hardening.md](features/control-plane-hardening.md)): the bus
now grants each agent its own reply-inbox prefix, closing a cross-agent read of
plaintext desired state that a shared `_INBOX.>` grant allowed (threat-model
§5.2 — the one deliberate agent/plane compatibility break, and the fix itself);
every response carries a trace id and a handler panic answers with the ordinary
error envelope; `GET /panel/version` reports the running build and an opt-out
release check that tells owners once per version; `GET /panel/logs` hands an
owner a bounded tail of the panel's own log; sign-in throttling gained a
per-account dimension, an honest `Retry-After`, and a client address that is
only read from `X-Forwarded-For` behind a configured proxy; `CYPHERD_PUBLIC_URL`
puts the right scheme on every link the panel writes to itself; and expired
sessions are finally purged.

**Agent identity and TLS** closes three gaps that were each a half-built
mechanism ([agent-identity-and-tls.md](features/agent-identity-and-tls.md)).
Agent certificates now **renew themselves**: an additive `Renew` RPC over the
mTLS channel the agent already holds, a renewal at two thirds of the
certificate's life with a fresh key pair each time, an atomic on-disk swap, and
— because the TLS stacks resolve the certificate per handshake — no reconnection
and no dropped desired state. Revocation now denies renewal too, so a deleted
server's identity expires instead of running to its `NotAfter`; the threat
model's open `[Phase 1: cert TTL decision]` tag is closed at 90 days with the
reasoning recorded (§5.2). **Let's Encrypt is a panel setting, not a per-host
environment variable**: `GET`/`PUT /api/v1/panel/tls` (owner) carries one ACME
account to every node inside `DesiredState`, and a new `work.<server>.resync`
nudge makes a change land within a reconcile rather than on the next reconnect.
The proxy stopped promising what it could not keep — with no resolver it writes
no `certResolver` and no HTTP→HTTPS redirect, serving plain HTTP instead of
redirecting visitors to a self-signed default certificate — and the plane
reports `Application.tls_state` so the UI can say "serving over HTTP meanwhile"
truthfully. Finally, the **join command completes on a fresh host**:
`install/agent.sh` defaults to the project's latest release asset the way
`install.sh` always has for `cypherd`, and a release panel pins
`CYPHER_AGENT_URL` to its own version so a server joins running the agent that
matches its plane. Verified live against a real plane/agent pair with a short
certificate TTL: the resolver appears on the node within a second of the setting
being saved, the certificate rotates at its logged renewal point onto the
alternate on-disk slot, and the whole rotation costs **zero bus reconnects**
(evidence in [the spec](features/agent-identity-and-tls.md) §9).

**Deploy protection** has landed
([deploy-protection.md](features/deploy-protection.md), feature-matrix
**V1.x**): an Environment can declare **who must approve a deploy there** and
**when deploys are not allowed at all**, and the plane enforces both at the
single point where a Deployment is born — before any work item reaches an
agent. A gated deploy is not a second pipeline; it is the ordinary pipeline
that has not been allowed to start, parked as
`Deployment.status = awaiting_approval` with no work published, no application
status touched and no queue slot held, so `Recover()` needs no new case and a
plane restart mid-approval is indistinguishable from no restart. Freeze windows
are weekly, declared in their own IANA zone, half-open and allowed to wrap the
week; the plane embeds the zone database (`_ "time/tzdata"`) so a static binary
on a bare image can still evaluate one, and a zone it cannot load refuses the
deploy rather than passing it. A refused deploy answers `409` naming the window
and when it lifts — from `POST /applications/{id}/deploy`, from
`POST /deployments/{id}/rollback`, and from the GitHub webhook, so a failed
delivery is diagnosable from the response alone and redeliverable after the
window — and from `POST /templates/{slug}/install`, which deploys and is
therefore rolled back rather than left half-created. **Break glass** is a
30-minute recorded override of the freeze — never of the approval — opened by a
team owner with a required reason. Approve, reject, break glass **and the policy
`PUT` itself** are `sessionOnly`: an API token inherits its owner's role, so a
`deploy`-able CI token could otherwise approve the deploy it had just requested,
and a `write`-able one could send `{require_approval:false, freeze_enabled:false,
windows:[]}` and delete the gate outright — either way it would be decorative
(threat-model §5.8); the deploy routes are unchanged, so CI keeps working and
its deploys park. Preview
environments stay unprotected by construction — freezing them would strand
every open PR.

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

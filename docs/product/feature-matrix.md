# CypherPanel — Feature Matrix

> Extracted from the reference sources on 2026-07-17 (paths verified in `../../coolify` and `../../dokploy`; deeper pointers in [research/](../../research/)). Legend — reference columns: ✅ has it, ⚠️ partial, ❌ no. CypherPanel column: **V1** (launch), **V1.x** (fast-follow), **Later**, **Out** (see [vision.md](../vision.md)).

## Git & build

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| GitHub (App + webhooks) | ✅ | ✅ | **V1** | `coolify/bootstrap/helpers/github.php`; `dokploy/.../utils/providers` |
| GitLab / Gitea / Bitbucket | ✅ | ✅ | **V1.x** | Dokploy: dedicated schema + routers per provider |
| Raw git URL + deploy key | ✅ | ✅ | **V1** | |
| Deploy from container image | ✅ | ✅ | **V1** | |
| Dockerfile builds | ✅ | ✅ | **V1** | BuildKit on builder agents |
| Nixpacks auto-build | ✅ | ✅ | **V1** | `dokploy/.../utils/builders/nixpacks.ts` |
| Railpack | ✅ | ✅ | **V1** | `builders/railpack.ts` |
| Heroku / Paketo buildpacks | ❌ | ✅ | **Later** | `builders/heroku.ts`, `builders/paketo.ts` |
| Static site builds | ✅ | ✅ | **V1** | `builders/static.ts` |
| Drag-and-drop file deploy | ❌ | ✅ | **Later** | `builders/drop.ts` — niche but loved |
| Build on dedicated node | ⚠️ (build server) | ❌ (manager node) | **V1** | Core architectural fix; builder role |
| Multi-arch image builds (build on amd64, run on arm64) | ⚠️ | ❌ | **V1.x** | BuildKit cross-platform; recurring Coolify pain point — and the cheapest servers (Hetzner ARM, Graviton, RPi) are arm64 |
| Watch paths (monorepo triggers) | ⚠️ | ✅ | **V1.x** | `dokploy/.../utils/watch-paths` |

## Deploy lifecycle

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Zero-downtime rolling deploy | ⚠️ | ✅ | **V1** | Default, per vision non-negotiable |
| Rollback to previous image | ⚠️ | ✅ | **V1** | `dokploy/.../db/schema/rollbacks.ts` |
| Preview environments (PR) | ✅ | ✅ | **V1** | `coolify/app/Jobs/ApplicationPullRequestUpdateJob.php`; `schema/preview-deployments.ts` |
| Health checks gate rollout | ⚠️ | ✅ | **V1** | Agent-local checks (ADR-005) |
| Deployment queue/concurrency control | ✅ | ✅ | **V1** | `dokploy/.../queues/concurrency.ts` worth studying |
| Blue/green, canary | ❌ | ❌ | **Later** | Desired-state model makes this natural; not v1 |
| Post-deploy hooks / commands | ✅ | ✅ | **V1.x** | |

## Runtime resources

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Managed PostgreSQL / MySQL / MariaDB / MongoDB / Redis | ✅ | ✅ | **V1** | Coolify `app/Actions/Database/Start*.php`; Dokploy `schema/{postgres,mysql,mariadb,mongo,redis}.ts` |
| Valkey (BSD-licensed Redis fork) | ❌ | ❌ | **V1** | Neither reference has it (verified against their engine lists). Protocol-compatible with Redis 7.2: same desired-state spec, different image — near-zero marginal cost. Recommended default for license-sensitive users; don't oversell "drop-in" (divergence since the fork). |
| ClickHouse, KeyDB, Dragonfly | ✅ | ❌ | **V1.x** | Coolify `StandaloneClickhouse` etc. |
| libSQL | ❌ | ✅ | **Later** | `schema/libsql.ts` |
| Compose Stack resources | ✅ ("Services") | ✅ ("Compose") | **V1** | Terminology per [glossary.md](../glossary.md) |
| One-click template catalog | ✅ (361 templates) | ✅ (remote registry) | **V1** (subset) → full in Phase 4 | `coolify/templates/compose/`; ADR-007 pending |
| Volumes / mounts | ✅ | ✅ | **V1** | `dokploy/.../schema/mount.ts` |
| Per-resource CPU/memory limits | ✅ | ✅ | **V1** | Noisy-neighbor control on shared servers |
| DB backups → S3, scheduled | ✅ | ✅ | **V1** | `coolify/app/Jobs/DatabaseBackupJob.php`; `dokploy/.../utils/backups` |
| Volume backups | ⚠️ | ✅ | **V1.x** | `dokploy/.../utils/volume-backups` — differentiator worth keeping |
| Backup restore (in-panel) | ⚠️ | ✅ | **V1** | `dokploy/.../utils/restore` — backups without tested restore fail P2 (Alex) |
| Private registries | ✅ | ✅ | **V1** | `schema/registry.ts`; ADR-008 pending for built-in registry |
| Scheduled tasks (cron in containers) | ✅ | ✅ | **V1** | `coolify/app/Jobs/ScheduledTaskJob.php`; `schema/schedule.ts` |
| GPU support | ❌ | ⚠️ | **Out** (v1) | `dokploy/.../utils/gpu-setup.ts` |

## Networking & TLS

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Custom domains per resource | ✅ | ✅ | **V1** | |
| Auto Let's Encrypt (HTTP-01) | ✅ | ✅ | **V1** | |
| Wildcard certs (DNS-01) | ⚠️ | ⚠️ | **V1.x** | Key for P4 (Hendrik) |
| Custom/user certificates | ✅ | ✅ | **V1.x** | `dokploy/.../schema/certificate.ts` |
| Redirects & middleware | ⚠️ | ✅ | **V1.x** | `schema/redirects.ts`, `schema/forward-auth.ts` |
| TCP/UDP port exposure | ✅ | ✅ | **V1** | `schema/port.ts` |
| Cloudflare DNS automation (auto-create records on domain add) | ❌ | ❌ | **V1.x** | Same API token unlocks DNS-01 wildcard certs — one credential, two features |
| Cloudflare CDN/proxy mode (trusted headers, origin lockdown) | ❌ | ❌ | **V1.x** | Agent applies Traefik hardening automatically (ADR-004); HTTP/S only — raw TCP ports stay direct |
| Cloudflare Tunnel (public traffic, zero inbound ports) | ⚠️ (manual guides) | ❌ | **Later** | Rhymes with the dial-home agent (ADR-002); transformative for P4 behind CGNAT |

## Platform & operations

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Multi-server | ✅ (SSH) | ✅ (Swarm) | **V1** (agents) | The core differentiator — ADR-002 |
| Docker Swarm | ⚠️ | ✅ | **ADR-006 pending** | `dokploy/.../routers/swarm.ts` |
| Horizontal replica scaling | ❌ | ⚠️ (Swarm replicas, manual) | **Later** | Stateless apps only; needs ingress strategy — see roadmap post-v1 |
| Cloud provider server provisioning | ⚠️ (partial) | ❌ | **Later** | `coolify/app/Services/HetznerService.php`; join-token enrollment (ADR-002) makes this cheap |
| Metric-triggered autoscaling | ❌ | ❌ | **Later** | Desired-state controller (ADR-005) + agent metrics; cooldowns and cost caps required |
| Agent-based, no SSH keys stored | ⚠️ (Sentinel, metrics only) | ❌ | **V1** | Coolify Sentinel validates the direction |
| Live container logs | ✅ | ✅ | **V1** | |
| Persistent log retention (bounded, survives crashes/restarts) | ❌ | ❌ | **V1.x** | Logs stream off-box via `logs.*` (ADR-003) as they happen; defined window (e.g. 7 days / N MB per resource), searchable. Fixes the "crashed at 3am, logs gone" gap both references have |
| Log drains to external systems (Loki, Axiom, syslog, CloudWatch…) | ❌ | ❌ | **V1.x** | Heroku-proven pattern: agent already tails everything, sinks are cheap; long retention and complex queries stay off-platform (vision.md footprint budgets) |
| Metric threshold alerts | ⚠️ (disk/server checks) | ✅ | **V1.x** | `dokploy/.../utils/notifications/server-threshold.ts`; ours routes through `core/notify` channels |
| Interactive terminal | ✅ | ✅ | **V1.x** | Both use xterm + websockets; ours via gRPC stream |
| Server metrics / monitoring | ✅ (Sentinel) | ✅ (Go monitoring app) | **V1.x** | `dokploy/apps/monitoring` — already Go, port concepts |
| Disk cleanup automation | ✅ | ✅ | **V1.x** | `coolify/app/Jobs/DockerCleanupJob.php` |
| Notifications: Email/Discord/Slack/Telegram | ✅ (+Pushover) | ✅ (+Gotify) | **V1** (these 4) | `coolify/app/Jobs/SendMessageTo*Job.php`; `dokploy/.../utils/notifications` |
| Auto-update of the platform | ✅ | ✅ | **V1.x** | Agent self-update channel is the hard part |
| Panel-level backup/restore | ⚠️ | ✅ | **V1.x** | Control plane = 1 binary + pg_dump makes this easy |
| ARM64 servers | ⚠️ | ⚠️ | **V1** | Agent ships linux/arm64 from day one (tech-stack.md) |
| Server OS update checks / patching | ✅ | ❌ | **Later** | `coolify/app/Jobs/ServerPatchCheckJob.php` |
| Migration importer from Coolify / Dokploy | ❌ | ❌ | **V1.x** | Read their Postgres schemas (already mapped in `research/`) and recreate as desired state — the single biggest adoption lever |

## Collaboration & API

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Teams / multi-tenancy | ✅ | ✅ (Organizations) | **V1** | |
| Roles / permissions | ✅ (Member/Admin/Owner) | ✅ (granular) | **V1** (simple roles) | Granular RBAC **V1.x** |
| REST API + OpenAPI spec | ✅ | ✅ | **V1** | Both ship `openapi.json`; ours is spec-first |
| API tokens with scoped abilities | ✅ (read/write/deploy) | ✅ | **V1** | Coolify's ability model is worth copying |
| Two-factor authentication (TOTP + recovery codes) | ✅ | ✅ | **V1** | Panel compromise = fleet control; account security is not optional here |
| Login rate limiting & session management | ✅ | ⚠️ | **V1** | Brute-force protection, session revocation; threat-model deliverable (roadmap Phase 1) |
| Audit log | ⚠️ | ✅ | **V1.x** | `dokploy/.../schema/audit-log.ts` |
| SSO / OIDC | ⚠️ | ✅ (+SCIM, proprietary) | **Later** | License caution — see [research/dokploy.md](../../research/dokploy.md) |
| CLI | ⚠️ | ❌ | **V1.x** | Generated from OpenAPI |
| Terraform provider | ❌ | ❌ | **Later** | Enabled by API-first |
| AI features | ⚠️ | ✅ | **Out** (v1) | `dokploy/.../schema/ai.ts`; not our fight |
| Localization (i18n) | ❌ | ✅ | **Later** | Dokploy ships multiple languages; English-only is a quiet adoption ceiling — deliberate deferral, not oversight |

## Summary of deliberate gaps at v1

No Swarm decision yet (ADR-006), no buildpacks, no GPU, no SSO, no AI, no replica scaling / cloud provisioning / autoscaling (recorded as post-v1 in [roadmap.md](../roadmap.md)), simple roles only, template subset only, English-only at launch, no OS patching. Every **V1** row above is otherwise a launch blocker — this table *is* the v1 scope contract referenced by [roadmap.md](../roadmap.md).

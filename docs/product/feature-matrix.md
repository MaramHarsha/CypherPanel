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
| Verbose build logs streamed by default | ⚠️ (needs `BUILDKIT_PROGRESS=plain`) | ⚠️ | **V1** | Silent build failures are a top Reddit complaint; no hidden verbosity toggles |
| Framework build presets (`.dockerignore`, Next.js standalone, memory caps) | ❌ | ❌ | **V1.x** | Heavy Next.js/Nuxt builds crash modest hosts today; presets + build resource caps prevent it before it happens |
| Watch paths (monorepo triggers) | ⚠️ | ✅ | **V1.x** | `dokploy/.../utils/watch-paths` |

## Deploy lifecycle

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Zero-downtime rolling deploy | ⚠️ | ⚠️ (stale-container reports) | **V1** | Default, per vision non-negotiable. "Success" is only reported when *observed* state confirms the new revision serving and the old drained — Dokploy's months-unresolved stale-container bug is definitionally impossible under ADR-005 |
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
| Private registries | ✅ | ✅ | **V1** (optional, never required) | `schema/registry.ts`; [ADR-008](../adrs/ADR-008-no-registry-required.md): no registry required — local image / mTLS relay / external optional |
| Scheduled tasks (cron in containers) | ✅ | ✅ | **V1** | `coolify/app/Jobs/ScheduledTaskJob.php`; `schema/schedule.ts` |
| Shared variables (project / environment scope) | ✅ | ✅ | **V1** | Coolify `app/Models/SharedEnvironmentVariable.php` with `{{scope.KEY}}`; Dokploy `project.env`/`environment.env` resolved by `prepareEnvironmentVariables` with `project.${ref}`. Ours seals the value and refuses an unresolved reference rather than shipping the literal — the one behaviour deliberately not ported ([shared-variables.md](../features/shared-variables.md) §3) |
| GPU support | ❌ | ⚠️ | **Out** (v1) | `dokploy/.../utils/gpu-setup.ts` |

## Networking & TLS

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Custom domains per resource | ✅ | ✅ | **V1** | |
| Auto Let's Encrypt (HTTP-01) | ✅ | ✅ | **V1** | One panel-wide ACME account (`PUT /panel/tls`, owner) carried to every node as desired state, so nothing is configured per host ([agent-identity-and-tls.md](../features/agent-identity-and-tls.md) §4). With no account configured a routed app is served over plain HTTP and the API says so (`tls_state: http_only_no_resolver`) rather than pointing routes at a resolver that does not exist |
| Wildcard certs (DNS-01) | ⚠️ | ⚠️ | **V1.x** | Key for P4 (Hendrik) |
| Custom/user certificates | ✅ | ✅ | **V1.x** | `dokploy/.../schema/certificate.ts` |
| Redirects & middleware | ⚠️ | ✅ | **V1.x** | `schema/redirects.ts`, `schema/forward-auth.ts` |
| TCP/UDP port exposure | ✅ | ✅ | **V1** | `schema/port.ts` |
| Cloudflare DNS automation (auto-create records on domain add) | ❌ | ⚠️ (manual only) | **V1** | Dokploy ships a DNS provider abstraction (`utils/dns/{cloudflare,route53}.ts`, `services/dns-provider.ts`) but never wires it to a domain: records are operator-driven CRUD, so nothing is created when a domain is added or reaped when it is removed. Coolify's Cloudflare support is Tunnel/`cloudflared` only. Ours additionally gates routing on ownership — a domain outside a connected Zone is not published ([dns-automation.md](../features/dns-automation.md)). Same API token unlocks DNS-01 wildcard certs — one credential, two features |
| Cloudflare CDN/proxy mode (trusted headers, origin lockdown) | ❌ | ❌ | **V1.x** | Agent applies Traefik hardening automatically (ADR-004); HTTP/S only — raw TCP ports stay direct |
| Cloudflare Tunnel (public traffic, zero inbound ports) | ⚠️ (manual guides) | ❌ | **Later** | Rhymes with the dial-home agent (ADR-002); transformative for P4 behind CGNAT |

## Platform & operations

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Multi-server | ✅ (SSH) | ✅ (Swarm) | **V1** (agents) | The core differentiator — ADR-002 |
| Docker Swarm | ⚠️ | ✅ | **V1.x** | [ADR-006](../adrs/ADR-006-docker-only-at-launch.md): `docker` only at launch; Swarm driver fast-follows. `dokploy/.../routers/swarm.ts` |
| Horizontal replica scaling | ❌ | ⚠️ (Swarm replicas, manual) | **Later** | Stateless apps only; needs ingress strategy — see roadmap post-v1 |
| Cloud provider server provisioning | ⚠️ (partial) | ❌ | **Later** | `coolify/app/Services/HetznerService.php`; join-token enrollment (ADR-002) makes this cheap |
| Metric-triggered autoscaling | ❌ | ❌ | **Later** | Desired-state controller (ADR-005) + agent metrics; cooldowns and cost caps required |
| Agent-based, no SSH keys stored | ⚠️ (Sentinel, metrics only) | ❌ | **V1** | Coolify Sentinel validates the direction |
| Agent identity renews itself (no re-enrollment, no expiry cliff) | n/a (SSH keys, no expiry) | n/a (Swarm join tokens) | **V1** | Neither reference has the problem because neither has short-lived agent identities. Ours does, so it also has the renewal: a `Renew` RPC over the mTLS channel the agent already holds, at two thirds of the certificate's life, fresh key each time, atomic on-disk swap and no reconnection ([agent-identity-and-tls.md](../features/agent-identity-and-tls.md) §3) |
| Live container logs | ✅ | ✅ | **V1** | |
| Persistent log retention (bounded, survives crashes/restarts) | ❌ | ❌ | **V1.x** | Logs stream off-box via `logs.*` (ADR-003) as they happen; defined window (e.g. 7 days / N MB per resource), searchable. Fixes the "crashed at 3am, logs gone" gap both references have |
| Log drains to external systems (Loki, Axiom, syslog, CloudWatch…) | ❌ | ❌ | **V1.x** | Heroku-proven pattern: agent already tails everything, sinks are cheap; long retention and complex queries stay off-platform (vision.md footprint budgets) |
| Metric threshold alerts | ⚠️ (disk/server checks) | ✅ | **V1.x** | `dokploy/.../utils/notifications/server-threshold.ts`; ours routes through `core/notify` channels |
| Interactive terminal | ✅ | ✅ | **V1.x** | Both use xterm + websockets; ours via gRPC stream |
| Server metrics / monitoring | ✅ (Sentinel) | ✅ (Go monitoring app) | **V1.x** | `dokploy/apps/monitoring` — already Go, port concepts |
| Proactive disk management (threshold alerts, auto-pruning policies, desired-state GC) | ⚠️ (cleanup exists; outages still common) | ⚠️ (same) | **V1** | **Reddit's #1 production killer for both tools** — silent disk fill until the panel itself crashes. Desired state makes GC principled: prune anything not referenced. Control plane reserves headroom for its own DB. `coolify/app/Jobs/DockerCleanupJob.php` shows the insufficient version |
| Notifications: Email/Discord/Slack/Telegram | ✅ (+Pushover) | ✅ (+Gotify) | **V1** (these 4) | `coolify/app/Jobs/SendMessageTo*Job.php`; `dokploy/.../utils/notifications` |
| Auto-update of the platform (safe-by-design) | ⚠️ (breaks: #3687, #7193, #7599) | ✅ | **V1.x** | The community's #1 trust wound in Coolify — bricked panels, lost encryption keys. ADR-010 scope: pre-update snapshot, atomic apply + health-verified rollback, update lock; see [research/community-pain-points.md](../../research/community-pain-points.md) |
| Panel-level backup/restore | ⚠️ | ✅ | **V1.x** | Control plane = 1 binary + pg_dump makes this easy |
| ARM64 servers | ⚠️ | ⚠️ | **V1** | Agent ships linux/arm64 from day one (tech-stack.md) |
| Server OS update checks / patching | ✅ | ❌ | **Later** | `coolify/app/Jobs/ServerPatchCheckJob.php` |
| Migration importer from Coolify / Dokploy | ❌ | ❌ | **V1.x** | Read their Postgres schemas (already mapped in `research/`) and recreate as desired state — the single biggest adoption lever. Community explicitly wants **in-place adoption of running containers without downtime** (dokploy#3098) — the harder, more valuable form |
| Move a resource between servers | ❌ | ❌ (top-voted request) | **V1.x** | Nearly free under desired state: reassign server → reconcile → migrate volumes |
| IPv6 servers & dual-stack routing | ❌ (top-voted bug #2484) | ⚠️ | **V1.x** | IPv6-only VPSes are the cheapest machines — P1 territory |
| Advanced Docker passthrough (labels, host network, extra options) | ⚠️ (top-voted pain: #2549, #1092) | ⚠️ | **V1.x** | Escape hatches that survive panel management instead of fighting it |
| External secret managers (Vault, Infisical, Doppler) | ❌ | ❌ (requested) | **Later** | Same bring-your-token pattern as cloud providers |
| Guided onboarding wizard (finish line = first deployed app) | ✅ (7-step "Boarding") | ⚠️ (register → empty dashboard) | **V1** | Ours is 4 steps: welcome → add server (join command or "use this machine") → deploy app/template → live URL. ADR-002 deletes Coolify's SSH-key steps |

## Collaboration & API

| Feature | Coolify | Dokploy | CypherPanel | Evidence / notes |
|---|---|---|---|---|
| Teams / multi-tenancy | ✅ | ✅ (Organizations) | **V1** | |
| Roles / permissions | ✅ (Member/Admin/Owner) | ✅ (granular) | **V1** (simple roles) | Granular RBAC **V1.x** |
| REST API + OpenAPI spec | ✅ | ✅ | **V1** | Both ship `openapi.json`; ours is spec-first |
| API tokens with scoped abilities | ✅ (read/write/deploy) | ✅ | **V1** | Coolify's ability model is worth copying |
| Outbound webhooks (signed, retried, replayable) | ❌ | ❌ | **V1** | Neither reference has one: Coolify's `app/Notifications/Channels/` is Discord/Email/Pushover/Slack/Telegram and Dokploy's `utils/notifications/` likewise — all human channels. Ours is the machine-facing twin of a Notifier: HMAC-SHA256 over `timestamp.body`, bounded retries, a readable per-attempt log ([outbound-webhooks.md](../features/outbound-webhooks.md)) |
| Two-factor authentication (TOTP + recovery codes) | ✅ | ✅ | **V1** | Panel compromise = fleet control; account security is not optional here |
| Login rate limiting & session management | ✅ | ⚠️ | **V1** | Brute-force protection, session revocation; threat-model deliverable (roadmap Phase 1). Throttled per client address *and* per account, so one attacker behind a shared proxy cannot lock everyone out; `429` carries `Retry-After`; expired sessions are purged rather than merely ignored ([control-plane-hardening.md](../features/control-plane-hardening.md) §§5, 7) |
| Panel version, update check & diagnostics | ⚠️ | ⚠️ | **V1** | `GET /panel/version` reports the running build and any newer release (opt-out feed check, once-per-version inbox item to owners; the panel never updates itself — [ADR-010](../adrs/ADR-010-agent-auto-update.md)). Every response carries `X-Request-Id`, repeated as `trace_id` in every error body, and `GET /panel/logs` hands an owner a bounded tail of the panel's own log — what a bug report needs, without shell access ([control-plane-hardening.md](../features/control-plane-hardening.md) §§2–4) |
| Deploy protection (approvals + freeze windows) | ❌ | ❌ | **V1.x** | Neither reference gates a deploy on a person or a clock. An Environment declares who must approve and when deploys are refused; the plane enforces both at the single point where a Deployment is born, so a gated deploy is the ordinary pipeline that has not been allowed to start — no second pipeline, no agent change. Break glass is a 30-minute recorded override of the freeze, never of the approval ([deploy-protection.md](../features/deploy-protection.md)) |
| Audit log | ⚠️ | ✅ | **V1.x** | `dokploy/.../schema/audit-log.ts`. Approval decisions and break-glass grants are first-class rows in deploy protection, not log lines, so they do not wait for this |
| SSO / OIDC | ⚠️ | ✅ (+SCIM, proprietary) | **Later** | License caution — see [research/dokploy.md](../../research/dokploy.md) |
| CLI | ⚠️ | ❌ | **V1.x** | Generated from OpenAPI |
| Terraform provider | ❌ | ❌ | **Later** | Enabled by API-first |
| AI features | ⚠️ | ✅ | **Out** (v1) | `dokploy/.../schema/ai.ts`; not our fight |
| Localization (i18n) | ❌ | ✅ | **Later** | Dokploy ships multiple languages; English-only is a quiet adoption ceiling — deliberate deferral, not oversight |

## Summary of deliberate gaps at v1

No Swarm at launch ([ADR-006](../adrs/ADR-006-docker-only-at-launch.md) — V1.x fast-follow), no buildpacks, no GPU, no SSO, no AI, no replica scaling / cloud provisioning / autoscaling (recorded as post-v1 in [roadmap.md](../roadmap.md)), simple roles only, template subset only, English-only at launch, no OS patching. Every **V1** row above is otherwise a launch blocker — this table *is* the v1 scope contract referenced by [roadmap.md](../roadmap.md).

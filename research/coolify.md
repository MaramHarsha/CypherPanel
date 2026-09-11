# Research: Coolify — Extraction Map

> Purpose: **where to look in `../coolify` when porting a capability, and what verdict to apply**. We extract logic, schemas, and edge-case knowledge — never code (ADR-001). The reference repo is read-only. Verified against the source on 2026-07-17.

## Stack (for orientation while reading)

Laravel 12 on the Laravel-10 file structure, PHP 8.4+, Livewire 3 + Alpine.js, Tailwind v4, PostgreSQL 15, Redis 7 + Horizon (queues), Soketi (websockets), Pest (tests). Domain logic lives in `app/Actions/` (laravel-actions pattern), `app/Jobs/`, and — beware — global helper functions in `bootstrap/helpers/*.php`.

## Architecture in one paragraph

The control plane does everything itself, over SSH: deployment, proxy config, health checks, cleanup — all shell commands executed remotely from queue jobs, with SSH connection multiplexing as a performance crutch. Organizational model: Team → Project → Environment → resources, with Traefik (or Caddy) per server. Its strengths are breadth (templates, 8 database engines, notification channels) and battle-tested edge-case handling; its weakness is that the SSH orchestration model contaminates every feature.

## Extraction map

| Area | Where in `../coolify` | Verdict | Notes |
|---|---|---|---|
| Deployment lifecycle | `app/Jobs/ApplicationDeploymentJob.php` | **Adapt** | Monolithic (thousands of lines) but encodes the complete stage sequence: clone → build-pack selection → env injection → container config generation → rollout → proxy update. Extract the *stages and edge cases*, re-express as work items + reconciler (ADR-005). |
| Build-pack selection | `app/Enums/` (`BuildPackTypes`: nixpacks, static, dockerfile, dockercompose) | **Adapt** | Detection heuristics worth mining from the deployment job. |
| SSH layer | `app/Helpers/SshMultiplexingHelper.php`, `SshRetryHandler.php`, `bootstrap/helpers/remoteProcess.php`, `app/Jobs/CleanupStaleMultiplexedConnections.php` | **Avoid** | The anti-pattern ADR-002 exists to kill. Read once to appreciate the retry/mux/cleanup tax; port nothing. |
| Sentinel (push metrics agent) | `app/Jobs/CheckAndStartSentinelJob.php`, `app/Jobs/PushServerUpdateJob.php` | **Study closely** | Coolify's own admission that SSH polling doesn't scale — a proto-cypher-agent. Their push payload shape is a useful reference for our `state.*` subjects. |
| Proxy management | `app/Actions/Proxy/*.php` (Check/Get/Save/Start/Stop), `bootstrap/helpers/proxy.php` | **Adapt** | Config generation logic → `agent/proxy/`. Note dual Traefik/Caddy support — our proxy driver seam (ADR-004). |
| Template catalog | `templates/compose/` (361 YAML files), `templates/service-templates-latest.json`, `app/Jobs/PullTemplatesFromCDN.php` | **Port** | The crown jewels — mechanically portable data. The magic-env conventions (`SERVICE_FQDN_*`, `SERVICE_PASSWORD_*`, generated per instantiation) live in `bootstrap/helpers/services.php` + `parsers.php`; that convention is the real thing to port. Feeds ADR-007. |
| Managed databases | `app/Actions/Database/Start*.php` + `app/Models/Standalone{Postgresql,Mysql,Mariadb,Mongodb,Redis,Keydb,Dragonfly,Clickhouse}.php` | **Adapt** | Per-engine container specs (images, env, volumes, healthchecks) → our desired-state schemas. 8 engines; also `StartDatabaseProxy` for public TCP exposure. |
| Backups | `app/Jobs/DatabaseBackupJob.php`, S3 storage models | **Adapt** | Per-engine dump commands + retention logic. |
| Notifications | `app/Notifications/` (+ `Channels/`), `app/Jobs/SendMessageTo{Discord,Slack,Telegram,Pushover}Job.php` | **Port logic** | Event catalog × channel matrix → `core/notify`. Note per-team notification settings auto-init. |
| Git & preview environments | `bootstrap/helpers/github.php`, `app/Jobs/ProcessGithubPullRequestWebhook.php`, `ApplicationPullRequestUpdateJob.php`, `CleanupOrphanedPreviewContainersJob.php` | **Adapt** | The orphan-cleanup job is a lesson: previews leak unless lifecycle is owned by state, not events (our TTL model). |
| Server health & ops | `app/Jobs/Server{Check,ConnectionCheck,StorageCheck,LimitCheck,Manager}Job.php`, `DockerCleanupJob.php` | **Adapt, inverted** | They poll from the center; we run the same checks agent-local and push. The *check content* (disk thresholds, container states) is the extractable part. |
| API design | `openapi.yaml`, `routes/api.php`, `app/Http/Controllers/Api/` | **Adapt** | Two ideas worth copying: Sanctum token *abilities* (`read`/`write`/`deploy`) and the `ApiSensitiveData` middleware that masks credentials in responses. |
| AuthZ model | Policies + `Role` enum (MEMBER < ADMIN < OWNER with rank comparison), Teams | **Adapt** | Matches our v1 "simple roles" scope. |
| Scheduled tasks | `app/Jobs/ScheduledTaskJob.php`, `ScheduledJobManager.php` | **Adapt** | Cron-in-container semantics. |
| SSL | `app/Helpers/SslHelper.php`, `app/Jobs/RegenerateSslCertJob.php`, `SslExpirationNotification.php` | **Adapt** | Expiry notification lead times are tuned by real-world pain. |
| Cloud/billing | `app/Jobs/Stripe*.php`, `app/Actions/Stripe/` | **Ignore** | Out of scope; also a cautionary tale — billing code threads through core (vision.md forbids this). |

## Lessons (what their issues taught them, so we don't relearn)

1. **Polling is a tax that compounds** — count the `*CheckJob` classes; each exists because the center can't see the edge. Push-based state (`state.*`) eliminates the category.
2. **The monolithic deploy job is untestable** — stage-by-stage work items with recorded transitions are the fix, and also what makes our UI's live progress honest.
3. **Global helper functions** (`bootstrap/helpers/`) made logic reachable from everywhere and ownable by no one — our ENGINEERING.md package rules exist because of this.
4. **Template magic-envs are the best idea in the codebase** — a template author writes `SERVICE_PASSWORD_MYAPP` and the platform generates and wires the secret. Keep this UX exactly.
5. **Name every network explicitly.** A long-standing field bug (Reddit-reported): the proxy loses track of which Docker network to attach under load → CORS errors recurring for hours, fixed only by explicitly naming the network. Our agent creates all networks with deterministic names and references them by name everywhere — never Docker auto-naming. See [community-pain-points.md](community-pain-points.md).

## License

Apache-2.0 (`LICENSE` at repo root) — logic extraction is unproblematic; we still don't copy code (ADR-001).

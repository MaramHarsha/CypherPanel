# CypherPanel — Task Tracker

> Living document: update whenever a task starts or finishes. `[x]` done · `[~]` in progress · `[ ]` pending.
> Phases follow `plan.md` Section 9 (Implementation Roadmap).
>
> **Agent skills:** the full 23-skill catalog (`.claude/skills/`) was written upfront (2026-07) at the user's request, overriding the plan's write-at-end-of-phase timing. Skills for not-yet-built phases carry a `Status: design-intent` line and must be re-verified against real code when their phase lands (updated in the same PR). Read the relevant SKILL.md before writing code in any area.

## Progress at a Glance

| Phase | Scope | Status |
|-------|-------|--------|
| **1** | Core Foundation & Agent Comms | ✅ Complete (2 minor deploy-time items deferred) |
| **2** | Admin Plane & Provisioning + UI Shell | 🟨 In progress (UI shell ✅) |
| **3** | Web Server & PHP Management | ⬜ Not started |
| **4** | Files, FTP, & Databases | ⬜ Not started |
| **5** | Email & DNS Servers | ⬜ Not started |
| **6** | Logging, Auditing, & Hardening | ⬜ Not started |
| — | Post-MVP (`upcoming-features.md`) | ⬜ After MVP |
| — | Extensibility, SDKs & Ops (`plan.md` §11-19) | ⬜ Reservations start Phase 2; most items post-MVP — see below |

---

## Phase 0 — Planning & Repo Setup ✅

- [x] `plan.md` architecture doc (stack, MVP defaults, auth model, scale targets, UI direction, portability rules)
- [x] `upcoming-features.md` post-MVP roadmap (incl. deferred adapters, differentiators)
- [x] cPanel installer analysis → `plan.md` Appendix A (2GB RAM floor, private Perl runtime, anti-patterns to avoid)
- [x] Monorepo scaffold: `cmd/core`, `cmd/agent`, `internal/`, `proto/`, `migrations/`
- [x] Repo hygiene: `.gitattributes` (LF enforcement), `.editorconfig`, `.gitignore`, `.env.example`
- [x] Apache-2.0 LICENSE, README
- [x] Makefile: Linux cross-compile (amd64+arm64, CGO off), test/vet/proto/migrate targets
- [x] `docker-compose.yml` dev stack: PostgreSQL 17 + Redis 8 + NATS JetStream (verified healthy)
- [x] Go module renamed to `github.com/MaramHarsha/CypherPanel`
- [ ] Initial git commit & push to GitHub *(waiting on user go-ahead)*

## Phase 1 — Core Foundation & Agent Comms ✅

### Done
- [x] Central env-driven config package (`internal/config`) — no hardcoded endpoints/secrets; prod-mode hard requirements
- [x] Distro path-mapping layer (`internal/paths`) — Debian/RHEL layouts, `CYPHER_PATH_*` overrides, unit-tested
- [x] Platform abstraction (`internal/platform`) — Linux syscall code behind interfaces, `_linux.go` build tags, stub for dev machines
- [x] Auth foundation (`internal/auth`): Argon2id (PHC format, OWASP params), 15-min JWTs with role+scope claims, single-use Redis refresh tokens with rotation, centralized RBAC middleware — unit-tested
- [x] Audit log (`internal/audit`) + synchronous writes; login/login-failed/logout/create-admin events recorded
- [x] PostgreSQL migration 000001: users, servers, packages, accounts, audit_log (golang-migrate; applied to dev stack)
- [x] Data access layer (`internal/store`) — hand-written SQL via pgx, no ORM
- [x] CypherCore API server: Gin, `/healthz`, `/api/v1/auth/{login,refresh,logout}`, `/api/v1/me`, graceful shutdown
- [x] `create-admin` subcommand (bootstrap first root admin)
- [x] gRPC contract `proto/agent/v1/agent.proto` (Register / Heartbeat / ReportTaskResult) + buf config
- [x] CypherAgent skeleton: config load, distro detection, signal handling (3.1MB static Linux binary)
- [x] E2E verified on dev stack: login → token → protected route → refresh rotation → audit rows ✅
- [x] gRPC codegen via buf (`gen/agent/v1`), no system protoc needed
- [x] PKI package + CLI: `pki init` (CA), `pki issue-server`, `pki issue-agent` (ECDSA P-256, TLS 1.3)
- [x] gRPC AgentService in CypherCore on :9090 — Register / Heartbeat / ReportTaskResult, mTLS when certs configured (enforced in production)
- [x] Servers table wiring: registration upserts row, heartbeat updates `last_seen_at`/`agent_status`, unknown ID → agent re-registers
- [x] Agent: mTLS dial, Register with retry/backoff, 30s Heartbeat loop
- [x] E2E verified with real mTLS: CA → certs → register → heartbeat advanced `last_seen_at`; certless client rejected at handshake ✅
- [x] NATS JetStream job pipeline: WorkQueue stream, per-server subjects, task-ID dedup, durable per-agent consumers, max-5 redelivery + permanent-error dead-lettering
- [x] Tasks table (migration 000002) + admin API: `POST /api/v1/admin/servers/:id/tasks`, `GET /api/v1/admin/tasks/:id` (root-admin RBAC-gated — first live use of RequireRole)
- [x] Agent task executor (noop, system_user.create) + result reporting via gRPC with retry
- [x] **First real provisioning action verified**: system user created inside a Debian container via API → NATS → agent → `useradd` (idempotent re-run also succeeds); on Windows dev the same task dead-letters immediately with a clear "Linux only" error
- [x] Agent RSS measured under real load: **3.6 MiB** (budget: <50MB; cPanel minimum: 2GB) ✅

- [x] Host stats in heartbeat (load/mem/disk from /proc + statfs on Linux, zeros on dev machines) persisted to servers table (migration 000003)
- [x] OpenAPI 3.1 spec generated from handler annotations (swaggo v2), served at `GET /api/v1/openapi.json`, regenerate with `make openapi`
- [x] CI pipeline (GitHub Actions): build + vet + test + linux amd64/arm64 cross-compile + buf proto lint on every push/PR
- [x] NATS credentials support (`CYPHER_NATS_CREDS` / `CYPHER_AGENT_NATS_CREDS`)
- [x] **Phase 1 agent-skills batch** (`.claude/skills/`, per `plan.md` §9 catalog #1-8): `go-backend-conventions`, `database-and-migrations`, `grpc-proto-contracts`, `jobs-and-agent-tasks`, `auth-and-rbac`, `api-contract-workflow`, `testing-conventions`, `linux-system-integration` — each grounded in the shipped Phase 1 code, not speculation

### Pending
- [ ] NATS server-side auth config in production deployments (client support ready; server config is an installer/deploy concern)
- [ ] Frontend TypeScript client generation from the OpenAPI spec (do when web/ lands in Phase 2)

## Phase 2 — Admin Plane & Provisioning + UI Shell 🟨

### Done
- [x] UI shell & design system foundation: Next.js 16 (App Router, Turbopack, `output: "standalone"`) + Tailwind + shadcn/ui in `web/`; sidebar admin shell, login screen, light/dark/system theming (next-themes), Lucide icons
- [x] Typed API client: TS types generated from the OpenAPI spec (`npm run gen:api`), fetch wrapper with in-memory access token + single-use refresh rotation and one-shot retry on 401
- [x] No-CORS architecture: Next.js rewrites proxy `/api/*` to CypherCore (`CYPHER_CORE_API_URL`) — same-origin in dev and prod
- [x] `GET /api/v1/admin/servers` (root-admin, with latest host stats; `host()` cast so IPs render clean) + OpenAPI regen
- [x] Dashboard: live server cards (status badge, load, memory/disk usage bars) refreshing every 15s; skeleton loaders; designed-empty-first states
- [x] Servers page: registered-nodes table with liveness ("last seen Xs ago")
- [x] E2E verified: login + servers list through the Next proxy against the live control plane with a containerized Linux agent reporting real stats ✅

- [x] Package templating system: `packages` store + REST (`GET/POST/DELETE /api/v1/admin/packages`) with limits (disk, bandwidth, domains, DBs, email, CPU%, memory) + Packages UI (create dialog, cards, delete)
- [x] Account provisioning: `accounts` store (atomic user+account tx), REST create/suspend/unsuspend/terminate, `system_user.create`/`system_user.remove` tasks, agent-reported results drive status transitions (provisioning→active/failed, terminating→deleted)
- [x] Accounts UI: create dialog (server/package selects), status badges, suspend/unsuspend, type-to-confirm terminate; 5s polling to reflect provisioning→active
- [x] **Full provisioning E2E verified** ✅: create package → create account (`provisioning`) → agent created real Linux user `cyph_alicedb00f2` → status auto-flipped to `active`; suspend blocked alice's login, unsuspend restored it; terminate → account row deleted + Linux user removed from the container
- [x] Web build green (Next.js 16 / Base UI): `render` prop + Select `string|null` fixes compile; all 7 routes build

### Pending
- [ ] Server node detail view + register/remove flows in UI
- [ ] Automated systemd service monitors
- [ ] Reseller resource pools
- [x] **Event Bus** (`internal/events`, §12): `EVENTS` JetStream stream (`events.>`, Limits retention, 14d) strictly separate from `tasks.*`; JetStream publish + in-process pub/sub fan-out; emits `server.registered`, `package.created`/`deleted`, `account.created`/`activated`/`suspended`/`unsuspended`/`terminating`/`terminated`/`failed` — secret-free snapshots. **E2E verified**: 5 events in the stream + all logged by an in-process subscriber ✅
- [x] **Plugin reservations** (§11): migration 000005 `plugins` table; finalized `plugin.yaml` manifest schema (`internal/plugins`, `api_version: v1`, validated, unit-tested) + `docs/plugin-manifest.md`; read-only `GET /api/v1/admin/plugins` and `/plugins/manifest-schema` reserving the namespace (no loader yet)
- [x] **Phase 2 skills batch** (catalog #9-11): `ui-development`, `async-ui-patterns`, `extensibility-and-events`

## Phase 3 — Web Server & PHP Management ⬜

- [ ] Nginx virtual-host config generator (MVP default)
- [ ] Multi-PHP install scripts + per-account PHP-FPM pool configs
- [ ] PHP INI Editor API
- [ ] Lego ACME integration (Let's Encrypt / ZeroSSL)
- [x] **Phase 3 skills batch** (catalog #12-14): `agent-config-generators`, `php-runtime-management`, `ssl-acme` *(design-intent — verify vs code when built)*

## Phase 4 — Files, FTP, & Databases ⬜

- [ ] Web File Manager (Next.js client + Go filesystem handler)
- [ ] Pure-FTPd virtual users (MVP default)
- [ ] MariaDB provisioning APIs (MVP default)
- [ ] phpMyAdmin / Adminer auto-setup
- [x] **Phase 4 skills batch** (catalog #15-16): `filesystem-operations-safety`, `user-database-provisioning` *(design-intent — verify vs code when built)*

## Phase 5 — Email & DNS Servers ⬜

- [ ] Postfix SMTP + Dovecot IMAP/POP3 configuration
- [ ] Mail user auth database & quotas
- [ ] PowerDNS zone configuration (MVP default)
- [ ] DNS cluster synchronization engine
- [x] **Phase 5 skills batch** (catalog #17-18): `mail-stack`, `dns-management` *(design-intent — verify vs code when built)*

## Phase 6 — Logging, Auditing, & Hardening ⬜

- [ ] System metrics collection (CPU/memory/disk IO → time-series store, not Postgres)
- [ ] User terminal & cron job managers
- [ ] Audit log dashboards & retention policies
- [ ] Security hardening + release candidate packaging
- [ ] Single-command installer (per Appendix A rules: consent-based takeover, uninstaller, no forced bundling)
- [ ] Version Upgrade & Migration Framework: `system_version` tracking, sequential migration replay, mandatory pre-upgrade backup, rollback path (`plan.md` §13) — **release-candidate gate, must exist before first production update ships**
- [ ] Compatibility matrix doc (`docs/compatibility-matrix.md`): Core ↔ Agent ↔ plugin API version ranges, enforced at registration (`plan.md` §13)
- [ ] Metrics API: `GET /api/v1/metrics/{scope}` (server/account/domain) + raw Prometheus `/metrics` scrape endpoint (`plan.md` §16)
- [ ] CI: add load-test, security-test, and UI-test (Playwright) job tiers alongside existing unit/integration/cross-compile jobs (`plan.md` §19)
- [x] **Phase 6 skills batch** (catalog #19-23): `observability-and-metrics`, `backups`, `installer-and-packaging`, `upgrade-and-compatibility`, `public-interfaces` *(partly design-intent — verify vs code when built)*

## Extensibility & Operability Backlog — post-MVP (`plan.md` §11-19) ⬜

> Reservations (route namespace, event subject namespace, upgrade framework, metrics API) are tracked inline in the phases above since they gate MVP correctness. Everything below is the full post-MVP build-out.

- [ ] Plugin runtime: process-isolated backend plugin loader, permission enforcement against the manifest, plugin lifecycle (install/enable/disable/uninstall) (`plan.md` §11)
- [ ] UI plugin slots: sidebar entries, dashboard cards, settings pages registered from a manifest (`plan.md` §11)
- [ ] Themes and language packs as installable plugin types on top of the white-labeling/i18n scaffolding (`plan.md` §11)
- [ ] Plugin marketplace (hosted registry, review/signing) — deferred indefinitely, own product scope (`plan.md` §11)
- [ ] Internal Event Bus: core domain events (`account.created`, `account.suspended`, `domain.added`, `dns.record.changed`, …) wired to JetStream `events.>`, plus in-process pub/sub for same-binary subscribers (`plan.md` §12)
- [ ] `cypherctl` CLI: account/server/dns/backup/ssl commands + `upgrade`/`rollback` (`plan.md` §14)
- [ ] Go SDK generated from OpenAPI (`oapi-codegen`) (`plan.md` §14)
- [ ] Node SDK generated from OpenAPI, shared generator config with the CypherUI TS client (`plan.md` §14)
- [ ] Python SDK generated from OpenAPI (`plan.md` §14)
- [ ] Webhooks: signed (HMAC-SHA256) delivery via dedicated JetStream consumer, retry/dead-letter, management UI + delivery log + manual redelivery (`plan.md` §15)
- [ ] Multi-region: `region` field on server registration, region-aware NATS partitioning, admin UI fleet grouping/filtering by region, data-residency query filters (`plan.md` §17)
- [ ] Billing integration adapter contract generalized from the WHMCS module; publish spec so community can build Blesta/HostBill adapters without touching CypherCore (`plan.md` §18)

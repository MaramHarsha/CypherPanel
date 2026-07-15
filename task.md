# CypherPanel — Task Tracker

> Living document: update whenever a task starts or finishes. `[x]` done · `[~]` in progress · `[ ]` pending.
> Phases follow `plan.md` Section 9 (Implementation Roadmap).
>
> **Agent skills:** the full 23-skill catalog (`.claude/skills/`) was written upfront (2026-07) at the user's request, overriding the plan's write-at-end-of-phase timing. Skills for not-yet-built phases carry a `Status: design-intent` line and must be re-verified against real code when their phase lands (updated in the same PR). Read the relevant SKILL.md before writing code in any area.

## Progress at a Glance

| Phase | Scope | Status |
|-------|-------|--------|
| **1** | Core Foundation & Agent Comms | ✅ Complete (NATS server-side auth re-homed to the Phase 6 installer) |
| **2** | Admin Plane & Provisioning + UI Shell | ✅ Complete (service control, server detail/remove, reseller shell all landed) |
| **3** | Web Server & PHP Management | ✅ Complete (DNS-01 *live* solver awaits Phase 5 PowerDNS; selection + seam done) |
| **4** | Files, FTP, & Databases | ✅ Complete (MariaDB, Pure-FTPd, File Manager, Adminer handoff) |
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
- [x] Frontend TypeScript client generation from the OpenAPI spec — **done** in Phase 2: `npm run gen:api` (openapi-typescript) generates `web/lib/api-types.ts` from `docs/swagger.json`; every endpoint added since is regenerated and consumed via `web/lib/api.ts`. No hand-written endpoint shapes.
- [ ] NATS server-side auth config — **not application code**; the client already supports credentials (`CYPHER_NATS_CREDS`/`CYPHER_AGENT_NATS_CREDS`). Generating the NATS server's accounts/users/permissions is a deploy concern owned by the **Phase 6 single-command installer** (tracked there).

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

- [x] **Reseller resource pools** (§4A): `reseller_pools` table (migration 000006) + resellers store; centralized scoping helpers `auth.OwnerFilter`/`auth.CanAct` (unit-tested, no ad-hoc role checks in handlers); packages/accounts stores scoped by owner; `POST/GET /admin/resellers`; packages+accounts shared under a root-admin+reseller route group with per-handler scoping, package-ownership check, and pool-quota enforcement on account create; Resellers admin UI (create + usage table). `reseller.created` event emitted.
  - **Security E2E verified** ✅: reseller sees only own packages/accounts; **IDOR blocked** (reseller suspending admin's account → 404); **quota enforced** (3rd account over max_accounts=2 → 403); admin unrestricted; reseller-provisioned account fully provisioned a real Linux user through the same pipeline
- [x] **Reseller-facing scoped UI shell**: the admin shell is now role-aware — resellers see a filtered nav (Dashboard + Packages + Accounts; **Servers and Resellers hidden** as root-admin/fleet concerns) and an account-focused dashboard (their accounts + packages summary, not fleet server cards) instead of the fleet view. Driven by `GET /me` role; the API remains the real enforcement (defense in depth).
  - **E2E verified** ✅: created reseller `shelltest` → `/me` role=`reseller`; reseller may `GET /admin/servers` (200, needed to create accounts) but is **403** on `GET /admin/resellers` and on service control — exactly the surfaces hidden from their nav.

- [x] **Automated systemd service monitors** (§4A): `internal/services` probes managed units (nginx/mariadb/postfix/dovecot/pdns, `CYPHER_MANAGED_SERVICES` override) via `systemctl show` — Linux impl + `!linux` stub, telemetry never fails a heartbeat, not-installed units omitted; pure parser unit-tested. Proto add-only `repeated ServiceStatus services` on heartbeat; persisted to `servers.services` jsonb (migration 000007); surfaced in `GET /admin/servers` and as color-coded **service health chips** on the dashboard cards + servers table.
  - **E2E verified** ✅ (fake `systemctl` fixture in the container exercised the real probe→parse→heartbeat→DB→API path): reported nginx=active, mariadb=active, postfix=failed, pdns=inactive; dovecot correctly omitted as not-installed

- [x] **Global service control actions** (§4A): `service.control` task (start/stop/restart/reload) — `services.Control` (Linux systemd) + `!linux` stub, **allowlist-validated twice** (`services.IsManaged`/`ValidAction` at the core boundary *and* re-checked on the agent so a payload can never target an arbitrary unit); root-admin-only `POST /admin/servers/:id/services/:name/control`, audited.
  - **E2E verified** ✅ (fake `systemctl` in the container logging invocations): `restart nginx` → 202 → task succeeded → fake log recorded `restart nginx`; unmanaged `sshd` → 400; bad action `nuke` → 400.
- [x] **Server node detail view + remove flow** (UI): `GET /admin/servers/:id` (detail) + `DELETE /admin/servers/:id` (root-admin, **refused with 409 while accounts exist**, audited); new server detail page (`/servers/[id]`) with stat tiles, per-service control menu, type-safe remove confirm, and agent-enrollment instructions (self-registration over mTLS — no manual entry); servers table links to detail.
  - **E2E verified** ✅: `GET` detail → 200; `DELETE` on a server hosting `tls1` → 409; `DELETE` on an account-free (stale) server → 200 `removed` → subsequent `GET` → 404; both UI routes compile (200).

### Pending
- [ ] Register-new-server UX beyond the shown enrollment command (optional; agents self-register today)
- [x] **Event Bus** (`internal/events`, §12): `EVENTS` JetStream stream (`events.>`, Limits retention, 14d) strictly separate from `tasks.*`; JetStream publish + in-process pub/sub fan-out; emits `server.registered`, `package.created`/`deleted`, `account.created`/`activated`/`suspended`/`unsuspended`/`terminating`/`terminated`/`failed` — secret-free snapshots. **E2E verified**: 5 events in the stream + all logged by an in-process subscriber ✅
- [x] **Plugin reservations** (§11): migration 000005 `plugins` table; finalized `plugin.yaml` manifest schema (`internal/plugins`, `api_version: v1`, validated, unit-tested) + `docs/plugin-manifest.md`; read-only `GET /api/v1/admin/plugins` and `/plugins/manifest-schema` reserving the namespace (no loader yet)
- [x] **Phase 2 skills batch** (catalog #9-11): `ui-development`, `async-ui-patterns`, `extensibility-and-events`

## Phase 3 — Web Server & PHP Management 🟨

### Done
- [x] **Nginx vhost generator** (`internal/webserver`): `text/template` from typed `VHostSpec` behind a `VHostRenderer` interface (Apache/OpenLiteSpeed adapters drop in later); golden-file tested
- [x] **Per-account PHP-FPM pool generator**: dedicated user/group + private socket per account (plan.md §7 isolation), `listen.group` = web-server user, package memory → `memory_limit`; golden-file tested
- [x] `paths.Layout` extended: `WebServerUser`, PHP socket/pool-path/account-log-dir helpers (Debian layout; `CYPHER_PATH_*` overridable)
- [x] `platform.Sites` apply layer (`_linux` + stub): account-owned dirs (mkdir+chown), root-owned configs, **validate-then-reload nginx with rollback**, idempotent; nginx-absent degrades gracefully (writes, skips reload)
- [x] Agent tasks `site.provision` / `site.deprovision`; dispatched on account create/terminate (after user create / before user remove); `CYPHER_DEFAULT_PHP_VERSION` config
- [x] **E2E verified** ✅ (real agent in container): account create → generated correct nginx vhost + PHP-FPM pool + web root/logs owned by the account user; terminate → vhost, pool, Linux user, and DB row all removed (symmetric teardown)

- [x] **MultiPHP INI Editor** (§4B): per-account `php_version` + allowlisted `php_settings` (migration 000008); `internal/phpini` bounded allowlist + value validation (unit-tested); `PATCH /admin/accounts/:id/php-settings` (scoped, audited) re-provisions via idempotent `site.provision`; overrides applied as pool-level `php_admin_value` (user setting overrides package memory default); `GET /admin/php/ini-keys`; PHP column + settings dialog in the accounts UI
  - **E2E verified** ✅: non-allowlisted directive → 400, newline-injection value → 400, valid update → 202 → FPM pool regenerated with new `php_admin_value` lines (memory_limit 256M→1024M, upload_max_filesize, max_execution_time), settings persisted

- [x] **Lego ACME / SSL issuance** (§4B): `internal/acme` Lego HTTP-01 webroot issuer (idempotent — valid cert >30d = no-op), persistent ACME account key; nginx vhost TLS variant (443 + HTTP→HTTPS redirect, ACME path stays on 80) golden-tested; `platform.Sites.InstallCertificate` (key 0600) + `ApplyVHost` (validate-then-reload); `ssl.issue` task; proto add-only `metadata` on ReportTaskResult carries cert expiry; account `ssl_status`/`ssl_expires_at` (migration 000009) driven by task result; `POST /admin/accounts/:id/ssl` (scoped, audited); SSL column + issue button in accounts UI; `CYPHER_ACME_DIRECTORY` config
  - **E2E verified against Pebble** ✅ (offline ACME test server, `PEBBLE_VA_ALWAYS_VALID`; Lego trusts Pebble CA via `LEGO_CA_CERTIFICATES`/`LEGO_CA_SERVER_NAME`): account create→active (webroot exists) → `POST /ssl` (202, `issuing`) → agent ran Lego HTTP-01 (register→validate→obtain) → `fullchain.pem` (0644) + `privkey.pem` (**0600**) + persistent account key written; nginx vhost gained the **443 ssl block** (cert paths) with ACME challenge staying on :80 + HTTP→HTTPS redirect; account `ssl_status=active` + `ssl_expires_at` set. **Idempotency proven**: a second `POST /ssl` completed the task but obtained **no** new cert (ACME "Obtaining certificate" logged exactly once) — valid cert >30d = no-op.

- [x] **SSL auto-renewal scheduler** (`internal/sslrenew`): core-side background scheduler scans every `CYPHER_SSL_RENEW_INTERVAL` (default 12h) and re-dispatches the idempotent `ssl.issue` task for active certs expiring within `CYPHER_SSL_RENEW_THRESHOLD` (default 30d, matching the agent's >30d skip guard so a due cert is actually renewed). New account store query `ListExpiringSSL`; per-account failures logged and skipped (one bad cert never stalls the batch); loop unit-tested (dispatch-each, failure-continues, no-op, list-error, cutoff-uses-threshold) with no DB/NATS.
  - **E2E verified** ✅: with an inflated threshold the boot scan reported `certificates due count=2` → marked `issuing` → dispatched `ssl.issue` → live agent processed the renewal and correctly **skipped** actual re-issue (cert >30d; ACME "Obtaining" stayed at 1) → account returned to `active`; with the default 30d threshold the scan is a correct no-op (all certs ~90d out). (Note: a mid-flight core restart exposed a pre-existing, all-task-types robustness gap — an agent that exhausts its result-report retries leaves the account stuck in the transitional state; tracked separately, not renewal-specific.)

- [x] **Per-account PHP version selection** (§4B): `php.version.change` task carries old+new version; agent removes the old version's pool (releasing the per-account, version-independent socket) then writes the new version's pool — via a shared, **TLS-aware** `applySite` that also fixed a latent SSL-clobber (re-provisioning previously regenerated a plain-HTTP vhost, dropping the 443 block for any account with a cert; now it preserves HTTPS when a cert is installed). Added best-effort **PHP-FPM reload** to the platform layer (`reloadPHPFPM`, systemd `reload` not restart, skips absent versions — mirrors the nginx pattern) + `RemovePHPPool`. `CYPHER_PHP_VERSIONS` config (default `8.2,8.3,8.4`); store `SetPHPVersion`; `PATCH /admin/accounts/:id/php-version` (scoped, audited, allowlist-validated, active-only, same-version no-op) + `GET /admin/php/versions`; version dropdown added to the accounts PHP-settings dialog. Executor unit tests (remove-before-provision ordering, same-version skip, TLS-preserved vs plain-HTTP) pass on any OS.
  - **E2E verified** ✅ (live agent, on the SSL-active `tls1` account): `PATCH php-version 8.3→8.4` → task succeeded → **old `8.3` pool.d emptied, new `8.4` pool written** with the **same socket**, account `php_version=8.4`, and the vhost **kept `listen 443 ssl` + cert path** (HTTPS preserved through the version change). Guards verified: same-version → 200 `unchanged` (no dispatch); unsupported `9.9` → 400; `GET /php/versions` → `["8.2","8.3","8.4"]`.

- [x] **Multi-PHP install/uninstall** (§4B): `php.runtime` task installs/removes a PHP-FPM branch via the distro package manager. `internal/phpruntime` builds the package-manager commands **distro-aware** (Debian `apt-get update` + `install -y php8.3-fpm` + common extensions; RHEL/Remi `dnf install php83-php-*` dotless naming) as a **pure, unit-tested** function (injection-proof: version must match `^\d+\.\d+$`); Linux `Run` + `!linux` stub. Root-admin `POST /admin/servers/:id/php` (version must be in the `CYPHER_PHP_VERSIONS` allowlist; agent re-validates); PHP runtimes card on the server detail page with Install/Uninstall per version.
  - **E2E verified** ✅ (fake `apt-get` logging invocations): install 8.4 → 202 → task succeeded → apt log recorded `update` then `install -y php8.4-fpm php8.4-cli … php8.4-bcmath`; unpermitted `7.0` → 400; bad action `purge` → 400. Command-construction unit tests cover Debian/RHEL, install/uninstall, and injection rejection.

- [x] **SSL DNS-01 challenge selection** (§4B): the `acme.Issuer` now auto-selects the challenge by domain shape — single hostnames keep HTTP-01 (webroot); **wildcards (`*.`) route to DNS-01** via an injectable `challenge.Provider` seam (`SetDNSProvider`). Until DNS management lands, a wildcard request fails **fast and permanently** with `ErrWildcardNeedsDNS` (no confusing HTTP-01 attempt, no pointless retries — the agent marks it `jobs.Permanent`). Unit-tested (wildcard detection + fast-fail). **The concrete PowerDNS-backed provider is wired in Phase 5** (DNS management) — the seam is ready; live wildcard issuance activates then.

### Pending (Phase 3)
- [x] Wire the PowerDNS DNS-01 provider into `acme.Issuer.SetDNSProvider` — **done in Phase 5** (`dns.ACMEProvider`, E2E-verified against live PowerDNS)
- [x] **Phase 3 skills batch** (catalog #12-14): `agent-config-generators` + `php-runtime-management` + `ssl-acme` *(now code-grounded)*

## Phase 4 — Files, FTP, & Databases ✅

- [x] **MariaDB database provisioning** (§4B, MVP default): `internal/usersdb` **adapter** (`Manager` interface — MariaDB impl via go-sql-driver, PostgreSQL adapter can drop in later) with **idempotent** provision (CREATE DB/USER IF NOT EXISTS + ALTER USER to re-assert password + **least-privilege** `GRANT ALL ON <db>.*` only) and drop; identifiers regex-guarded against injection. `db.create`/`db.drop` agent tasks (enabled by `CYPHER_AGENT_MARIADB_DSN`). **Secret-safe credential flow**: the password is **generated on the agent** (never in a task payload/stream), returned as result metadata, **encrypted at rest** with AES-256-GCM (`internal/secretcrypt`, key from `CYPHER_DB_ENCRYPTION_KEY`, dev-derived from JWT secret) — no plaintext column. Migration 000010 (`account_databases`); account-scoped REST (list/create/delete + one-shot password reveal) with **package `databases` limit** enforcement; databases dialog in the accounts UI (create/status/reveal/delete). Unit tests: secretcrypt round-trip/tamper, usersdb identifier-injection + password safety.
  - **E2E verified** ✅ (real agent + MariaDB container): create `blog` → db + user created with **least-privilege grants** (`USAGE ON *.*` + `ALL ON <own_db>.*`, no globals); revealed password **authenticates** and the user sees only its own DB (isolation); **limit** enforced (3rd db over `databases=2` → 403); invalid name → 400; delete → db + user + record all removed.
- [x] **Pure-FTPd virtual users** (§4B, MVP default): `internal/ftp` adapter (`Manager` interface — Pure-FTPd via `pure-pw`; ProFTPD can drop in later), each virtual user mapped to the account's **system uid/gid + home** so uploads are account-owned (isolation); idempotent (userdel-then-useradd). `ftp.create`/`ftp.delete` tasks; **secret-safe** (agent generates the password → result metadata → AES-GCM encrypted at rest); home dir **agent-derived** from the distro layout (no hardcoded path in Core). Migration 000011; account-scoped REST (list/create/delete/reveal); FTP dialog in the accounts UI.
  - **E2E verified** ✅ (fake `pure-pw` logging invocations): create → 202 → task succeeded → log recorded `useradd cyph_..._deploy -u cyph_... -g cyph_... -d /home/cyph_... -m`; `home_dir` returned `/home/cyph_...`; encrypted password reveals (32 chars).
- [x] **Web File Manager** (§4B, highest-risk surface): Core↔Agent **NATS request-reply** transport (`filemanager.Subject`), synchronous FS ops (list/read/write/mkdir/delete/rename) run on the agent **as the account uid/gid** (`setfsuid`/`setfsgid` on a locked OS thread — OS-enforced isolation, refuses uid/gid 0). **Path-traversal defence**: `CleanRel` neutralises `..`/absolute/backslash inputs (pure, unit-tested), then a **symlink-resolved under-root re-check** before every op. Account-scoped REST + a full file-browser UI (breadcrumb nav, tree, in-browser text editor, new file/folder, delete).
  - **E2E verified** ✅ (real agent + provisioned account): list/mkdir/write/read all work; created file **owned by the account user, not root** (operate-as-user confirmed); `../../../../etc/passwd` resolved to `/home/<acct>/etc/passwd` — **never escaped** to the real `/etc` (traversal blocked).
- [x] **phpMyAdmin / Adminer handoff** (§4B): one-click, auto-login **Adminer** handoff (`GET /admin/accounts/:id/databases/:dbid/adminer`, `CYPHER_ADMINER_URL`) — the UI builds an auto-submitting POST form with the account's DB credentials. **Scoped by construction**: the DB user is least-privilege (own database only), so the session can't reach other accounts' data; audited without the secret; 503 when unconfigured.
  - **E2E verified** ✅: handoff returns `url` + `driver=server` + `server=localhost` + least-privilege `username`/`db` + decrypted password.
- [x] **Phase 4 skills batch** (catalog #15-16): `filesystem-operations-safety` + `user-database-provisioning` *(both now code-grounded)*

## Phase 5 — Email & DNS Servers 🟨

- [x] **PowerDNS zone/record management** (§4B/5, MVP default): `internal/dns` — `Provider` interface (PowerDNS via its **REST API**; BIND can drop in later) + **per-record-type validation** (A/AAAA/CNAME/MX/TXT/SRV/CAA/NS, CNAME-can't-coexist, unit-tested) + PowerDNS content canonicalisation (TXT quoted, hostnames dotted). Account-scoped zone editor (`GET/POST/DELETE /admin/accounts/:id/dns`, `NameInZone` scoping), **zone auto-created on first view** with apex+www A → the account's server IP + NS records; DNS zone-editor dialog in the accounts UI.
  - **E2E verified** ✅ (real PowerDNS 4.9 + MariaDB backend): auto-zone created; add TXT (stored quoted) + MX (dotted) → 200; validation rejects bad IP / CNAME-coexistence / out-of-zone name (400); delete works; zone present in PowerDNS; **live resolution** `dig @pdns dbuser.example.com A` → the server IP.
- [x] **SSL DNS-01 for wildcards** (closes the Phase 3 loose end): `dns.ACMEProvider` implements lego's `challenge.Provider` (sets/removes `_acme-challenge` TXT, longest-suffix zone match), wired into `acme.Issuer.SetDNSProvider` on the agent when `CYPHER_AGENT_PDNS_API_URL` is set (logs "wildcard SSL enabled").
  - **E2E verified** ✅ (live PowerDNS integration test): `Present` created the `_acme-challenge` TXT, `CleanUp` removed it.
- [ ] Postfix SMTP + Dovecot IMAP/POP3 configuration
- [ ] Mail user auth database & quotas
- [ ] DNS cluster synchronization engine (primary/secondary AXFR/native replication)
- [x] **Phase 5 skills batch** (catalog #17-18): `mail-stack` *(design-intent)*, `dns-management` *(now code-grounded)*

## Phase 6 — Logging, Auditing, & Hardening ⬜

- [ ] System metrics collection (CPU/memory/disk IO → time-series store, not Postgres)
- [ ] User terminal & cron job managers
- [ ] Audit log dashboards & retention policies
- [ ] Security hardening + release candidate packaging
- [ ] Single-command installer (per Appendix A rules: consent-based takeover, uninstaller, no forced bundling) — **includes NATS server-side auth config** (accounts/users/permissions; client credential support already shipped in Phase 1)
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

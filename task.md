# CypherPanel — Task Tracker

> Living document: update whenever a task starts or finishes. `[x]` done · `[~]` in progress · `[ ]` pending.
> Phases follow `plan.md` Section 9 (Implementation Roadmap).

## Progress at a Glance

| Phase | Scope | Status |
|-------|-------|--------|
| **1** | Core Foundation & Agent Comms | 🟨 In progress (~85%) |
| **2** | Admin Plane & Provisioning + UI Shell | ⬜ Not started |
| **3** | Web Server & PHP Management | ⬜ Not started |
| **4** | Files, FTP, & Databases | ⬜ Not started |
| **5** | Email & DNS Servers | ⬜ Not started |
| **6** | Logging, Auditing, & Hardening | ⬜ Not started |
| — | Post-MVP (`upcoming-features.md`) | ⬜ After MVP |

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

## Phase 1 — Core Foundation & Agent Comms 🟨

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

### Pending
- [ ] NATS JetStream job pipeline: publish tasks from Core, agent consumer, idempotent handling + dead-letter
- [ ] System user creation task end-to-end (first real provisioning action, tested in a Linux container)
- [ ] Host stats in heartbeat (load, memory, disk — currently unset)
- [ ] OpenAPI spec generation for the REST API (swaggo/huma per plan)
- [ ] CI pipeline (GitHub Actions): build + vet + test + Linux cross-compile on every push
- [ ] Agent idle-RSS measurement harness (verify <50MB budget from day one)

## Phase 2 — Admin Plane & Provisioning + UI Shell ⬜

- [ ] UI shell & design system foundation (Next.js + shadcn/ui, sidebar layout, auth screens, light/dark theming)
- [ ] Server node registration API + UI
- [ ] Package templating system (limits: disk, bandwidth, domains, DBs, mail, cgroup caps)
- [ ] Account creation, suspension, termination flows
- [ ] Automated systemd service monitors
- [ ] Reseller resource pools

## Phase 3 — Web Server & PHP Management ⬜

- [ ] Nginx virtual-host config generator (MVP default)
- [ ] Multi-PHP install scripts + per-account PHP-FPM pool configs
- [ ] PHP INI Editor API
- [ ] Lego ACME integration (Let's Encrypt / ZeroSSL)

## Phase 4 — Files, FTP, & Databases ⬜

- [ ] Web File Manager (Next.js client + Go filesystem handler)
- [ ] Pure-FTPd virtual users (MVP default)
- [ ] MariaDB provisioning APIs (MVP default)
- [ ] phpMyAdmin / Adminer auto-setup

## Phase 5 — Email & DNS Servers ⬜

- [ ] Postfix SMTP + Dovecot IMAP/POP3 configuration
- [ ] Mail user auth database & quotas
- [ ] PowerDNS zone configuration (MVP default)
- [ ] DNS cluster synchronization engine

## Phase 6 — Logging, Auditing, & Hardening ⬜

- [ ] System metrics collection (CPU/memory/disk IO → time-series store, not Postgres)
- [ ] User terminal & cron job managers
- [ ] Audit log dashboards & retention policies
- [ ] Security hardening + release candidate packaging
- [ ] Single-command installer (per Appendix A rules: consent-based takeover, uninstaller, no forced bundling)

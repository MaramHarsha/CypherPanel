# CypherPanel: Open-Source cPanel & WHM Alternative

CypherPanel is a modern, secure, and self-hosted control panel designed to be a direct, feature-complete replacement for cPanel and WHM. It provides a modular, API-first architecture designed for shared hosting providers, VPS administrators, agencies, and web developers.

---

## 1. Project Vision & Philosophy

*   **API-First Design:** Every feature in the UI must be powered by a fully documented, RESTful or gRPC API.
*   **Modular Architecture:** Components (e.g., DNS, mail, database servers) are decoupled, allowing administrators to swap out services (e.g., swap BIND for PowerDNS, or Nginx for OpenLiteSpeed).
*   **Host Isolation:** Ensure strict security and resource control using modern Linux capabilities, systemd cgroups, and isolated PHP-FPM pools.
*   **Ease of Installation:** A single-command shell installer that automatically provisions the core system, configures repositories, and bootstraps the panel.
*   **Distro Agnostic:** Native support for major Linux distributions, focusing on Enterprise Linux (Rocky Linux, AlmaLinux) and Debian/Ubuntu systems.

---

## 2. High-Level Architecture

CypherPanel is divided into three distinct layers:
1.  **CypherCore (Central Controller):** The management plane, running the central API, scheduler, database, and message queue.
2.  **CypherUI (Admin & User Frontends):** Next.js UIs communicating with CypherCore.
3.  **CypherAgent (Server Daemon):** A lightweight service running on each managed server that executes local operations (creating virtual hosts, databases, system accounts, etc.).

> **MVP Default Stack:** The modular architecture is a target end-state, not a Phase 1 requirement. Building config generators for every listed alternative (Nginx *and* Apache *and* OpenLiteSpeed; BIND9 *and* PowerDNS; PostgreSQL *and* MariaDB for user databases) in parallel will stall the roadmap. MVP ships against **one default per category**, with the others implemented later as swappable adapters behind the same internal interface:
> *   **Web server:** Nginx (widest compatibility, lightest resource footprint, best PHP-FPM socket support).
> *   **DNS server:** PowerDNS (REST API-driven zone management fits an automation-first control plane far better than hand-editing BIND zone files).
> *   **User databases:** MariaDB (the default the vast majority of shared-hosting PHP applications — WordPress, etc. — actually expect). PostgreSQL support for user databases ships as a post-MVP adapter, not simultaneously.
> *   **Mail:** Postfix + Dovecot (already singular — no change needed).

```mermaid
graph TD
    subgraph Control Plane
        UI[CypherUI Next.js App] -->|HTTPS REST/WS| Core[CypherCore Go API]
        Core -->|SQL| DB[(PostgreSQL)]
        Core -->|Pub/Sub & Jobs| MQ[NATS JetStream]
        Core -->|Cache/Sessions| Cache[(Redis)]
    end

    subgraph Managed Server
        Agent[CypherAgent Daemon Go] <-->|mTLS / gRPC| MQ
        Agent -->|Executes| OS[Linux OS System]
        Agent -->|Configures| Nginx[Web Server: Nginx MVP default]
        Agent -->|Provisions| DBServer[DB Engine: MariaDB MVP default]
        Agent -->|Configures| Mail[Mail Server Postfix/Dovecot]
        Agent -->|Manages| DNS[DNS Server: PowerDNS MVP default]
    end
```

---

## 3. Technology Stack

> **Version Policy:** Version numbers in this document are not pins — treat every one as "latest stable as of the last time this plan was checked," last verified **2026-07-14**. Before scaffolding each component (and again at the start of each roadmap phase, since phases may span months), search online for the current latest stable release of that language/framework/server/database (or current LTS, where the project publishes one) as of the date you're actually building it, and use that instead of any version written here. Never deliberately build against an older release when a newer stable one is available — the only exception is a documented compatibility blocker (e.g., a required library not yet updated for a new major version), which should be noted inline where it applies.

### Backend Control Plane
*   **Programming Language:** Go (latest stable release — check the official Go release page at implementation time) for its speed, safety, static binaries, and system-level performance.
*   **API Framework:** Gin (net/http-compatible, mature middleware ecosystem, predictable memory behavior under load). Avoid Fiber for the control plane — its fasthttp base breaks net/http middleware/library compatibility and complicates HTTP/2 and streaming support; the performance delta over Gin does not matter at the API layer, where the database and message queue are the real bottlenecks.
*   **RPC Framework:** gRPC with mTLS for secure and fast communications between CypherCore and CypherAgent.
*   **ORM / Database Driver:** `pgx` + `sqlx` only. Do **not** use GORM for hot paths — its reflection-based mapping and default eager-loading behavior cause N+1 queries and materially higher CPU/RAM per request at scale. Hand-written SQL via `sqlx`/`pgx` keeps query plans predictable, which matters once tables (accounts, domains, DNS records) reach millions of rows.
*   **Connection Pooling:** PgBouncer in transaction-pooling mode in front of PostgreSQL. Go's own pgx pool is per-process; without PgBouncer, horizontally-scaled CypherCore replicas will each open their own pool and exhaust Postgres `max_connections` long before you reach millions of accounts.

### Frontend
*   **Framework:** Next.js (App Router, latest stable release — check current version at implementation time) with React, deployed in `output: "standalone"` mode. Keep pages that don't need SSR (static docs, marketing) statically generated; reserve SSR for pages that genuinely need per-request data, since each SSR request holds a Node.js worker — at scale this is the more expensive part of the control plane, not the Go API.
*   **Styling:** Tailwind CSS or Vanilla CSS for premium, responsive layouts.
*   **State & Data Fetching:** React Query (TanStack Query) for cached server-state management.
*   **Icons & Assets:** Lucide React for consistent vector symbols.

### Data & Messaging
*   **Database:** PostgreSQL (stores users, server info, packages, metadata, and task history), fronted by PgBouncer. Plan for read replicas once reporting/dashboard queries start competing with provisioning writes.
*   **Caching & Sessions:** Redis (user authentication sessions, API rate-limiting, and short-term cache).
*   **Task Queue:** NATS with JetStream. Prefer this over RabbitMQ as the default: NATS is a single small Go static binary with a low idle memory footprint, whereas RabbitMQ runs on the Erlang/OTP VM and carries meaningfully more baseline RAM overhead per broker node — the opposite of the "use less resources" goal. Keep RabbitMQ only as a documented alternative for operators who already run it, not the default install path.

### Daemon & OS Integrations
*   **Service Manager:** Systemd (creating custom user services, setting limits, and starting/stopping services).
*   **SSL Engine:** Lego (Go-based Let's Encrypt / ACME client library).
*   **Process Isolation:** systemd-run with control groups (cgroups v2) to enforce CPU, memory, and IO limits per user pool.

### Portability & Development Environment

The product targets Linux servers (that's where CypherAgent runs), but the *codebase* must be developable and buildable from any OS — contributors will be on Windows, macOS, and Linux, and an open-source project that only builds on one platform loses contributors at the door.

*   **No hardcoded paths — anywhere.** Every filesystem path is either (a) constructed with `filepath.Join` (never string concatenation with `/`), or (b) loaded from a single central config/defaults package — never scattered as string literals through feature code. This is not only a Windows-dev concern: paths differ *between Linux distros* too (Nginx vhosts: `/etc/nginx/sites-enabled` on Debian/Ubuntu vs `/etc/nginx/conf.d` on RHEL-family; PHP-FPM pools, systemd unit dirs, and web roots all vary). CypherAgent resolves all system paths through a per-distro path-mapping layer with config-file overrides, giving distro-agnostic support and operator customization from the same mechanism.
*   **Cross-compilation as the build model:** Go cross-compiles trivially (`GOOS=linux GOARCH=amd64 go build`, plus `arm64` for the growing ARM VPS market). Developing on Windows and producing Linux binaries is a one-flag operation; `CGO_ENABLED=0` everywhere so this never breaks. The Next.js UI is platform-agnostic already.
*   **Linux-dependent code behind interfaces:** Everything that only exists on Linux (systemd calls, cgroup writes, `/etc/passwd` user creation, PAM) lives behind Go interfaces with the real implementation in `_linux.go` build-tagged files. Business logic, config generation (`text/template` output), API handlers, and job orchestration stay platform-neutral and unit-testable on any dev machine — only the thin syscall layer needs Linux.
*   **Dev environment:** `docker-compose` dev stack (PostgreSQL, Redis, NATS, and a Linux container for agent integration tests) as the canonical way to run CypherPanel locally on any OS — on Windows this runs via Docker Desktop/WSL2. CypherCore and the UI run natively anywhere; full CypherAgent E2E tests run in the container/WSL2 or CI.
*   **Repo hygiene for cross-OS contributors:** `.gitattributes` enforcing LF line endings on all shell scripts, configs, and templates (a CRLF shell script silently breaks on servers — the classic Windows-contributor trap); `.editorconfig` for consistent formatting; CI builds and tests on Linux so the target platform is always the source of truth, regardless of what OS the code was written on.

### API Contract & Repository Strategy
*   **REST Contract:** OpenAPI 3.1 spec for the CypherCore REST API, generated from Go handler annotations (e.g., `swaggo` or `huma`) rather than hand-maintained separately — a spec that drifts from the implementation is worse than no spec. All routes versioned under `/api/v1/...` from day one so breaking changes later don't require a new domain/port.
*   **Frontend Client Generation:** Generate the CypherUI's TypeScript API client from the OpenAPI spec (e.g., `openapi-typescript`) instead of hand-writing fetch calls — keeps frontend and backend from silently drifting apart as the API grows.
*   **CypherCore ↔ CypherAgent Contract:** `.proto` files are the source of truth for the gRPC interface, versioned in the same repo as both binaries. Never reuse or renumber a proto field once shipped — only add new optional fields — since Core and Agent versions will not always match exactly during rolling upgrades across a large agent fleet.
*   **Schema Migrations:** `golang-migrate` or `Atlas` for PostgreSQL schema changes, checked into the repo and applied as an explicit CI/deploy step — never hand-applied to a running database.
*   **Repository Layout:** A single monorepo (`cmd/core`, `cmd/agent`, `internal/...`, `web/` for the Next.js app) for MVP. This keeps CypherCore/CypherAgent/CypherUI versioned and released together while the contracts between them are still moving; split into separate repos later only if release cadences genuinely diverge.

---

## 4. Architectural Component Breakdown

### A. Admin Panel (WHM Equivalent)
The management dashboard used by hosting providers.
*   **Server Management:** Register, monitor, and configure nodes running CypherAgent.
*   **Package Management:** Define hosting templates with limits on:
    *   Disk space (inodes and MBs)
    *   Monthly bandwidth
    *   Number of domains (addon, subdomain, aliases)
    *   Number of databases and email accounts
    *   CPU, Memory, and Concurrent Connections (cgroup limits)
*   **Account Provisioning:** Provision, suspend, and terminate user accounts across servers.
*   **Reseller Management:** Allocate resource pools to resellers to allow them to create packages and provision user accounts.
*   **Global Service Control:** Monitor, stop, start, and configure global services (MVP: Nginx, Postfix, Dovecot, PowerDNS, MariaDB — extended to Apache/OpenLiteSpeed/BIND once those adapters ship post-MVP).
*   **SSL Certificates:** Global management of auto-renewing host certificates.
*   **DNS Clustering:** Setup primary/secondary DNS sync relationships across server nodes.
*   **Backup Policies:** Schedule automated system-wide and user-level backups to external endpoints (S3, SFTP, local paths), using incremental, deduplicated backups (restic or Borg) rather than repeated full tar/zip archives — at scale, full re-archiving of every account on every run is the single fastest way to blow past a "low resource usage" goal on both disk and CPU.

### B. User Panel (cPanel Equivalent)
The portal used by individual site owners to manage their hosting environments.
*   **Domain Management:**
    *   **Addon Domains:** Separate directory hosting.
    *   **Subdomains:** Virtual subdirectories.
    *   **Aliases/Parked Domains:** Map secondary domains to target sites.
    *   **Redirects:** Configure 301/302 redirects.
*   **DNS Zone Editor:** Complete control over A, AAAA, CNAME, MX, TXT, SRV, and CAA records.
*   **File Management:**
    *   A responsive, modern web-based File Manager (drag-and-drop, upload/download, ZIP/UNZIP, inline code editor).
    *   FTP Accounts management (Pure-FTPd virtual users — MVP default; ProFTPD adapter post-MVP).
*   **Database Management:**
    *   Create MariaDB databases and users (MVP default; PostgreSQL user databases post-MVP).
    *   Assign user permissions to databases.
    *   One-click access to phpMyAdmin / Adminer.
*   **Email Management:**
    *   Create accounts, set mailbox quotas.
    *   Configure mail forwarders, autoresponders, and filters.
    *   Spam filters control (Rspamd management).
    *   SPF, DKIM, and DMARC config.
*   **Security & SSL:**
    *   One-click Let's Encrypt / ZeroSSL generator.
    *   Custom SSL/TLS certificate uploads.
    *   IP Blocker, Password Protected Directories, Hotlink Protection.
*   **Advanced Management:**
    *   **Cron Jobs:** Manage crontab entries with template shortcuts.
    *   **PHP Selector:** Choose PHP version per domain across all currently-supported PHP release branches (check php.net's supported-versions list at implementation time, since older branches referenced here may be EOL by launch) and edit parameters (memory_limit, upload_max_filesize) via a custom MultiPHP INI Editor. Still offer the last 1-2 EOL branches unmaintained/unpatched for legacy app compatibility, but clearly flag them as insecure in the UI.
    *   **Git Integration:** Deploy repositories directly from Github/Gitlab into public directories.
    *   **SSH Keys:** Import and generate keypairs for SSH access.
    *   **Terminal:** Secure, web-based terminal executing within the user's isolated shell context.

### C. Server Daemon (CypherAgent)
The Go-based daemon service running locally on every managed machine.
*   Runs with `root` privileges to handle OS configuration but implements strict verification for received commands.
*   **Task Executor:** Subscribes to NATS JetStream subjects to process events in the background.
*   **Config Generators:** Uses Go's `text/template` library to generate configurations for:
    *   Nginx virtual hosts (MVP default; Apache/OpenLiteSpeed adapters post-MVP)
    *   PowerDNS zone files (MVP default; BIND adapter post-MVP)
    *   Postfix / Dovecot mailboxes and domains
    *   PHP-FPM pool files
*   **Linux PAM Integration:** Handles creation of system users and configures permissions in `/home/{username}/`.
*   **SSL Issuance:** Automatically solves ACME challenges (HTTP-01 or DNS-01) using the Lego library, moving certificates to target paths and reloading web servers.
*   **Backups:** Local packagers running restic/Borg (incremental, deduplicated, chunk-level) plus db-dumps, writing to desired endpoints without locking the UI. Reserve raw tar/zip only for one-off manual export/download requests, not the scheduled backup path.

---

## 5. UI/UX Design Direction

For an open-source challenger, the UI is not cosmetic — it is the primary adoption lever. cPanel's dated, page-reload-heavy interface is one of its most common user complaints, and Plesk wins customers largely on looking cleaner. CypherUI must feel like a modern SaaS product (Vercel/Linear/Stripe-dashboard tier), not a themed legacy panel.

### Design System & Foundations
*   **Component Library:** shadcn/ui on top of Tailwind CSS and Radix UI primitives. This fits the already-chosen stack (Next.js + Tailwind + Lucide), gives accessible components out of the box, and — because shadcn components are copied into the repo rather than imported as a dependency — the design system is fully ownable and themeable without fighting a vendor's CSS.
*   **Theming:** Light and dark mode from day one (CSS variables / Tailwind theme tokens), respecting `prefers-color-scheme` with a manual toggle. Dark mode is table stakes for a developer-facing tool in this era, not a post-MVP nicety.
*   **White-Labeling:** Hosting providers and resellers must be able to rebrand the panel — logo, accent colors, product name, custom login-page domain — via config, not code forks. cPanel and Plesk both sell this; for CypherPanel it's a headline reason for an organization to adopt (their customers see *their* brand). Build the theme layer around design tokens from the start so this is a config file, not a refactor.
*   **Accessibility:** WCAG 2.1 AA as the bar — full keyboard navigability, visible focus states, correct ARIA on all interactive components (Radix primitives cover most of this), and color-contrast-safe palettes in both themes. Organizations (especially government/education hosts) increasingly require this in procurement.
*   **Internationalization:** i18n scaffolding (e.g., `next-intl`) from the first screen, even if MVP ships English-only. Hosting is a global market, retrofitting i18n across hundreds of screens is brutal, and community translations are one of the easiest ways for an open-source project to gain international contributors and users.

### Application Shell & Navigation
*   **Layout:** Persistent left sidebar (collapsible, grouped by domain area: Domains, Files, Databases, Email, Security, Advanced) + breadcrumbs — replacing cPanel's icon-grid homepage, which forces a full page navigation for every action.
*   **Command Palette:** Ctrl/Cmd+K fuzzy search across every feature, domain, database, and email account ("pma" → phpMyAdmin for db X, "ssl example.com" → cert manager). This single feature does more to make a panel feel modern than any visual styling, and it doubles as the answer to "cPanel has 100 features and I can't find any of them."
*   **Separate Admin and User shells:** WHM-equivalent (fleet/servers/packages/resellers) and cPanel-equivalent (one account's hosting) are distinct apps with distinct navigation — sharing the design system and component library, not the layout. Admins managing 200 nodes and end users managing one WordPress site have opposite information-density needs.

### Interaction Patterns
*   **No full-page reloads:** All mutations through React Query with optimistic updates where safe (DNS record edits, cron toggles) and pending states where not (account provisioning).
*   **Async job transparency:** Long-running operations (provisioning, backups, SSL issuance) already flow through the NATS job pipeline — surface them in a persistent notification center with live progress and success/failure states, streamed over WebSocket/SSE. Never leave the user staring at a spinner wondering if the backup started; this is a direct UX win over cPanel's fire-and-hope forms.
*   **Live resource meters:** Dashboard cards for disk/bandwidth/inode/CPU usage against package limits, fed from the metrics store — visible at a glance on login, since "am I near my limit?" is the single most common reason end users log into a panel.
*   **Empty states & first-run onboarding:** Every list screen designed empty-first (clear call-to-action, one-line explanation), plus a guided first-run wizard (add server → create package → create first account) for the admin shell. Open-source tools live or die on the first 10 minutes after `curl | bash`.
*   **Destructive-action safety:** Type-to-confirm for irreversible operations (terminate account, delete database, delete domain) — never a bare "Are you sure?" modal.

### Performance Budget (UI)
*   The panel must feel fast on the cheap VPSes it will actually run on: per-route code splitting (the File Manager's editor bundle must not load on the DNS page), initial route JS kept lean, virtualized tables for large lists (10k DNS records, 50k accounts in admin), and skeleton loaders over spinners. A "lightweight panel" claim is judged by UI responsiveness as much as by daemon RSS.

---

## 6. Authentication & Access Control Model

This is foundational plumbing that every feature in Section 4 sits on top of — it must be designed before Phase 1, not deferred to Phase 6 hardening.

*   **Token Model:** Short-lived JWT access tokens (~15 min) for API requests, plus a refresh token stored server-side in Redis (so it can be revoked immediately on logout/suspension — a pure stateless JWT can't be revoked before expiry). Access tokens keep the API stateless and horizontally scalable; the revocable refresh token closes the "can't kill a session" gap that comes with pure JWT.
*   **Password Storage:** Argon2id for all human-user passwords. Note this is distinct from the mTLS certificates used for CypherCore ↔ CypherAgent auth — those secure machine-to-machine traffic, not user logins, and use their own cert lifecycle.
*   **Role Model (MVP):**
    *   **Root Admin** — full WHM-equivalent control: server registration, global service control, all packages/resellers/accounts.
    *   **Reseller** — scoped to a resource pool allocated by a Root Admin; can create packages and provision/suspend/terminate accounts only within that pool.
    *   **End User** — scoped to their own single hosting account (cPanel-equivalent); no visibility into other accounts, packages, or server internals.
    *   (Post-MVP: sub-roles for team/developer collaboration per `upcoming-features.md` §6, layered on top of the End User role rather than replacing this model.)
*   **Authorization Enforcement:** Every JWT claim carries role plus the specific resource IDs the caller is scoped to (e.g., `reseller_id`, `account_id`). Authorization checks must live in a single centralized middleware/policy layer that resolves "does this caller own/have grant to this resource," not ad-hoc `if role == admin` checks scattered per handler — scattered checks are exactly where IDOR-style bugs (a reseller reading another reseller's accounts by guessing an ID) slip through at this scale.
*   **Audit Trail:** Every provisioning/suspension/permission-changing action logged with actor identity, target resource, and timestamp from Phase 1 — this can't be bolted on later without losing history, and Section 4A's reseller/account management is exactly the kind of privileged action surface that needs it from day one.

---

## 7. Security & Isolation Model

To provide safe shared hosting, CypherPanel adopts the following isolation standards:

*   **Dedicated System Users:** Every hosted account runs under its own Linux user (e.g., `cyph_user12`). Direct ownership of public directories prevents directory traversal vulnerabilities.
*   **PHP-FPM Pool Isolation:** A dedicated PHP-FPM socket (e.g., `/var/run/php/cyph_user12.sock`) is generated for each user account. Web servers pass PHP requests exclusively through this socket.
*   **cgroups v2 Sandboxing:** Systemd slices are configured for each account, enforcing limits directly in the kernel:
    ```ini
    # Example slice configuration
    [Slice]
    CPUQuota=100%
    MemoryMax=1G
    IOReadBandwidthMax=/dev/sda 10M
    ```
*   **Restricted Shell Access:** Users given shell access are mapped to custom environments or isolated using chroot/jail shells like `jailkit` or systemd-based sandboxing, preventing raw access to global system binaries.
*   **mTLS Communication:** CypherCore communicates with CypherAgents using mutual TLS (mTLS) to prevent spoofing or unauthorized controller access.

---

## 8. Scalability & Resource Efficiency Strategy

cPanel/WHM's reputation for heavy RAM usage largely comes from per-account cPanel daemons, `cpsrvd`, and legacy Perl processes running persistently even when idle. CypherPanel avoids this by design, but hitting "millions of accounts" scale and staying lighter than cPanel requires explicit targets, not just a lightweight language choice.

### Target Design Numbers (initial — revisit after Phase 2 load testing)

"Millions of users" isn't itself a design input; these are placeholder numbers to design against so Phase 1 architecture decisions (pooling, indexing, queue topology) have a concrete target instead of an abstract one. Adjust once real usage data exists:

| Metric | Phase 1-2 target (single-region MVP) | Long-term target (design must not preclude this) |
|---|---|---|
| Hosted accounts | ~50,000 | 1,000,000+ |
| CypherAgent nodes | ~200 | 10,000+ |
| CypherCore API nodes | 2-3 stateless replicas behind LB | N replicas, added without redesign |
| PostgreSQL topology | 1 primary + PgBouncer | Primary + read replicas + partitioning/Citus |
| Control-plane API latency | p95 < 200ms for CRUD ops | Same — must not degrade with account count |
| CypherAgent idle RSS | < 50MB | < 50MB (must not grow with accounts on that node) |

The important design rule this implies: nothing in Phase 1-2 should require a rewrite (not just a re-tune) to reach the long-term column — e.g., don't hardcode single-primary assumptions into query code that partitioning would later have to unwind.

*   **Stateless Control Plane:** CypherCore instances must hold no in-process session state (sessions live in Redis, not memory), so they can scale horizontally behind a load balancer with no sticky-session requirement.
*   **Database Scaling Path:** Single PostgreSQL primary + PgBouncer is sufficient for early scale. As account count grows, plan for (a) read replicas for reporting/dashboard queries, and (b) partitioning or the Citus extension for the largest tables (domains, DNS records, audit/task history) — do not defer this decision until the single primary is already the bottleneck.
*   **Time-Series Data Does Not Belong in Postgres:** Metrics (CPU/RAM/IO per account, bandwidth logs) should go to a purpose-built time-series store (Prometheus/VictoriaMetrics), not Postgres tables — this is both a performance and a resource-usage issue, since Postgres index bloat from high-cardinality metrics writes will drive up RAM/disk faster than anything else in the system.
*   **CypherAgent Memory Budget:** The per-server daemon should have an explicit idle RSS target (e.g. under ~30-50MB) and no per-hosted-account persistent process — it must react to gRPC/NATS events and generate configs on demand, not poll or keep long-lived state per user. This is the direct architectural answer to cPanel's per-account daemon overhead.
*   **Agent Fleet Communication at Scale:** With potentially thousands of CypherAgent nodes, use NATS subject partitioning per server/region rather than one global subject, and rely on JetStream clustering for message durability instead of a single broker instance.
*   **Idempotent, Retryable Jobs:** All agent-directed tasks (provisioning, SSL issuance, backups) must be idempotent with retry/dead-letter handling in the queue — at millions-of-accounts scale, some fraction of jobs will always fail transiently, and without idempotency this becomes a support burden, not just an edge case.

---

## 9. Implementation Roadmap

```
  Phase 1: Core Foundation & Agent Comms
  ├── docker-compose dev stack & cross-platform build setup (works from Windows/macOS/Linux dev machines)
  ├── Go backend scaffolding & API routing
  ├── Auth (JWT + refresh tokens), RBAC middleware & audit logging foundation (per Section 6)
  ├── Database schema migrations (PostgreSQL)
  ├── CypherAgent installation & gRPC/mTLS channel
  ├── User/Group creation & system daemon architecture
  └── Project agent skills (.claude/skills/) — write the Phase 1 batch of the full
      skills catalog (see "Project Agent Skills" below); every later phase ends by
      writing/updating its own batch

  Phase 2: Admin Plane & Provisioning
  ├── UI shell & design system foundation (shadcn/ui tokens, sidebar layout, auth screens,
  │   light/dark theming — per Section 5, so every later feature lands on finished foundations)
  ├── Server node registration
  ├── Package templating system
  ├── Account creation, suspension, and termination
  └── Automated Systemd service monitors

  Phase 3: Web Server & PHP Management
  ├── Virtual host config generators (Nginx MVP default; Apache/OpenLiteSpeed adapters deferred)
  ├── Multi-PHP installation scripts and PHP-FPM pool configs
  ├── PHP INI Editor API & user configuration files
  └── Lego ACME client integration (Let's Encrypt / ZeroSSL automation)

  Phase 4: Files, FTP, & Databases
  ├── Web File Manager (Next.js client + Go filesystem handler)
  ├── FTP Daemon configuration (Pure-FTPd virtual users)
  ├── Database provisioning APIs (MariaDB MVP default; PostgreSQL user-DB adapter deferred)
  └── phpMyAdmin and Adminer automatic setup/routing

  Phase 5: Email & DNS Servers
  ├── Postfix SMTP & Dovecot IMAP/POP3 configuration
  ├── Mail user authentication database & quotas
  ├── PowerDNS zones configuration (MVP default; BIND9 adapter deferred)
  └── DNS cluster synchronization engine

  Phase 6: Logging, Auditing, & Hardening
  ├── System metrics collecting (CPU, Memory, Disk IO)
  ├── User terminal and cron job managers
  ├── Audit log dashboards & retention policies (logging itself starts in Phase 1)
  └── Security hardening and release candidate packaging
```

### Project Agent Skills (`.claude/skills/`) — Full Catalog

Following the pattern used by Coolify (which ships `.claude/skills/` with skills like `laravel-best-practices`, `pest-testing`, and `tailwindcss-development`), CypherPanel maintains its own project skills so that AI coding agents (Claude Code and compatible tools) working in this repo automatically follow its conventions instead of generic defaults. Each skill is a directory containing a `SKILL.md` (instructions + decision rules), optionally with `references/` files for templates and examples.

This is the catalog for the **entire project**. Timing rule: each skill is written **at the end of the phase that establishes its conventions** (a skill written before the pattern it describes exists in code is speculation, not guidance), so it is ready on day one of the phases that consume it. Skills are living documents — when a later phase extends a pattern, it updates the corresponding skill in the same PR (e.g., the post-MVP Apache adapter updates `agent-config-generators`).

#### Written at end of Phase 1 (used by every later phase)

1.  **`go-backend-conventions`** — How Go code is written in this repo: Gin handler/middleware structure, error-wrapping style, the central config/defaults package, the **no-hardcoded-paths rule** (`filepath.Join` or config only), `CGO_ENABLED=0`, and the `_linux.go` build-tag pattern that keeps Linux-only syscall code behind interfaces so everything else stays unit-testable on Windows/macOS.
2.  **`database-and-migrations`** — pgx/sqlx hand-written SQL patterns (explicitly: **no GORM**), query/scan conventions, how to create and apply `golang-migrate` migrations (paired `.up.sql`/`.down.sql`, never editing a shipped migration), and indexing expectations for tables that will reach millions of rows.
3.  **`grpc-proto-contracts`** — Rules for evolving the Core↔Agent `.proto` files: never reuse or renumber a shipped field, only add optional fields, regeneration workflow, and the mTLS/versioning assumptions that let mixed Core/Agent versions coexist during rolling upgrades.
4.  **`jobs-and-agent-tasks`** — How to add a NATS JetStream job type: subject naming/partitioning, the idempotency requirement (every task must be safely re-runnable), retry/dead-letter handling, and reporting results back through `ReportTaskResult`. Used by every phase that adds an agent-executed operation (provisioning, SSL, backups, mail, DNS).
5.  **`auth-and-rbac`** — How to secure a new endpoint: JWT claim structure, the centralized policy middleware (never ad-hoc `if role == admin` checks in handlers), resource-scoping rules (reseller/account ownership), and when an action must write to the audit trail (any provisioning/suspension/permission change).
6.  **`api-contract-workflow`** — Adding/changing a REST endpoint end to end: `/api/v1` versioning rules, OpenAPI annotations so the spec stays generated (never hand-edited), and regenerating the TypeScript client for CypherUI so frontend and backend cannot drift.
7.  **`testing-conventions`** — Table-driven Go tests, what runs natively on any dev OS vs. what requires the docker-compose stack or a Linux container (agent E2E), and the expectation that platform-neutral logic is tested without Linux.
8.  **`linux-system-integration`** — Creating system users/groups, systemd unit and slice management, cgroups v2 limit enforcement, and the per-distro path-mapping layer (Debian vs. RHEL-family locations) — the conventions established by Phase 1's user/group creation work and reused by every phase that touches the OS.

#### Written at end of Phase 2 (UI foundations + provisioning exist)

9.  **`ui-development`** — shadcn/ui component usage (copied into repo, themed via design tokens — never inline colors, so white-labeling stays a config file), Tailwind token conventions, light/dark theming, React Query data-fetching patterns (no raw `fetch` in components), Lucide icons, i18n string extraction (`next-intl` — no hardcoded UI strings), and the accessibility bar (WCAG 2.1 AA, keyboard nav, focus states). Consumed by every feature screen in Phases 3-6.
10. **`async-ui-patterns`** — How UI surfaces long-running jobs: wiring a mutation to the NATS job pipeline, live progress via WebSocket/SSE into the notification center, optimistic updates vs. pending states, empty-state and skeleton-loader conventions, and type-to-confirm for destructive actions.

#### Written at end of Phase 3 (config generation + SSL patterns exist)

11. **`agent-config-generators`** — Adding a service config generator: Go `text/template` conventions, template testing (golden files), resolving output paths through the distro path-mapping layer, validate-then-reload sequencing (e.g., `nginx -t` before reload, never blind restarts), and the adapter interface that post-MVP alternatives (Apache, OpenLiteSpeed, BIND) must implement. Consumed heavily by Phases 4-5 (FTP, mail, DNS configs).
12. **`php-runtime-management`** — Multi-PHP install layout, per-account PHP-FPM pool file conventions and socket isolation, INI override handling, and how EOL PHP branches are flagged.
13. **`ssl-acme`** — Lego library usage: HTTP-01 vs. DNS-01 challenge selection, certificate storage paths and permissions, renewal job scheduling, and web-server reload coordination after issuance.

#### Written at end of Phase 4 (file/database surfaces exist)

14. **`filesystem-operations-safety`** — The rules for any code that touches user files (File Manager, FTP, backups): path-traversal prevention (canonicalize + verify under the account root), operating with the account user's privileges (never root), quota/inode accounting, and safe handling of uploads, archives (zip-slip), and symlinks.
15. **`user-database-provisioning`** — MariaDB database/user/grant provisioning conventions, credential generation and storage, phpMyAdmin/Adminer session handoff, and the adapter interface PostgreSQL user-DBs will implement post-MVP.

#### Written at end of Phase 5 (mail + DNS stacks exist)

16. **`mail-stack`** — Postfix/Dovecot config conventions, virtual mailbox/domain layout, quota enforcement, Rspamd integration points, and DKIM/SPF/DMARC record generation.
17. **`dns-management`** — PowerDNS REST API usage patterns, zone/record CRUD conventions, validation rules per record type (A, AAAA, CNAME, MX, TXT, SRV, CAA), and primary/secondary cluster synchronization.

#### Written during Phase 6 (hardening & release)

18. **`observability-and-metrics`** — What goes to the time-series store (Prometheus/VictoriaMetrics) vs. Postgres (never high-cardinality metrics in Postgres), metric naming conventions, structured logging fields, and audit-log query/retention patterns.
19. **`backups`** — restic/Borg invocation conventions (incremental/deduplicated — raw tar only for one-off manual exports), endpoint configuration (S3/SFTP/local), db-dump coordination, and restore-path testing requirements.
20. **`installer-and-packaging`** — Shell installer conventions from Appendix A: GPG-verify every artifact, LF-only line endings, detect-and-ask (never silently purge conflicting services), no auto-reboot, release tiers (`stable`/`edge`), working uninstaller, and the 1GB-RAM install target.

---

## 10. Licensing & Contribution

*   **License:** Apache-2.0 — pick one license, not "or MIT". Apache-2.0 is the better default for infrastructure software: it includes an explicit patent grant, which matters more here than for a typical app since hosting-provider adopters and contributors are more exposed to patent risk than end users.
*   **Governance:** Open-source project governed by community pull requests. All development occurs in public repositories, with dedicated tests and documentation.

---

## Appendix A: Findings from cPanel's Official Installer (analyzed 2026-07)

Extracted and reviewed cPanel's `securedownloads.cpanel.net/latest` bootstrap (Makeself archive, installer v00187). What it confirms, and the lessons it sets for CypherPanel's installer:

*   **Their stack (validates our choices):** Exim + Dovecot for mail (they use Exim; we chose Postfix — both fine), **PowerDNS or BIND** for DNS (PowerDNS is their modern option — matches our MVP default), MySQL/MariaDB with a version compatibility matrix (matches our MariaDB default), Apache via EasyApache repos (we differentiate with Nginx).
*   **Their weight is structural:** the installer's first act is downloading a private cPanel-only Perl runtime into `/usr/local/cpanel/3rdparty/` — an entire second Perl ecosystem — and the installer hard-fails below **2GB RAM**. A Go static binary needs neither; "installs where cPanel refuses to" (1GB VPSes) is a marketable claim worth protecting in CI (e.g., an install test on a 1GB VM).
*   **Installer lessons (what to copy):** GPG-verify every downloaded artifact (they do, via a fetched signing key); checksum the self-extracting payload; support named release tiers (`stable`/`edge`) and a pinned-version flag; provide `--noexec`-style inspection flags so admins can audit before running.
*   **Installer lessons (what to avoid):** their installer force-purges conflicting OS packages (`rpm -e --nodeps`), disables systemd-resolved, kills unattended-upgrades, may auto-reboot, requires a fresh OS, and has no uninstaller. CypherPanel's installer must instead: detect conflicting services and **ask** (or require an explicit `--take-over` flag) rather than silently purging; never auto-reboot; and ship a working uninstaller from the first release — "you can actually remove it" is a genuine differentiator in this market.
*   **No forced bundling:** cPanel auto-installs commercial add-ons (Imunify360, ImunifyAV, WordPress Toolkit, CloudLinux conversion) unless explicitly skipped. CypherPanel add-ons must be opt-in, not opt-out.

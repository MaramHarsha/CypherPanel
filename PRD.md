# CypherPanel — Product Requirements Document

> This is the product-level view: what we're building, for whom, and what it must do. For the full technical architecture see [Architecture.md](Architecture.md), for AI/contributor boundaries see [Rules.md](Rules.md), for visual direction see [Design.md](Design.md), and for the deep original design rationale see [plan.md](plan.md) (post-MVP detail in [upcoming-features.md](upcoming-features.md)).

## 1. What We're Building

CypherPanel is an open-source, self-hosted web hosting control panel — a direct, feature-complete replacement for **cPanel/WHM**, built to run comfortably on hardware cPanel's own installer refuses (cPanel hard-fails below 2GB RAM; CypherPanel's per-server agent targets **<50MB idle RSS**, currently measured at **3.6MB** under load).

It ships as three cooperating pieces:

1. **CypherCore** — the central control plane (API, auth, scheduling, database).
2. **CypherUI** — the web dashboards (admin/WHM-equivalent and end-user/cPanel-equivalent).
3. **CypherAgent** — a lightweight daemon installed on every managed server that actually creates vhosts, databases, mailboxes, DNS zones, etc.

## 2. Vision & Philosophy

- **API-first.** Every UI feature is powered by a documented REST/gRPC API — no UI-only logic. This is what makes the CLI, SDKs, webhooks, and (post-MVP) MCP server nearly free to add later.
- **Modular, swappable services.** Nginx, PowerDNS, MariaDB, Postfix/Dovecot are the MVP defaults, not permanent locks — Apache/OpenLiteSpeed, BIND9, PostgreSQL-for-user-DBs, and ProFTPD ship later behind the same internal adapter interface.
- **Host isolation by default.** Per-account Linux users, per-account PHP-FPM pools, cgroups v2 resource limits — safe shared hosting is a property of the architecture, not a bolt-on.
- **One-command install, distro-agnostic.** Single shell installer, native support for RHEL-family (Rocky/Alma) and Debian/Ubuntu.
- **Modern UI as an adoption lever, not decoration.** cPanel's dated, reload-heavy UI is one of its most-cited complaints. CypherUI must feel like a modern SaaS dashboard (Coolify/Linear/Vercel tier), not a themed legacy panel — see [Design.md](Design.md).

## 3. Target Users

| User | What they need from CypherPanel |
|---|---|
| **Shared hosting providers / VPS admins** | WHM-equivalent fleet management: register servers, define packages, provision/suspend/terminate accounts at scale (target: millions of accounts, thousands of agent nodes). |
| **Resellers** | A scoped slice of a provider's resource pool — create packages and manage accounts only within their allocation, with no visibility outside it. |
| **Agencies managing many client sites** | Bulk-friendly account/domain management, and (post-MVP) declarative GitOps hosting + a Terraform provider so 200 client sites aren't managed by hand-clicking. |
| **Individual end users / site owners** | cPanel-equivalent single-account portal: domains, files, databases, email, DNS, SSL, cron, PHP version — everything needed to run one site without touching server internals. |
| **Developers** | Git-based deploys, SSH access, web terminal, (post-MVP) one-click app runtimes for Node/Python/Go/Rust, and a local dev-sync CLI (`cypher pull`/`cypher push`). |
| **Third-party integrators** | Billing platforms (WHMCS/Blesta/HostBill), monitoring (Prometheus/Grafana), and — uniquely — LLM agents via a first-class MCP server (post-MVP). |

## 4. MVP Feature Set

### A. Admin Panel (WHM-equivalent)
- Server (CypherAgent node) registration, monitoring, configuration
- Package templates: disk (MB + inodes), bandwidth, domain/database/email counts, CPU/memory/connection limits (cgroup-enforced)
- Account provisioning, suspension, termination
- Reseller resource-pool allocation and scoping
- Global service control (Nginx, Postfix, Dovecot, PowerDNS, MariaDB)
- Global SSL certificate management (auto-renewing)
- DNS cluster (primary/secondary) sync setup
- Backup policy scheduling (incremental/deduplicated via restic/Borg — never full re-archiving on every run)
- Metrics dashboards + audit log with retention policy
- Cron manager, version/upgrade framework, install/uninstall scripts

### B. User Panel (cPanel-equivalent)
- Domain management: addon domains, subdomains, aliases/parked domains, redirects
- Full DNS zone editor (A, AAAA, CNAME, MX, TXT, SRV, CAA)
- Web-based File Manager (drag-and-drop, ZIP/UNZIP, inline editor) + FTP accounts
- Database management (MariaDB) + one-click phpMyAdmin/Adminer
- Email: accounts, quotas, forwarders, autoresponders, filters, spam control, SPF/DKIM/DMARC
- SSL: one-click Let's Encrypt/ZeroSSL, custom cert upload, IP blocker, protected directories, hotlink protection
- Cron job manager, MultiPHP selector + INI editor, Git deploy, SSH key management, web terminal

### C. Cross-Cutting (foundational, not optional)
- Auth: Argon2id passwords, short-lived JWT + revocable Redis-backed refresh tokens, 3 MVP roles (root_admin / reseller / end_user)
- Centralized authorization policy layer (no ad-hoc `if role == admin` checks)
- Audit trail on every provisioning/suspension/permission-changing action, from Phase 1
- mTLS between CypherCore and every CypherAgent

Full phase-by-phase build order lives in `plan.md` §9; current build status lives in [Memory.md](Memory.md) and `task.md`.

## 5. Post-MVP Roadmap (see `upcoming-features.md` for full detail)

Modernization features: Docker/app runtimes + Git-ops auto-deploy, Coraza WAF + malware scanning, eBPF telemetry + Prometheus/Grafana, built-in billing (CypherBilling) + WHMCS module, Cloudflare DNS sync + external mail relays, WordPress staging + team RBAC, plus deferred stack adapters (Apache/OpenLiteSpeed, BIND9, ProFTPD, Postgres-for-user-DBs).

**Differentiators — verified to exist in *no* competing panel (open-source or commercial), market-checked 2026-07:**
- Built-in MCP server (manage hosting from an LLM client) + log-reading diagnostics copilot
- Declarative GitOps hosting + an official Terraform provider (desired-state reconciliation for accounts/domains/DNS/mail/cron)
- DMARC (RUA) report ingestion + deliverability/RBL dashboard
- Zero-downtime live account migration between agent nodes (proxy-based cutover)
- Atomic Git deployments with instant rollback (symlink-flip releases)
- Built-in uptime monitoring + auto-generated public status pages
- Per-account carbon/energy footprint reporting
- Local dev-sync CLI (`cypher pull`/`cypher push`)
- Passkey-first auth + CypherCore as an OIDC provider (SSO across webmail/phpMyAdmin/panel)

These are the strongest long-term moat and should inform which post-MVP work gets prioritized first (suggested order in `upcoming-features.md` §8).

## 6. Non-Goals (for now)

- A hosted plugin marketplace (architecture reserves the extension points; the marketplace product itself is post-MVP)
- Multi-region/multi-fleet control planes (design must not preclude it — see `plan.md` §17 — but it isn't built for MVP)
- A second, hand-maintained parallel API client for CLI/SDKs (everything generates from the OpenAPI spec)
- Bundling any commercial-style add-on by default — every add-on (billing, security suites, integrations) is opt-in, never opt-out (the anti-pattern CypherPanel explicitly rejects from cPanel's own installer, see `plan.md` Appendix A)

## 7. Success Criteria

- Installs and runs a real hosting workload on a 1GB RAM VPS (cPanel's installer refuses below 2GB)
- CypherAgent idle RSS stays under 50MB regardless of account count on that node
- Control-plane API p95 latency < 200ms for CRUD ops, unaffected by total account count
- A new admin can go from `curl | bash` to a provisioned first account in a guided first-run wizard, no docs required
- Every MVP feature above is reachable through the documented REST API, not just the UI

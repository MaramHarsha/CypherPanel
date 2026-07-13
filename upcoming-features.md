# CypherPanel: Upcoming Features (Post-MVP Roadmap)

Once the core MVP reaches feature parity with standard cPanel/WHM configurations, CypherPanel will focus on modernizing the hosting experience. Below are the planned post-MVP features aimed at developers, agency owners, and large-scale hosters.

> This document inherits two rules from `plan.md`: the **Version Policy** (verify latest stable versions of every tool online at implementation time) and the **opt-in principle** from Appendix A (every add-on below — billing, security suites, integrations — installs only when the operator explicitly enables it; never bundled by default the way cPanel bundles Imunify/WP Toolkit).

---

## 1. Modern Application Engine (Containerization)

Unlike legacy control panels that focus exclusively on PHP, CypherPanel plans to offer native application runtimes:

*   **Docker Container Runner:** A user-friendly dashboard interface allowing users to deploy, update, and manage containerized services (e.g., custom PostgreSQL instances, Redis, Node/Python applications) within their account limits.
*   **Multi-Language App Manager:** One-click configurations and reverse proxies for:
    *   **Node.js** (Next.js, Express, NestJS)
    *   **Python** (Django, FastAPI, Flask)
    *   **Go** and **Rust** binary runtimes
*   **Git-Ops / Auto-Deployment:** Git Webhook listeners that automatically pull latest changes, install dependencies (e.g., `npm install`, `pip install`), and reload processes upon pushes to specific branches (e.g., `main`, `staging`).

---

## 2. Advanced Security & Threat Intelligence

*   **Coraza WAF Integration:** A toggleable Web Application Firewall interface with pre-configured OWASP Core Rule Sets (CRS) to block SQL injections, XSS, and local file inclusions. Coraza is the default (Go-native — fits the stack, actively maintained under OWASP); legacy ModSecurity supported only as a compatibility option for operators migrating existing rule sets.
*   **Automated Malware Scanner:** Weekly background scans of public directories utilizing ClamAV and Maldet (LMD), with email notifications and automated quarantines of infected scripts. **Resource constraint:** run as scheduled batch scans (cgroup-capped, off-peak) — do *not* keep the resident `clamd` daemon running, since it holds the full signature database (1GB+) in RAM permanently, which alone would break the low-resource-footprint promise on small servers.
*   **Jailed SSH Sandbox:** Ephemeral, isolated SSH containers for terminal users using Linux Namespaces and `chroot`/`jailkit` to completely isolate the user filesystem from the host OS.
*   **Brute-Force Shield:** Advanced rate-limiting integrated directly with Nginx/nftables (a modern, lightweight alternative to Fail2ban) blocking malicious IPs before they hit user processes.

---

## 3. High-Performance Observability & Analytics

*   **eBPF-Based Telemetry:** Real-time resource usage monitoring (CPU, RAM, Disk I/O, Network traffic) mapped directly to individual processes and file handles using eBPF, showing users exactly which PHP script or database query is causing high loads.
*   **Prometheus & Grafana Integration:** Ready-to-go endpoints for host operators to hook their global monitoring clusters into CypherCore.
*   **Nginx Access Log Analyzer:** A built-in dashboard parsing Nginx traffic logs to visualize unique visitors, geo-location, HTTP response codes, and bandwidth usage without requiring third-party scripts.

---

## 4. Built-in Billing & Reselling Core

*   **CypherBilling Module:** An integrated billing and client management system that handles:
    *   Hosting packages sales.
    *   Automatic invoicing (PDF generation).
    *   Payment gateway integrations (Stripe, PayPal, Mollie).
    *   Automated account suspension/termination on unpaid invoices.
*   **WHMCS Provisioning Module:** An officially maintained, open-source provisioning module to allow standard hosting companies to hook WHMCS directly into the CypherCore API — meeting the industry where it already is, since most established hosts won't drop WHMCS for CypherBilling on day one.

---

## 5. Cloud Integration & DNS Automation

*   **Cloudflare DNS Sync:** An API bridge that automatically pushes zone updates to Cloudflare DNS and manages Cloudflare proxy ("Orange Cloud") status directly from the user panel.
*   **External Mail Relays:** Quota-based configurations to allow users to proxy outgoing mail through third-party relays (SendGrid, Mailgun, AWS SES, Mailerlite) to bypass IP blacklisting issues common on VPS hosts.
*   **Hybrid Cloud Backups:** Extended destination support for the restic/Borg backup engine defined in `plan.md` (which natively covers S3/B2/SFTP), using `rclone` as an additional *transport backend* for destinations restic doesn't speak natively (Google Drive, Dropbox, OneDrive, etc.). rclone is the transport layer here, not the backup engine — incremental/deduplicated snapshots remain the strategy everywhere.

---

## 6. One-Click Staging & Collaboration

*   **WordPress Staging Manager:** One-click cloning of live WordPress sites to a private subdomain (e.g., `staging.example.com`), allowing users to test plugins or themes and merge database/file updates back to production.
*   **Developer Teams & Collaboration:** RBAC (Role-Based Access Control) for user panels, allowing site owners to invite developers, database admins, or billing managers with granular, limited permissions. Per `plan.md` Section 6, these sub-roles layer on top of the End User role in the existing auth model rather than replacing it.

---

## 7. Deferred Stack Adapters (from MVP scope)

`plan.md` ships the MVP against one default per service category; these adapters complete the modular-architecture promise afterward. Each implements the same internal interface as its MVP default, so adding one never touches feature code:

*   **Web servers:** Apache and OpenLiteSpeed adapters (virtual-host config generators + service control), alongside the Nginx default.
*   **DNS:** BIND9 adapter (zone-file generation + sync), alongside the PowerDNS default.
*   **FTP:** ProFTPD adapter, alongside the Pure-FTPd default.
*   **User databases:** PostgreSQL provisioning for end-user databases, alongside the MariaDB default (the control plane's own PostgreSQL is unrelated and ships in MVP).
*   **Message queue:** documented RabbitMQ deployment option for operators who already run it (NATS JetStream remains the default install).

Prioritize these by actual user demand after launch rather than building speculatively — each adapter is real ongoing maintenance surface (config templates, version tracking, test matrix).

---

## 8. Differentiators: Features No Existing Panel Ships (market-checked 2026-07)

Sections 1–6 modernize the hosting experience, but most of them exist *somewhere* (Docker/Git-ops → Coolify/Dokploy/CapRover; staging & team RBAC → WP Toolkit/RunCloud; Cloudflare sync → Plesk/CyberPanel extensions). The features below were checked against cPanel, Plesk, and the open-source field (HestiaCP, CyberPanel, ISPConfig, Virtualmin, CloudPanel, aaPanel, Froxlor, Coolify, Dokploy, CapRover) and exist in **none of them** — this is greenfield territory:

*   **AI-Agent-Native Panel (built-in MCP server):** Expose the entire CypherCore API as a first-class MCP (Model Context Protocol) server so users can manage hosting from Claude, ChatGPT, or any LLM client ("create a staging subdomain and clone my site there"). Plesk shipped a *proprietary extension* for this in 2025; no open-source panel has it at all, and none has it built into the core. Being API-first makes this nearly free to implement — the MCP layer is a thin wrapper over the already-documented REST API. Pair it with a **diagnostics copilot** that reads Nginx/PHP-FPM/mail logs and explains failures in plain language.
*   **Declarative GitOps Hosting + Terraform Provider:** Define accounts, domains, DNS zones, mail, and cron in versioned YAML; CypherCore continuously reconciles actual state to desired state (Kubernetes-style). Ship an official Terraform provider on top of the same API. Verified: no Terraform provider exists for cPanel/Plesk account management, and no panel anywhere does desired-state reconciliation — agencies managing 200 client sites currently click through UIs or write brittle API scripts. This is the single strongest "no one else has this" feature for the professional market.
*   **DMARC Report Ingestion & Deliverability Dashboard:** cPanel/Plesk only *check* that SPF/DKIM/DMARC DNS records exist. Nobody in-panel ingests the aggregate (RUA) XML reports, visualizes who is sending as your domain and what's failing, or monitors the server IP against RBL blacklists with alerts — users pay EasyDMARC/Valimail/MXToolbox for exactly this. A self-hosted deliverability center is a killer feature for the VPS mail crowd.
*   **Zero-Downtime Live Account Migration:** Move an account between CypherAgent nodes with no downtime: sync files/DB incrementally (restic/replication), then cut over via a temporary reverse-proxy on the old node while DNS TTL drains. cPanel/Plesk transfers all incur a downtime window; no panel does proxy-based live cutover.
*   **Atomic Deployments with Instant Rollback:** Every Git deploy lands in a timestamped release directory with a symlink flip (Capistrano/Envoyer-style), giving one-click rollback to any previous release. Exists only in closed SaaS tools (RunCloud, Ploi) — no open-source panel has it. Natural extension of §1's Git-Ops listener.
*   **Built-in Uptime Monitoring + Public Status Pages:** Synthetic HTTP/keyword/SSL-expiry checks per site with alerting, plus an auto-generated public status page per account (`status.example.com`). cPanel bundles the *commercial* 360 Monitoring for this; no open-source panel has native uptime checks, and no panel anywhere generates status pages.
*   **Per-Account Carbon & Energy Footprint:** Estimate energy/CO₂ per account from the eBPF/cgroup metrics already collected (§3) — CPU-seconds × node power profile. No hosting panel on the market reports sustainability metrics, while EU hosts increasingly need them for ESG/CSRD reporting. Cheap to build once §3's telemetry exists.
*   **Local Dev Sync CLI:** `cypher pull example.com` clones a live site (files + DB, with URL rewriting) into a local Docker/DDEV environment; `cypher push` deploys back through the staging pipeline. Panel-to-laptop round-tripping exists nowhere — WP Toolkit stops at server-side staging.
*   **Passkey-First Auth & Panel as SSO Provider:** WebAuthn/passkey login as the default (not bolted-on TOTP), and CypherCore acting as an OIDC identity provider so webmail, phpMyAdmin, and user-panel logins are one passwordless identity. cPanel/Plesk and all open-source panels remain password+TOTP only.

Suggested build order by effort-to-differentiation ratio: **MCP server** (thin layer over existing API) → **atomic deploys** (extends §1) → **DMARC dashboard** → **uptime/status pages** → **GitOps reconciler + Terraform provider** (biggest lift, biggest moat) → the rest.

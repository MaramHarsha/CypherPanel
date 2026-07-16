# CypherPanel — Memory (Living Progress Log)

> Purpose: a fast-loading "where things stand" snapshot so a new chat/session doesn't waste tokens re-reading the whole codebase or, worse, guessing. This is the *condensed* current-state view — the granular per-item checklist is `task.md`, the requirements are [PRD.md](PRD.md), the system design is [Architecture.md](Architecture.md). **Update this file whenever a phase's status changes or a significant architectural decision is made** — it should never drift more than a few days behind reality.

**Last verified against repo state: 2026-07-16.**

## Where we are, in one paragraph

All six MVP phases (per `plan.md` §9 / `task.md`) are **complete and E2E-verified**: Phase 1 (core/agent/mTLS/NATS), Phase 2 (provisioning, RBAC/reseller scoping, service control, UI shell), Phase 3 (Nginx/PHP-FPM per account, MultiPHP, SSL + DNS-01 wildcard renewal), Phase 4 (MariaDB DBs, Pure-FTPd, File Manager, Adminer), Phase 5 (PowerDNS zone editor + DNS cluster sync, Postfix/Dovecot virtual mailboxes + MX/SPF/DMARC), Phase 6 (Prometheus metrics + Metrics API, audit dashboard + retention, cron manager, version/upgrade framework, install/uninstall scripts, CI security+UI+install-test tiers, auth rate-limiting + security headers, file quotas + zip-slip-safe extract, **web terminal** via PTY-over-NATS + WebSocket + xterm.js). 26 migrations applied. Git has **20+ real commits** on `main`, remote `origin` = `git@github.com:MaramHarsha/CypherPanel-Dev.git`.

The project is now past MVP-complete and into: (a) closing the short list of minor remaining items below, and (b) starting to plan post-MVP work from `upcoming-features.md`, prioritizing the differentiator features (PRD.md §5) that no competing panel has.

## Remaining minor items (not yet done)

- Inode-count quota (byte quota already done)
- DKIM signer + Rspamd for mail (SPF/DMARC/MX already auto-published)
- NATS server-side auth config in the installer (client-side creds support already exists)
- Distributed (Redis-backed) rate limiter for multi-instance Core (current limiter is in-memory, single-instance)

## Locked architecture decisions (don't re-litigate these)

- Go + Gin + pgx/sqlx (no GORM) · PostgreSQL + PgBouncer · Redis · NATS JetStream (not RabbitMQ) · Next.js + shadcn/ui · Apache-2.0 license
- MVP service defaults: Nginx / PowerDNS / MariaDB / Postfix+Dovecot / Pure-FTPd — everything else is a post-MVP adapter behind the same interface
- Auth: Argon2id + 15-min JWT + single-use Redis refresh tokens; 3 roles (root_admin / reseller / end_user); audit log from Phase 1
- No hardcoded filesystem paths anywhere — `internal/paths` distro-mapping layer + env config; `CGO_ENABLED=0` cross-compile
- Secret handling: agent generates DB/FTP passwords → returned as result metadata → AES-GCM encrypted (`internal/secretcrypt`); mail passwords bcrypt-hashed in Core; nothing plaintext ever sits in a task payload
- Version policy: every tool/library version mentioned in any doc is "verify latest stable at implementation time," never a pin

Full rationale for all of the above: `plan.md`. Day-to-day boundaries derived from it: [Rules.md](Rules.md).

## Design/UX research on file

Cloned 7 open-source competitor panels (HestiaCP, CyberPanel, CloudPanel CE, Froxlor, Ajenti, Webmin, VestaCP) into `Website-references/` (gitignored, reference-only, not part of the product) to study dashboard navigation UX and backend architecture patterns. Key takeaways already folded into [Design.md](Design.md) §5 ("Navigation anti-patterns to actively avoid") — command palette as the escape hatch for deep feature sets, no flat 20+-item grids, no icon-only nav, no settings buried in collapsed toggles. Full findings in Claude's project memory (`competitor-panel-ux-research`) if deeper detail is needed.

## House-keeping notes for whoever picks this up next

- `.claude/skills/` has the full 23-skill catalog already written (done upfront at the user's request, ahead of the normal per-phase timing) — skills for already-shipped phases are code-grounded; a few Phase 6 skills (`backups`, `public-interfaces`) are still `Status: design-intent` and should be re-verified once their post-MVP work actually lands.
- Keep `plan.md` ⟷ `upcoming-features.md` ⟷ this file ⟷ `task.md` consistent when any one changes — they're intentionally layered (deep rationale → post-MVP roadmap → fast-context snapshot → granular checklist), not independent documents.
- User develops on Windows; product targets Linux servers only — don't assume a Linux dev shell when suggesting commands.

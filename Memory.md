# CypherPanel — Memory (Living Progress Log)

> Purpose: a fast-loading "where things stand" snapshot so a new chat/session doesn't waste tokens re-reading the whole codebase or, worse, guessing. This is the *condensed* current-state view — the granular per-item checklist is `task.md`, the requirements are [PRD.md](PRD.md), the system design is [Architecture.md](Architecture.md). **Update this file whenever a phase's status changes or a significant architectural decision is made** — it should never drift more than a few days behind reality.

**Last verified against repo state: 2026-07-16 (post command-palette + option-UX pass + modal-to-page architecture change).**

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

## Command palette shipped (2026-07-16)

Acted on Design.md §5's headline recommendation: `web/components/command-palette.tsx` (shadcn `command`/cmdk), Ctrl/Cmd+K + header search button, fuzzy-searches nav (now shared between sidebar and palette via `web/lib/nav.ts`) plus live accounts/servers. Quick actions deep-link with `?new=1` (auto-opens the create dialog). Account selection originally deep-linked to `?highlight=<id>` (scroll + CSS-flash the row) — **superseded** by the modal-to-page change below, which gave every account a real page to land on instead; the palette now navigates straight to `/accounts/{id}`.

**While E2E-verifying this with a throwaway headless-Playwright script, found and fixed a real, pre-existing bug**, unrelated to the palette itself but exposed by it: `web/lib/api.ts`'s `tryRefresh()` had no in-flight de-duplication. On any hard page load with no in-memory access token (a bookmarked/shared link — exactly what the new deep links produce), every query on the page 401s at once and each independently tried to redeem the same single-use refresh token; only the first succeeded, the rest failed permanently, and the page hung on skeleton loaders forever. Fixed with a shared in-flight promise so concurrent 401s coalesce into one refresh call. Worth knowing: **this class of bug (multiple simultaneous 401s after a cold load) can recur** if new code fires several independent queries on mount without going through the now-deduped `tryRefresh` — it's a general property of the auth layer, not something scoped to one page.

Full detail + verification transcript: `task.md` Phase 2. No new files were committed to `git` by this work (see repo hygiene note above — commits only happen when the user asks); `@playwright/test` was installed only to manually verify in a real browser and was uninstalled again afterward — it is *not* a project dependency. `cmdk` **is** a real new runtime dependency (`package.json`), needed by the shadcn `command` component.

## Option-selection UX pass shipped (2026-07-16)

The user's original complaint ("options are hard to select, don't want clumsy UI") was about individual settings/forms, not just navigation — a narrower zoom level than the command-palette work above. Researched how the same 7 cloned competitor panels present options at the *field* level (not nav) and rewrote CypherPanel's worst offenders: `php-settings-dialog.tsx` (plain-language labels + grouped sections + a real tri-state control instead of a free-text "On/Off" field), `dns-dialog.tsx` (persistent labels, human TTL presets, record-type-adaptive value hints), `cron-dialog.tsx` (plain-language schedule presets that insert into the raw crontab — independently reinvented by 3 of the 5 relevant panels researched), `packages/page.tsx` (rendered a hint string that existed in code but was never displayed, added unit suffixes + an explicit "Unlimited" `Switch`).

**Two more real bugs found while E2E-verifying with Playwright, both fixed:** Base UI's `Select.Value` renders the raw stored `value` unless given a `children` mapping function — this silently affected the **account-creation dialog's Server/Package selects**, which stored each row's UUID as the value, so picking a server showed a raw ID in the trigger afterward instead of its name. This predates the option-UX pass entirely and is a correctness bug in the app's most important workflow, not a style nit — check any *other* `<SelectValue>` usage the same way if one gets added without a `children` fn and the raw value isn't itself the desired display text (the DNS record-type select is fine as-is, since there the raw code *is* the display).

`components/ui/switch.tsx` was added via the shadcn CLI (like `command` before it) — real new UI primitive, no new npm dependency (Base UI's Switch is part of the already-installed `@base-ui/react`).

## Modal-to-page architecture change shipped (2026-07-16)

Explicit user request: they wanted CypherPanel's per-account features (Mail, Databases, FTP, DNS, Cron, PHP Settings) to work like classic cPanel and the other open-source panels researched — a dedicated page per feature reached from a big labeled icon, not a modal dialog opened from a tiny icon button buried in a dense admin table. They were also explicit that they did **not** want this to become a "WHM thing" — i.e. don't blend fleet-admin actions (suspend/terminate) into the per-account cPanel-style experience.

What changed:
- The 6 per-account dialogs are gone; their logic now lives at `/accounts/[id]/{mail,databases,ftp,dns,cron,php}/page.tsx`, following the same convention Files/Terminal already used.
- New `/accounts/[id]/page.tsx` is the per-account "home" — a cPanel-style icon grid (big tiles, not tiny icons) linking to each feature page, plus an inline SSL status/issue card. This is the thing the command palette and the admin table's "Manage" link both point to now.
- `web/app/(shell)/accounts/use-account.ts` — small shared hook (`listAccounts()` + find-by-id) so every per-account page gets username/domain/status without a new backend endpoint. There is still no single-account GET; if that ever becomes a real bottleneck (large fleets, cache staleness), that's the point to add one — not before.
- The admin Accounts table went from ~10 icons per row down to one "Manage" link. Suspend/Unsuspend/Terminate/SSL-issue **deliberately stayed in the admin table** (WHM-level actions, matching real cPanel/WHM's own split) rather than moving onto the per-account pages.
- Removed the now-dead `?highlight=` mechanism (`accounts/page.tsx` state/effects + `.highlight-row` in `globals.css`) rather than leave it unreachable — nothing links to it anymore now that accounts have a real page to land on.

Full detail + verification transcript: `task.md` Phase 2. E2E-verified with headless Playwright (hub page renders the grid with correct conditional Terminal tile, all 6 converted pages load and their create/list/delete flows work, palette → account lands on the hub, Files back-link resolves to the hub not the fleet list).

## House-keeping notes for whoever picks this up next

- `.claude/skills/` has the full 23-skill catalog already written (done upfront at the user's request, ahead of the normal per-phase timing) — skills for already-shipped phases are code-grounded; a few Phase 6 skills (`backups`, `public-interfaces`) are still `Status: design-intent` and should be re-verified once their post-MVP work actually lands.
- Keep `plan.md` ⟷ `upcoming-features.md` ⟷ this file ⟷ `task.md` consistent when any one changes — they're intentionally layered (deep rationale → post-MVP roadmap → fast-context snapshot → granular checklist), not independent documents.
- User develops on Windows; product targets Linux servers only — don't assume a Linux dev shell when suggesting commands.

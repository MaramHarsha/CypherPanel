# CypherPanel — Design

> Visual/UX direction for CypherUI. For component/theming implementation conventions see `.claude/skills/ui-development`; for the product rationale behind these choices see `plan.md` §5. This file is the single source of truth for brand tokens — if a component hardcodes a color instead of using a token here, that's a bug (see [Rules.md](Rules.md) §2.6).

## 1. Design Philosophy

For an open-source cPanel challenger, the UI is not cosmetic — it's the primary adoption lever. cPanel's dated, page-reload-heavy interface is one of its most common complaints; Plesk wins customers largely by looking cleaner. CypherUI must feel like a modern SaaS product (Vercel/Linear/Stripe-dashboard tier, explicitly **Coolify-inspired**), not a themed legacy panel.

## 2. Brand

- **Accent color: violet** (`oklch` ~0.55 lightness / 0.65 chroma, hue ~277), defined **only** as theme tokens in `web/app/globals.css` — never a hardcoded Tailwind palette class (no `text-green-600`, `bg-violet-500`, etc. in components).
- **Dark-first.** The dark palette is the primary, default experience — on cool blue-tinted neutrals, not pure gray/black. Light mode exists and must stay fully themed, but dark is what gets designed first.
- **Semantic status tokens**: `--success`, `--warning` (+ corresponding badge variants), alongside the standard shadcn destructive/muted/etc. tokens. Any new status color goes through this token system, not a one-off class.
- Brand utilities: `.text-gradient-brand`, `.bg-brand-glow` in `globals.css` for the gradient brand mark used in the sidebar header.

## 3. Typography

- **Sans: Inter** (`--font-sans`), wired in `web/app/layout.tsx`.
- **Mono: JetBrains Mono** (`--font-mono`) — used for terminal output, code, IDs, and anything tabular/technical.
- Both are real font variables, not left to fall back silently — double-check `--font-sans` actually points at Inter and not back at itself (a real regression this project already hit once).

## 4. Component System

- **shadcn/ui** on Tailwind CSS + Radix UI primitives. Components are copied into `web/components/ui/`, not imported as an opaque dependency — this is what makes full theming and white-labeling possible without fighting vendor CSS.
- **White-labeling**: hosting providers/resellers must be able to rebrand (logo, accent color, product name, custom login domain) via config, not a fork. Build every new visual surface on tokens from the start so this stays a config file forever.
- **Accessibility bar: WCAG 2.1 AA** — full keyboard navigability, visible focus states, correct ARIA (Radix covers most of this for free), color-contrast-safe in both themes.
- **i18n scaffolding** (`next-intl`) from the first screen, even though MVP ships English-only — retrofitting across hundreds of screens later is the alternative, and community translations are an easy adoption lever for an OSS project.

## 5. Layout & Navigation

- **Persistent, collapsible left sidebar**, grouped by domain area (Domains, Files, Databases, Email, Security, Advanced) + breadcrumbs. This directly replaces cPanel's icon-grid homepage, which forces a full navigation for every single action.
- **Command palette (Ctrl/Cmd+K)** — fuzzy search across every feature, domain, database, and email account (e.g. "pma" → phpMyAdmin for database X, "ssl example.com" → that domain's cert manager). This is deliberately the single highest-leverage feature for making the panel feel modern, and it's the direct answer to "cPanel has 100 features and I can't find any of them."
- **Separate Admin and User shells** — the WHM-equivalent (fleet/servers/packages/resellers) and cPanel-equivalent (one account's hosting) share the design system and component library, but are distinct navigation structures. An admin managing 200 nodes and an end user managing one WordPress site have opposite information-density needs — don't force one nav shape onto both.
- Shared `web/components/page-header.tsx` for consistent page tops across every screen.

### Navigation anti-patterns to actively avoid

(Verified against 7 cloned competitor panels — full detail in project memory `competitor-panel-ux-research`, kept here as binding design guidance, not as something to re-derive each time.)

- **No flat grids/lists of 20-30+ near-identical items with no sub-grouping.** This is the single most common failure mode in competitor panels (Webmin's 26-module "Servers" tab; CyberPanel's ~28-tile "Server" hub) — it's exactly the "hard to select options" problem this project exists to avoid. Cap what appears in any one screen; sub-group before a list gets long, don't just paginate it.
- **No icon-only navigation** without a visible text label next to it.
- **No giant single-page edit forms that hide important settings behind collapsed "Advanced" toggles** (HestiaCP buries PHP version this way; VestaCP crams SSL/proxy/PHP/FTP/stats onto one form). Split into tabs/sections within a resource instead, and never bury a commonly-needed setting.
- **No near-duplicate/confusingly-named menu items** competing for attention (e.g. three separate "SSL" entries, three "backup" systems).
- The command palette above exists specifically as the escape hatch for a deep feature set — build it early rather than retrofitting once the nav is already too deep to search.

## 6. Interaction Patterns

- **No full-page reloads.** All mutations via React Query, optimistic updates where safe (DNS record edits, cron toggles), pending states where not (account provisioning).
- **Async job transparency.** Long-running operations (provisioning, backups, SSL issuance) already flow through the NATS job pipeline — surface them in a persistent notification center with live progress and success/failure states over WebSocket/SSE. Never leave a user staring at a spinner wondering if a backup actually started.
- **Live resource meters.** Dashboard cards for disk/bandwidth/inode/CPU usage against package limits, visible at a glance on login — "am I near my limit?" is the most common reason an end user opens a panel at all.
- **Empty states & first-run onboarding.** Every list screen is designed empty-first (clear CTA, one-line explanation); the admin shell has a guided first-run wizard (add server → create package → create first account). Open-source tools live or die in the first 10 minutes after `curl | bash`.
- **Destructive-action safety.** Type-to-confirm for irreversible operations (terminate account, delete database, delete domain) — never a bare "Are you sure?" modal.

## 7. Performance Budget (UI)

The panel must feel fast on the cheap VPSes it actually runs on:

- Per-route code splitting — the File Manager's editor bundle must not load on the DNS page
- Lean initial route JS
- Virtualized tables for large lists (10k DNS records, 50k accounts in the admin view)
- Skeleton loaders over bare spinners everywhere

A "lightweight panel" claim is judged by UI responsiveness as much as by daemon RSS — this is a real product requirement, not polish.

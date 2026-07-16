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

- **Persistent, collapsible left sidebar** for the admin/WHM-equivalent shell (fleet/servers/packages/resellers/accounts list), grouped by domain area + breadcrumbs — this is the top-level navigation an admin uses to get *to* an account in the first place.
- **Command palette (Ctrl/Cmd+K)** — fuzzy search across every feature, domain, database, and email account (e.g. "pma" → phpMyAdmin for database X, "ssl example.com" → that domain's cert manager). This is deliberately the single highest-leverage feature for making the panel feel modern, and it's the direct answer to "cPanel has 100 features and I can't find any of them."
- **Separate Admin and User shells, and they look genuinely different, not just differently populated.** The WHM-equivalent (fleet/servers/packages/resellers, the sidebar above) and the cPanel-equivalent (one account's hosting) share the design system and component library but are distinct navigation *shapes*: an admin managing 200 nodes needs the dense sidebar+table; a single account's hosting features (`/accounts/[id]`, per `web/app/(shell)/accounts/[id]/page.tsx`) instead get a **big-tile icon grid landing page** — one large, clearly labeled card per feature (File Manager, Databases, Email, FTP, DNS, Cron, PHP), each linking to its own full page, matching real cPanel and every other open-source panel researched. This is the one place a cPanel-style icon grid is the *right* call, not the anti-pattern the admin-shell sidebar above replaces — don't collapse these back into dialogs or cram them into the admin accounts table as row-icons; that WHM-style density is exactly what a single account's owner shouldn't have to navigate.
- Shared `web/components/page-header.tsx` for consistent page tops across every screen.

### Navigation anti-patterns to actively avoid

(Verified against 7 cloned competitor panels — full detail in project memory `competitor-panel-ux-research`, kept here as binding design guidance, not as something to re-derive each time.)

- **No flat grids/lists of 20-30+ near-identical items with no sub-grouping.** This is the single most common failure mode in competitor panels (Webmin's 26-module "Servers" tab; CyberPanel's ~28-tile "Server" hub) — it's exactly the "hard to select options" problem this project exists to avoid. Cap what appears in any one screen; sub-group before a list gets long, don't just paginate it.
- **No icon-only navigation** without a visible text label next to it.
- **No giant single-page edit forms that hide important settings behind collapsed "Advanced" toggles** (HestiaCP buries PHP version this way; VestaCP crams SSL/proxy/PHP/FTP/stats onto one form). Split into tabs/sections within a resource instead, and never bury a commonly-needed setting.
- **No near-duplicate/confusingly-named menu items** competing for attention (e.g. three separate "SSL" entries, three "backup" systems).
- The command palette above exists specifically as the escape hatch for a deep feature set — build it early rather than retrofitting once the nav is already too deep to search.

## 6. Option & Form Design

Section 5 is about finding a feature; this section is about actually *using* it once you're there — the level the "hard to select options" complaint that motivated this whole project actually lives at. Verified against the same 7 cloned competitor panels, at the field level rather than the navigation level (full findings: project memory `competitor-panel-ux-research`, applied concretely in `web/app/(shell)/accounts/[id]/php/page.tsx`, `.../dns/page.tsx`, `.../cron/page.tsx`, and `packages/page.tsx` — the first three were originally built as dialogs and later converted to standalone pages per Section 5's per-account icon-grid pattern, but the option-level UX inside them is unchanged by that move).

- **Plain-language labels, not raw config keys.** A field's primary label is what it does in plain English ("Memory limit"), never the underlying directive name (`memory_limit`). If the raw key is worth surfacing for transparency/power users, show it as small secondary text under the label — never as the only label. This was Webmin's single biggest failure mode (raw Apache directive names as the only label, 40-80+ per page) and the thing every other panel researched got at least partially right.
- **One-line description under every non-obvious field.** What it controls and, where relevant, a valid-range/example (`"e.g. 512M"`, `"Seconds a script may run before PHP stops it"`). Every panel that scored well on this (Froxlor's `desc` key, HestiaCP's `form-text`, CyberPanel's range hints) rendered it as small muted text directly under the label — not a tooltip you have to hover to discover.
- **Group related options into labeled sections**, not one flat list. A "Version" vs. "Performance & limits" split, or Froxlor/CyberPanel's titled cards, beats Webmin's single continuous table every time. Group *before* a form gets long — don't wait until it's already unwieldy to add structure.
- **Real controls for the actual number of states, not fewer or more than that.** A true on/off gets a `Switch` (`components/ui/switch.tsx`). A setting that can also be *unset* to inherit a default (common in override forms) is genuinely three states and needs a three-way control (segmented buttons: `Default / On / Off`) — collapsing it into a 2-state switch silently drops the "inherit default" case. Never make a boolean a free-text field the user has to type "On"/"Off" into.
- **Plain-language presets for anything schedule- or syntax-shaped**, with the raw syntax still available underneath for power users. Cron is the canonical case — three of the panels researched (HestiaCP, CyberPanel, VestaCP) independently arrived at the same shape: pick "Every day at 2am" from a list, it fills in the raw fields, and the raw form is still right there if you actually know cron syntax. Apply the same idea anywhere else a field's valid syntax isn't obvious from looking at it.
- **Numeric limits get a unit shown inline and an explicit affordance for "no limit"** — never a bare number input where the user has to already know that `0` (or `-1`, or blank) means unlimited. An "Unlimited" `Switch` next to the input (disabling it, matching Froxlor's `input_ul` / HestiaCP's infinity-toggle) makes the convention visible instead of tribal knowledge.
- **Adaptive hints beat one generic hint for every case.** Where a field's valid shape depends on another field (DNS record value format depends on the selected record type), make the hint reactive to that selection instead of writing one placeholder that's only really correct for the most common case.
- **A `<Select>`'s displayed value must resolve through a label lookup, not the raw stored value**, whenever the two differ (an id, a raw cron string, a raw seconds count). Base UI's `Select.Value` renders the raw `value` unless given a `children` mapping function — this is an easy, silent trap (it broke the account-creation dialog's Server/Package pickers, which showed a raw UUID after selection instead of the row's name). Check this on every new `<Select>` where the option's `value` isn't already the text you want shown.

## 7. Interaction Patterns

- **No full-page reloads.** All mutations via React Query, optimistic updates where safe (DNS record edits, cron toggles), pending states where not (account provisioning).
- **Async job transparency.** Long-running operations (provisioning, backups, SSL issuance) already flow through the NATS job pipeline — surface them in a persistent notification center with live progress and success/failure states over WebSocket/SSE. Never leave a user staring at a spinner wondering if a backup actually started.
- **Live resource meters.** Dashboard cards for disk/bandwidth/inode/CPU usage against package limits, visible at a glance on login — "am I near my limit?" is the most common reason an end user opens a panel at all.
- **Empty states & first-run onboarding.** Every list screen is designed empty-first (clear CTA, one-line explanation); the admin shell has a guided first-run wizard (add server → create package → create first account). Open-source tools live or die in the first 10 minutes after `curl | bash`.
- **Destructive-action safety.** Type-to-confirm for irreversible operations (terminate account, delete database, delete domain) — never a bare "Are you sure?" modal.

## 8. Performance Budget (UI)

The panel must feel fast on the cheap VPSes it actually runs on:

- Per-route code splitting — the File Manager's editor bundle must not load on the DNS page
- Lean initial route JS
- Virtualized tables for large lists (10k DNS records, 50k accounts in the admin view)
- Skeleton loaders over bare spinners everywhere

A "lightweight panel" claim is judged by UI responsiveness as much as by daemon RSS — this is a real product requirement, not polish.

# CypherPanel — UI Principles

> Binding rules for every screen. The visual design system (`design-system.md`: tokens, typography, exact components) is deliberately **not written yet** — it gets documented in roadmap Phase 4 from the real components, so it describes truth instead of fiction. These principles govern until then and after.

## 1. The page contract

Every page and every data region ships all four states, no exceptions:

1. **Loading** — skeletons matching final layout; no layout shift on arrival; no full-page spinners for partial data.
2. **Empty** — explains what belongs here and offers the primary action ("No applications yet — Deploy your first"), never a bare "No data".
3. **Error** — says what failed in glossary terms, preserves user input, offers retry; raw error details available behind an expander, never as the headline.
4. **Content.**

A PR adding a page without all four is incomplete by definition (see ENGINEERING.md).

## 2. Destructive actions

- Every destructive action gets a confirmation that states the blast radius ("Deletes this database **and its 3 backups**").
- **Irreversible** actions (delete server, delete project, delete database) require typing the resource name.
- Confirmations never stack — if confirming requires a second dialog, the flow is wrong.
- Destructive buttons are visually distinct and never the default-focused action.

## 3. Async work & feedback

- Actions >300 ms show progress. Long operations (deploys, backups) show **live progress with log access**, not a spinner — this is an ops tool; hiding the log is hiding the product.
- Optimistic updates only where rollback is trivial (renames yes, deploys no).
- Background completion/failure surfaces as a toast **and** in the resource's status; toasts are never load-bearing (missable without data loss).
- Never block navigation during a deploy — state lives on the server; the UI is a window, not a session.

## 4. Navigation & density

- **Landing page is Projects.** V1 has no separate "home dashboard" — decided 2026-07-17 after checking both references: Coolify's dashboard is literally two lists (projects, servers), Dokploy lands straight on the projects grid, and that directness is what users describe as "easy to manage." A richer overview page is post-v1, and only if real usage demands it.
- **The sidebar is exactly four items: Projects · Servers · Templates · Settings.** (Coolify has ~12, Dokploy 8.) Everything else lives *inside* its context — backups as tabs on databases plus a Settings section, logs/domains/deploys as tabs on resources. A new top-level nav item requires a recorded decision; it competes with every existing one.
- No jargon in the nav — "Sources", "Destinations" as top-level concepts (Coolify) are what the [glossary](../glossary.md) exists to prevent.
- Hierarchy is Team → Project → Environment → Resource, always visible as breadcrumbs.
- Prefer drawers/panels over modals; modal depth is 1, maximum.
- Maximum 3 levels of visual nesting inside any page.
- Information-dense but calm: status should be scannable from across the room (P1 checks this on a phone).

## 5. Status language

One vocabulary, everywhere (list rows, detail pages, API):
`running` (green) · `deploying` (animated blue) · `stopped` (gray) · `error` (red) · `degraded` (amber) · `unknown` (hollow gray — e.g. agent offline; never fake certainty).
Status changes stream in via SSE — no manual refresh, ever.

## 6. Forms & data entry

- Inline validation on blur; submit-time errors scroll to the field.
- Dirty forms warn before navigation discards them.
- Secrets are write-only by default: masked, revealable with a click, revealed state is logged to the audit trail.
- Every generated value (passwords, tokens, webhook URLs) has a copy button.

## 7. Tables & lists

- Server-side pagination and filtering beyond 50 rows; client-side sorting is a lie at scale.
- Keyboard navigable (arrows + enter to open); row click targets are the full row.
- Follow the page contract (§1) per table, including filtered-to-zero as a distinct empty state ("No results for this filter — clear filters").

## 8. Terminology & copy

- UI copy uses [glossary.md](../glossary.md) terms exactly. Copy that needs a term the glossary lacks means the glossary gets a PR first.
- Sentence case everywhere. No jargon in primary copy; jargon allowed in expandable detail.

## 9. Accessibility & responsiveness

- WCAG 2.1 AA as the working floor: visible focus states, 4.5:1 text contrast, `aria-live` for status changes, full keyboard operability.
- Usable at 360 px wide: tables collapse to cards; monitoring/deploy status must work on a phone (P1's success moment happens there).
- Dark mode is the default (the audience lives there); light mode is fully supported, not an afterthought. Both themes ship with every component from day one — retrofitting themes is how inconsistency wins.

## 10. Real-time integrity

- The UI must never show state it can't currently verify: if the SSE stream drops, show a "reconnecting" banner and mark statuses stale rather than frozen-fresh.
- Timestamps are absolute on hover, relative in display ("2m ago"), and always UTC-safe.

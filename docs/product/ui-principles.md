# CypherPanel — UI Principles

> Binding rules for every screen. The visual design system (`design-system.md`: tokens, typography, exact components) is deliberately **not written yet** — it gets documented in roadmap Phase 4 from the real components, so it describes truth instead of fiction. These principles govern until then and after.

## 1. The page contract

Every page and every data region ships all four states, no exceptions:

1. **Loading** — skeletons matching final layout; no layout shift on arrival; no full-page spinners for partial data.
2. **Empty** — explains what belongs here and offers the primary action ("No applications yet — Deploy your first"), never a bare "No data".
3. **Error** — says what failed in glossary terms, preserves user input, offers retry **and the most likely remedy** ("Build failed — the Dockerfile wasn't found at `./Dockerfile`. Check the path in Settings → Build"); raw error details available behind an expander, never as the headline.
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
- **Top-level nav is exactly four items: Projects · Servers · Templates · Settings.** (Coolify has ~12, Dokploy 8.) Everything else lives *inside* its context — backups as tabs on databases plus a Settings section, logs/domains/deploys as tabs on resources. A new top-level nav item requires a recorded decision; it competes with every existing one.
- **Those four items live in a top bar, not a sidebar** (revised 2026-08-02 with the Mission Control direction; the count and the composition are unchanged). The panel's content is wide tables and side-by-side resource boards, and a persistent 208 px sidebar taxes every one of them on every screen. The bar sits on the product's strongest rule — a 1.5 px ink line — so chrome and content never blur. Below `sm` it stays a bar and scrolls horizontally rather than becoming a second navigation model to learn.
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

- **Simple by default, power underneath.** A create form shows only the fields a first-time user must answer; everything with a working default is pre-filled and folded into a collapsed "Advanced" section. Deploying an app must never require knowing what a health-check interval is. (Both references fail this: their create forms dump every knob at once.)
- **Every working default is visible, not hidden** — the user sees `Port 8080 · Health check / every 10s` as filled-in values they *may* change, so the form teaches what the system does instead of demanding the user already know.
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
- **Light is the default; dark is the stored opt-in** (revised 2026-08-02 with the Mission Control direction — previously dark-default). The surface is warm paper, not a terminal emulator: the panel is read in daylight as often as at 2am, and the editorial light face is what makes a wall of machine state legible rather than oppressive. The audience that lives in the dark still gets a first-class dark theme, one click away and remembered. Both themes ship with every component from day one — retrofitting themes is how inconsistency wins.
- Log and terminal panes are ink in **both** themes. They are already terminals; inverting them in light mode would make them harder to read, not easier.

## 10. Real-time integrity

- The UI must never show state it can't currently verify: if the SSE stream drops, show a "reconnecting" banner and mark statuses stale rather than frozen-fresh.
- Timestamps are absolute on hover, relative in display ("2m ago"), and always UTC-safe.

## 11. Beginner-first: the UI must teach (added 2026-07-21)

P1 has "no ops background and no desire for one" ([personas.md](personas.md));
both references assume knowledge the user may not have and dump every concept
at once. We take the opposite bet: **a first-time self-hoster must be able to
go from empty panel to a deployed app without reading documentation.** The
rules:

- **The golden path is chained empty states.** A fresh panel guides, screen by
  screen, with no separate wizard to build or maintain: no servers → "Join your
  first server" (the copy-paste command front and center, with "what this does"
  in one sentence) → no projects → "Create a project" → no apps → "Deploy your
  first app". Each empty state is the next step, already in context. When the
  path is done, it disappears — no permanent onboarding chrome.
- **Every technical concept gets one plain-language line where it appears.**
  "Deploy key — lets CypherPanel read a private repository", "Backup target —
  where backup files are stored (any S3-compatible service)". The line lives
  inline under the field or title, not in a tooltip the user must discover.
  Deeper detail goes behind a "learn more" expander — never on the happy path.
- **Copy speaks in outcomes, not mechanisms.** "Your app will be live at
  `app.example.com`" beats "Route fragment will be applied to the proxy".
  Mechanism language is available in expandable detail for those who want it
  (§8 already allows jargon there) — the headline is always the outcome.
- **No dead ends.** Every screen answers "what can I do next?" — an error
  offers its remedy (§1), an empty state offers its action, a completed deploy
  offers "open app / view logs". A screen the user can only stare at is a bug.
- **Progressive disclosure is layering, not removal.** Beginner-first never
  deletes the power: density, keyboard navigation, raw logs, and full config
  stay (§4, §7) — they sit one honest click deeper, not gone. The test for any
  screen is two personas at once: P1 must not feel lost, P3 must not feel
  slowed down.
- **The "explain it cold" test.** Before a screen ships, read it as someone
  who has never self-hosted: every visible word either is plain language or
  has its one-line explanation present. If a field can't be explained in one
  line, the design — not the explanation — is wrong.

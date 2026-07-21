# Web UI design — the Phase 4 plan, written before the code

> This document is the **complete design plan for the real web UI**, written
> before the first line of `web/` code so implementation never has to guess.
> It collects every decision already made, makes the remaining ones, maps every
> screen to its API surface, and fixes the build order. It complements — never
> replaces — [ui-principles.md](ui-principles.md) (the binding rules for every
> screen) and does **not** preempt `design-system.md`, which roadmap Phase 4
> deliberately writes *from the real components afterwards* so it documents
> truth, not fiction.
>
> Written 2026-07-21, at Phase 4 start. Vocabulary per
> [glossary.md](../glossary.md). Anything here that contradicts
> ui-principles.md is a bug in this file.

## 1. Decisions already made — do not re-litigate

| Decision | Value | Source |
|---|---|---|
| Stack | React 19 + Vite + TanStack Router/Query, Tailwind CSS + shadcn/ui, pnpm | [tech-stack.md](../tech-stack.md) |
| Delivery | Static build embedded in `cypherd` via `go:embed`; **no SSR server** (a deliberate security property — the Dokploy Next.js CVE class does not apply to us, [research/community-pain-points.md](../../research/community-pain-points.md) §6) | tech-stack.md, ADR-001 |
| API client | **Generated from the OpenAPI spec** (orval or openapi-typescript); hand-written fetch calls are forbidden | tech-stack.md |
| Real-time | SSE for status/logs; WebSocket only for the interactive terminal | tech-stack.md |
| Location | `web/` with `src/{api, features, components, routes}` | [project-structure.md](../project-structure.md) |
| Navigation | Sidebar is **exactly four items**: Projects · Servers · Templates · Settings | ui-principles §4 |
| Landing page | **Projects** — no separate home dashboard at v1 (decided 2026-07-17) | ui-principles §4 |
| Hierarchy | Team → Project → Environment → Resource, always visible as breadcrumbs | ui-principles §4 |
| Status vocabulary | `running` green · `deploying` animated blue · `stopped` gray · `error` red · `degraded` amber · `unknown` hollow gray | ui-principles §5 |
| Themes | Dark is default; light fully supported; **both ship with every component from day one** | ui-principles §9 |
| Accessibility | WCAG 2.1 AA floor; usable at 360 px | ui-principles §9 |
| Page contract | Every data region ships Loading / Empty / Error / Content — a PR missing one is incomplete | ui-principles §1 |
| E2E | Playwright | tech-stack.md |
| Beginner-first | The UI must teach: chained golden-path empty states, one plain-language line per technical concept, outcome copy, no dead ends, progressive disclosure (layering, never removal) | ui-principles §11 |

**One reconciliation, recorded here:** roadmap Phase 4's scope word "dashboard"
does **not** reintroduce a home dashboard — ui-principles §4 (the later, more
specific decision) stands. "Dashboard" in the roadmap means the *product
surface as a whole*: the Projects landing plus rich in-context resource views.
If real usage later demands an overview page, that is a new recorded decision.

## 2. Aesthetic direction — "the calm terminal"

The [frontend-design skill](../../.claude/skills/frontend-design/SKILL.md)
demands a named, opinionated direction so the UI cannot read as a shadcn
template. This is it; per-screen work loads that skill and executes within
this direction.

**Thesis.** CypherPanel's product truth is *honesty under pressure*: an ops
tool whose UI is a window onto server-side state, watched at 2am and from
phones. The aesthetic is **the calm terminal** — the confidence of a good
tmux session with the legibility of a well-set book. Dense but never busy;
dark-first because the audience lives there; nothing decorative that isn't
information.

- **Typography carries the identity.** Two faces only: a humanist sans for UI
  chrome and prose (e.g. Inter or a similarly quiet face), and a **monospace
  face promoted to a first-class display role** — not just code blocks:
  resource IDs, domains, statuses, log excerpts, cron expressions, image tags
  all set in mono. The mono-as-identity is the one deliberate aesthetic risk;
  it encodes the truth that this product's content *is* machine state.
- **Color is status.** The chromatic palette is reserved almost entirely for
  the six status colors (§1 vocabulary) plus one restrained accent for primary
  actions. Everything else is a neutral ramp. A screen with nothing wrong on
  it should be almost monochrome — so that when something *is* wrong, the red
  is the loudest thing in the room. Both themes derive from the same tokens;
  status colors must hold 4.5:1 contrast in both.
- **Structure is information** (skill principle, applied): section labels are
  set as small mono eyebrows (`DEPLOYMENTS`, `ENV VARS`); no numbered markers
  anywhere (nothing here is a sequence except pipeline stages — which *are*
  one, and render as one: `build → distribute → rollout → serving`).
- **Motion is earned.** Exactly three animations own the product: the
  `deploying` status pulse, the live log tail, and the deploy pipeline stage
  progression. Everything else is instant or a ≤150 ms fade. No scroll
  reveals, no hero moments, no skeleton shimmer louder than the content.
- **Density with air.** Information-dense tables at comfortable line height;
  scannable from across the room (ui-principles §4). Empty states are quiet,
  single-action, and never illustrated with mascots.

What this direction bans: gradient heroes, glassmorphism, decorative
illustration, celebratory confetti, more than one accent color, and any
component that looks like it shipped with a template.

## 3. Information architecture

```
/login
/  (redirect → /projects)
├── /projects                                 landing: project cards/rows (team-scoped)
│   └── /projects/:id                         project home: environments + resources
│       ├── ?env=:envId                        environment switcher (tabs, not routes)
│       ├── settings                           project settings: rename · notifiers · danger zone
│       └── (resources open in context:)
│           /applications/:id                  tabs: Overview · Deployments · Logs ·
│           │                                    Env vars · Previews · Scheduled tasks · Settings
│           │   /deployments/:id               drawer/page: pipeline stages + live build log
│           /databases/:id                     tabs: Overview · Backups · Connection · Settings
├── /servers                                  list + join flow (the curl|sh moment)
│   └── /servers/:id                          detail: status, workloads placed here, danger zone
├── /templates                                catalog (Phase 4 feature; empty-state ships first)
└── /settings                                 tabs: Teams · Users · Backup targets · Deploy keys · Account
```

Rules the tree encodes:

- **Resources live inside their project context** — no top-level "Applications"
  or "Databases" nav (ui-principles §4: everything lives inside its context).
- **Environments are a switcher within a project**, not nested routes —
  previews appear in the same switcher marked with their TTL, because previews
  are ordinary environments (preview-environments.md §1).
- **Teams are context, not a place you visit often**: a team switcher in the
  sidebar footer scopes the Projects list; team/user management lives in
  Settings. Role gating follows the API: the UI **renders what 404s away**
  (non-members never see foreign resources) and disables what 403s (rank), with
  the role shown — never a mystery-disabled button.
- **Breadcrumbs always**: `team / project / environment / resource`.
- Drawers over modals; the deployment detail is the canonical drawer (list
  stays visible behind it). Modal depth 1 max (ui-principles §4).

**The four items survive scale only with two companions** (recorded here so
they are commitments, not hopes):

- **Status rollups on the Projects landing.** With no home dashboard, the
  landing must answer P2's "which of my 25 apps is broken right now?" at a
  glance: each project row carries an aggregated status ("1 app error ·
  1 backup failed"), worst-status-first sort, red visible from across the
  room. If the rollup is weak, the no-dashboard bet fails.
- **A ⌘K command palette** (slice 4): type any resource name → jump straight
  there; common actions ("deploy web", "logs api") included. It is the fast
  lane the 2–3-click hierarchy needs for power users (P3) — and invisible to
  beginners until they want it (§11: layering, not removal). It is not a
  navigation replacement and never a requirement for any flow.

## 4. Screen inventory — every screen, its data, its states

Every row ships the four page-contract states; "Realtime" is what must update
without refresh (ui-principles §10).

| Screen | Primary API | Realtime | Notes |
|---|---|---|---|
| Login | `POST /auth/login`, `GET /auth/me` | — | Rate-limit 429 gets a human message |
| Projects (landing) | `GET /projects` (+ `/auth/me` teams) | statuses | Rollup status per project; team switcher filters |
| Project home | `GET /projects/{id}`, env resources: `GET /environments/{id}/applications`, `…/databases` | statuses | Env switcher incl. previews (TTL badge) |
| Application · Overview | `GET /applications/{id}` | status, observed revision | Domain (link), health, revision, webhook URL (copy) |
| Application · Deployments | `GET /applications/{id}/deployments` | active deployment progress | Deploy + rollback actions; pipeline stages |
| Deployment detail (drawer) | `GET /deployments/{id}`, `GET /deployments/{id}/logs` **SSE** | build log tail, status | The celebrated animation lives here |
| Application · Logs | `GET /applications/{id}/logs` **SSE** | runtime tail | Replay-then-tail; reconnect banner |
| Application · Env vars | `GET/PUT/DELETE /applications/{id}/env…` | — | Write-only values (ui-principles §6); keys listed, values never |
| Application · Previews | `GET /applications/{id}/previews`, `DELETE /previews/{id}` | preview statuses | Enable/config lives in app Settings |
| Application · Scheduled tasks | `GET/POST… /applications/{id}/scheduled-tasks`, `GET …/runs` | run history | Cron helper text; argv input (array, not shell string) |
| Application · Settings | `PATCH /applications/{id}` | — | Sectioned form; danger zone (typed-name delete) |
| Database · Overview | `GET /databases/{id}` | status | Engine/version, start/stop, reset password (shown once) |
| Database · Backups | `GET/POST… /databases/{id}/backups…`, `…/history`, `POST …/restore` | run history, last status | Restore = destructive confirm with blast radius |
| Database · Connection | `GET /databases/{id}/connection-info` | — | Copy fields; internal vs external host |
| Servers | `GET /servers` | heartbeat status | — |
| Server join | `POST /servers` | joining server appears | The `curl \| sh` command front and center, copy button; “running within 60 s” progress |
| Server detail | `GET /servers/{id}` | status | Workloads placed here; revoke = typed-name delete |
| Templates | (Phase 4 catalog feature — own spec first) | — | Ships as an honest empty state until the catalog lands |
| Settings · Teams | `GET/POST /teams`, members CRUD | — | Last-owner guard errors surfaced verbatim (409) |
| Settings · Users | `GET/POST /users`, role PATCH | — | Panel-role gated; hidden below admin |
| Settings · Backup targets | `GET/POST/DELETE /backup-targets` | — | Secrets write-only |
| Settings · Deploy keys | `GET/POST/DELETE /deploy-keys` | — | Public key copy |
| Settings · Account | `GET /auth/me` | — | Token/session info; logout |
| Project settings · Notifiers | `GET/POST… /projects/{id}/notifiers`, `POST …/test` | — | Config write-only + masked hint; **Test** button is first-class |

## 5. Frontend architecture

```
web/
├── package.json  vite.config.ts  tailwind.config.ts  tsconfig.json
└── src/
    ├── api/          # GENERATED from openapi.yaml (orval) + one thin fetch wrapper
    ├── lib/          # auth store, SSE hook, query-key factory, role helpers
    ├── components/   # shared, shadcn-derived (see §6)
    ├── features/     # projects/ applications/ databases/ servers/ deployments/
    │                 #   backups/ previews/ notifiers/ scheduled-tasks/ teams/ settings/
    └── routes/       # TanStack Router file routes mirroring §3
```

- **Data layer.** TanStack Query owns all server state; **no client cache of
  truth beyond it** (the UI is a window, ui-principles §3). Query keys come
  from one factory (`qk.application(id)`, `qk.deployments(appId)`…). Mutations
  invalidate by resource family. Polling interval 5 s for statuses **until the
  status SSE endpoint exists** (§8 prerequisite), then SSE-driven invalidation
  with polling as the reconnect fallback.
- **SSE.** One `useSSE(url)` hook: buffers replay, marks the region **stale on
  disconnect** (visible "reconnecting" banner — never frozen-fresh, ui-principles
  §10), exponential backoff, resumes tail.
- **Auth.** Bearer token from `POST /auth/login`, held in memory + `localStorage`
  (static SPA; XSS is mitigated by a strict CSP served by `cypherd` — no inline
  script, no external origins — and by React's default escaping; the API's
  session TTL bounds exposure). 401 anywhere → purge + redirect to `/login`
  preserving return path. `GET /auth/me` (user + teams + roles) hydrates the
  role helpers; **the server remains the enforcer** — UI gating is UX, never
  security.
- **Errors.** The API's envelope is `{"error": string}` in glossary terms —
  render `error` verbatim as the headline (it is written for humans), keep
  status/detail behind the expander (ui-principles §1).
- **Embedding.** `make build-web` → `vite build` → output committed into the
  Go embed path (or built in CI) → `cypherd` serves it at `/` with the SPA
  fallback; the interim console at `core/api/rest/console/` is deleted the
  moment slice 1 (§7) replaces its capabilities — two UIs is worse than one.
- **Bundle budget.** Initial JS ≤ 300 KB gzipped, enforced in CI — the
  panel's frugality (vision.md) extends to the browser. Route-level code
  splitting from day one; no moment.js-class dependencies; charts (Phase 4
  metrics) load lazily.

## 6. Component strategy

shadcn/ui is the base layer (owned source, restyled to §2 — not a themed
dependency). The product components, each shipping both themes + all states:

| Component | Encodes |
|---|---|
| `StatusBadge` | The §1 vocabulary — the single source of status rendering, mono label + dot |
| `PageState` | The four-state page contract as a wrapper: `<PageState query={q} empty={…}>` — makes ui-principles §1 the path of least resistance |
| `ResourceTable` | Keyboard-navigable rows, full-row click, filtered-to-zero empty state (§7) |
| `ConfirmDestructive` | Blast-radius sentence + typed-name gate for irreversible actions (§2) |
| `SecretField` / `CopyField` | Write-only reveal-on-click; copy affordance for every generated value (§6) |
| `LogViewer` | Replay + live tail, autoscroll-with-opt-out, mono, wrap toggle |
| `PipelineStages` | `build → distribute → rollout → serving` with the one celebrated animation |
| `SSEBanner` | The "reconnecting — data may be stale" state (§10) |
| `Breadcrumbs` | team / project / environment / resource, always |
| `RoleGate` | Renders children only at sufficient rank; shows the required role on the disabled affordance |
| `EnvSwitcher` | Environments incl. previews with TTL badges |
| `EmptyState` | One sentence + one primary action, quiet; **chains the golden path** on fresh panels (§11) |
| `InlineHint` | The one plain-language line under a technical field/title ("Deploy key — lets CypherPanel read a private repository"), with an optional "learn more" expander (§11) |
| `AdvancedSection` | Collapsed container for everything with a working default — create forms show only what a first-timer must answer (§6) |
| `CronField` | Schedule input with next-3-runs preview (parse client-side, same 5-field grammar) |
| `CommandPalette` | ⌘K jump-to-anything + actions (slice 4); fed by the resources the caller can already see — never a search across foreign teams |
| `ArgvInput` | Scheduled-task command as an argv list — the UI mirrors ADR-011: never a shell-string textbox |

**Definition of done, per screen** (the PR checklist):
four states · both themes · 360 px usable · keyboard operable · copy from the
glossary · data only via the generated client · role-gated actions render
their rank · the **"explain it cold" pass** (every visible term is plain or
carries its one-line hint — ui-principles §11) · a Playwright smoke
(login → screen → primary action) passes.

## 7. Build order — five slices, each shippable and verified

Each slice ends **live-verified**: boot the stack (the `verify` skill), drive
the UI with the `webapp-testing` skill (Playwright: log in, exercise,
screenshot both themes), and only then move on. UI slices need no new feature
specs (this document + ui-principles govern); the *product features* in slice
5 get their own specs first, per CLAUDE.md rule 7.

1. **Foundation.** `web/` scaffolding; OpenAPI completion (§8) + generated
   client; auth + shell (sidebar, breadcrumbs, theme, team switcher);
   `PageState`/`StatusBadge`/core components; embed + CSP + SPA fallback in
   `cypherd`; Playwright harness; bundle-budget CI check.
2. **The deploy loop** — the product's heart, done end to end: Projects →
   project home → application tabs (Overview, Deployments + drawer with live
   build log, Logs, Env vars, Settings) → server join flow, **including the
   full golden path**: a fresh panel chains join server → create project →
   deploy app through empty states alone (ui-principles §11). *Gate: P1's
   success moment — an empty panel to a live app with no docs open, then watch
   a push deploy from a phone-sized viewport.* **Interim console deleted
   here.**
3. **State-model breadth, part 1.** Databases (all tabs), backup targets,
   backups + restore flows.
4. **State-model breadth, part 2.** Previews, notifiers (+ test send),
   scheduled tasks (+ run history), Settings: teams/users/roles; the ⌘K
   command palette (§3).
5. **Phase 4 features, each spec-first:** template catalog (+ `adding-a-template`
   skill, per project-structure.md), Compose stacks, interactive terminal
   (WebSocket; carries its own security section — threat-model §5.6),
   metrics/observability (charts via the `dataviz` skill). `design-system.md`
   is written **during** this slice from the real component inventory of §6.

## 8. Backend prerequisites (do these before or with slice 1)

Honest gaps the UI depends on — plane work, not `web/` work:

1. **Complete the OpenAPI spec.** `core/api/rest/openapi.yaml` documents the
   Phase 2 surface (27 paths); the real API is 80+ routes. The generated
   client makes the spec load-bearing, so this closes rule 19's drift for
   Phase 3 routes as a side effect. This is the largest prerequisite.
2. **Status SSE endpoint.** ui-principles §10 requires streamed status. Today
   SSE exists only for logs. Add one read-only endpoint (e.g.
   `GET /api/v1/events` scoped to the caller's visible teams) publishing
   status transitions the plane already consumes from `state.*`. Until it
   lands, the UI polls at 5 s (§5) — acceptable for slice 1, not for done.
3. **SPA serving.** `cypherd` needs the embed of `web/dist`, SPA fallback for
   client routes, and the CSP header (§5 auth note).
4. **Bundle-size + web CI jobs.** pnpm lint/typecheck/test/build + Playwright
   smoke in ci.yml, mirroring the Go gates.

## 9. Skills to use while building (from `.claude/skills/`)

| Skill | When |
|---|---|
| `frontend-design` (repo-scoped) | Before each new screen/layout: hold it to §2's direction; it is the taste enforcer |
| `webapp-testing` | Every slice's live verification: Playwright drive, both-theme screenshots, console errors |
| `CypherPanel-Dev:verify` | Boot cypherd + agent + Postgres for real-backend UI sessions |
| `dataviz` | Slice 5 metrics/observability charts — load **before** writing any chart code |
| `skill-creator` | To create `adding-a-template` when the catalog work starts (project-structure.md plans it) |
| `reconciler-development` | Only if UI work motivates agent/scheduler changes (e.g. the events endpoint touches consumers) |

## 10. Out of scope for the UI (recorded so nobody wonders)

A home dashboard (§1 reconciliation) · SSO (post-v1, feature matrix) · TOTP
enrollment screens (lands with the TOTP backend, V1.x) · i18n (English-only at
launch) · mobile *apps* (the responsive web at 360 px is the phone story) ·
building any UI capability the API lacks (API-first is absolute — the API PR
comes first) · white-labeling.

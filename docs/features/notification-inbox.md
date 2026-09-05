# Feature spec: Notification inbox

> A deploy fails at 3am. If the project has a Slack notifier, someone sees it —
> in a channel, hours later, five messages down. If it has none (a notifier is
> opt-in, per project, and carries credentials), the only witness is `slog`. The
> **inbox** is the panel's own record of what happened to what you own: the
> *same* observed outcomes `core/notify` already fans out to channels, persisted
> per user, counted on a bell in the top bar. It is the one channel that needs
> no configuration, no webhook, and no secret.
>
> Canvas 13u; the per-user preference list it filters by is canvas 13i's "Notify
> me about". Phase 4 polish ([roadmap.md](../roadmap.md)) — the in-panel half of
> the **V1** feature-matrix row "Notifications: Email/Discord/Slack/Telegram"
> ([feature-matrix.md](../product/feature-matrix.md)), not a new row.
>
> Written 2026-08-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Nothing is ported: the extraction maps'
> Notifications rows cover outbound channel delivery, which we already shipped
> ([notifications.md](notifications.md)).

## 1. The core idea: one event source, a second audience

There is exactly **one** place where an observed outcome becomes news:
`notify.Manager.dispatch` (notifications.md §4), reached from the scheduler's
terminal transitions. This slice adds no second event source, no second
taxonomy, and no new call sites — it adds a second **audience** to the existing
fan-out, one that is durable and per-user instead of best-effort and
per-channel.

```
agent observation ──▶ scheduler transition ──▶ status row          (existing)
                                            └─▶ notify.Manager.dispatch
                                                   ├─▶ channel fan-out  (existing, best-effort)
                                                   └─▶ inbox.Record     ← new, persisted
                                                          │ project → team → members
                                                          ▼ minus each member's muted kinds
                                                     inbox_items (one row per recipient)
```

The consequences are the point. The bell and the Slack message say the same
words, because both render one `domain.NotifyEvent`. A new event key costs
nothing here: the inbox subscribes to the taxonomy, not to individual
transitions, so notifications.md §8's follow-ons render unchanged.

**One exception, added later: panel-level kinds.** Some news is about the panel
rather than about a project's resources, and has no `NotifyEvent` behind it —
today just `panel.update_available`
([control-plane-hardening.md](control-plane-hardening.md) §3), written by the
update check straight to owners. Those items have no project (`project_id` is
nullable from migration `0028`), so the team-removal sweep never touches them,
and they are *inbox* kinds only: `domain.InboxKinds()` is `EventTypes()` plus
the panel kinds, preferences validate against `ValidInboxKind`, and notifiers
and webhook endpoints keep subscribing to `EventTypes()` alone. Nothing emits a
panel kind to a channel. Nothing new
reaches an agent — like notifiers, this is a plane-internal reaction to state
that already exists (ADR-005): no work item, no subject, no proto change, no
imperative path (CLAUDE.md rule 3). And unlike channel delivery, the inbox write
is **persistence, not delivery**: it happens first, and its failure is logged,
not swallowed by a dead webhook (§4).

## 2. The resource model

Two tables, migration **`0024_notification_inbox.sql`** (0023, outbound
webhooks, landed between this spec being written and being implemented; 0021
and 0022 went to profile photos and panel mail on main).
Additive, reversible, no backfill — the inbox records forward; `deployments` and
`backup_records` remain the historical record (rule 16).

```
InboxItem:                                   -- id prefix inb_
  id           TEXT PK
  user_id      TEXT NOT NULL → users(id)    ON DELETE CASCADE
  project_id   TEXT NOT NULL → projects(id) ON DELETE CASCADE
  kind         TEXT NOT NULL        -- an existing event key (§3)
  severity     TEXT NOT NULL        -- info | error  (= domain.NotifyLevel)
  digest       BOOLEAN NOT NULL DEFAULT false
  title        TEXT NOT NULL        -- immediate: event title; digest: label ("Backups")
  body         TEXT NOT NULL DEFAULT ''      -- capped at 2 KB (§5)
  link         TEXT NOT NULL DEFAULT ''      -- in-panel path, validated (§5)
  link_label   TEXT NOT NULL DEFAULT ''
  count_ok     INTEGER NOT NULL DEFAULT 1    -- digest counters (§3)
  count_total  INTEGER NOT NULL DEFAULT 1
  sources      TEXT[]  NOT NULL DEFAULT '{}' -- source ids already rolled in (§4)
  dedupe_key   TEXT NOT NULL
  read_at      TIMESTAMPTZ                   -- NULL = unread
  created_at, updated_at
  UNIQUE (user_id, dedupe_key)

InboxPreferences:
  user_id      TEXT PK → users(id) ON DELETE CASCADE
  muted_kinds  TEXT[] NOT NULL DEFAULT '{}'
  updated_at
```

Indexes: `(user_id, created_at DESC)` for the list and a partial `(user_id)
WHERE read_at IS NULL` for the bell — the two queries this feature runs on
every panel load.

**Decided — preferences are stored as *mutes*, in their own table.** An absent
row and an empty array both mean "everything on", so a kind added later is on
by default for everyone; a positive subscription list would silently exclude
every future failure kind from every existing account — the wrong direction to
fail. It is this slice's table rather than two more columns on `users` (0020's
shape) so the row is owned and cascaded here.

**Decided — the item is denormalised, not a pointer.** It stores the rendered
title, body and link rather than `(kind, resource_id)` resolved at read time. A
notification is a statement about a moment: resolving it later against current
state would make yesterday's failure read like today's success, and would break
exactly when the resource is deleted — the case an operator most wants to read
about.

## 3. Kinds, severity, and the digest rule

`kind` is an existing event key from `core/domain/notify.go` — the same closed
set notifiers already subscribe to. **No new constants:**

| Kind | Severity | Inbox behaviour |
|---|---|---|
| `deploy.failed`    | `error` | immediate, one item |
| `backup.failed`    | `error` | immediate, one item |
| `deploy.succeeded` | `info`  | rolled into the day's digest |
| `backup.succeeded` | `info`  | rolled into the day's digest |

`severity` is `domain.NotifyLevel` (`info` \| `error`) unchanged — deliberately
not a third status vocabulary. ui-principles §5's running/deploying/stopped/…
words describe a *resource*; these describe an *event*, and the panel already
has exactly one word set for that.

Failure items carry a deep link, rendered server-side so a CLI prints the same
words the drawer does: `/projects/{p}/applications/{a}/deployments?dep={d}`
labelled "View deployment", `/projects/{p}/databases/{d}/backups` labelled "View
backups". Digests carry none (§7).

**The rule: severity `error` is immediate and individual, severity `info` is
digested.** A digest is one item per `(user, project, kind, UTC day)`, created
by the first success in the window and incremented by each later one; its line
is composed by `inbox.DigestTitle(kind, ok, total)` → `"Backups: 3/3
succeeded"`, so a counter that moves after the row is written never rewrites
stored copy. A **failure in the same window bumps `count_total` only** on an
existing digest (never creating one), which is what makes the denominator
honest: `"Backups: 2/3 succeeded"` sitting beside the one failure item that
explains the missing third. A digest is one unread, however many events it
holds — that is the whole point of digesting.

**Decided — the window is a UTC calendar day.** Users have a `timezone` (0020),
but a bucket boundary is storage, not display: a per-user local window would put
one event in different windows for different readers and would move under a
profile edit. Timestamps stay UTC, rendered relative and absolute on hover
(ui-principles §10).

**Decided — the board's digest copy is trimmed to what we can prove.** Canvas
13u reads "Nightly backups: 3/3 succeeded, verified". *"Nightly"* goes (the
plane does not know a schedule's cadence, and a day's digest can include a
manually triggered backup); *", verified"* goes (nothing verifies a backup —
restore is operator-initiated, managed-databases.md), and claiming it would be
showing state we cannot verify, which ui-principles §10 forbids.

**Decided — the board's first two rows are out of this slice, on purpose.** "api
crashed … restarting 3/5" needs a crash/restart-loop observation nothing reports
today; "Deploy #215 awaits your approval" needs an approval feature that does
not exist. Inventing either means a second event source, which §1 refuses — and
both are one taxonomy entry away (§9).

## 4. Fan-out — who gets an item, and where the call goes

`core/notify` gains one consumer-defined dependency (rule 6), optional and
nil-guarded like every other:

```go
// Inbox persists an observed outcome as per-user items (consumer-defined;
// *inbox.Service satisfies it). nil disables the inbox; channels are unaffected.
type Inbox interface {
    Record(ctx context.Context, ev domain.NotifyEvent) error
}
```

It arrives through `notify.New(st, box, log, inbox)` in `main.go` — a
constructor argument, not a setter (rule 5). `domain.NotifyEvent` gains four
additive fields so an item can carry a deep link: `ProjectID`, `ResourceKind`
(`application` \| `database`), `ResourceID`, `FocusID` (the deployment or backup
record the link opens); `NotifyDeploy`/`NotifyBackup` fill them from values
already in hand, and `dispatch` sets `ProjectID` where it already resolves the
environment.

Inside `dispatch`, **the inbox write comes first**, before the notifier lookup —
which today returns early on error. Recording first means the bell works on a
panel with no channels configured at all, which is most panels. `dispatch`
already runs detached (`context.WithoutCancel` + a 10s timeout) in a goroutine it
owns (rule 7), so the added database work is off the scheduler's path and cannot
slow a deploy. `inbox.Service.Record` then:

1. **Resolves recipients** — one indexed query, `projects ⋈ team_members` left
   joined to `inbox_preferences`, dropping anyone whose `muted_kinds` holds the
   kind (`NOT (@kind::text = ANY(...))`, the `@named`-cast idiom from
   `queries/notifiers.sql`; an absent row yields an empty array, so it receives
   everything). None → return.
2. **Mints an id per recipient** (`ids.New(ids.PrefixInboxItem)` — service layer
   only) and **inserts in one statement** via `unnest($ids, $user_ids)` with the
   shared columns as scalars, `ON CONFLICT (user_id, dedupe_key) DO NOTHING`.
3. For an `info` kind, **rolls up** instead — the same statement with
   `ON CONFLICT … DO UPDATE SET count_ok = count_ok + 1, count_total =
   count_total + 1, sources = sources || @focus_id WHERE NOT (@focus_id =
   ANY(inbox_items.sources))`.
4. **Prunes** to the most recent `inboxRetention = 200` per affected user — the
   shape `DeleteOldTaskRuns` already uses.

`dedupe_key` is `<kind>:<focus_id>` for an immediate item and
`digest:<kind>:<project_id>:<YYYY-MM-DD>` for a digest. With the `sources`
guard this makes the path safe under redelivery (rule 12) and gives it the
idempotency test rule 13 wants: `HandleDbBackupEvent` has no terminal-state
guard today, so a redelivered `DbBackupEvent` reaches `NotifyBackup` twice —
without the guard that silently inflates a digest counter.

**Decided — membership is the subscription; the panel-owner bypass is not.**
Recipients come from explicit `team_members` rows. A panel `owner` holds
implicit team-owner rank everywhere (teams-and-roles.md §1), but that is an
authorization escape hatch for recoverability, not an opinion about what they
want to read; on a thirty-team panel it would make the bell useless for the one
person who can least afford to ignore it.

**Decided — leaving a team empties that team's items from the ex-member's
inbox.** `teams.RemoveMember` gains one `DELETE … WHERE user_id = $1 AND
project_id IN (SELECT id FROM projects WHERE team_id = $2)`: the rule is "never
hold an item for a team you do not belong to", and a stale title naming a
project you were just removed from breaks it as surely as a live delivery would.

`POST /notifiers/{id}/test` writes no item — it goes through `Manager.Deliver`,
not `dispatch`, and is a wiring check, not an observed outcome.

## 5. Security & bounds

- **Tenancy is structural.** An item *is* a per-user row; every route filters
  `WHERE user_id = <caller>` and **no route accepts another user's id**. There is
  no `projectIDForInboxItem` resolver in `authz.go` and no 404-over-403 posture
  to get wrong — the first feature here whose authorization is a column. Leakage
  would require the fan-out query (§4) to be wrong, which is what acceptance 1
  proves.
- **No secrets, by inheritance.** The body is the `NotifyEvent` body, already
  constrained to deploy/backup metadata the API surfaces anyway (notifications.md
  §6). The inbox never touches `secret.Box`, and log lines carry ids only, never
  titles or bodies (rules 4, 20).
- **Deep links are validated in-panel paths**: a single leading `/`, no scheme,
  host, or `//`; rejected at write time otherwise. A free-text link rendered
  inside the authenticated shell is a stored open-redirect and a convincing
  phishing surface. Titles and bodies render as text — no
  `dangerouslySetInnerHTML`.
- **API tokens act as their owner** (`requiredAbility`: GET → `read`, POST/PUT →
  `write`), so a token reads and clears its owner's inbox and nobody else's. That
  is not credential management, so these routes are not `sessionOnly`.
- **Bounds:** 200 items per user pruned on insert; `body` truncated to 2 KB
  (scheduled-tasks.md §6's diagnostic-tail discipline); the digest collapses the
  success flood that would otherwise fill the cap. A twenty-member team costs
  twenty rows and one prune per event, inside the detached goroutine.

## 6. API surface (under `/api/v1`)

```
GET    /inbox?unread=true&limit=20&before=<item_id>  → { items: [InboxItem], next_before: string|null }
GET    /inbox/unread-count                            → { unread: 3 }
POST   /inbox/{id}/read                               → 204   (idempotent)
POST   /inbox/read-all                                → 200   { marked: 7 }
GET    /inbox/preferences                             → { muted_kinds: [...], available_kinds: [...] }
PUT    /inbox/preferences                             → { muted_kinds: [...], available_kinds: [...] }
```

**Decided — the collection is `/inbox`, not `/users/{id}/inbox`:** the inbox is
always the caller's, and the absence of an owner segment makes §5's guarantee
syntactic. Paging is **keyset**, not offset — `before=<item_id>` continues from
that item's `(created_at, id)` descending, `limit` defaults to 20 and caps at
100 — because a feed gains rows at the head while you page it and offsets then
skip rows (ui-principles §7 requires server-side paging past 50 anyway).

The `InboxItem` DTO carries a **composed** `title` (`"Deploy failed: web"`,
`"Backups: 3/3 succeeded"`) plus `kind`, `severity`, `digest`, `body`, `link`,
`link_label`, `project_id`, `read_at`, `created_at`, `updated_at`. Counters stay
out of the contract: a client rendering them into English would be a second home
for copy, and a CLI would get it subtly different (CLAUDE.md rule 4).
`available_kinds` is served rather than hardcoded, so canvas 13i's checkbox list
shows exactly what this plane can emit — the event taxonomy plus the panel-level
kinds (§1) — and a new entry needs no front-end change; `PUT` replaces the whole
set and 400s on a kind outside it. `project_id` is `""` for a panel-level item.
Contract lands in `openapi.yaml` first (rule 19) under a new `inbox` tag; the
client is regenerated with `make generate-web`.

## 7. UI — the bell and the inbox panel (canvas 13u)

The bell sits in the `TopBar` in `web/src/routes/_app.tsx`, beside the ⌘K pill
and the account menu. **It is chrome, not navigation:** ui-principles §4 fixes
the top-level nav at exactly four items, and the bell is not a fifth — it opens
a panel in place and leads nowhere.

- **Badge.** Unread count from the generated `useGetInboxUnreadCount` hook;
  exact to 99, then `99+`; `aria-label="Inbox, 3 unread"`. Zero unread renders no
  badge, never a `0`.
- **Panel.** The existing `Drawer` (`tone="paper"`) — right column from `sm` up,
  bottom sheet below, so 360 px needs no second layout (ui-principles §4, §9).
  Header: "Inbox"
  with the count and **Mark all read** as a `secondary` button, not accent
  (accent marks the one unmissable action on a screen).
- **Rows.** Sentence-case title, dim body, then `relativeTime(created_at)` with
  `absoluteTime` in `title=` and the action link. `severity: "error"` takes the
  square mark and `text-status-error`, matching `StatusDot`'s shape language
  instead of inventing a second one. Unread rows carry an ink left rule and
  full-strength text; read rows go `text-text-dim`. A `digest` row shows
  "· digest" and **no action link** — as the board draws it, because a rollup of
  three backups has no single thing to open.
- **Reading is explicit.** Opening the panel marks nothing; clicking an item's
  link marks that one and navigates. Auto-clearing on open would destroy the only
  signal the bell carries, and would make the board's own "Mark all read"
  meaningless.
- **Four states** via `PageState` (ui-principles §1): skeletons, error with
  retry, an empty state saying what belongs here ("Deploys and backups for your
  teams show up here"), and — distinctly, per ui-principles §7 —
  filtered-to-zero for the unread filter ("Nothing unread. Show all").
- **Freshness.** The mark mutations invalidate the count key, and `LiveProvider`
  invalidates it on any `invalidate` event. That is exact, not approximate: every
  transition that mints an item also emits a status invalidation, and §4's
  recipient set is a subset of that stream's visibility set. Stream down, the
  QueryClient's existing SSE-gated 5s poll covers it — no new stream, no new
  endpoint (ui-principles §10).

## 8. Acceptance (testable)

1. Two teams, one member each: a failed deploy in team A's project produces one
   unread item for A's member and **zero rows** for B's; `GET /inbox` as B is
   empty, and no route exists by which B addresses A's item.
2. A user who mutes `deploy.succeeded` gets no deploy digest but still gets
   `deploy.failed`; a teammate who muted nothing gets both.
3. Three successful backups in one project on one UTC day → **one** digest item
   per recipient reading "Backups: 3/3 succeeded", unread count 1, not 3.
4. Make the third fail: the failure is its own immediate item, the digest reads
   "Backups: 2/3 succeeded", unread count 2.
5. The same terminal observation handled twice leaves item count, counters and
   `sources` unchanged (rule 13 — converging twice equals converging once).
6. Mark one read, then all: the count falls and the items stay listed; marking
   an already-read item returns 204 and changes nothing.
7. Remove a user from a team → their items for that team's projects are gone,
   other members' untouched; deleting the project cascades the rest.
8. A user at 200 items receives one more → the oldest is pruned; `POST
   /notifiers/{id}/test` still writes no item at all.

Coverage follows the house tiers: `core/inbox` unit tests with a `fakeStore` and
a table-driven preference matrix; a real-Postgres `TestStoreInboxRoundtrip`
(rule 29) for the `TEXT[]`, the unread count, the digest upsert, the redelivery
guard and the cascade; and a `core/notify` test proving a dispatch with zero
notifiers still records items.

## 9. Out of scope this slice

Crash/restart-loop and deploy-approval items (canvas 13u's first two rows — no
observation point, no approval feature; they arrive with the taxonomy, §3) ·
every other notifications.md §8 follow-on kind, including canvas 13i's "new team
member" and "agent degraded" · live push of new items over `/api/v1/events`
(when it lands it is an additive `resource: "inbox"` invalidate, never a second
stream) · personal **email** digests (canvas 13i's other half — it reuses this
preference row when it ships) · per-team or per-project preference overrides ·
quiet hours, snooze, and any grouping beyond the daily digest · item-level
actions (approve, retry, redeploy — an item links, it never acts) · search and
filters beyond `?unread` · an audit log (different artifact, different
retention: this one caps at 200 rows and prunes) · unread counts broken out per
project · a keyboard shortcut for the bell (the shortcut table in `_app.tsx` is
a shared surface) · desktop, web-push or mobile notifications · real-time
cross-device read sync · localised relative timestamps.

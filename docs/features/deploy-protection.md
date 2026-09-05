# Feature spec: Deploy protection — approvals and freeze windows

> It is 18:04 on Friday. A push lands on `production`, the webhook fires, the
> pipeline starts, and the one person who could have judged the change is on a
> train. Deploy protection lets an Environment refuse to be surprised: an
> operator declares **who must approve a deploy there** and **when deploys are
> not allowed at all**, and the plane enforces both at the single point where a
> Deployment is born — before any work item reaches an agent.
>
> There is no feature-matrix row for this yet; this slice adds one under
> "Collaboration & API" — *Deploy protection (approvals + freeze windows)*,
> **V1.x** — in the same PR, and [glossary.md](../glossary.md) gains **Deploy
> Protection**, **Freeze Window**, and **Break Glass** (CLAUDE.md rule 5,
> ENGINEERING rule 33). It builds on [teams-and-roles.md](teams-and-roles.md)
> (the ranked role set it approves with) and
> [application-deploy.md](application-deploy.md) (the pipeline it gates).
> Neither reference has an equivalent, so nothing is ported — the model here is
> ours.
>
> Written 2026-08-21, just before implementing. **Revised at implementation and
> at review** (see [§10](#10-what-changed-at-implementation-and-why) for the five
> changes and their reasons: the migration number, an inbox audience the first
> draft put out of scope, the approval summary the drawer needs on a Deployment,
> the narrowing of the no-self-approval rule to *approval*, and the policy `PUT`
> becoming session-only). Vocabulary per [glossary.md](../glossary.md).

## 1. The core idea: one admission check, on one transition

Protection is **desired state about deploying**, not a new deploy mechanism. A
policy row hangs off an Environment; the pipeline in
[`core/scheduler`](../../core/scheduler/scheduler.go) consults it once, in
`Deploy` and `Rollback`, at the moment a Deployment is created and **before any
work item is published**. Nothing changes on the wire — no proto message, no
NATS subject, no agent code — exactly the posture
[teams-and-roles.md](teams-and-roles.md) §3 takes. A gated deploy is not a
special pipeline; it is the ordinary pipeline that has not been allowed to start
yet.

```
POST /applications/{id}/deploy       ┌─ frozen ──▶ 409, nothing written
POST /deployments/{id}/rollback ─gate┤             (an owner may break glass, then retry)
POST /webhooks/github/{id}           ├─ gated ───▶ Deployment(awaiting_approval)
                                     │            + DeployApproval(pending)   ← no work item
                                     └─ clear ───▶ tryStart() → build → rollout → serving
                                                     ▲
                            approve ─────────────────┘
                            reject  ──▶ Deployment(failed, "rejected by …")
```

**Protection is off by default.** An Environment with no protection row behaves
exactly as it does today, so this slice changes no existing panel's behaviour
until an operator opts in (ENGINEERING rule 17).

## 2. The resource model

Four small tables, all plane-side, all in migration
`core/store/migrations/0031_deploy_protection.sql` (additive and reversible,
rule 16; Down drops children first). The number is the next free one at
implementation time — `0021` was taken by `user_avatars` between this spec being
written and being built.

```
EnvironmentProtection:                  -- one row per protected Environment
  environment_id    TEXT PK → environments(id) ON DELETE CASCADE
  require_approval  BOOLEAN NOT NULL DEFAULT false
  min_approver_role TEXT    NOT NULL DEFAULT 'owner'  -- member|admin|owner (domain.RoleRank)
  freeze_enabled    BOOLEAN NOT NULL DEFAULT false    -- master switch over the windows
  created_at, updated_at

FreezeWindow:                           -- weekly recurring, may wrap the week
  id             TEXT PK (fzw_ prefix)
  environment_id TEXT NOT NULL → environments(id) ON DELETE CASCADE
  start_dow      SMALLINT NOT NULL   -- 0=Sunday … 6=Saturday (time.Weekday)
  start_minute   INTEGER  NOT NULL   -- 0–1439, minutes past local midnight
  end_dow        SMALLINT NOT NULL
  end_minute     INTEGER  NOT NULL
  timezone       TEXT     NOT NULL   -- IANA name, e.g. "Europe/Berlin"
  created_at
  INDEX (environment_id)

DeployApproval:                         -- exactly one gate decision per Deployment
  deployment_id  TEXT PK → deployments(id) ON DELETE CASCADE
  environment_id TEXT NOT NULL → environments(id) ON DELETE CASCADE
  requested_by   TEXT     → users(id) ON DELETE SET NULL   -- NULL for webhook deploys
  required_role  TEXT     NOT NULL   -- snapshot of min_approver_role at park time
  state          TEXT     NOT NULL   -- pending | approved | rejected
  decided_by     TEXT     → users(id) ON DELETE SET NULL
  decided_at     TIMESTAMPTZ
  reason         TEXT     NOT NULL DEFAULT ''
  created_at
  INDEX (environment_id, state)

BreakGlassGrant:                        -- a bounded, recorded freeze override
  id             TEXT PK (bg_ prefix)
  environment_id TEXT NOT NULL → environments(id) ON DELETE CASCADE
  opened_by      TEXT NOT NULL → users(id)
  reason         TEXT NOT NULL          -- required, 1–500 chars
  created_at, expires_at TIMESTAMPTZ NOT NULL
  INDEX (environment_id, expires_at DESC)
```

Two shapes are deliberate. **`DeployApproval` is keyed by `deployment_id`**: a
deployment gets at most one gate decision, and the natural key makes that an
invariant the database enforces rather than a rule the service must remember (no
new id prefix needed). **`required_role` is a snapshot**, for the same reason
`revisions` snapshot their config — relaxing `min_approver_role` while a deploy
is parked must not relax the deploy already parked. New prefixes in
`pkg/ids/ids.go`: `PrefixFreezeWindow = "fzw"`, `PrefixBreakGlass = "bg"`.

## 3. The parked state: one new word in the existing vocabulary

`domain.DeploymentStatus` (`core/domain/deploy.go`) gains **one** value:

```go
DeployAwaitingApproval DeploymentStatus = "awaiting_approval"   // non-terminal
```

`deployments.status` is already a plain `TEXT` column, so no DDL is needed;
`Terminal()` is unchanged, because a parked deploy has not finished.

**Rejection terminates the deployment as `failed`**, with
`detail = "rejected by alex@acme.com: shipping Monday"`. The terminal set
`('succeeded','failed')` is load-bearing in three places —
`core/store/queries/deployments.sql` (which rows are active, and when
`finished_at` is stamped), `DeploymentStatus.Terminal()`, and the web
`isTerminal()` — and a fifth terminal status would touch all three for no
observable gain. A rejected deploy is a deploy that did not ship; *why* it did
not ship lives on the `DeployApproval` row, which is what answers governance
questions anyway.

**Parked deployments leave the pipeline queue alone.** The two queue queries in
`core/store/queries/deployments.sql` become
`status NOT IN ('succeeded','failed','awaiting_approval')`. Without that, an
approval nobody gets round to would sit at the head of its application's queue
and block every later deploy, and `Scheduler.Recover` would try to resume it on
boot. With it: a parked deploy holds no pipeline slot, `Recover` needs no new
case, and approving it re-enters the ordinary queue through the existing
`tryStart`, so two approvals granted at once serialize like two manual deploys.

**Nothing is published, so nothing has to be recovered** — the parked state is a
row in Postgres and no more (ENGINEERING rule 15, vacuously). A plane restart
mid-approval is indistinguishable from no restart. And the **Application's own
status is untouched** while parked: `start()` is what sets an app to
`deploying`, and a parked deploy never reaches it, so the app keeps reporting
what it is actually doing. The six-word resource vocabulary
([ui-principles §5](../product/ui-principles.md)) gains nothing and lies about
nothing.

## 4. Gate evaluation

The gate is `core/protection.Service`, reached by the scheduler through a
consumer-defined interface (rule 6) that returns a plain
`domain.DeployAdmission{Frozen, FreezeDetail, NeedsApproval, RequiredRole}`. A
nil gate means "protection not wired" and every deploy is clear — the same
nil-guard the optional `Notifier` uses.

**Freeze is evaluated first**, and refuses before any row is written (no orphan
Revision): a hard "not now" is more useful than parking something that would
have to be refused later anyway. Evaluation is minute-of-week arithmetic in the
window's own zone:

```
now  → time.LoadLocation(window.timezone) → weekday*1440 + hour*60 + minute
in-window = start <= now < end                (ordinary window)
          = now >= start || now < end         (window wraps Sunday, e.g. Fri 18:00 → Mon 08:00)
```

Half-open, so `Mon 08:00` is already clear. Windows are evaluated on **wall
clock in the declared zone**, so a DST change makes the window an hour shorter
or longer in absolute terms twice a year — that is what "no deploys after six on
Friday" means to whoever wrote it, and an absolute-instant window would drift off
the working day.

`time.LoadLocation` resolves against the host's zone database, and `cypherd` is
a static binary (ADR-001) that can land on an image without
`/usr/share/zoneinfo`. The plane therefore imports `_ "time/tzdata"` in
`core/cmd/cypherd/main.go` — ~450 KB of embedded IANA data, negligible against
vision's <300 MB plane budget, which also hardens the existing profile-timezone
validation in `core/auth/profile.go`. A zone that still fails to load at gate
time **refuses the deploy** (§5).

**The approval branch** stays inside the scheduler, because the transition it
guards is the scheduler's: `Deploy`/`Rollback` create the Revision and the
Deployment as they do today, write the `DeployApproval` row (`state=pending`,
`requested_by` = the calling user or NULL for a webhook), set the deployment to
`awaiting_approval`, and return *without* calling `tryStart`. The protection
service drives the decision back through two new scheduler methods —
`ApproveDeployment` (assert `awaiting_approval`, set `queued`, `tryStart`) and
`RejectDeployment` — so the pipeline keeps its single owner and its lock.

**Break glass** is `POST /environments/{id}/break-glass` with a required
`reason`: a team owner opens a grant that expires 30 minutes later, and while an
unexpired grant exists the freeze gate for that environment is bypassed. It does
**not** bypass the approval gate — two independent controls, and an owner who is
also the approver can decide their own deploy under the sole-approver rule
anyway (§5). A bounded grant beats a flag on the deploy request because an
incident is rarely one deploy (rollback, then hotfix, then second hotfix), the
grant is one record the screen can show live — *"Break glass: 22 min left,
opened by alex — checkout returning 500s"* — and the deploy routes keep their
request shapes (rule 17). The 30 minutes is a constant, not a setting: long
enough for an incident, short enough that it cannot quietly become the operating
mode.

## 5. Authorization, security and bounds

| Action | Requires |
|---|---|
| Read protection, windows, approvals, grants | team **member** |
| `PUT` protection (flags, roles, windows) | team **admin**, in an **interactive session** |
| Approve / reject a parked deploy | rank ≥ the approval's `required_role`, in an **interactive session** |
| Open a break-glass grant | team **owner**, in an **interactive session** |

Enforcement reuses `core/api/rest/authz.go` unchanged: environment routes
resolve through the existing `projectIDForEnvironment`, deployment routes
through `projectIDForDeployment`, both via `authorizeResolved` — so a non-member
gets 404 and an under-ranked member gets 403, with the panel-owner bypass from
[teams-and-roles.md](teams-and-roles.md) §1 applying as everywhere else.

- **No self-approval.** On **approve**, `decided_by` may not equal
  `requested_by` — *unless* the approver is the only member of the team at or
  above `required_role`. Without that escape a solo operator (personas P1/P4)
  would wedge their own panel the moment they enabled protection; with it,
  "two-person rule where two people exist" is the honest description. The count
  is of explicit `team_members` rows: a panel owner's implicit rank is an
  authorization escape hatch, not a second person.

  The rule applies to approve **only**. Rejecting your own deploy is
  withdrawing your own request: it grants nothing, so the two-person rule has
  nothing to protect there, and refusing it would leave a requester unable to
  cancel a deploy they no longer want.
- **Approve, reject, break-glass and the policy `PUT` are `sessionOnly`.** An
  API token inherits its owner's role, so a `deploy`-ability token in CI could
  otherwise approve the deploy it had just requested and the gate would be
  decorative — the same reasoning that already keeps credential management off
  tokens (threat-model §5.8). Deploy routes are unchanged, so CI keeps working;
  its deploys park. `deployRoutes` in `core/api/rest/rest.go` therefore gains no
  entry: approve triggers a rollout, but no token ability can reach it.

  The `PUT` is on that list for a stronger reason than the other three, and its
  place there is a revision (§10.5). A `write`-ability token whose owner is a
  team admin does not need to *open* the gate: it can send
  `{require_approval: false, freeze_enabled: false, windows: []}` and delete it,
  which is strictly more powerful than the single 30-minute grant that suspends
  one freeze. "A leaked CI token must not be able to switch a protection control
  off" has to cover switching it off wholesale, or it covers nothing.
- **Fail closed.** An unloadable timezone, an unreadable protection row, or any
  store error during the check refuses the deploy rather than passing it. A
  protection control that fails open is worse than none, because it is trusted.
  Decisions are once-only: approving or rejecting a non-`pending` approval → 409.
- **Bounds.** At most 8 freeze windows per environment; `reason` 1–500
  characters, single-line, stored verbatim and rendered as text; grants expire on
  their own and are never revoked early.
- **This is not a control against a compromised plane.** Threat-model §5.1 is
  explicit that an attacker holding the plane can command deployments; deploy
  protection reduces the *accidental* blast radius — the Friday push, the
  mis-clicked rollback — and is honest about that bound. It stores no
  credentials, so no `secret.Box` sealing appears anywhere in this slice.

## 6. API surface (under `/api/v1`)

```
GET  /environments/{id}/protection            → EnvironmentProtection    (member; defaults when unset)
PUT  /environments/{id}/protection            → EnvironmentProtection    (team admin, session only; whole document)
GET  /environments/{id}/approvals?state=      → [DeployApproval]         (member; default state=pending, 100 newest)
POST /environments/{id}/break-glass           → 201 BreakGlassGrant      (team owner, session only)
GET  /environments/{id}/break-glass           → [BreakGlassGrant]        (member; active + recent)

GET  /deployments/{id}/approval               → DeployApproval           (member)
POST /deployments/{id}/approve                → 202 Deployment           (approver rank, session only)
POST /deployments/{id}/reject   {reason}      → 200 Deployment           (approver rank, session only)
```

`PUT /protection` carries the **whole** document — flags plus the complete
window list — and replaces it wholesale (desired state; empty `windows` means no
freeze). Every field of it is required for that reason, `min_approver_role`
included: an omitted `require_approval` read as `false` would turn a client that
forgot a field into "approval is now off", answered `200`, and an omitted
`min_approver_role` defaulted to `owner` is the same silent rewrite in the
tightening direction. Each omission answers `400` naming the field. `GET` on an unprotected environment
returns the default document, not 404: "not protected" is an answer, and a 404
would make the UI invent one. `GET /approvals` defaults to `state=pending` — the
queue is what the screens want — and takes the literal word `all` for no filter,
because an empty query parameter is indistinguishable from a client that forgot
it. `GET /break-glass` derives an `active` flag from the plane's own clock, so a
screen needs no clock to know whether an override still applies. Both listings
are **bounded** — 100 approvals, 20 grants — because `state=all` on a long-lived
environment asks for a history that grows without end, and `Deployment.approval`
on a list is looked up for the ids on the page rather than for every deploy the
application has ever run.

One **additive** field appears on an existing resource: `Deployment.approval`,
the gate decision summary (`state`, `required_role`, `requested_by`,
`requested_by_email`, `decided_by`, `decided_by_email`, `decided_at`, `reason`).
It is present only on a deployment that was gated and omitted entirely
otherwise, so no existing client sees a change (rule 17), and it is what lets
the pending card render from the payload the drawer already has instead of a
second request per row. The list route attaches it for a whole Deployments tab
in one query rather than one lookup per row, and it is best-effort on both:
a summary that cannot be loaded leaves the deployment looking exactly like an
ungated one rather than failing a request that is otherwise answerable.

The two derived read-model fields elsewhere follow the same rule. A
`FreezeWindow` is returned with a `summary` — `"Fri 18:00 → Mon 08:00
(Europe/Berlin)"` — composed on the plane so the panel, the API and a CLI print
the same sentence (CLAUDE.md rule 4), and never stored.

Behaviour changes on existing routes, all additive:

```
POST /applications/{id}/deploy    → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
POST /deployments/{id}/rollback   → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
POST /webhooks/github/{id}        → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
POST /templates/{slug}/install    → 202                                       |  409 (frozen)
```

A template install deploys, so it passes the same gate — and a refusal rolls the
install back, so the 409 leaves no half-created resources behind.

The 409 body names the window and when it lifts
(`"production is frozen until Mon 08:00 Europe/Berlin"`), so a failed GitHub
delivery is diagnosable from the response alone and can be redelivered after the
window.

## 7. The screens

> **Status.** This slice ships the API these screens need — the protection
> document with rendered window summaries, the approval queue, `Deployment.approval`
> for the pending card, the `awaiting_approval` status in the
> OpenAPI enum, the 409 body, and the break-glass grant with its derived
> `active` flag — and the generated client in `web/src/api/gen` follows from it.
> The routes and components below are the web slice and are not in this change
> set (§11).

**Protection settings** is a new route at
`web/src/routes/_app/projects/$projectId/protection.tsx`, a sibling of the
existing `$projectId/settings.tsx`, reached at
`/projects/{id}/protection?env={envId}` — environments are a search param, not a
route, and the top-level nav stays at exactly four items (ui-principles §4).
Breadcrumbs read `ATLAS-CRM / PRODUCTION / PROTECTION`. Two `FactCard` sections
mirror the board: **Require approval** (a switch plus a role select, under
"Deploys to this environment wait for 1 approval from an owner. Webhook deploys
queue as pending approval.") and **Freeze window** (start day+time, end
day+time, IANA zone, rendering "No deploys Fri 18:00 → Mon 08:00
(Europe/Berlin)" and "Owners can break glass — it's recorded with their name and
reason"). Break glass opens a dialog shaped like `ConfirmDestructive` — blast
radius spelled out, the required reason as the gate instead of a typed name.

**The pending-approval card** appears at the top of the environment view on
`$projectId/index.tsx` when that environment has pending approvals, and inside
the deployment drawer on the application's Deployments tab: *Pending approval —
deploy `dep_9f2…`* with the relative queue age, then
`web · c99d2e1 · requested by alex@acme.com` (or `· pushed via webhook`), then
**Approve & deploy** (the screen's one accent action) and **Reject**, which
opens a dialog for the required reason. An under-ranked viewer sees both
controls disabled with a `disabledReason` ("Only an owner can approve deploys to
production") — never hidden, never mystery-disabled.

Two board ambiguities resolved. It shows *"deploy #215"* and a commit message
with a GitHub author; we mint no deployment sequence numbers and persist neither
commit messages nor git authors, so the card shows the short deployment id and
the revision's commit SHA — the missing metadata is a deferral, not an omission
(§9). And `deployments.tsx` currently maps every non-terminal deployment to the
animated blue `deploying` dot, which would pulse for a deploy doing nothing:
parked deploys map to grey `stopped` with the word "awaiting approval", and
`PipelineStages` orders `awaiting_approval` beside `queued` at `-1`. Four page
states, both themes, 360 px, as for every screen (ui-principles §1, §9).

## 8. Acceptance (testable)

1. `PUT` protection on `production` with `require_approval=true`,
   `min_approver_role=owner`; deploy an app as a team member → 202 with
   `status=awaiting_approval`, no work item is published (the agent observes
   nothing), and `GET /environments/{id}/approvals` lists it as pending.
2. Approve it as a team owner → the same deployment runs the ordinary pipeline
   unchanged and ends `succeeded` with the app serving the new revision.
3. Reject it with a reason → the deployment ends `failed` with a detail naming
   the rejecter, the approval row reads `rejected` with `decided_by`,
   `decided_at` and the reason, and no container ever changed.
4. A signed GitHub push webhook into a protected environment → 202 with a parked
   deployment, not a build.
5. Restart `cypherd` while a deploy is parked → after `Recover()` it is still
   `awaiting_approval`, was not started, and approving it still works.
6. The requester approving their own deploy → 403 while a second qualifying
   approver exists; the identical call succeeds when they are the only member at
   or above `required_role`.
7. An API token with the `deploy` ability creates a parked deployment
   successfully but is refused at `POST /deployments/{id}/approve` (403,
   interactive session required).
8. Freeze `Fri 18:00 → Mon 08:00 Europe/Berlin`: a deploy at Sat 12:00 Berlin →
   409 naming the window and when it lifts, with no Revision or Deployment row
   left behind; the same deploy at Mon 08:00 Berlin runs.
9. Break glass as a team owner with a reason → the next deploy inside the same
   window runs; `GET /environments/{id}/break-glass` shows who, why and when it
   expires; a team admin attempting the same call → 403.
10. `GET /environments/{id}/protection` on an environment that has never been
    protected → the default document (`require_approval=false`, `windows: []`),
    and every deploy there behaves exactly as before this slice.

## 9. Out of scope this slice

N-of-M / multi-approver rules (one approval is the whole v1 model) ·
per-application protection (the Environment is the unit an operator reasons
about) · approval by API token or bot (§5 — it would make the gate decorative) ·
one-off or dated freeze windows such as a holiday range (weekly recurring only;
the shape generalizes later without a rewrite) · auto-release of anything when a
freeze lifts (a deploy nobody re-requested is stale by then) · TTL or auto-reject
on a stale pending approval · a **notifier or outbound-webhook** event for a
pending approval or a decision ([notifications.md](notifications.md) §3's
taxonomy is fed by *terminal* observed transitions, and a parked deploy has not
finished; announcing a rejection down those channels would also misreport a
governance decision as an infrastructure failure — see §9.1 for the in-panel
audience that *is* in scope) · commit message and git author on the pending card
(nothing persists them today) · a general audit log — landed since, as
[audit-log.md](audit-log.md): approval decisions and break-glass grants remain
first-class rows *here*, and are additionally recorded there as
`protection.approved`, `protection.rejected` and `protection.break_glass_opened`
so they appear in the same timeline as the deploys they gated · early
revocation of a break-glass grant · protection on Managed Databases and Compose
Stacks (they have no Deployment record to park) · inherited protection for
preview environments (previews are ordinary environments with a TTL and get no
protection row, so they stay unprotected — freezing them would strand every open
PR) · requiring approval to *change* protection itself.

### 9.1 In scope after all: the inbox, and nothing louder

A parked deploy that nobody is told about is a deploy that sits until someone
happens to look, which is the failure mode this feature exists to prevent. The
first draft put "a notification event for a pending approval" out of scope
without separating the two audiences it could mean; the second half of that
sentence — *"the card plus the environment view carry it"* — only holds for
somebody already on the screen.

So the slice adds **inbox items and only inbox items**
([notification-inbox.md](notification-inbox.md)), three new kinds in
`domain.InboxKinds()`:

| Kind | Written when | Addressed to |
|---|---|---|
| `deploy.awaiting_approval` | a deploy parks | the project's team members at or **above** the approval's `required_role` |
| `deploy.approved` | an approval is granted | the requester, and nobody else |
| `deploy.rejected` | an approval is refused | the requester, and nobody else |

Three properties are the point. **The awaiting item is rank-narrowed**: an item
you cannot act on is noise, so a `member` is not told about an owner-only
approval — the fan-out query filters on the same `RoleRank` ladder the routes
authorize with. **The decision items go to one person**, the one who asked;
a webhook deploy has no requester, so it produces none at all rather than
fanning out to a team that never asked for anything. And **all three are
severity `info` and written immediately**, not digested: a deploy waiting for a
person is the control working, not a fault, and a rollup of "3 deploys awaited
approval today" is unactionable — the digest windows stay defined only for the
two terminal outcome families.

They are inbox kinds *only*, exactly like `panel.update_available`: no notifier
channel, no outbound webhook, no new `domain.EventType`. Mutes apply as they do
to every other kind, the dedupe key is `<kind>:<deployment_id>` so a redelivered
decision is a no-op (rule 12), and a requester who has left the team holds no
item for it. The write is best-effort at the call site: the decision is already
recorded and the approval is reachable from the environment's queue and the
deployment drawer, so a failing inbox is logged, never propagated into a failed
decision.

It is also **detached**, on the same contract `notify.Manager.dispatch` already
uses: a context that outlives the caller's, a bounded timeout, a logged failure.
Two reasons, one per call site. `Park` runs *inside the scheduler's pipeline
lock* — a fan-out there would hold every other deploy on the panel behind a
handful of inbox writes. And a decision announcement runs on a request context
that dies the moment the response is written, which would cancel the write
mid-flight. The approval row itself is written synchronously and its failure
does fail the deploy (§4), because a deployment parked with no approval row
could never be decided; only the telling is detached.

## 10. What changed at implementation, and why

Five changes to the spec above, all recorded here rather than silently applied.

1. **The migration is `0031`, not `0021`.** `0021` was taken by `user_avatars`
   between this spec being written and being built. §2 updated.

2. **Inbox items are in scope** (§9.1). The original §9 exclusion conflated the
   *notifier* taxonomy — which is fed by terminal observed outcomes and is
   rightly untouched here — with telling the people who can act that something
   is waiting for them. The first is still out; the second is the difference
   between a gate and a bottleneck.

3. **`Deployment.approval` is exposed** (§6). §7 asks for a pending card in
   three places, two of which render from a deployment payload the screen
   already has. Without the summary each of them needs a second request per row
   — `GET /deployments/{id}/approval` — which is a request per row on the
   Deployments tab. The field is additive and absent on an ungated deployment,
   so it costs no existing client anything.

4. **The no-self-approval rule is narrowed to approve** (§5). The original
   sentence — "`decided_by` may not equal `requested_by`" — sat under a heading
   about approval but was written over both verbs. Applying it to rejection
   would stop a requester withdrawing their own deploy, which grants nobody
   anything and has no privilege to protect.

5. **The policy `PUT` is `sessionOnly` too** (§5). The first draft's table put
   it at team admin and stopped there, and the implementation matched the table
   faithfully — but the table was wrong. The reasoning that makes break glass
   session-only is that a leaked CI token must not be able to switch a
   protection control off; a `write`-ability token whose owner is a team admin
   did not need to open the gate, it could `PUT`
   `{require_approval: false, freeze_enabled: false, windows: []}` and remove
   it, which is more powerful than the 30-minute grant that route protects.
   Fixed at review, with the threat-model bullet ("A deploy gate an API token
   cannot open") narrowed to say what actually holds.

Two smaller decisions the spec did not settle, recorded for the reviewer:

- **A rejection emits nothing to the notifier/webhook sinks.** The scheduler's
  ordinary `fail()` announces a terminal outcome; a parked deploy takes a
  separate path (`failParked`) that does not, because the deploy never ran and
  "deploy failed" in Slack would describe a governance decision as an
  infrastructure failure. It also has no `deploying` override to take back and
  no queue slot to promote, so `fail()`'s other two jobs are moot as well.
- **Both approval listings are bounded** (§6). `state=all` and the
  `Deployment.approval` decoration on a Deployments tab both grew with an
  application's or an environment's whole history, beside neighbours
  (`ListDeploymentsByApplication`, `ListBreakGlassGrants`) that are bounded. The
  environment queue takes a limit of 100; the per-application read now takes the
  ids of the page being decorated rather than the application.
- **A freeze window whose zone will not load refuses the deploy** and says so,
  naming the zone (operator-supplied configuration, not a secret) so the fix is
  visible from the response. Break glass suspends *the freeze gate*, whatever
  made it say no — including that refusal — which keeps a typo'd zone from
  being unrecoverable for an owner mid-incident. Where several windows overlap,
  the refusal names the one that lifts **last**: naming an earlier one would be
  a lie the next attempt exposes.

## 11. Deferred to the web slice

Everything in §7 below the status note **except the status mapping**: the
`$projectId/protection.tsx` route, the shared `Switch` primitive, the
freeze-window editor, the reason-gated break-glass dialog, and the
pending-approval card in its three places. The API they consume is complete and
generated into `web/src/api/gen`, so none of them is blocked on the plane.
Nothing else from §§1–8 is outstanding.

**Not deferred, and it should not have been:** the panel's three `isTerminal`
helpers classified `awaiting_approval` as *live*, which is not a cosmetic
mismatch. A parked deploy would have shown the animated blue `deploying` dot for
something doing nothing, the application's Deploy button would have stayed
disabled for as long as the deploy sat there, and both the drawer and the deploy
toast would have polled every three seconds forever, because their
`refetchInterval` only stops on a terminal status. So the mapping ships with the
plane that can produce the state: `awaiting_approval` is neither live nor
terminal, it renders as the grey `stopped` dot with the word "awaiting
approval", and `PipelineStages` orders it beside `queued` at `-1`. The editor
and the card can wait; a panel that lies about what it is doing cannot.

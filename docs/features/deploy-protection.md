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
> Written 2026-08-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

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
`core/store/migrations/0021_deploy_protection.sql` (additive and reversible,
rule 16; Down drops children first).

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
| `PUT` protection (flags, roles, windows) | team **admin** |
| Approve / reject a parked deploy | rank ≥ the approval's `required_role`, in an **interactive session** |
| Open a break-glass grant | team **owner**, in an **interactive session** |

Enforcement reuses `core/api/rest/authz.go` unchanged: environment routes
resolve through the existing `projectIDForEnvironment`, deployment routes
through `projectIDForDeployment`, both via `authorizeResolved` — so a non-member
gets 404 and an under-ranked member gets 403, with the panel-owner bypass from
[teams-and-roles.md](teams-and-roles.md) §1 applying as everywhere else.

- **No self-approval.** `decided_by` may not equal `requested_by` — *unless* the
  approver is the only member of the team at or above `required_role`. Without
  that escape a solo operator (personas P1/P4) would wedge their own panel the
  moment they enabled protection; with it, "two-person rule where two people
  exist" is the honest description.
- **Approve, reject and break-glass are `sessionOnly`.** An API token inherits
  its owner's role, so a `deploy`-ability token in CI could otherwise approve the
  deploy it had just requested and the gate would be decorative — the same
  reasoning that already keeps credential management off tokens (threat-model
  §5.8). Deploy routes are unchanged, so CI keeps working; its deploys park.
  `deployRoutes` in `core/api/rest/rest.go` therefore gains no entry: approve
  triggers a rollout, but no token ability can reach it.
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
PUT  /environments/{id}/protection            → EnvironmentProtection    (team admin; whole document)
GET  /environments/{id}/approvals?state=      → [DeployApproval]         (member; default state=pending)
POST /environments/{id}/break-glass           → 201 BreakGlassGrant      (team owner, session only)
GET  /environments/{id}/break-glass           → [BreakGlassGrant]        (member; active + recent)

GET  /deployments/{id}/approval               → DeployApproval           (member)
POST /deployments/{id}/approve                → 202 Deployment           (approver rank, session only)
POST /deployments/{id}/reject   {reason}      → 200 Deployment           (approver rank, session only)
```

`PUT /protection` carries the **whole** document — flags plus the complete
window list — and replaces it wholesale (desired state; empty `windows` means no
freeze). `GET` on an unprotected environment returns the default document, not
404: "not protected" is an answer, and a 404 would make the UI invent one.

Behaviour changes on existing routes, all additive:

```
POST /applications/{id}/deploy    → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
POST /deployments/{id}/rollback   → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
POST /webhooks/github/{id}        → 202 Deployment(status=awaiting_approval)  |  409 (frozen)
```

The 409 body names the window and when it lifts
(`"production is frozen until Mon 08:00 Europe/Berlin"`), so a failed GitHub
delivery is diagnosable from the response alone and can be redelivered after the
window.

## 7. The screens

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
on a stale pending approval · a notification event for a pending approval
([notifications.md](notifications.md) §3's taxonomy is fed by *terminal*
transitions, and the card plus the environment view carry it until that taxonomy
grows) · commit message and git author on the pending card (nothing persists
them today) · a general audit log (**V1.x**, feature-matrix — approval decisions
and break-glass grants are first-class rows here, not log lines) · early
revocation of a break-glass grant · protection on Managed Databases and Compose
Stacks (they have no Deployment record to park) · inherited protection for
preview environments (previews are ordinary environments with a TTL and get no
protection row, so they stay unprotected — freezing them would strand every open
PR) · requiring approval to *change* protection itself.

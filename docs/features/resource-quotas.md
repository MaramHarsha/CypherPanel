# Feature spec: Resource quotas

> A cap on the memory, disk and live previews one **Project** or one **Team**
> may consume, so a runaway hobby project cannot starve a client's production
> app on the same fleet. The panel warns at 90% of a cap, refuses new work at
> 100%, and a bounded, recorded owner override exists for the night the fix has
> to ship anyway.
>
> This spec crosses a line on [vision.md](../vision.md)'s explicitly-out-of-scope
> list and §1.1 is about that, not about quotas.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. The failure this exists to stop

The panel can already cap **one container**. `applications.cpu_limit` /
`memory_limit_mb` and the same pair on `databases` have existed since
`0012_app_resource_limits.sql`, and the feature matrix calls that row
*"noisy-neighbour control on shared servers"* — **V1**, shipped. What no
number in this codebase can express is the aggregate: an agency running eleven
clients on four boxes can cap every container individually and still watch one
project open forty preview environments, keep six revisions of images per
application, and fill the disk that a paying client's database writes to. Every
individual limit was respected. The outage happened anyway.

That is the gap. A per-resource limit bounds a container; nothing bounds a
*tenant*. The three dimensions where a tenant actually runs the fleet out of
room are memory commitment, disk, and the number of live previews — and the
third is not a rounding error: [preview-environments.md](preview-environments.md)
§9 lists *"per-environment preview count caps"* as deferred, and §6 of that spec
is honest that a noisy fleet of previews is bounded today *"by the TTL and by
the operator not enabling previews on unlimited apps"*, which is to say by
nothing the panel enforces.

**The fix is admission control on the control plane.** A Quota is desired state
about what a scope may consume; the meter is arithmetic over desired state and
over the usage figures [metrics-and-usage.md](metrics-and-usage.md) already
collects; and the enforcement point is the place a Deployment is born, which is
the same place [deploy-protection.md](deploy-protection.md) already asks its
question. Nothing new runs on a node. Nothing new rides the wire. No agent
learns that quotas exist.

### 1.1 The vision line this crosses, and how it is resolved

[vision.md](../vision.md) lists **"SaaS billing and metering"** under
*Explicitly out of scope*, with the reason in the parenthesis: *"the
open-source product comes first; cloud concerns must never leak into core, the
way Stripe code is threaded through Coolify."* And
[metrics-and-usage.md](metrics-and-usage.md) §8 named this feature from the
other side, in the strongest terms it could:

> **Nothing in this feature ever refuses an action.** There is no quota, no
> cap, no soft limit. Usage is reported; it never gates a deploy. Metering with
> teeth is what "metering" means in that vision line, and this has none. […] If
> a future feature wants price fields or quota enforcement, that is a change to
> the vision's out-of-scope list and needs a recorded decision, not an extra
> column on this table.

So this is not a spec that can quietly proceed. It is the feature that section
predicted, and the sentence to take seriously is *"metering with teeth is what
metering means in that vision line."* Quotas have teeth. The question is whether
those teeth are the commerce the vision excludes.

They are not, and the distinction is sharper than it first looks.

**A billing quota is an entitlement.** It is granted by a seller to a buyer,
denominated in money or in a plan that has a price, enforced to protect
*revenue*, measured to an accuracy a bill can survive being disputed at, and
reconciled against an external system of record — a payment processor, an
invoice, a subscription. It is a commercial relationship expressed as a limit.

**A guardrail quota is an operator's policy about their own machines.** Nobody
is charged. There is no seller, no buyer, no entitlement grant, no plan, no
upgrade path, no "contact sales". The operator sets it, the operator raises it,
and the only party it protects is the operator's own other workloads. Its
measurement may be approximate — §4.2 is explicit that the disk figure is
conservative and up to an hour stale — because the consequence of being wrong
is a deploy refused ten minutes early, which is a nuisance, not a disputed
invoice.

**And the panel already does this.** A Freeze Window refuses a deploy. An
approval requirement parks one. `ON DELETE RESTRICT` on a registry credential
refuses a deletion and names what is using it. Deploy protection is a guardrail
with teeth, set by the operator, enforced by the plane, overridable under
Break Glass, and nobody has ever called it billing. A quota is the same
governance mechanism with a resource dimension where protection has a clock.

Three rules make that boundary enforceable rather than rhetorical, and they are
deliberately the same shape metrics-and-usage §8 used for its own:

1. **No monetary concept exists anywhere in this feature** — no price, no rate,
   no currency, no plan, no invoice, no payment integration, in the schema, the
   API or the UI. A quota is denominated in bytes and in counts. An operator
   who wants a bill multiplies our numbers by their own, in their own
   spreadsheet, off the panel — which is precisely where metrics-and-usage §8
   left that boundary and this does not move it.
2. **A quota is set by an operator on infrastructure they own.** There is no
   grant, no entitlement, no external system of record, no upgrade flow and no
   tier. The panel never learns what anything costs, and nothing here is
   reachable by a caller who is not already an administrator of the thing being
   capped.
3. **It cannot become billing by increments.** Adding a price column to a quota
   row, an entitlement source outside the panel, or a tier concept is a change
   to the vision's out-of-scope list in its own right, and needs its own
   recorded decision. This spec buys enforcement, not a foundation for commerce.

**The recorded decision.** metrics-and-usage §8 asked for one, and CLAUDE.md
rule 2 plus ENGINEERING rule 31 say where it lives: architectural decisions go
in `docs/adrs/`, and a feature spec that re-read a vision non-negotiable on its
own authority would be exactly the re-litigation those rules forbid. So the
implementation PR lands **ADR-012 — guardrail quotas are not metering** beside
this spec, recording the three rules above as its consequences and the
alternative it rejects (deferring quotas entirely and leaving the aggregate
gap open indefinitely). This spec is the design; the ADR is the permission.
The same PR corrects metrics-and-usage §8 to point here — its own sentences
stay true, because that feature still never refuses anything, but its
"quota enforcement is banned" line needs the forward reference (ENGINEERING
rule 33, one topic one home).

## 2. What a Quota is, and what it is not

**Resource Quota** — a cap on what one Project or one Team may consume, in
three named dimensions, with a warning band and a refusal point. Desired state,
stored in Postgres, evaluated on the plane.

The single sentence everything else follows from: **a quota refuses new work; it
never acts on work that is already running.** It does not kill a container,
throttle one, evict one, change an OOM score, truncate a volume or delete a
revision. It declines to admit the next deploy, the next preview, the next
raised limit.

That is a deliberate narrowing and it is worth defending, because "enforce" to
most people means the other thing. Three reasons:

- **A quota that killed containers would make the guardrail the outage.** The
  whole premise is protecting a client's production app; a mechanism that
  reclaims memory by stopping something is one bad arithmetic away from
  stopping the wrong thing. Refusing a deploy is recoverable by the person who
  asked for it, thirty seconds later. Stopping a running app is not.
- **The signal cannot support it.** Disk is an hourly, deliberately
  over-counting figure (metrics-and-usage §4.2 and §4.2 below). Acting
  destructively on a number that is approximate by design is indefensible;
  refusing new work on it is fine, because the cost of being ten minutes early
  is nothing.
- **It keeps the feature inside ADR-005 and ADR-002.** There is no verb. The
  plane does not tell a node to do anything, because there is nothing for a
  node to do.

## 3. Scope: Project and Team

A Quota attaches to a **Project** or to a **Team**, and to nothing else.

Membership follows the ownership chain the plane already stores and the access
layer already walks: a resource is inside a project scope when its Environment
belongs to that Project, and inside a team scope when that Project belongs to
that Team. **Preview environments count**, because previews are ordinary
environments with a TTL (glossary) and a project that opens forty of them is
exactly the failure this exists to stop.

**Both apply, and the most restrictive wins.** A resource may sit under a
project quota and its team's quota at once; each is evaluated and the refusal
names which one refused, with its two numbers. A message that says only "over
quota" sends an operator to raise the wrong cap.

**Over-commitment is allowed on purpose.** The sum of a team's project caps may
exceed the team cap, and that is not an error. Thin provisioning is the normal
case — eleven clients do not peak together — and forcing an operator to keep
two numbers in lockstep would buy nothing except a refusal at the moment they
were trying to be careful. The team quota screen shows the sum against the cap
so the over-commitment is *visible*, which is the honest treatment: show it,
do not forbid it.

**Rejected: a quota on an Environment.** An Environment is the unit of
promotion and of deploy protection, not of capacity — production and its
staging sibling usually share a box, and an operator reasons about "this
client", not "this environment". A per-environment cap would also collide with
previews, which are environments that appear without anyone setting policy on
them.

**Rejected: a quota on a Server.** A cap per host is a placement policy, and
placement is [app-scaling.md](app-scaling.md) §4's problem — that spec spent
its vision argument (§1.1) keeping a scheduler out of this product, and a
per-server quota is the input a packing scheduler needs. Server capacity is
already answered honestly by [disk-management.md](disk-management.md) §4's
`disk_total_bytes`/`disk_free_bytes` on the Server DTO and by
metrics-and-usage's host rows.

**Who may set which.** The asymmetry is the point of the feature:

| Quota | Set/cleared by | Overridden by |
|---|---|---|
| Project | team **admin** (the rank that creates projects in a team) | team **owner** |
| Team | panel **admin** (the rank that already gates shared infrastructure) | panel **owner** |

A team quota set by that team's own owner would guard nothing against that
team, which is the only thing it exists to guard against. Panel admin is the
rank that already owns servers, deploy keys and backup targets
([teams-and-roles.md](teams-and-roles.md) §1) — shared infrastructure — and a
fleet-wide capacity policy is that. Overriding needs one rank above setting, the
same relationship deploy protection has between its policy `PUT` (team admin)
and Break Glass (team owner).

**Both `PUT`s and both override `POST`s are `sessionOnly`**, for the reason
`core/api/rest/rest.go` already records for the protection policy: an API token
inherits its owner's role, so a `write`-ability token belonging to a team admin
could otherwise raise the cap and then deploy freely, and a control a leaked CI
credential can switch off is decorative. Reading a quota is *not* session-only
and needs only team **member** rank — a member whose deploy was refused must be
able to see why, and a 409 that cannot be explained by any page the reader can
open is a dead end (ui-principles §11).

## 4. The three dimensions, and what each number actually is

### 4.1 Memory is declared, not observed

The meter is the **sum of declared memory limits** of the Applications and
Managed Databases in scope — `memory_limit_mb × replicas` — not their observed
consumption.

This is the one place where the obvious design is wrong, and the argument is
short: **a workload that has not started uses nothing.** An observed meter is
zero for the very thing being admitted, so it can never refuse a new
application, a new database or a new preview; it would admit everything and
complain an hour later, which is the behaviour the panel has today. Only a
declared meter can answer *"will this fit"* before the container exists, and
only arithmetic over desired state can answer it exactly.

It follows that **a resource with no declared limit makes the quota a
fiction**, and that hole is closed rather than tolerated, because the runaway
project this feature exists to stop is precisely the one that never set a
limit:

- **Setting a memory quota on a scope containing an Application or Managed
  Database with a null `memory_limit_mb` is a `409` that NAMES them**, and no
  quota row is written. This is [registries.md](registries.md)'s rule — *a
  capability is checked when it is attached, not when it is spent* — for that
  spec's reason: the operator learns at the moment they can act, not at 3am
  when somebody's deploy is refused for a reason they cannot see.
- **Creating or deploying an unlimited resource into a scope that already has a
  memory quota is the same `409`**, naming the resource and the fix.
- The UI's remedy is constructive: each named resource gets a limit field
  prefilled from its observed peak plus 25%, out of metrics-and-usage's
  `memory_bytes_peak`. **The panel suggests; the operator confirms.** Stamping
  a number on someone's application on their behalf is how a platform earns an
  OOM kill nobody can explain.

**Compose Stacks are not in the memory meter, and that is a real hole.** Their
memory is declared inside their own file, and `core/compose/compose.go` parses
exactly two fields on purpose — its own comment says *"Everything else is
passed through untouched — the point of this resource is that the file runs
as-is, and reimplementing the spec here would make that false."* Reading
`mem_limit` and `deploy.resources.limits.memory` out of it would be worse than
not reading them: most catalog stacks declare a limit on some services and none
on others, so a six-service stack would meter as two services' worth and *look*
complete. The alternatives are both rejected — refusing Compose Stacks in
memory-capped scopes would break ~130 catalog templates for a number we could
not compute anyway, and storing a panel-side limit beside the file would create
the second place to say the same thing that [compose-stacks.md](compose-stacks.md)
§1 exists to avoid. So the quota screen states it in words: *"N compose stacks
are not counted — their memory is declared in their own file."* §13 records the
condition on which this changes.

`replicas` is written into the meter now even though every application has one
today, because the meter is a function over the scope rather than a stored
counter (§8); when app-scaling lands, nothing here changes.

### 4.2 Disk is observed, hourly, and deliberately conservative

The disk meter reads `resource_disk_usage` (metrics-and-usage §4.2) — the
latest complete hourly bucket per resource in scope, summed. Three things are
true about that number and all three belong in the UI, not only here:

1. **It over-counts.** Image layers are shared, and metrics-and-usage §4.2 is
   explicit that per-resource disk figures do not sum to the host's usage: a
   base layer used by four applications is counted for each. So a project's
   figure is *what this project is responsible for*, and it is larger than what
   the host would reclaim by deleting the project. The quota is therefore
   **conservative — it refuses slightly early**, which is the safe direction for
   a guardrail, and the screen says so rather than implying precision it does
   not have.
2. **It can be up to an hour stale**, because the daemon's verbose disk call is
   expensive and that spec made it hourly on purpose. A scope can cross its cap
   and keep deploying until the next bucket closes. Stated, not hidden.
3. **It cannot stop a running container filling a volume.** Nothing here can.
   §2 said the general form; this is the case where it costs the most.

**Rejected: a real filesystem quota.** XFS project quotas, or `--storage-opt
size` on the local volume driver, would actually enforce disk. They work only
on specific filesystems and graph drivers, they are host configuration the
panel does not own and ADR-006 does not give it, and they fail in the worst
possible way: a write error inside a customer's database at 3am instead of a
refused deploy at 10am. A guardrail that turns into data loss is not a
guardrail.

### 4.3 Live previews are exact

The count of live preview environments in scope. This is the one dimension with
no lossiness anywhere: it is a row count over desired state, gated at
`previews.Manager.ensureAndDeploy`, which is the single place a preview
environment is born. It closes preview-environments.md §9's deferral by name.

The refusal has a property the others do not: **nobody is watching.** A preview
is created by an inbound webhook, so a `409` goes to GitHub's delivery log and no
further. So the refusal writes an inbox item for the project's team admins and
owners and an audit event with a `system` actor — the same shape
preview-environments.md §6 already uses for `environment.created` under preview
automation — and nothing else happens: no preview environment, no cloned
application, no deployment. It is not retried and needs no retry queue, because
the next `synchronize` on that PR retries it for free, and by then the TTL
sweeper may well have made room.

## 5. Warn at 90, refuse at 100

Three states per (scope, dimension), **derived, never stored as the truth**:
`ok` below 90% of the cap, `warning` at 90% and above, `exceeded` at 100% and
above. Both thresholds are panel-wide constants, not per-quota fields.

disk-management.md §5 argued for *"a single threshold, not two"* — that a
"warning" and a "critical" level double the noise for one fact, because the
second only ever arrives after the first was ignored. That reasoning does not
apply here, and the difference is what makes two thresholds correct: these two
do **different things**. 90% tells you. 100% stops you. An operator who ignores
the first gets the second as an outcome, not as a second announcement of the
same outcome.

They are constants rather than settings for that spec's other reason: a
configurable warn percentage is a second number to get wrong for one fact, and
the cap itself is already the number the operator tunes.

**Announced on the transition only** — never per evaluation, never per
heartbeat — the rule disk-management §5 states for `server.disk_low` and
[deployment-control.md](deployment-control.md) §5 states for `app.crashed`, for
the same reason: a channel that repeats itself gets muted, taking the next real
warning with it. One `quota_state` row per (scope, dimension) holds the last
announced state so a transition is a comparison rather than a guess.

Evaluation happens in two places, and both are cheap:

- **Synchronously, wherever the gate computes a meter** (§6), so a memory
  commitment that crosses 90% is announced at the moment it is made rather than
  up to an hour later.
- **On an hourly tick**, one owned goroutine with a cancellation path and
  observable failure (ENGINEERING rule 7), aligned behind metrics-and-usage's
  hourly disk buckets, because disk changes without anybody making a request.

**Three events**: `quota.warned` (error), `quota.exceeded` (error),
`quota.cleared` (info) — one edit to `domain.eventTypes` for a **project**
quota, which gives notifiers, outbound webhooks and the inbox all three at once,
the property that one-place taxonomy exists to have. The delivery path needs one
small addition and no new mechanism: `notify.Manager.dispatch(ctx, envID, ev)`
resolves environment → project before it does anything else, and
`Store.ListEnabledNotifiersForEvent(ctx, projectID, eventType)` is *already*
project-keyed — so a project-scoped event needs an entry point that skips the
first resolution and nothing beyond it.

**A team quota reaches the inbox and no channel**, and that gap is named rather
than papered over. Items are addressed to the team's owners and admins on the
`team_id` inbox scope
[invitations-and-access-requests.md](invitations-and-access-requests.md) added,
and to panel admins, who are the people who set it. Channel delivery would need
a team-scoped Notifier, which does not exist; the panel-scoped Notifier
[threshold-alerts.md](threshold-alerts.md) §7 proposes is a panel admin's
channel and a team's capacity is not panel-wide news. Fanning a team quota out
to every project's notifiers is the same leak that spec's §7 rejects for server
alerts. If panel notifiers land first, a team quota event may subscribe to one;
that is an ordering fact, not a dependency, and this feature ships without it.

## 6. Where it is enforced: one gate, four call sites

The design screen says *"enforced by the same reconciler that places
workloads"*, and the literal reading is the correct one: **there is no new
loop.** `core/scheduler` and `core/previews` ask one more question in the place
they already ask deploy protection's.

The gate is a consumer-defined interface on the scheduler (ENGINEERING rule 6),
exactly as `protection.Gate` already is at `core/scheduler/scheduler.go:212`:

```go
// Gate is the quota admission check (consumer-defined; *quota.Service satisfies it).
type Gate interface {
    Admit(ctx context.Context, scopeOf ScopeRef, delta Delta) (domain.QuotaAdmission, error)
}
```

| Call site | What it checks |
|---|---|
| `Scheduler.DeployAs` | the scope is not `exceeded` in any dimension |
| Application / Database create and `PATCH` that raises `memory_limit_mb` | the raise fits |
| `previews.Manager.ensureAndDeploy` | the preview count fits, and memory does |
| the replica scale path, when app-scaling lands | `Δreplicas × memory_limit_mb` fits |

**Where it sits relative to deploy protection.** The quota check runs
immediately after the freeze check and **before** any row is written, for the
reason `protection/gate.go` already records — a refused deploy must leave no
orphan Revision behind. It also runs **before the approval branch**, so a
deploy that will be refused for space is refused rather than parked; that spec's
own sentence covers it: *"a hard 'not now' is more useful than parking something
that would have to be refused later anyway."*

**What is deliberately not gated**, and the one that matters:

- **Rollback is never refused by a quota.** A rollback is the recovery path, and
  a guardrail that blocks recovery has become the outage it was installed to
  prevent. Deploy protection *does* refuse a rollback inside a freeze, and the
  difference is exactly what each control is about: a freeze exists to stop new
  code shipping, and a rollback ships code; a quota exists to bound
  consumption, and a rollback re-runs a revision whose consumption the scope
  already carried. The cost of the exemption is stated rather than waved away —
  a rollback to a revision that declared a *larger* memory limit can push a
  scope over its cap, after which the scope sits `exceeded` and refuses forward
  deploys until someone resolves it. That is the right place for the friction.
- **Restart** — deployment-control §3 already established that a restart ships
  no code; it consumes nothing new either.
- **Converge, a stopped resource starting, backup runs, scheduled task runs.**
  None of them increase a declared commitment, and gating a backup on a quota
  would be a way of losing data to save disk.

**The gate fails OPEN, and that is the deliberate opposite of
`protection.Admit`.** Protection fails closed because its failure mode is an
unapproved change reaching production — a governance failure, where refusing is
always the safe answer. A quota's failure mode is a project growing past a cap
the operator set to protect themselves: slow, visible, and recoverable. Failing
closed would turn a metrics gap, a stopped agent or a plane restart into a
**fleet-wide deploy outage** — the guardrail becoming the disaster. So a store
error, an unreadable meter, or a dimension with no data **admits**, logs at
`WARN` with the resource ids it concerns (ENGINEERING rule 4), and the quota
screen reports that dimension as **not enforced** rather than drawing a 0% bar:
`unknown` never fakes certainty (ui-principles §5, §10), and a bar at zero on a
screen whose whole job is a number would be the most dangerous possible lie.

The honest consequence, stated once: **a quota is not a security control.**
teams-and-roles §1 already says teams are collaboration scopes and not security
boundaries between mutually distrusting tenants; a quota inherits that exactly.
It stops accidents and it makes intent visible. It does not stop a determined
member of a team from consuming a fleet, and the threat model gains no control
here.

## 7. The Quota Override

**Quota Override** — a bounded, recorded suspension of a quota's *refusal* on
one named dimension of one scope. Thirty minutes, a required reason, never
revoked early. It is Break Glass with a dimension instead of a freeze window,
and it borrows that design deliberately rather than inventing a second shape for
the same idea.

```
quota_overrides
  id           TEXT PK (qov_…)
  project_id   TEXT REFERENCES projects(id) ON DELETE CASCADE   -- exactly one of
  team_id      TEXT REFERENCES teams(id)    ON DELETE CASCADE   -- these two
  dimension    TEXT NOT NULL        -- memory | disk | previews
  reason       TEXT NOT NULL        -- required, bounded
  opened_by    TEXT NOT NULL        -- user id, snapshotted name in the audit row
  opened_at    TIMESTAMPTZ NOT NULL
  expires_at   TIMESTAMPTZ NOT NULL -- opened_at + 30 min, fixed
```

**It suspends the refusal on the named dimension and nothing else** — not the
warning, not the other two dimensions, not another scope, not the memory
declaration rule of §4.1 (an unlimited application is still refused, because
admitting it would make the meter meaningless for everyone else). Break Glass's
narrowness — *"two independent controls"* — applied here.

**Per-dimension rather than per-scope**, which is where it differs from Break
Glass, and the reason is that the dimensions are independent: an override
opened because the disk figure is stale should not admit a 4 GB memory
commitment nobody looked at. It costs one field, and it makes the audit row say
what was actually overridden.

Rejected alternatives:

- **A one-shot override consumed by the next deploy.** An override spent by a
  inbound-webhook deploy nobody was watching is strictly worse than a clock: the
  operator's next attempt fails and they cannot tell why.
- **An indefinite "quota disabled" toggle.** That is deleting the quota, and it
  should look like deleting the quota. A control that is permanently off while
  still appearing on the screen is how a panel lies.
- **An override that raises the cap for the window.** Raising the cap *is*
  raising the cap; if the cap is wrong, the honest action is `PUT`, and the UI
  offers both side by side with "raise the cap" first. **A recurring override
  is a cap that lies**, and the override history exists partly so that is
  visible.

Audit: `quota.policy_set`, `quota.policy_cleared`, `quota.override_opened`,
`quota.deploy_refused`. Details carry the scope, the dimension, both numbers and
the reason text — operator-authored configuration, never a secret (ENGINEERING
rule 20, audit-log §4's rule that details carry key names and reasons and never
a value).

## 8. The resource model

**Migration `0044_resource_quotas.sql`.** The highest on disk is
`0039_server_disk.sql`; `0040`–`0043` are contested by sibling specs written
the same week, so the PR takes the next free number (the convention
[status-pages.md](status-pages.md) §5 records). Reversible and additive
(ENGINEERING rule 16): two new tables, nothing existing altered.

```
resource_quotas
  id                  TEXT PK (quo_…)
  project_id          TEXT REFERENCES projects(id) ON DELETE CASCADE  -- exactly
  team_id             TEXT REFERENCES teams(id)    ON DELETE CASCADE  -- one
  memory_limit_bytes  BIGINT      -- NULL = this dimension is uncapped
  disk_limit_bytes    BIGINT      -- NULL = uncapped
  preview_limit       INTEGER     -- NULL = uncapped
  updated_by          TEXT NOT NULL
  created_at, updated_at TIMESTAMPTZ NOT NULL
  CHECK ((project_id IS NOT NULL) <> (team_id IS NOT NULL))
  UNIQUE (project_id), UNIQUE (team_id)

quota_states                        -- last announced state, for §5's transitions
  (scope kind+id, dimension) PK, state TEXT, changed_at TIMESTAMPTZ
```

**Two nullable owner columns with a `CHECK`, not a polymorphic
`(scope_kind, scope_id)` pair.** metrics-and-usage §6 used the polymorphic shape
and had to, because its `resource_kind` names four different tables. Here there
are exactly two, so the shape that gives real foreign keys and a real cascade is
available, and it is worth taking: a quota is **live policy**, and policy for a
deleted project is meaningless. That is the opposite of the audit-log §2
argument — an audit event carries no foreign keys precisely because it must
outlive what it names. Both are right, and the reason they differ is that one is
evidence and the other is configuration. The evidence that a quota existed is
the audit row, which survives the cascade by design.

**A cap of `0` is refused with a `400`.** Removing a quota is `DELETE`; a cap of
zero means "nothing may be deployed here", which is deploy protection's job and
says so much more clearly. `NULL` per dimension means uncapped, so an operator
who wants a disk cap only writes one number.

**The meter is computed, never materialised.** Three bounded queries at
admission: memory is one aggregate over `applications` and `databases` joined
through the environment→project chain the access layer already walks; disk is
one lateral "latest bucket per resource" over `resource_disk_usage`, which its
primary key `(resource_kind, resource_id, bucket_start)` already serves;
previews is one count. All three are bounded by the resource count of a single
project. A materialised `quota_usage` counter was rejected: it is a cache of a
fact Postgres can compute, and every cache of desired state is a chance to be
wrong at exactly the moment it matters. (app-scaling §4.3 stores its placement
for the opposite and correct reason: a placement is a *decision*, not a
derivation.)

**Nothing on the wire.** No proto change, no `DesiredState` field, no new
subject, no new work item, no agent code. `buf breaking` has nothing to check.
That absence is the check that this is admission control on the plane and not a
runtime mechanism smuggled in through a spec.

## 9. API surface (under `/api/v1`)

Ten operations. The contract is **198** today; sibling specs contest the
numbers above it, so this PR states the count it actually produces.

| Route | Rank | Notes |
|---|---|---|
| `GET /projects/{id}/quota` | team member | caps, meters, state, per dimension |
| `PUT /projects/{id}/quota` | team admin, **session** | `409` naming unlimited resources |
| `DELETE /projects/{id}/quota` | team admin, **session** | |
| `GET /teams/{id}/quota` | team member | plus the sum of project caps (§3) |
| `PUT /teams/{id}/quota` | panel admin, **session** | |
| `DELETE /teams/{id}/quota` | panel admin, **session** | |
| `POST /projects/{id}/quota/override` | team owner, **session** | `{dimension, reason}` |
| `POST /teams/{id}/quota/override` | panel owner, **session** | |
| `GET /projects/{id}/quota/overrides` | team member | bounded to the 20 most recent |
| `GET /teams/{id}/quota/overrides` | team member | |

The OpenAPI spec is the source of truth and handlers are generated from it
(ENGINEERING rule 19); every route is additive (rule 17).

Every `GET` returns, per dimension: the cap or `null`, the current usage, the
state, `as_of`, and an `enforced` boolean with a `reason` when it is false
(§6's fail-open, made legible to a client rather than inferable from a zero).
A quota in a team the caller does not belong to answers the same "no such
project/team" it already does — a quota screen must not become a way to
enumerate other teams' scopes, the rule registries.md §7 states for credentials.

Refusals are `409` with a body that names the scope, the dimension, both
numbers, and the two remedies — the shape `FrozenError` already produces for a
freeze, so `handlers_deployments.go` gains one more typed error and no new
pattern.

## 10. Screens

Design screens are the source of truth for layout and copy.

**Project → Settings → Quotas** (`web/src/routes/_app/projects/$projectId/settings/quotas.tsx`),
breadcrumb `ATLAS-CRM / SETTINGS / QUOTAS`: three rows — **Memory**, **Disk**,
**Previews** — each a label, a bar, and the figure in the mono face:
`5.1 / 8 GB cap`, `41 / 50 GB cap`, `2 / 5 live cap`. A bar at or past 90% is
drawn in the alert colour, with the figure alongside it. Below, the banner in
its own colour when any dimension is in the warning band, with the design's
sentence: *"Disk at 82% of quota — new deploys warn at 90%, block at 100%
(owner override, audit-logged)."* And the caption, unchanged: *"Caps per project
or per team — a runaway hobby project can't starve a client's production.
Enforced by the same reconciler that places workloads; reads from usage."*

The caption's last clause is exactly true of Disk, which is metrics-fed. The
Memory row is a commitment (§4.1) and shows the observed peak as a subordinate
marker on the same bar, so the screen answers both *"how much have I promised"*
and *"how much is actually in use"* without two rows for one dimension.

**Team → Settings → Quotas**: the same three rows, plus the one line §3 owes —
the sum of the team's project caps against the team cap, with the plain-language
note that over-committing is allowed.

**Four states per ui-principles §1**, including the two this feature invents:

- **No cap set** — the empty state explains what a quota is in one line and
  offers "Set a cap". Not a bar at 0.
- **Not enforced** — a dimension whose meter is unknown (§6) says so in words
  with the reason ("no disk usage reported yet — a first figure arrives within
  the hour"). Never a zero bar, never a spinner.

**The refusal is a screen too.** A deploy refused by a quota shows which scope
refused, which dimension, both numbers, and the two remedies — raise the cap, or
override for 30 minutes — each greyed with the required rank named when the
viewer does not hold it. ui-principles §11: no dead ends.

The explain-it-cold line under the page title: *"A cap on what this project may
use, so one project can't take everything."*

**Glossary PR first** (ui-principles §8): **Resource Quota** and **Quota
Override**. It also records the one vocabulary decision here — the noun in the
API, the schema and the code is **quota**; **cap** is the inline word for the
number itself, because the design writes `8 GB cap` and that reads as a unit
rather than as a competing term for the resource. Two words for one concept is
exactly what the glossary exists to prevent, so the boundary is written down
rather than left to each screen.

## 11. Alternatives considered

- **Enforcing in the agent, with a per-project cgroup slice.** This would be
  real enforcement rather than admission control. Rejected on three counts: a
  project spans servers, so the sum lives on the plane and a per-host cgroup
  could only ever cap a fraction of it; expressing it would need a new
  `DesiredState` concept per node or, worse, a verb, and ADR-002/ADR-005 exist
  to prevent the second; and it changes what a quota *is* — a cgroup throttles
  and OOM-kills a running workload, which §2 rules out on purpose.
- **Reusing [threshold-alerts.md](threshold-alerts.md) for the 90% warning.**
  Genuinely tempting: it is a rule engine over exactly these numbers, with a
  sustained window and a flap guard already designed. Rejected because the
  operator would have to write the cap and then separately write a rule at 90%
  of the cap, and the two would drift the instant the cap moved — a rule
  silently measuring 90% of last month's number is worse than no rule. A quota
  declares its own threshold by existing. (The reverse is welcome: an operator
  who wants a *sustained* alert on memory can still write one, and it will
  agree with this screen because both read the same buckets.)
- **A CPU quota**, alongside the other three. Rejected for v1, and the reason is
  specific rather than a lack of time: a CPU limit is a *share* under
  contention, not a reservation, so a declared meter over `cpu_limit` would sum
  numbers that were never promises; and the observed alternative is the most
  volatile signal metrics collects, so a cap enforced on a five-minute mean
  would refuse a deploy because a build finished. §13 records the condition on
  which it returns.
- **One panel-wide quota.** Much cheaper, and it answers the wrong question.
  The failure is one tenant starving another; a single fleet number cannot
  express it, and the fleet number that *is* useful already exists on the Server
  DTO (disk-management §4).
- **Refusing at 100% by stopping containers to fit.** §2.
- **Automatic remediation** — deleting the oldest preview, or the oldest
  retained revision, to make room. Deleting somebody's work to satisfy
  arithmetic is the wrong failure mode, and it would be doing it on the strength
  of an hour-old, over-counting disk figure. The panel refuses and names the
  remedy; a person decides what goes.
- **A materialised usage counter.** §8.

## 12. Acceptance (testable)

Against real Postgres (ENGINEERING rule 29).

1. A project whose applications declare 5.1 GB, capped at 8 GB → the meter
   reports `5.1 / 8`, state `ok`, `enforced: true` on all three dimensions.
2. `PATCH` an application's limit so the scope crosses 90% → exactly one
   `quota.warned` inbox item for the team's owners and admins and one project
   notifier delivery; a second `PATCH` still inside the band produces neither.
3. `PATCH` past 100% → `409` naming the scope, the dimension and both numbers,
   and the application's `memory_limit_mb` is unchanged.
4. Deploy into a scope already `exceeded` on disk → `409`, **no `revisions` row
   is created**, and no work item is published.
5. Rollback in that same over-cap scope → admitted, and the rollout runs.
6. Open a 30-minute override on `disk` and deploy → admitted. Deploy again in a
   scope also over on `memory` → refused, proving the override is per dimension.
   Advance the injected clock past 30 minutes → refused again.
7. Set a memory quota on a scope containing an application with a null
   `memory_limit_mb` → `409` naming that application; **no quota row is
   written**.
8. A sixth open PR against a project capped at 5 previews → no environment, no
   cloned application, no deployment; one inbox item and one audit row with a
   `system` actor. Close a PR, re-deliver `synchronize` → the preview appears.
9. **The fail-open test.** Point the scope at resources with no
   `resource_disk_usage` rows, or make the meter query error → the disk
   dimension reports `enforced: false` with a reason, deploys are admitted, and
   nothing draws a 0% bar. This one must never regress.
10. Delete the project → its quota and override rows are gone; the
    `quota.policy_set` and `quota.override_opened` audit events that name it
    survive (audit-log §2).
11. A `write`-ability API token whose owner is a team owner calls the quota
    `PUT` and the override `POST` → `403` on both.
12. Evaluate an unchanged scope twice → no second event, no state row change.
    (ENGINEERING rule 13's shape, for a loop that mutates only announcements.)

## 13. Deliberately out of scope

- **Anything monetary** — prices, currency, plans, tiers, entitlements,
  invoices, upgrade flows, payment integrations (§1.1, vision.md). This is the
  boundary the whole feature is built to stay behind, and crossing it needs its
  own recorded decision, not a column.
- **CPU quotas.** §11. They return when there is a defensible denominator — most
  likely a reservation concept that does not exist today, not a share.
- **Compose Stack memory in the memory meter.** §4.1. It changes when the panel
  has a reason to parse the file beyond the two directives it refuses today —
  and that is a change to compose-stacks.md's model, not a paragraph here.
- **Runtime enforcement of any kind** — killing, throttling, evicting, OOM-score
  tuning, cgroup slices, filesystem or volume quotas (§2, §4.2, §11).
- **Per-environment and per-server quotas.** §3.
- **A packing scheduler** that places workloads into the room a quota leaves.
  app-scaling §1.1 spent this product's vision argument keeping a scheduler out;
  a quota is an input one would want, and that is a reason to be careful, not a
  reason to build one.
- **Bandwidth or egress quotas.** metrics-and-usage §12 rejected per-container
  network counters with reasons that still hold, so there is no signal to
  enforce against; the closest number — bytes served, from the access log's
  response size — is a different meter with a different meaning, and it would
  need its own section rather than a fourth row.
- **Build-minute or deploy-count quotas.** That is CI budgeting, and vision.md
  is explicit that we integrate with CI rather than replace it.
- **Quotas as a security boundary.** §6. Teams are collaboration scopes
  (teams-and-roles §1) and this inherits that exactly.
- **A quota on the control plane's own host.** disk-management §6 already
  decided the plane reserves and enforces nothing about itself, because a
  `cypherd` that refuses to write is an outage of the thing that reports
  outages. Nothing here revisits that.
- **Per-user quotas.** The tenancy model is Team → Project → Environment; a user
  owns no resources to cap.
- **Channel delivery for a team-scoped quota event.** §5. Real gap, named, and
  it belongs to whichever spec builds a Notifier scope that fits — not to this
  one improvising a second delivery path.
- **A quota history or usage-over-time chart.** The trend already exists in
  metrics-and-usage's screens; a second chart of the same buckets with a
  horizontal line on it is a UI decision, not a feature, and it can be added the
  day someone misses it.

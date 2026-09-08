# Feature spec: Promoting revisions between environments

> Staging has been green for a day. The images that made it green are already
> built, already health-gated, already proven. The only way into production
> today is to press Deploy on each application and rebuild every one from git —
> which produces *different images* from the ones that were tested, and does it
> three times.
>
> A **Promotion** re-points the applications of one Environment at the exact
> images another Environment of the same Project is already running. No build
> runs. The panel shows the whole diff — which application moves from which
> revision to which, and which environment variables the two environments do
> not agree on — **before anything moves**, and rolls the result out through
> the ordinary zero-downtime pipeline under the ordinary deploy protection.
> The design canvas's **Promote** card is the screen: *"Exactly what changes,
> before anything moves. Same images, no rebuild."*
>
> No [feature-matrix.md](../product/feature-matrix.md) row exists yet; one lands
> with this spec under "Deploys" — *Environment promotion (staging → production,
> no rebuild)*, **V1** — and [glossary.md](../glossary.md) gains **Promotion**
> (CLAUDE.md rule 5, ENGINEERING rule 33). Neither reference has an equivalent —
> Coolify and Dokploy both redeploy from source per environment — so nothing is
> ported and the model here is ours.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). It builds on
> [application-deploy.md](application-deploy.md) (the pipeline it reuses whole),
> [builder-role-and-relay.md](builder-role-and-relay.md) (the stage that moves an
> image between daemons), [deploy-protection.md](deploy-protection.md) (the gate
> it must not become a way around) and
> [disk-management.md](disk-management.md) (the retain set that decides how long
> a promoted image survives).

## 1. The one sentence

**A promotion moves the artifact, never the configuration.** Every hard case
below is decided by it.

The image is the artifact: it is the thing that was tested, and moving it
unchanged is the whole point. A domain, a container network, a route prefix, a
resource limit, a health path, an environment variable — those are
configuration, and configuration is what an Environment is *for*. A promotion
that copied them would put `staging.acme.com` on the production container and
the staging database URL in the production process; that is not a promotion, it
is an outage with a confirmation dialog.

So a promoted Revision takes exactly two things from the source — the **image**
and the **source commit**. Its config snapshot comes from the target
application's own current configuration (the same `snapshotOf(app)` an ordinary
deploy takes) and its environment from the target application's own sealed
variables, resolved at rollout as always. Where the two environments disagree
about a variable, the plan **says so** and changes nothing (§4).

**And it is an action, not a pipeline.** No schedule, no trigger, no stages of
its own, no conditions, no user code. That boundary is deliberate:
[vision.md](../vision.md) puts *"a CI system — we integrate with CI via webhooks
and API, we don't replace it"* on the explicitly out-of-scope list, and "promote
staging to production" is close enough to that line to need it drawn on purpose.
The commitment: **a promotion is a deploy that chooses its artifact from another
Environment instead of from a git ref** — the same shape as a rollback, which
chooses from history. The moment it grows *"promote automatically when staging
has been green for 24 hours"* it becomes a pipeline, and that step needs an ADR
(§9).

## 2. The resource model

One new table and two columns, in migration `0040_revision_promotion.sql` —
additive and reversible (rule 16). `0039_server_disk.sql` is the highest in the tree as
this is written; several V1 specs are being drafted in parallel and each names
`0040`, so the implementing PR takes whatever is actually next.

```
Promotion:                                  -- one recorded batch
  id                    TEXT PK (prm_ prefix)
  project_id            TEXT NOT NULL → projects(id)     ON DELETE CASCADE
  source_environment_id TEXT NOT NULL → environments(id) ON DELETE CASCADE
  target_environment_id TEXT NOT NULL → environments(id) ON DELETE CASCADE
  requested_by          TEXT          → users(id)        ON DELETE SET NULL
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
  INDEX (target_environment_id, created_at DESC)

ALTER TABLE deployments ADD COLUMN promotion_id TEXT
    REFERENCES promotions(id) ON DELETE SET NULL;
ALTER TABLE revisions   ADD COLUMN promoted_from_revision_id TEXT
    REFERENCES revisions(id) ON DELETE SET NULL;
```

Three shapes are deliberate.

**A Promotion has no status column.** Its state is *derived* from its child
deployments on every read and never stored — the rule **Endpoint Health**
already follows ([glossary.md](../glossary.md)). A stored status would be a
second copy of facts the `deployments` rows own, and it would go stale in
exactly the interesting case: a plane restart mid-batch. Derivation is five
words: `awaiting_approval` when every non-terminal child is parked, `running`
when any child is non-terminal and they are not all parked, then `succeeded`,
`failed` or `partial` once they are all terminal. These are Promotion words, not
the six-word resource vocabulary ([ui-principles §5](../product/ui-principles.md))
— a Promotion is a record, like a Deployment, not a Resource.

**Both new foreign keys are `ON DELETE SET NULL`.** A deployment must outlive
the batch record, and a promoted revision must outlive the staging revision it
came from: deleting the staging application (which cascades its revisions) must
not delete production's deploy history and must not stop production rolling
back. With the link gone, the promoted revision degrades to an ordinary built
revision — which is precisely what it is.

**There is no link table between the two applications.** Matching is by **name**
within the Project (§3). A `promotion_links` table was considered and rejected:
`applications.name` is already `UNIQUE (environment_id, name)`, it is already
the identity operators use when they say "promote web and api", and a link
table would need creating, maintaining and repairing after a rename — a second
source of truth for something the first one answers.

## 3. The plan: what is compared before anything moves

`GET /api/v1/environments/{id}/promotion-plan?to={env_id}` is a **pure read**:
it writes nothing, publishes nothing, takes no lock. It is what the Promote
screen renders and what a CI job must call before it may promote (§7).

**Pairing.** Source and target must belong to the same Project and must differ.
Cross-project promotion is refused: the two projects can sit in different Teams,
on different servers, with different shared variables, and name-matching across
them means nothing. The **target must not be a preview environment**
(`environments.kind = 'preview'`) — a preview exists to be a faithful copy of one
pull request, promoting into it makes it a copy of something else, and it is
destroyed on the PR's close regardless. Promoting *out of* a preview is allowed
("this PR build is the one, ship it" is a real thing an operator means) and the
audit row records that the artifact came from a preview.

Applications pair by name; every name in either environment appears in the plan
with an `action`:

| `action` | Meaning |
|---|---|
| `promote` | in both; the source has an image the target is not running — this one moves |
| `up_to_date` | in both; the target's desired revision already carries the same image |
| `blocked` | in both, but promoting it right now would be wrong |
| `not_in_target` | only in the source environment |
| `not_in_source` | only in the target environment |

Only `promote` entries are promotable, and the button counts them — *"Promote 2
apps"*. Everything else is shown with a one-line reason, because
[ui-principles §11](../product/ui-principles.md) forbids a screen the user can
only stare at.

**`not_in_target` never creates an application.** Creating one needs a server, a
port, a route, a domain and a health check — decisions with a blast radius, none
of which a promotion has any business inventing. The reason line says so and
links to the create form.

**`up_to_date` is excluded, not promoted.** Minting a revision carrying an image
the target already runs would still change the container's identity (the
revision id *is* the container's identity), so the reconciler would recreate a
container for nothing. That is a restart wearing a deploy's ceremony, and
`POST /applications/{id}/restart` is the honest way to ask for one
([deployment-control.md](deployment-control.md) §3). There is no `force` flag,
for the same reason.

**Three blockers**, each reported as a sentence rather than a code:

1. **The source application has an active deployment.** Staging is mid-flight;
   what the operator is looking at is about to change. *"staging is still
   deploying `web` — promote when it lands."*
2. **The source application has never deployed** (`desired_revision_id IS
   NULL`). There is no artifact.
3. **The target application's environment cannot be resolved** — one of its own
   variables references a `{{shared.KEY}}` not defined in the target
   environment's scope. The deploy would fail at `start()` anyway
   ([shared-variables.md](shared-variables.md) §4, `envStrict`); failing at plan
   time costs nothing, failing at rollout costs a queue slot and a scare. This
   is a pre-existing condition of the target application rather than drift the
   promotion introduces, but "this promotion will fail" is exactly what a screen
   called *before anything moves* is for.

Not a blocker: the target agent being offline. The work item queues in JetStream
and converges when the agent returns, as every other deploy already does;
refusing here would invent a new failure mode to avoid an existing one that
works.

**Which revision is promoted** is the source application's **currently desired**
one — what staging is actually converging on, not its newest. The newest can
come from a queued or failed deployment that staging never ran, and promoting
something that was never live is the opposite of what this feature promises.
`desired_revision_id` is only set inside `startRollout`, which is only reached
once an image exists, so it always names a built revision; the planner still
checks rather than trusting the invariant.

### The number the mock shows, and the number we can compute

The canvas card reads `a81f3e0 ↑ 4 commits`. **The plane cannot compute a commit
count.** It holds two SHAs and no git graph. Producing the count would mean the
control plane cloning a user repository with a deploy key — which strains vision
non-negotiable 5 ("the control plane never runs user workloads or builds") in
spirit if not in letter — or minting a work item and making a synchronous read
wait on an agent. Neither is worth a decoration, so two honest replacements ship
instead:

- **`source_revisions_ahead`** — how many *built* revisions of the source
  application were created after the one the target is currently running,
  defined only when the target's current revision was itself promoted from this
  source application (so the two are comparable) and `null` otherwise. The copy
  says *"4 staging revisions"*, never *"4 commits"*: they are different numbers
  and only one of them is true.
- **`compare_url`** — for a `github` source,
  `https://github.com/<repo>/compare/<a>...<b>`. The panel does not count; it
  links to the party that already knows. Empty for a `git_url` source, where the
  two short SHAs stand alone.

A deliberate deviation from the canvas copy, recorded rather than quietly
shipped as a plausible-looking number.

## 4. Environment-variable drift: flagged, never copied

The card's third row is what earns the feature its trust:

```
env vars    31 keys    + FEATURE_SAVED_VIEWS · 1 key only in staging
```

**Nothing about environment variables is ever copied by a promotion.** Not
optionally, not behind a checkbox, not with a confirmation. Copying variables
across environments is the most effective way there is to point production at a
staging database, and a panel that offers it will eventually do it. The panel's
job is to make the disagreement *visible* at the moment it matters and then get
out of the way: the remedy is a deep link to the target application's env
screen, where setting a variable is an ordinary, audited, sealed write.

Two categories, both computed from **key names only**:

- **`only_in_source`** — a key staging has and production does not. The card's
  `+ FEATURE_SAVED_VIEWS`: usually a flag the promoted code reads, occasionally
  a staging-only debug switch. Advisory.
- **`only_in_target`** — the mirror. Less often interesting, but omitting it
  would make "exactly what changes" false in one direction.

Plus one entry that is not a comparison at all but is listed here because this
is where an operator looks for it: `unresolved_in_target`, the blocker from
§3.3, reported as `{env_key, shared_key}` pairs.

**Values that differ are deliberately NOT reported.** A key present in both
environments with a different value is not drift; it is the normal, intended
state of two environments. Every per-environment secret differs by design —
`DATABASE_URL`, `STRIPE_KEY`, `SENTRY_ENVIRONMENT` — and a panel that flagged
all of them would show a wall of amber on every promotion. An operator who sees
twenty warnings that are all fine learns to click past the twenty-first, which
is the exact failure [deployment-control.md](deployment-control.md) §5 records
when it refuses to fire `app.crashed` on `degraded`.

That decision has a second consequence, and it is the stronger reason to prefer
it: **the planner never unseals anything.** It reads key names from
`app_env_vars`, the cleartext `shared_refs` array, and the key/scope list of
`shared_variables`. No `Opener` is wired into the promotion service at all, so
there is no code path — present or future — in which a plan could leak a value,
mask one imperfectly, or leak the single bit "these two secrets are equal"
(ENGINEERING rule 20). A value comparison would have meant unsealing both sides
of every shared key on every render of a screen a UI is tempted to refetch.

## 5. Moving the image without a rebuild

`Scheduler.start()` already contains the whole idea:

```go
if rev.Image != "" {
    // Already built (rollback): straight to rollout.
    return s.startRollout(ctx, dep, app, rev)
}
```

A revision that already names an image does not build. Rollback has used that
branch since Phase 2; a promotion is the same trick pointed sideways instead of
backwards.

### The promoted revision names the target's own image

The promoted revision's `image` is **`cypher/<target app id>:<new revision
id>`** — the ordinary canonical tag, exactly what a build would have produced —
and *not* the source's tag. This is the most important decision in the slice,
and the reason is garbage collection.

The agent parses ownership out of the tag (`parseManagedTag` splits
`cypher/<app>:<revision>`), and `gcImages` drops **every** managed reference of
an image whose applications are all absent from that server's desired set. A
promoted revision carrying `cypher/<staging app>:<staging rev>` would, on the
production server, belong to an application that is not desired there — and the
first reconcile after the rollout would try to reclaim the image production is
serving from. The retain set cannot save it either: `retainFor` is keyed by
application id, so production's retain instruction names revisions of the
production application and says nothing about staging's.

Two alternatives were considered and rejected:

- **Emit a `RetainSpec` for the source application on the target server.**
  `gcImages` consults `desiredApps` *before* the retain set, so the spec is
  never read — and making the source application "desired" on a server it does
  not run on is a lie told to the reconciler, when ADR-005's whole contract is
  that the desired set is true.
- **Route the image through a registry** (ADR-008 path 3,
  [registries.md](registries.md)). It works, and an operator who has configured
  one already gets it through the existing push/pull path. Rejected as *the
  mechanism*: ADR-008 is explicit that no registry is ever required, and a
  promotion that only worked for panels with a registry would break that promise
  for exactly the single-server and small-fleet cases this product is for.

### The distribute stage does the work

The stage that makes an image exist on a target daemon already exists:
`distributing`, from [builder-role-and-relay.md](builder-role-and-relay.md).
Promotion generalizes its meaning by one word — from *"obtain this deployment's
image from the relay"* to **"make this deployment's image exist, under the name
it will run as, on this host"** — and that generalization is the entire agent
change.

`start()` gains one branch: a revision with `promoted_from_revision_id` set goes
to `startDistribute` rather than straight to `startRollout`. The scheduler
records the **source application's runtime server** in the existing
`deployments.builder_server_id`, which is already defined as the server that
holds this deployment's image and already carries the relay's authorization
(`PushImage` requires the caller certificate's CN to equal it;
`HandleDeployEvent` accepts `STAGE_DISTRIBUTE` from it). No new authorization
concept appears anywhere.

**One additive proto field**, the only wire change in the slice:

```proto
message DistributeWork {
  string deployment_id = 1;
  string app_id        = 2;
  string image         = 3;
  // The reference the image is expected to arrive (or already exist) under.
  // Empty for an ordinary build relay, where it equals `image`. For a promotion
  // the two differ: the tar carries the SOURCE application's tag, and the
  // target must re-tag it as `image` before it can be run — or garbage-
  // collected — under its own identity (revision-promotion.md §5).
  string source_image  = 4;
}
```

`buf breaking` is clean and `PushImageWork` is untouched (the source agent saves
the tag it has). The target's distribute worker becomes three ordered cases:

1. `HasImage(image)` → success. Unchanged: the redelivery and crash-recovery
   anchor.
2. `source_image != "" && HasImage(source_image)` → `TagImage(source_image,
   image)` → success. **This is the same-server promotion** — staging and
   production on one host, where the whole "move" is one `docker tag` and no
   relay stream is opened at all.
3. otherwise pull through the relay, verify `HasImage(source_image)` after the
   load (the tar carries the source tag, so verifying the target tag would
   always fail), then tag, then verify `HasImage(image)`.

`startDistribute` publishes the **push** work item only when
`builder_server_id` is non-`NULL`. That is a no-op for every existing build (the
column is set exactly when builder ≠ target) and it is load-bearing here: a
same-server promotion sets no builder, and publishing a push to the target would
have it open a relay stream, wait three minutes for a puller that is itself, and
fail the deployment at `maxDeliveries`. `Recover` gets this for free, because it
republishes by calling `startDistribute`.

Everything downstream is untouched. `buildSpec` reads `rev.Image` as it always
has; the rollout is the same health-gated, route-flipping, drain-after
convergence, so **the promotion is zero-downtime for the same reason a deploy
is** (vision non-negotiable 4). `pinRevisionImage` never fires, because it runs
only for `Pull` specs. Rollback is unchanged: the promoted revision is an
ordinary member of the target application's retain set, and rolling back to what
production ran *before* the promotion needs an image that never left. A rollback
*to* a promoted revision re-runs distribute, which succeeds instantly from case
1 when the image is retained and repairs it from the source when it is not — a
small free improvement over today, where that rollback fails at container-create
with a daemon error. With the source gone (`promoted_from_revision_id` `NULL`
after a cascade) it degrades to today's behaviour exactly.

### Two honest costs

**A lingering tag.** After a cross-server promotion the target daemon holds one
image under two managed names — the source tag that arrived in the tar and the
target tag that was applied — and the source one is not reclaimed while the
target application lives, because `gcRetainedRevisions` skips references whose
application is absent from desired (assuming the absence path handled them,
which is true only when the *image* is also undesired). It costs one name and
zero layers, and it goes with the target application. Not a new category
either: the same lingering alias already appears when two applications share a
pulled image and one is deleted. Tightening `gcRetainedRevisions` to drop a
managed reference whose application is absent from desired is a one-line
correctness fix belonging to [disk-management.md](disk-management.md) — named
here rather than smuggled in.

**Build-time configuration cannot be promoted.** The image that runs in
production is byte-for-byte the one that ran in staging, including anything
baked in at build time — a `VITE_API_URL` compiled into a bundle, an
`ARG`-substituted config file, a `NODE_ENV` fixed by the Dockerfile. If an
application bakes environment configuration into its image, promotion moves
staging's configuration into production and the panel reports a perfectly
successful deploy. Nothing in the panel can detect this and this spec does not
pretend otherwise: the remedy is a runtime-configured image, and the plan screen
carries one line of "learn more" detail saying so
([ui-principles §11](../product/ui-principles.md)). It is the price of moving
the tested artifact rather than rebuilding, and it is the right price — a
rebuild avoids it only by shipping something else.

## 6. Deploy protection, and who may promote

**A promotion is not a way around the gate.** Each promoted deployment is born
in `DeployAs` exactly as a manual deploy is, so
[deploy-protection.md](deploy-protection.md)'s single admission check runs for
every one of them, on the **target** environment, before any work item is
published.

- **Frozen target → the whole promotion is refused, `409`, nothing written.**
  The gate is evaluated once for the target before any deployment row is
  created, so a freeze cannot leave half a promotion behind. The body is the
  existing sentence — *"production is frozen until Mon 08:00 Europe/Berlin"* —
  and the plan reports the same thing up front, so the button is disabled with a
  reason rather than failing on click.
- **Approval required → every child parks as `awaiting_approval`.** N
  deployments produce N `DeployApproval` rows, and that is correct rather than
  unfortunate: `DeployApproval` is keyed by `deployment_id` deliberately
  (deploy-protection §2), and one approval covering a batch would be a second
  gate object needing its own semantics for "approved three, rejected one". What
  the promotion adds is **legibility**: `Deployment.promotion_id` lets the
  approvals queue group the rows and say *"3 deploys · promotion staging →
  production"* instead of showing three unexplained deploys.
- **Break glass** applies to the freeze and only the freeze, unchanged. `Admit`
  already accounts for an active grant, so the plan simply reports the target as
  not frozen while one is open.

The plan carries `requires_approval` and `required_role` so the primary action
can read *"Request approval for 2 apps"* rather than promising something that
will park.

**Authorization.** Promoting is deploying, at the rank that deploys: team
**member**, `deploy` ability, for `POST .../promote`; `read` and member for the
plan and the listings. It grants nothing an operator could not do by hand —
deploy each application, one at a time — and the target environment's own
protection governs whether that is allowed. Both environments resolve to the
same Project, so the existing `projectIDForEnvironment` → `authorizeResolved`
path covers source and target in one check: a non-member gets `404`, an
under-ranked member `403`, panel-owner bypass as everywhere.

**Promotion is deliberately available to API tokens** and is added to
`deployRoutes`. "Run the tests, then promote staging to production" is the best
reason this feature exists, and refusing it to CI would push operators back to
rebuilding from source in a GitHub Action — a different image, and the point
defeated. Deploy protection is what makes it safe and was designed for exactly
this: a token's promotion parks at the gate, and approve/reject are
`sessionOnly`, so a CI token can request a production promotion and can never
open its own gate (deploy-protection §5).

Audited as one new action, `deploy.promoted` (in the `deploy` family, so
existing filters pick it up), on the target Environment, detailing source,
target and each application's `name @ <source rev> → <new rev>`. No secret is
involved anywhere in the flow, so nothing needs masking. The children write
nothing extra: `deploy.started` is written by the `POST
/applications/{id}/deploy` handler, which a promotion does not go through.

## 7. API surface (under `/api/v1`)

Four new operations (198 → 202), two additive response fields, one additive enum
value.

```
GET  /environments/{id}/promotion-plan?to={env_id}  → PromotionPlan   (member, read)
POST /environments/{id}/promote                     → 202 Promotion   (member, deploy)
GET  /environments/{id}/promotions                  → [Promotion]     (member, read; 20 newest)
GET  /promotions/{id}                               → Promotion       (member, read)
```

Additive on existing schemas, omitted when absent so no client sees a change
(rule 17): `Deployment.promotion_id`, and `Deployment.trigger` gains
`promotion` (`deployments.trigger` is plain `TEXT`, so no DDL; an added enum
value in a response is additive).

```jsonc
POST /environments/env_staging/promote
{
  "to": "env_production",
  "applications": [
    { "application_id": "app_web", "source_revision_id": "rev_a81f3e0…" },
    { "application_id": "app_api", "source_revision_id": "rev_c99d2e1…" }
  ]
}
```

**The application list is required and non-empty; there is no "promote
everything" shorthand.** A request meaning "promote whatever staging happens to
hold right now" is precisely the unreviewed surprise deploy protection exists to
prevent, and the plan is what made the set legible in the first place. A CI job
calls the plan and then the promote — two calls, and it gets a race-free result
rather than a fast one.

Each entry names the **source revision the operator was shown**. If staging has
deployed since the plan was rendered, the plane answers `409` naming the
application — *"staging deployed `web` since you looked — review the plan
again"* — instead of shipping something nobody reviewed. That is the
confirmation dialog, expressed as a precondition rather than as a modal.

Other refusals, all before anything is written: `400` for a target in another
project, the same environment twice, a preview target, an empty list, or an
application that is not `promote` in a freshly computed plan; `409` for a frozen
target; `404` for an environment the caller cannot see.

`PromotionPlan` carries the two environments, the ordered entries (action,
reason, revision pair, short commits, `source_revisions_ahead`, `compare_url`,
`env_var_drift`) and the gate summary (`frozen`, `freeze_detail`,
`requires_approval`, `required_role`). `Promotion` carries its identity, its
derived status and its children as `{application_id, application_name,
deployment_id, status}` — enough to render the progress view without a request
per row, the shape `Deployment.approval` already takes for the same reason.

**Bounds and cost.** At most **25 applications** in one promotion: a
blast-radius bound rather than a technical limit, because 25 concurrent
health-gated rollouts is already a lot to ask of one small server and a larger
environment moving as a single unit is a change that should be looked at twice.
Serializing the batch on the plane was rejected — a sequencer that walks
applications one at a time is a long-running orchestration, which is the
imperative pipeline ADR-005 exists to keep out. The plan is
O(applications in the environment) small queries and is not cached: it is a read
an operator asks for, rendered once, never polled. Listings are bounded at 20,
for the reason deploy-protection bounds its own.

## 8. The screen, and what happens when it goes wrong

One drawer, opened from the environment tab bar on the project home (where
environments already live as tabs, not routes), plus one route for a promotion's
progress. Drawer, not modal, depth 1
([ui-principles §4](../product/ui-principles.md)). The canvas copy is the copy:
the `staging → production` title over *"Exactly what changes, before anything
moves. Same images, no rebuild."*; the three-column table (application ·
**PRODUCTION (NOW)** · **AFTER PROMOTE**) with the current short SHA in muted
ink and the incoming one in green, reading *"↑ 4 staging revisions"* where
`source_revisions_ahead` is known and linking both SHAs to `compare_url` where
there is one; the amber **env vars** row — `31 keys` against `+
FEATURE_SAVED_VIEWS · 1 key only in staging` — expanding to the full key lists
and the deep link; and **`Promote 2 apps →`** under the footnote *"rolls out
with the usual zero-downtime pipeline · env-var drift is flagged, never
auto-copied"*.

Four states per the page contract, and the non-content ones carry the weight:
the **empty** plan (*"production already runs everything staging does"*), the
**blocked** plan (button disabled with the blocker's own sentence — a freeze
names when it lifts, a mid-flight staging deploy names the application), and the
**approval** variant where the button reads *"Request approval for 2 apps"* and
the drawer says what happens next. The progress view is not new machinery: each
child deployment already has a status, a log stream and a detail row, statuses
stream over the existing SSE channel, and the derived status sits on top.

| Failure | Behaviour |
|---|---|
| Target frozen at execute time | `409` before any row is written; nothing partial |
| Source revision moved since the plan | `409` naming the application; nothing written |
| Target requires approval | every child parks; nothing published; the app's own status untouched |
| One child fails, others succeed | promotion derives `partial`; each application keeps its own deployment, log and rollback. **No automatic rollback of the successful ones** |
| Relay peer never arrives | the existing 3-minute rendezvous timeout, NAK, redeliver, then `STAGE_DISTRIBUTE` failure fails that one deployment |
| Source image gone from the source host | distribute fails naming the image, rather than the rollout failing with a daemon error |
| Plane restart mid-promotion | nothing new to recover: children are ordinary deployments, `Recover` republishes by status, the Promotion's status is derived on read |
| Source application deleted afterwards | `promoted_from_revision_id` goes `NULL`; production keeps running and keeps rolling back |

**A promotion is not a transaction, and this spec does not pretend it is.**
Automatic rollback of the succeeded half was rejected on three counts: an
uncommanded rollback is a second deploy nobody asked for; it can fail too,
leaving a worse state and no story; and it would have to run inside a freeze it
was admitted through only once. A dependency graph ordering `api` before `web`
was rejected too: the panel has no dependency model between Applications,
inventing one inside a UI convenience would be a resource-model change in
disguise, and a confidently wrong order is worse than no order. The children
deploy concurrently, each through its own per-application queue, exactly as N
manual deploys would — and the panel reports what is true per application, the
same honesty it applies to observed state everywhere else.

## 9. Deliberately out of scope

- **Automatic or scheduled promotion** — "promote when staging has been green
  for 24 hours", "promote on every green build". This is the step from an action
  to a pipeline, and [vision.md](../vision.md) puts "a CI system" on the
  explicitly out-of-scope list. It needs an ADR before it needs an
  implementation; the API shipped here is exactly what a CI job needs to do it
  *outside* the panel today.
- **Promoting a Managed Database or a Compose Stack.** A database's revision is
  configuration, not an artifact, and "promote" for one would mean either
  nothing or copying data — two different features, neither of them this one. A
  stack's revision is a file whose images the target pulls itself, so promoting
  one is copying a file across environments: a real request with a different
  design and a different authorization story (writing a stack file is team
  admin).
- **Copying environment variables, in any form** — not behind a checkbox, not
  for keys the operator selects (§4). The panel's contribution is visibility.
- **Reporting values that differ** (§4). It is the intended state of two
  environments, it would cry wolf, and it would require the planner to unseal
  secrets.
- **A commit count between two revisions** (§3). The plane has no git graph and
  will not grow one.
- **Rolling a whole promotion back as one action.** It is N deploys; each
  application rolls back through `POST /deployments/{id}/rollback` unchanged. A
  single button implying atomic reversal would be the same lie all-or-nothing
  execution would be (§8).
- **Promotion between Projects, or into a preview environment** (§3).
- **Creating the missing application in the target environment** (§3).
- **A `promotion.succeeded` event type.** The children already fire
  `deploy.succeeded` / `deploy.failed`, and a batch-level event would need an
  answer to "what does two-of-three mean" — the question
  [compose-stacks.md](compose-stacks.md) §6 declined for `compose.crashed`. It
  belongs to alerting policy, out of scope there too.
- **Coalescing the N approval inbox items a gated promotion produces.** A real
  annoyance, and an inbox-wide concern (every batch action has it) rather than a
  promotion one. Named as a known cost, not fixed here.
- **A built-in registry as the transfer path.** ADR-008 keeps it a Later
  candidate behind a superseding ADR; the relay plus a local re-tag is what this
  slice uses, and an operator who *has* a registry already gets push/pull
  through [registries.md](registries.md) for free.

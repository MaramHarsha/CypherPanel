# Feature spec: Project shared variables

> Six applications in one project need the same `SENTRY_DSN`, the same
> `SMTP_HOST`, the same internal API base URL. Today each carries its own copy
> — pasted six times, rotated six times, and stale in the one place nobody
> remembered. A **shared variable** is that value defined once, for the whole
> project or for a single environment, and referenced from any application's
> environment variables as `{{shared.KEY}}`. Values are sealed and write-only
> exactly like an application's own env vars, and changing one tells you, per
> application, that it needs a redeploy to take effect.
>
> This feature has **no [feature-matrix.md](../product/feature-matrix.md) row
> yet**; the row ("Shared variables (project / environment scope)", **V1**)
> lands with this spec. It discharges part of Phase 4's acceptance — "a Coolify
> or Dokploy user can self-migrate a typical workload without losing a
> capability they used" ([roadmap.md](../roadmap.md)).
>
> Written 2026-08-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Prior art: Coolify's
> `SharedEnvironmentVariable` model and the `{{scope.KEY}}` interpolation in
> `app/Models/EnvironmentVariable.php`
> ([research/coolify.md](../../research/coolify.md)) — concept and syntax shape
> only, never code (CLAUDE.md rule 1). Its *miss* behaviour is the one thing we
> deliberately do not port (§3).

## 1. The core idea: a second source for an env map the plane already assembles

A shared variable adds **no new path to the agent**. `Scheduler.buildSpec`
already lists an application's sealed `app_env_vars`, unseals each with the
injected `Opener` (`*secret.Box`), and puts the plaintext into `AppSpec.Env`.
This feature adds one step inside that function: expand `{{shared.KEY}}`
against a second sealed table before the map goes on the wire.

That defines the whole slice: **`work.proto` does not change**, no new NATS
subject, no agent code, no reconciler. The agent receives a fully-resolved
`AppSpec.Env` exactly as today and cannot tell a shared value from a local one
— so "the expanded value stays sealed on the wire" is satisfied by the mTLS
channel already carrying it (ENGINEERING rule 23), with no second plaintext
path invented.

```
project scope          environment scope            app scope
shared_variables(NULL)  shared_variables(env_id)   app_env_vars
        │                       │                       │
        └──────── shadowed by ──┘                       │
                        └───────────┬───────────────────┘
                                    ▼
              Scheduler.buildSpec — unseal both, expand {{shared.KEY}}
                        (the single resolution point, in-process)
                                    │
                                    ▼
              AppSpec.Env (resolved) ──mTLS/NATS──▶ agent ──▶ container
```

## 2. The resource model

A **SharedVariable** hangs off a Project, optionally narrowed to one
Environment of that project:

```
SharedVariable:
  id             TEXT PK (sv_ prefix)
  project_id     TEXT NOT NULL → projects(id) ON DELETE CASCADE
  environment_id TEXT          → environments(id) ON DELETE CASCADE  -- NULL = whole project
  key            TEXT NOT NULL          -- [A-Za-z_][A-Za-z0-9_]*, same charset as app env keys
  value_ct       BYTEA NOT NULL         -- sealed value (secret.Box, AES-256-GCM)
  value_nonce    BYTEA NOT NULL
  created_at, updated_at
  UNIQUE NULLS NOT DISTINCT (project_id, environment_id, key)
```

`NULLS NOT DISTINCT` is load-bearing: under default semantics a NULL
`environment_id` is not equal to itself, so two project-scoped rows with the
same key would both be accepted and resolution would become order-dependent.
It requires PostgreSQL 15+, which our 16+ floor
([tech-stack.md](../tech-stack.md)) guarantees. The service separately rejects
an `environment_id` that does not belong to `project_id` (400) — the FK pair
cannot express that.

Two additive columns carry the rest of the feature:

```sql
ALTER TABLE app_env_vars ADD COLUMN shared_refs TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_app_env_vars_shared_refs ON app_env_vars USING GIN (shared_refs);

ALTER TABLE deployments  ADD COLUMN env_resolved_at TIMESTAMPTZ;  -- §5
ALTER TABLE applications ADD COLUMN env_applied_at  TIMESTAMPTZ;  -- §5
```

`shared_refs` records, **in cleartext**, which shared keys a sealed value
references — safe, because they are key names and key names are already
returned by `GET /applications/{id}/env`. It is what makes the used-by count
and the drift marker plain SQL: refs are computed at write time from the
plaintext the operator just supplied, so no read path ever unseals a value to
answer "who uses this".

Migration: `core/store/migrations/0021_shared_variables.sql` (0020 is the
current highest), goose Up/Down, additive-first and reversible (rule 16).
ID prefix `PrefixSharedVariable = "sv"` in `pkg/ids/ids.go`.

## 3. Reference syntax and resolution

- **Form.** Exactly `{{shared.KEY}}` — no inner whitespace, no other namespace.
- **Substring interpolation, several per value.** `DATABASE_URL` may be
  `postgres://{{shared.DB_USER}}:{{shared.DB_PASS}}@db:5432/app`. Composing a
  connection string out of shared parts is the main reason operators want this;
  whole-value-only would force the URL shape to be duplicated into every app.
- **Shadowing.** `KEY` resolves to the row scoped to the app's own environment
  if one exists, otherwise the project-scoped row. Nothing else — no team or
  server scope this slice (§10). One namespace with shadowing, rather than
  Coolify's `{{project.K}}`/`{{environment.K}}` pair, makes **scope a property
  of the variable, not of the reference**: promoting a value from project to
  environment scope needs no edit in any referencing application.
- **One level.** A shared variable's own value is stored verbatim; a
  `{{shared.…}}` inside it is rejected at write time. No recursion, no cycles,
  no expansion order to define.
- **Strict at write time.** `PUT /applications/{id}/env/{key}` rejects any
  `{{…}}` that is not a well-formed `{{shared.KEY}}` (`{{ shared.X }}`,
  `{{shared.}}`, `{{project.X}}` are 400s) **and** rejects a well-formed
  reference whose key does not currently resolve for that application. This is
  the behaviour we pointedly do not port: Coolify's resolver skips a reference
  it does not recognise and ships the literal `{{project.FOO}}` into the
  container.
- **One grammar, two callers.** `core/sharedvars` exports the pure pair
  `Refs(value) ([]string, error)` — used by `core/applications` on write, to
  validate and compute `shared_refs` — and `Expand(value, vals) (string,
  error)`, used by `core/scheduler` at spec-build. Neither touches the store,
  so the two cannot drift.

## 4. Expansion on the existing sealed-env path — and failing early

`buildSpec` gains one branch: if any of the app's env vars carries
`shared_refs`, load the shared variables in scope (one query, environment rows
overriding project rows), unseal them with the same `Opener`, and `Expand`.
Both maps live only in that stack frame, and errors name the **key**, never the
value (rule 20). Three call sites inherit it because they all go through
`buildSpec`: `startRollout` (deploy), `ConvergeApp` (converge pushes), and
`DesiredStateFor` (an agent's full sync on (re)connect).

**Failing early.** `Scheduler.start` resolves once, *before* the
`rev.Image != ""` branch — so a build-first deploy and a rollout-first one
(rollback, image-source app) fail identically — and before a builder is
selected or any work item is published. On failure it calls
`s.fail(ctx, dep, detail)`, the same fail-fast slot that already handles "no
eligible builder" and "dangling deploy key": the deployment row reads
`status=failed` with

```
environment variable SENTRY_DSN references {{shared.SENTRY_DSN}},
which is not defined in this project or in environment "production"
```

`Deploy` returns that record and the REST layer surfaces it. No build minutes
are spent, nothing reaches `work.*`, no container is touched.

**Sync and converge never invent a value and never remove an app.**
`ConvergeApp` propagates the error and does not publish — the container is
already running the environment it was deployed with, so nothing is lost.
`DesiredStateFor` **omits the offending key from that app's `Env` map and still
advertises the app**, logging the app id and the missing key. Omitting the
*application* is not an option: the sync reply is the complete desired set and
absence means remove (ADR-005), so a data-entry mistake must never read as
"tear this container down". Omitting the *key* is safe because container
identity is the revision id — the running container is untouched — and a later
recreate leaves the variable **unset** rather than empty, which fails loudly
inside the workload instead of silently.

*Rejected alternative:* seal the resolved environment onto the revision at
rollout time so sync and converge replay it. It would make this section
trivial, but it changes rollback semantics — a rollback today deliberately
applies *today's* env vars, a decision the UI states out loud in
`confirm-rollback.tsx`. That is its own decision, not a side effect of this
slice.

## 5. Drift: "redeploy to apply"

Container identity in the docker driver is the revision id
(`agent/driver/docker/docker.go` `convergeApp`), so a changed env map does not
recreate a container. A shared-variable change therefore needs a **deploy**,
not a converge push — the opposite of scheduled tasks, which ride the same
`AppSpec` but are re-armed by the agent without touching the container
([scheduled-tasks.md](scheduled-tasks.md) §4). We do **not** auto-deploy on
change: one edit would otherwise redeploy every referencing application across
every environment, production included, without anyone asking. The state is
made visible instead, and the deploy stays a deliberate act (ui-principles §3).

The marker is **derived, not a mutable flag**:

- `startRollout` stamps `deployments.env_resolved_at = now()` — the instant
  `buildSpec` froze that environment onto the wire.
- When that deployment is observed running (`HandleAppStatus`, the only path to
  `succeeded`), the plane copies it to `applications.env_applied_at`. A rollout
  that never succeeds never moves the stamp, so a failed deploy cannot mark an
  app clean.
- `redeploy_pending` is then *some shared variable in this app's scope, named
  in one of its `shared_refs`, has `updated_at > env_applied_at`* — one EXISTS
  query, no crypto.

A write always bumps `updated_at`, even when the value is unchanged: AES-GCM
with a fresh nonce yields different ciphertext for identical plaintext, so
comparing would mean unsealing on every write — and marking a redeploy that
turns out to be a no-op is safe, while missing one is not.

`redeploy_pending` is an additive boolean on the Application DTO, computed by a
dedicated store query rather than widened into `GetApplication`'s row — the
generated `db.Application` shape is load-bearing for the scheduler.

## 6. Security & bounds

- **Sealed at rest** (threat-model §5.1): values go through the same
  `secret.Box` as app env vars, deploy keys, and webhook secrets; two BYTEA
  columns, unsealed only in `buildSpec`, never logged, never returned.
- **No hint, no reveal.** Unlike a Notifier's `config_hint`, a shared variable
  carries **no masked summary** — it is already identified by its key, so a
  hint would be gratuitous partial disclosure. Responses carry key, scope,
  timestamps and a used-by count; the UI renders a fixed `•••••` mask
  (ui-principles §6). Read paths therefore never unseal anything.
- **Concentrated blast radius, made visible.** A shared variable reaches more
  containers by definition, so one compromised workload (threat-model §5.2)
  leaks it for every referencing application. That is inherent; the mitigations
  are *scope* — environment scope keeps a production credential out of staging
  containers — and the **used-by listing**, which shows the reach before the
  value is set.
- **Previews are excluded.** Preview environments carry no env vars at v1
  ([preview-environments.md](preview-environments.md) §5/§6), so a fork-PR
  preview resolves nothing and cannot exfiltrate a shared value (threat-model
  §5.6). This slice does not change that.
- **Authorization** reuses the project-scoped ladder: a new
  `projectIDForSharedVariable` resolver in `core/api/rest/authz.go`,
  `domain.RoleMember` for read and write — the rank app env vars already
  require, since they are the same class of secret. Non-member → 404,
  under-ranked → 403. Granular RBAC stays V1.x (feature-matrix).
- **Bounds.** Key charset `[A-Za-z_][A-Za-z0-9_]*`; value capped at 32 KiB (an
  env var, not a file); at most 16 references expanded per value.

## 7. API surface (under `/api/v1`)

```
POST   /projects/{id}/shared-variables       → 201 SharedVariable  (key, environment_id?, value)
GET    /projects/{id}/shared-variables       → [SharedVariable]    (both scopes, each with used_by_count)
GET    /shared-variables/{id}                → SharedVariable
PATCH  /shared-variables/{id}                → SharedVariable      (value only)
DELETE /shared-variables/{id}                → 204 | 409 if referenced
GET    /shared-variables/{id}/used-by        → [SharedVariableUsage]
```

`SharedVariable` is `{id, project_id, environment_id, environment_name, key,
used_by_count, created_at, updated_at}` — never the value.
`SharedVariableUsage` is `{application_id, application_name, environment_name,
redeploy_pending}`.

**`key` and `environment_id` are immutable after create.** Changing either
would silently re-point or orphan every referencing application; delete and
re-create is explicit, and the delete guard fires.

**Delete is guarded, with no force override.** If any app env var in scope
references the key, `DELETE` returns 409 naming the referencing applications;
the operator removes the references first, and `used-by` says exactly where.
With §3's write-time check, no single operator action can produce an
unresolvable reference — §4's handling covers only a write-write race.

`used_by_count` is scope-accurate: an application whose environment defines a
shadowing row of the same key is **not** counted against the project-scoped
variable, because it does not use it.

One additive change to an existing route: `GET /applications/{id}/env` gains a
`shared_refs` object mapping each env key to the shared keys it references, so
the Env vars tab can show the wiring without a reveal.

## 8. The project settings tab

`web/src/routes/_app/projects/$projectId/settings.tsx` becomes a layout with a
tab strip — mirroring `_app/settings.tsx` — with today's notifiers and danger
zone moving to `settings/index.tsx` and this screen landing at
`settings/shared-variables.tsx`. A new *top-level* nav item is not on the table
(ui-principles §4 fixes the top bar at four).

Per the design board: masthead `SHARED VARIABLES` under the
`ATLAS-CRM / SETTINGS /` dateline, an `+ Add` action, and the one
plain-language line (ui-principles §11) reading *"Defined once per project or
environment, referenced from any app's env vars as `{{shared.KEY}}`. Values
sealed and write-only, like everything else."* Rows carry `KEY` in mono, a
scope word (`project`, or the environment's name), the `•••••` mask,
`used by N apps`, and a `✕` opening `ConfirmDestructive` with the referencing
applications as its blast radius (ui-principles §2). The footer states the
consequence: *"Changing one marks every referencing app 'redeploy to apply'."*

On the application side, `redeploy_pending` renders as a **badge beside the
status, not a status word** — the six-word vocabulary in ui-principles §5 is
closed and "needs a redeploy" is not an observed state. It appears on the
application masthead and on its Env vars tab, each carrying the "Deploy now"
action already there. All four page states apply (ui-principles §1).

## 9. Acceptance (testable)

1. Create `SENTRY_DSN` at project scope; set an app env var
   `SENTRY_DSN={{shared.SENTRY_DSN}}`; deploy → the container's `SENTRY_DSN`
   holds the shared value, and no API response anywhere contains it.
2. Shadowing: `SMTP_HOST` = `mail.internal` at project scope and
   `smtp.sendgrid.net` scoped to `production` → an app in `production` resolves
   the second, an app in `staging` resolves the first.
3. `PUT /applications/{id}/env/X` with `{{shared.NOPE}}`, `{{ shared.A }}`, or
   `{{project.A}}` → 400 each, nothing stored.
4. Force the unresolvable case (delete the row directly in the store, then
   deploy) → `POST /applications/{id}/deploy` returns a `failed` deployment
   whose detail names `NOPE`, **no** build work item is published on `work.*`,
   and the running container is untouched with its previous value intact.
5. Three apps reference `SENTRY_DSN`; a fourth is in an environment that
   shadows it → the list reports `used_by_count: 3`, `used-by` names those
   three, and the fourth is absent.
6. `PATCH` the variable's value → all three apps report
   `redeploy_pending: true`; redeploy one → that one flips to false while the
   other two stay true.
7. `DELETE` the referenced variable → 409 naming the three applications;
   remove the references, then `DELETE` → 204.
8. Store round-trip (real Postgres, `TestStoreSharedVariableRoundtrip`): a
   project-scoped and an environment-scoped row with the same key both persist
   and a duplicate of either is `ErrConflict`; deleting the environment
   cascades only the environment-scoped row; deleting the project removes both.

## 10. Out of scope this slice

Team-scoped and server-scoped variables (Coolify has both; project and
environment cover the demand and the row shape is forward-compatible) ·
`{{shared.KEY}}` anywhere other than application env vars — image references,
domains, build args, Managed Database and Compose Stack configuration · nested
or recursive expansion · an escape sequence for a literal `{{…}}` (nothing
needs one yet; adding `\{{` later is additive) · reading a value back, with or
without an audit-logged reveal · per-variable comments, versioning, or change
history · bulk `.env` paste import · automatic redeploy on change (§5) ·
injecting shared variables into preview environments (§6) · generated
"shown once" values in the template-catalog sense · external secret managers
(Vault, Infisical, Doppler — **Later**, feature-matrix.md).

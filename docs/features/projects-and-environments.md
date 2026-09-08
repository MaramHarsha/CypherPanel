# Feature spec: Project and environment lifecycle

> Status: implemented. Migration `0030_project_lifecycle.sql`.
>
> Projects and environments have existed since the deploy slice
> ([application-deploy.md](application-deploy.md) §1) as little more than
> containers: a name, a team, a creation date. Everything an operator wants to
> *do* with one afterwards — rename it, hand it to another team, decide where
> "open this project" lands, see at a glance which one is on fire — had no API.
> This closes that gap.

## 1. Why this shape

Four questions the panel could not answer, each of which the redesign's project
screens ask directly (canvas `12c` project settings, `13y` create modal, `14b`
the mobile projects list):

1. **"What is this project called in a URL?"** The name is what a human reads
   and changes; a URL and a CLI invocation need something that does *not*
   change. Those are two different fields, and conflating them means every
   rename breaks a bookmark.
2. **"Which environment do I land in?"** A project can hold production,
   staging and a dozen previews. Without a declared default, every entry point
   has to guess, and different entry points guess differently.
3. **"What happened here recently?"** The projects list is ordered by
   relevance, and relevance is recency. Deriving it at read time means scanning
   every deployment of every resource under every project.
4. **"Which project needs me right now?"** A list of names is not an operations
   screen. Worst-first ordering needs a rollup of what is inside.

And one rule with teeth: **a preview environment belongs to its pull request**.
Previews are created and destroyed by the PR lifecycle
([preview-environments.md](preview-environments.md)). Letting an operator rename
or delete one by hand desynchronises it from the PR that owns it, so the
difference between a preview and a standing environment has to be a column the
API enforces, not a guess from the name.

## 2. The resource model

```
projects
  slug                    TEXT NOT NULL   -- unique per team, immutable
  default_environment_id  TEXT NULL REFERENCES environments(id) ON DELETE SET NULL
  last_activity_at        TIMESTAMPTZ NOT NULL DEFAULT now()

environments
  kind                    TEXT NOT NULL DEFAULT 'standard'
                          CHECK (kind IN ('production','standard','preview'))
```

**`slug` is unique per team, not globally.** Two customers may both have a
"website", and a global unique would make the second one's handle depend on who
signed up first.

**`default_environment_id` is `ON DELETE SET NULL`, not `RESTRICT`.** Deleting
the default environment should leave the project without one, not refuse the
deletion — the UI can ask for a new default, but it cannot recover from a
delete that half-succeeded.

**`last_activity_at` is maintained, not derived.** `TouchProject` and
`TouchProjectForEnvironment` are called on the paths that change what an
operator would want to see. A derived value would mean a scan of every resource
underneath on every list render.

### Slug derivation

`projects.Slugify` lowercases, replaces every run of non-`[a-z0-9]` with `-`,
trims separators, and caps at 60 characters. A name with nothing usable in it
(punctuation only, or a script with no ASCII) becomes `project`; the
disambiguating suffix does the rest.

Collisions take the next free `-2`, `-3` … up to 100, after which the name is
the problem rather than the suffix and the create is refused. **The backfill
migration implements the same rule in SQL**, numbering same-slug rows
oldest-first so the longest-standing project keeps the bare slug. Backfilled and
freshly created rows are indistinguishable afterwards — which is the point of
exporting `Slugify` rather than hiding it.

## 3. API surface

```
PATCH  /api/v1/projects/{id}        → Project          (see §4)
PATCH  /api/v1/environments/{id}    → Environment      (team admin)
DELETE /api/v1/environments/{id}    → 204              (team admin)
```

`Project` gains `slug`, `default_environment_id`, `last_activity_at`, and — on
the **list** only — `application_count`, `database_count`, `error_count` and
`worst_status`. The rollup is list-only because a single-project page shows the
resources themselves; sending counts there would be a second source of truth for
something already on screen.

A project holding nothing gets explicit zeros rather than omitted fields, so the
UI can say "no resources yet" without guessing whether the number is missing or
merely absent.

`Environment` gains `kind` and `is_default`.

**`slug` is not editable.** It is chosen once, at creation. There is no
`PATCH … {slug}` and there deliberately never will be: the whole reason it
exists separately from the name is that it does not move.

### The rollup query

One query for the whole page, not one per project — rendering a list of N
projects with N+1 queries scales with the operator's portfolio. Severity is
ranked in SQL rather than in Go so the ordering is the database's job and cannot
drift between callers:

| status | rank |
|---|---|
| `error` | 5 |
| `degraded` | 4 |
| `deploying` / `provisioning` | 3 |
| `running` | 1 |
| anything else | 0 |

Applications and managed databases are ranked together. An operator scanning the
page does not care which *kind* of resource is broken, and the two vocabularies
(`deploying` for applications, `provisioning` for databases) mean the same thing
at a glance.

## 4. Authorization

| Action | Requires |
|---|---|
| Rename, set default environment | team **admin** |
| Rename / delete an environment | team **admin** |
| **Transfer to another team** | **owner of both** teams, **session only** |

A transfer is different in kind from a rename: it changes *who can see
everything inside*. So it needs ownership of the destination as well as the
source — a transfer into a team you merely belong to would let a member move
work under someone else's roof — and it is refused to API tokens entirely. A
token that leaked must not be able to hand a project to a team the attacker
controls.

On transfer the slug is kept when it is free in the destination and reassigned
to the next free one when it is not. The transfer is **not** refused on a
clash: a name collision between two teams is not the operator's mistake to fix.

## 5. Refusals

| Case | Answer |
|---|---|
| Default environment belongs to another project | 400 |
| Rename or delete a **preview** environment | 409 — its pull request owns it |
| Delete the **last standing** environment | 409 — a project keeps at least one |
| Delete an environment holding resources | 409 (foreign keys) |
| Name taken in the team / project | 409 |

Previews do not count towards the "last standing environment" floor. A project
whose only other environment is a preview still cannot delete production,
because the preview will disappear on its own when the PR closes.

## 6. Acceptance (testable)

1. Creating two projects named "Atlas CRM" in one team yields slugs
   `atlas-crm` and `atlas-crm-2`; the same name in another team yields
   `atlas-crm` again. ✅ `TestCreateDisambiguatesSlugsWithinATeam`
2. A fresh project's `default_environment_id` is its production environment,
   set in the same transaction that creates both. ✅
   `TestCreateSetsTheFirstEnvironmentAsDefault`
3. Renaming a project changes `name` and leaves `slug` untouched. ✅
   `TestUpdateRenamesWithoutChangingTheSlug`
4. Transferring into a team where the slug collides reassigns the slug and
   succeeds; transferring where it does not, keeps it. ✅
   `TestUpdateTransferReassignsAClashingSlug`
5. Pointing `default_environment_id` at another project's environment → 400. ✅
   `TestUpdateRejectsAForeignDefaultEnvironment`
6. Renaming or deleting a preview environment → 409. ✅
   `TestPreviewEnvironmentsAreNotEditableByHand`
7. Deleting the only standing environment → 409, including when a preview sits
   alongside it; deleting a second standing environment succeeds. ✅
   `TestDeleteEnvironmentKeepsTheLastStandingOne`
8. The projects list carries counts and a worst status computed in one query,
   with explicit zeros for an empty project.
9. The migration's backfill produces the same slugs `Slugify` would, and its
   `Down` is reversible.

## 7. Out of scope

Slug editing (§3 — deliberately never) · project archival as distinct from
deletion · per-environment authorization (roles are per team; an environment is
not a security boundary) · moving an *environment* between projects (its
resources' server placement and DNS records both assume a stable project) ·
ordering the projects list server-side (`last_activity_at` and `worst_status`
are exposed so the client can order; a server-side `?sort=` is a paging concern
and paging is not here yet) · counting previews separately in the rollup.

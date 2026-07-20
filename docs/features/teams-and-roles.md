# Feature spec: Teams and roles

> Until now every authenticated user is an implicit superuser. This slice makes
> the panel multi-user: **Teams** own projects (glossary: "the tenancy
> boundary"), users belong to teams with a **ranked role** (member < admin <
> owner, Coolify's proven model — feature-matrix "simple roles" **V1**;
> granular RBAC is V1.x), and every project-scoped route enforces membership.
> Closes the last Phase 3 bucket item ([roadmap.md](../roadmap.md)).
>
> Written 2026-07-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Role model ported from Coolify's `Role` enum
> rank comparison ([research/coolify.md](../../research/coolify.md) AuthZ row)
> — logic only, never code (CLAUDE.md rule 1).

## 1. The model: two small role planes, one bypass

**Team roles** (per membership, `team_members.role`) gate everything under a
team's projects. **Panel roles** (the existing `users.role` column) gate the
shared infrastructure that servers workloads for every team. Rank comparison
everywhere: `member(1) < admin(2) < owner(3)`.

| Action | Requires |
|---|---|
| View/operate a team's workloads (apps, DBs, deploys, previews, notifiers, tasks, backups, env vars, logs) | team **member** |
| Create/delete projects in a team; manage members (below owner) | team **admin** |
| Rename/delete team; promote/demote owners | team **owner** |
| Manage servers, deploy keys, backup targets; create users; create teams | panel **admin** |
| Change users' panel roles; delete users | panel **owner** |

**Panel-owner bypass:** a panel `owner` holds implicit team-owner rank on every
team (the self-hosted superadmin — recoverability beats ceremony at v1). Panel
`admin`/`member` have no implicit team access.

**v1 honesty note (threat-model):** teams are *collaboration* scopes, not
security boundaries between mutually distrusting tenants — servers are shared
infra (§5), and any team member can deploy to any server. Hostile
multi-tenancy is out of scope for a self-hosted v1 panel, exactly as in both
references.

## 2. The resource model

```
Team:          id (tm_), name UNIQUE, created_at, updated_at
TeamMember:    team_id → teams CASCADE, user_id → users CASCADE,
               role (member|admin|owner), created_at, PK(team_id, user_id)
Project:       + team_id TEXT NOT NULL → teams(id)
```

**Migration (0011)** creates the `default` team (fixed id `tm_default`),
enrolls every existing user as its **owner** (they were all implicit
superusers before — no one loses access), and assigns every existing project
to it, then sets `team_id NOT NULL`. Down reverses cleanly (rule 16).
`bootstrapAdmin` additionally ensures the default team exists and the admin is
its owner, so a fresh boot and an upgraded panel converge to the same shape.

## 3. Enforcement — resolve to the project, check the membership

Authorization happens in the REST layer, after authentication, per request:

1. **Resolve** the addressed resource to its `project_id` using the chain the
   data model already provides: app → environment → project; database →
   environment → project; deployment → app…; preview → source app…; notifier →
   project (direct); scheduled task → app…; backup schedule → database….
2. **Look up** the caller's role in the project's team (one indexed query:
   `projects ⋈ team_members`), applying the panel-owner bypass.
3. **Compare rank** against the route's minimum; failure is **404 for
   non-members** (a project you cannot see does not exist — no tenancy
   probing) and **403 for insufficient rank** (a member who may see it but not
   do that).

Listings filter rather than fail: `GET /projects` returns the projects of the
caller's teams (all projects for a panel owner). Server/deploy-key/backup-
target **reads** stay open to any authenticated user (the app-create flow
needs them); **writes** need panel admin.

Nothing changes on the wire or in the scheduler: teams are an authorization
overlay on the plane's API; desired state, agents, and subjects are untouched.

## 4. API surface (under `/api/v1`)

```
POST   /teams                       → 201   (panel admin+)
GET    /teams                       → [Team + my role]   (mine; all for panel owner)
GET    /teams/{id}                  → Team               (member+)
PATCH  /teams/{id}                  → Team               (rename; team owner)
DELETE /teams/{id}                  → 204                (team owner; refused while projects exist)

GET    /teams/{id}/members          → [Member]           (member+)
POST   /teams/{id}/members          → 201  {email, role} (team admin+; owner grants need team owner)
PATCH  /teams/{id}/members/{uid}    → Member             (role change; same grant rules)
DELETE /teams/{id}/members/{uid}    → 204                (team admin+; owner removal needs team owner)

POST   /users                       → 201  {email, password, role?}  (panel admin+; panel-role grants above member need panel owner)
GET    /users                       → [User]             (panel admin+)
PATCH  /users/{id}                  → User               (panel role; panel owner)
DELETE /users/{id}                  → 204                (panel owner; never self)
```

- **Last-owner guard:** the final owner of a team cannot be demoted or removed
  (and cannot leave); a team always has an owner.
- **Project create** (`POST /projects`) gains optional `team_id`: defaulted
  when the caller belongs to exactly one team, required (400) when ambiguous.
  Existing single-team panels keep working unchanged.
- `GET /auth/me` gains `role` (already present) and `teams: [{id, name, role}]`.
- Team delete while projects exist → 409 (delete or move projects first —
  destroying a team must not silently cascade workloads).

## 5. Security

- **No self-service privilege escalation:** role grants always require
  strictly sufficient rank (a team admin can neither grant owner nor touch an
  owner; a panel admin creates only `member` users). Rank checks are
  server-side; the role vocabulary is a closed set.
- **404-over-403 for non-members** (§3) keeps team/project ids from being
  probeable across tenants (same posture as the webhook-id rule).
- **Sessions unchanged** — login, rate limiting, and token hashing stay as
  they are (threat-model §5.8); this slice adds authorization, not new
  authentication surface. TOTP remains its own V1 feature-matrix row, not
  part of this slice (§7).
- The **migration defaults to over-permission** (existing users → owners of
  the default team) rather than lockout: matching their pre-migration implicit
  rights, and self-correctable by the panel owner afterwards.

## 6. Acceptance (testable)

1. Two teams, one user in each: each user lists/sees only their team's
   projects; addressing the other team's app/db/deployment/preview/notifier/
   task by id → 404.
2. A team `member` deploys an app in their team; the same member cannot add
   members (403) or delete the project (403); a team `admin` can do both.
3. The last owner of a team cannot be demoted or removed (409/400); after
   adding a second owner, the first can be.
4. A panel `member` cannot create a server or deploy key (403); a panel
   `admin` can; only a panel `owner` can change another user's panel role.
5. A panel `owner` can see and operate every team's resources without
   membership rows.
6. After migrating a populated panel, every pre-existing user can still do
   everything they could before (owner of `default`), and every project lives
   in the default team.

## 7. Out of scope this slice

Granular/custom RBAC (V1.x) · team-owned servers and per-team infra (the
glossary's full "owns servers" target — lands with granular RBAC; servers stay
shared, panel-role-gated at v1) · invitations by email (users are created with
a password by an admin; SMTP-based invites can reuse core/notify later) · TOTP
2FA (its own V1 feature-matrix row) · API tokens with team scopes · audit log
· team switching UI state · hostile multi-tenant isolation (§1).

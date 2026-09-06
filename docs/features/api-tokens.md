# Feature spec: Scoped API tokens (personal access tokens)

> The feature-matrix V1 row "API tokens with scoped abilities". A credential a
> user can mint for CI/automation that drives the REST API without a browser
> login — because the API is the product (vision non-negotiable 4) and the whole
> platform must be drivable by curl (application-deploy.md §6 acceptance 4).
>
> Written 2026-07-21, just before implementation (CLAUDE.md rule 7). Vocabulary
> per [glossary.md](../glossary.md).

## 1. Model

A **personal access token** is an opaque bearer credential that authenticates as
its **owning user**, narrowed by its **abilities**. Every request it makes is
authorized as if that user made it — same panel role, same team memberships —
and then further checked against what the token itself may do. A token's
authority is therefore the *intersection*: it can never reach something its
owner could not. This composes for free with the existing teams-and-roles
authorization ([teams-and-roles.md](teams-and-roles.md)) — handlers already
resolve the acting user, and a token simply supplies one.

```
APIToken:
  id            tok_… (public identifier; safe to display and to name in URLs)
  user_id       → the owning user (ON DELETE CASCADE — deleting the user revokes)
  name          human label ("ci-deploy", "laptop"), 1–100 chars
  abilities     TEXT[] ⊆ {read, write, deploy, env, servers, admin}, non-empty
  project_id    → a project (ON DELETE CASCADE), NULL = unscoped
  token_hash    sha256(raw)   — only the hash is stored, never the raw token
  last_used_at  set on each successful authentication (best-effort)
  expires_at    NULL = never expires
  created_at
```

### Abilities

| Ability | Permits |
|---|---|
| `read` | Safe methods: `GET`, `HEAD`, `OPTIONS` |
| `write` | Every mutation, including the three below |
| `deploy` | Triggering deploys and rollbacks (`POST …/deploy`, `POST …/rollback`) |
| `env` | Setting and removing an application's environment variables |
| `servers` | Enrolling and removing servers, and reading join instructions |
| `admin` | Teams, members, invitations, access requests, users, panel settings |

Deploy is separated from write so a CI credential can ship code without also
being able to delete the application it deploys.

`env`, `servers` and `admin` carve narrower grants out of what `write` covers,
so a credential can be minted for one job instead of for every mutation the API
has. **`write` still satisfies all three** (`domain.Ability.Implies`), and that
is deliberate: making `write` mean "mutations that are not env, servers or
admin" would silently strip capability from credentials already sitting in
someone's CI configuration, which ENGINEERING rule 17 forbids. The narrow
abilities are for narrowing a *new* token, never for re-cutting an old one.

Reads stay on `read`, including reads of env keys and of servers. Requiring the
narrow ability for a listing would take listings away from every read-only token
already issued, for no gain: env *values* are write-only by construction, so
there is nothing sensitive behind the key list that `read` did not already
reach. The required ability is derived
from the request in one place (`requiredAbility` in `core/api/rest/rest.go`),
and the deploy-triggering paths are an explicit table rather than a pattern —
a new deploy-shaped route must be added deliberately, never inherit `write` by
accident. A token missing the required ability gets `403` naming what it lacks.

An omitted ability list on create means the full set, so clients written before
this existed keep working; an explicitly empty list is **rejected** rather than
silently widened — a credential's authority is always a deliberate choice.
Existing tokens were migrated with the full set (migration `0018`), so no live
credential changed authority.

### Narrowing a token to one project

Abilities say what a credential may do. They say nothing about *where*, so a
deploy token minted for one project can deploy every project its owner can
reach. A CI pipeline needs exactly one project, and handing it the whole account
is the gap between what the credential is for and what it can do.

`project_id` on create closes that gap. A scoped token:

- answers **404** for any other project — the same answer a non-member gets, so
  one token cannot be used to enumerate which projects its owner can see;
- answers **403** for panel- and team-level routes (`/teams`, `/users`,
  `/panel/*`, `/servers`, `/audit`, `/deploy-keys`, `/backup-targets`,
  `/registries`, `/invites`, `/access-requests`), which belong to no project, so
  "which project?" has no answer for them;
- may only be minted for a project its owner is a member of, from an
  interactive session like every other credential change.

The check lives at one seam. Every project-scoped authorization already funnels
through `requireProjectRole`, whether the route names a project directly or
resolves one from an application, database, deployment or notifier, so the scope
is compared there and nowhere else. Panel-level routes are refused earlier, in
the same middleware that checks abilities, because there is no project to
resolve.

Nullable, and unscoped is the existing behaviour: every token issued before this
existed keeps it. `ON DELETE CASCADE` on the project — a token scoped to a
deleted project would otherwise be confined to nothing and reach nothing, which
is a confusing way to say "revoked".

### Credential management is session-only

Minting tokens, listing/revoking sessions, and turning two-factor off are
reachable **only from an interactive session**, never from an API token, no
matter its abilities. This is the difference between a leaked CI credential
being a bounded incident and being durable account takeover: a token cannot
mint itself a wider token, cannot sign the operator out, and cannot remove the
second factor. Enforced by the `sessionOnly` wrapper, asserted by
`TestCredentialRoutesRejectAPITokens`.

**Secret discipline.** The raw token is `cyp_<secret>` and is returned exactly
once, in the create response — never again, by any endpoint. Only its SHA-256 is
persisted (identical discipline to sessions and join tokens, threat-model §5.3).
A database read yields no usable credential. The `cyp_` prefix is what lets the
authenticator route a bearer token to the token path without a speculative
session lookup: session secrets are uppercase base32 (`ids.Secret`) and can
never begin with the lowercase, underscore-bearing prefix, so the two credential
spaces are provably disjoint.

## 2. Authentication path

`Authorization: Bearer <token>` already flows through the `authed` middleware →
`Authenticator.Authenticate`. That method now branches on the `cyp_` prefix:

- **prefixed** → resolve the token: look up the owning user by
  `sha256(raw)`, filtered on `expires_at IS NULL OR expires_at > now()` so an
  expired token resolves to nothing (401). On success, record `last_used_at`
  (best-effort — a failed touch never fails the request) and return the user.
- **otherwise** → the existing session path, unchanged.

An expired or revoked token is indistinguishable from a never-existent one:
both yield `ErrInvalidSession` → 401. A bare prefix with no secret is rejected.

## 3. API surface (all under `/api/v1`, all require an authenticated caller)

```
POST   /tokens        {name, expires_in_days?}  → 201 {id,name,…,token}  (token shown once)
GET    /tokens                                   → 200 [{id,name,last_used_at,expires_at,created_at}]
DELETE /tokens/{id}                              → 204 · 404 if not the caller's
```

- **Create.** `name` is required (1–100 chars). `expires_in_days` is optional;
  `0`/omitted means never expires, and it is bounded at `3650` (ten years) so the
  day count cannot overflow when added to `now` (defence in depth, CWE-190). The
  response is the only place the raw `token` appears.
- **List.** Returns the caller's own tokens (metadata only — the secret is
  structurally absent from the DTO, so it cannot leak here).
- **Revoke.** Deletes by id, but only a token the caller owns. A token that does
  not exist *or* belongs to someone else both return 404 — a caller can neither
  revoke nor probe for other users' tokens.

Scoping to the caller is by `user_id`, mirroring how every other per-user
resource is gated. Cross-user administration of tokens is not a v1 need.

## 4. Security properties (threat-model §5)

- Stored hashed; raw shown once (§5.3). Asset A4 already lists API tokens.
- Ownership-checked revoke; enumeration-safe 404 (no oracle for others' ids).
- Expiry enforced in SQL, not application code, so it holds for every caller.
- Cascade delete: removing a user removes their tokens in the same transaction.
- A token never widens authorization — it can only ever do what its user can,
  so introducing tokens adds no new privilege path to audit.

## 5. Out of scope this slice

- Token rotation endpoints (revoke + create covers it), org/service accounts
  decoupled from a human user, and last-used IP/user-agent metadata.
- Per-*resource* token scoping ("this token may deploy *only* app X"). Project
  scoping (§1) narrows to a project; narrowing to one application inside it is a
  follow-on at the same handler seam.
- Scoping a token to more than one project, or changing a token's scope after
  it is minted. Both are revoke-and-recreate today, which is one action and
  leaves an audit row saying so.

*(Per-token abilities were listed here as out of scope when this spec was
written on 2026-07-21; they landed on 2026-08-02 and are specified in §1.)*

## 6. Acceptance (testable)

1. Create a token, use `cyp_…` as `Authorization: Bearer` on a protected route →
   200, acting as the owning user (rest handler test + auth unit test).
2. Listing never contains the secret; the DTO has no token field (test asserts).
3. Revoke → the same token is 401 on the next request (test).
4. An expired token is 401 (auth unit test + real-Postgres store test on the
   `expires_at` SQL filter).
5. Deleting the user cascades to their tokens (real-Postgres store test).
6. Abilities are enforced per request: a `read` token cannot create, delete, or
   deploy; a `write` token cannot deploy or rollback; a `deploy` token cannot
   change configuration (`TestAPITokenAbilitiesAreEnforced`, a method/path
   matrix). A session is never narrowed (`TestSessionUnaffectedByAbilities`).
7. An empty or unknown ability set is refused at creation
   (`TestCreateTokenRejectsBadAbilities`).
8. No API token, however privileged, can reach token, session, or two-factor
   management (`TestCredentialRoutesRejectAPITokens`).

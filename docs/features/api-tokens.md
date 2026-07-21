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
its **owning user**. It carries no authorization of its own: every request it
makes is authorized exactly as if that user made it — same panel role, same team
memberships. That inheritance *is* the token's scope. A member's token can do
what the member can do; an owner's token can do what the owner can do. This is
the GitHub-classic-PAT model, and it composes for free with the existing
teams-and-roles authorization ([teams-and-roles.md](teams-and-roles.md)) —
handlers already resolve the acting user, and a token simply supplies one.

```
APIToken:
  id            tok_… (public identifier; safe to display and to name in URLs)
  user_id       → the owning user (ON DELETE CASCADE — deleting the user revokes)
  name          human label ("ci-deploy", "laptop"), 1–100 chars
  token_hash    sha256(raw)   — only the hash is stored, never the raw token
  last_used_at  set on each successful authentication (best-effort)
  expires_at    NULL = never expires
  created_at
```

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

- **Fine-grained per-token abilities** (read-only vs deploy-only vs full, the
  "read/write/deploy" ability model in the feature-matrix reference). V1 scope
  is user-role inheritance; per-token capability narrowing is a follow-on that
  slots in as an added predicate at the same handler seam as object-level
  authz — no schema change to the credential itself, just an abilities column.
- Token rotation endpoints (revoke + create covers it), org/service accounts
  decoupled from a human user, and last-used IP/user-agent metadata.

## 6. Acceptance (testable)

1. Create a token, use `cyp_…` as `Authorization: Bearer` on a protected route →
   200, acting as the owning user (rest handler test + auth unit test).
2. Listing never contains the secret; the DTO has no token field (test asserts).
3. Revoke → the same token is 401 on the next request (test).
4. An expired token is 401 (auth unit test + real-Postgres store test on the
   `expires_at` SQL filter).
5. Deleting the user cascades to their tokens (real-Postgres store test).

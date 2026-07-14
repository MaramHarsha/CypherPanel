---
name: auth-and-rbac
description: How to secure a CypherPanel endpoint — JWT claims, RBAC middleware, resource scoping, and audit-trail requirements. Use when adding or changing any API endpoint or privileged operation.
---

# Auth & RBAC

## Token model (do not re-derive per feature)

- Access tokens: 15-min HS256 JWTs carrying `role` + resource scope (`reseller_id`, `account_id`) in claims (`internal/auth/tokens.go`) — authorization decisions never need a DB lookup for identity.
- Refresh tokens: opaque, server-side in Redis, **single-use** (`GetDel` rotation). Logout/suspension revokes immediately; a pure-JWT "can't revoke" design is explicitly rejected.
- Passwords: Argon2id in PHC string format with parameters embedded in the hash (`internal/auth/password.go`) — raise parameters without invalidating old hashes. Constant-time comparison always.

## Securing an endpoint — the only acceptable pattern

1. Route goes under the `authed` group (wrapped by `auth.Middleware`) in `internal/api/router.go`. Handlers never parse tokens themselves.
2. Role gating uses the **centralized** `auth.RequireRole(...)` group middleware (e.g. the `/admin` group requires `RoleRootAdmin`). **Never** write ad-hoc `if claims.Role == "admin"` checks inside handlers — scattered checks are where IDOR bugs live.
3. Resource scoping: read `auth.ClaimsFrom(c)` and verify the target resource belongs to the caller's scope (reseller pool / own account) before acting. As reseller/end-user surfaces grow, scoping helpers belong in `internal/auth`, not copy-pasted into handlers.
4. Roles are the fixed enum in `internal/auth/roles.go` (`root_admin`, `reseller`, `end_user`). Post-MVP team sub-roles layer on top of `end_user`; they do not extend this enum.

## Login behavior invariants

- Unknown username and wrong password return the **same** 401 body (no username probing). Suspended users are rejected at login *and* refresh.
- Failed logins are audited (`auth.login_failed`) with the attempted identifier and IP.

## Audit trail — when it's mandatory

Any provisioning, suspension, permission change, credential event, or task dispatch **must** write an `audit.Entry` (`internal/audit`) with actor ID/role, action (`domain.verb` naming: `auth.login`, `task.create`, `server.register`), target type/ID, and client IP. Audit writes are synchronous; `Detail` maps must never contain secrets. If you're unsure whether an action needs auditing, it does.

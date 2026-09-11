# Feature spec: Session management

> The feature-matrix V1 row "Login rate limiting & session management". Rate
> limiting shipped with the login path (`core/auth/ratelimit.go`); this spec
> covers the other half — seeing where an account is signed in, and being able
> to end those sessions.
>
> Written 2026-08-02, alongside implementation. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why

A password that leaks does not announce itself. The operator's recourse —
"change the password and sign everything else out" — needs the second half to
exist, or a stolen session keeps working for its full lifetime no matter how
many times the password changes. Panel compromise is fleet control
([threat-model](../security/threat-model.md)), so this is not a convenience
feature.

## 2. Model

A **session** is one live sign-in, created by `Login` (or first-run setup) and
identified to the client only by a bearer token whose SHA-256 is what the
database stores. The existing `sessions` table already carried everything
needed — `id`, `user_id`, `token_hash`, `expires_at`, `created_at` — so this
feature adds **no migration**.

```
Session (as presented):
  id          sess_… (public identifier; safe to name in URLs)
  current     true for the session making this request
  created_at  when the sign-in happened
  expires_at  when it stops working on its own
```

The DTO deliberately carries **no token material**. A session list must never
itself become a source of credentials: a read-only leak of this endpoint's
output gives an attacker nothing to replay.

**Not included, deliberately:** IP address and user-agent. Both are attacker-
controlled strings that would be rendered in the operator's browser, and
neither is load-bearing for the decision this screen supports ("sign out
everything that isn't me"). They are a follow-on if real usage shows the list
is hard to reason about without them — with the escaping and privacy questions
handled then, not smuggled in now.

## 3. API

```
GET    /api/v1/auth/sessions                 → [Session]   (live sessions, newest first)
DELETE /api/v1/auth/sessions/{id}            → 204         (one session, must be the caller's)
POST   /api/v1/auth/sessions/revoke-others   → {revoked:N} (everything except this device)
```

**Ownership.** Every query is scoped by `user_id` in SQL, not by a check after
the fetch. A session id belonging to someone else is reported exactly like one
that does not exist (`404`), so the endpoint cannot be used to probe for other
accounts' sessions.

**Which session survives `revoke-others`** is determined by the hash of the
bearer token on the request, never by an id the client supplies. Otherwise the
call could be inverted into "keep the attacker's session and drop the owner's".

**Session-only.** All three routes require an interactive session; an API token
is refused with `403` regardless of its abilities
([api-tokens.md](api-tokens.md) §1). A leaked automation credential must not be
able to sign the operator out of their own panel.

**Expired sessions are not listed.** An expired row is already unusable, so
showing it would invite the operator to "revoke" something that is not a way
in — a false sense of having done something.

## 4. UI

Settings → Account gains a **Sessions** section: one row per live session, the
caller's marked `current`, each with a revoke control, plus a single "Sign out
everywhere else" action that is disabled when there is nothing else to sign
out. It sits directly beneath two-factor authentication, because the two are
what an operator reaches for in the same moment.

## 5. Security properties

- Ownership enforced in SQL; foreign ids are indistinguishable from missing.
- The surviving session in `revoke-others` is identified by the presented
  token's hash, not by client input.
- No token material in any response.
- API tokens cannot reach these routes at all.
- Revoking a session is immediate: the next request on that token resolves no
  user and is `401` (sessions are looked up per request, never cached).

## 6. Out of scope this slice

- Device/IP/user-agent attribution (see §2).
- "Sign out everywhere" including the current device (logout already exists;
  chaining the two is a client concern).
- Forcing re-authentication for sensitive actions (sudo mode) — a broader
  policy layer that would apply to more than sessions.

## 7. Acceptance (testable)

1. The list shows every live session with exactly one marked `current`
   (`TestSessionListAndRevokeOthers`).
2. `revoke-others` leaves the caller signed in and every other session `401`
   (same test, plus `TestRevokeOtherSessionsKeepsCaller` at the service level).
3. One account cannot revoke another's session
   (`TestRevokeSessionRequiresOwnership`).
4. An API token is refused on all three routes
   (`TestCredentialRoutesRejectAPITokens`).

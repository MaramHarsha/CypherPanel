# Feature spec: First-run setup

> A freshly booted panel with no account used to be a dead end: the login
> screen asked for credentials that did not exist, and the only way in was to
> restart `cypherd` with `CYPHERD_ADMIN_EMAIL`/`PASSWORD` set. This adds an
> in-browser first-run setup — visit the panel, create the owner account, and
> you are signed in — so a self-hoster never has to touch env vars to get
> started.
>
> Written 2026-07-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Beginner-first per
> [ui-principles.md §11](../product/ui-principles.md).

## 1. The idea

The panel has exactly one bootstrapping question: *does an account exist yet?*
If not, the first person to reach it should be able to become the owner. This
is the same model Coolify and Dokploy use (a registration screen on first
run), with one hard rule that keeps it safe: **setup is one-time** — the moment
any account exists, the public setup path closes forever.

The env-var bootstrap (`CYPHERD_ADMIN_EMAIL`/`PASSWORD`) stays for automated
and headless installs. Both paths create the *same* thing — an owner in the
default team — through one code path (`onboarding.CreateFirstOwner`), so a
box provisioned by env vars and a box set up in the browser are
indistinguishable afterwards.

## 2. Ownership of "make the first owner"

`core/onboarding` is the single home for it. `bootstrapAdmin` in `cypherd`
main and the REST setup handler both call it — no duplicated "create user +
default team + membership" logic to drift (the previous bootstrap open-coded
it; that duplication is removed here).

## 3. Setup status — `GET /api/v1/auth/setup`

Public (no auth — there is no account to authenticate as). Returns:

```json
{ "needs_setup": true }
```

`true` only when the panel has zero users. The UI calls this before rendering
`/login`: `needs_setup` → show the setup screen; otherwise → show login. It
reveals nothing sensitive (only whether the panel is brand new).

## 4. Create the first owner — `POST /api/v1/auth/setup`

Public and **one-time**. Body `{email, password}`:

1. Validate: a plausible email, password ≥ 8 chars → 400 otherwise.
2. **Gate:** if any account already exists → `409 Conflict` ("already set up —
   sign in instead"). This is the invariant that makes the endpoint safe to
   expose without auth.
3. Create the owner + ensure the default team + enroll the owner in it.
4. Mint a session (`Authenticator.StartSession`) and return it exactly like
   login: `201` with `{token, user}` — so the browser is signed straight in
   and lands on the golden path (no servers → join your first server, §11).

A tiny race (two setups reading zero users at once) is benign: both would
create valid owners. The property that matters — an *already-set-up* panel
refuses — holds unconditionally, because the gate re-reads the count on every
call.

## 5. Security

- **One-time by construction** (§4 step 2): once one account exists, setup is
  a `409` forever. An attacker who reaches a fresh, unclaimed panel first can
  claim it — the same exposure every self-hosted panel with browser
  registration has, and the operator's responsibility to reach their own box
  first (the install docs say so). Env-var bootstrap closes the window
  entirely for those who want it.
- The endpoint mints an owner session, so it is treated as an auth-grade path:
  it goes through the same `StartSession` (session token hashed at rest, TTL
  bounded) as login. No new token machinery.
- Password floor is deliberately modest (8 chars): this is a single-operator
  self-hosted panel, not a consumer service; the operator controls the host
  regardless. Strength meters and rotation are out of scope.
- Setup carries no secret in `GET`; `POST` accepts a password over the same
  TLS the rest of the API uses (the operator terminates TLS in front of the
  panel — [deployment docs](../dev/README.md)).

## 6. UI (web-ui-design.md)

`/login` gates on `GET /auth/setup`. When `needs_setup`, it renders **Create
your admin account** instead of the sign-in form: email + password + confirm,
one primary action ("Create account & sign in"), and a single plain line of
what it does. On success the session is stored and the app navigates to
`/projects`, where the chained empty-state golden path takes over (§11). If
setup returns 409 (someone else claimed it in a race), the screen falls back
to login with a clear message.

## 7. Acceptance (testable)

1. Fresh panel (0 users): `GET /auth/setup` → `needs_setup: true`; the UI shows
   the setup screen.
2. `POST /auth/setup` with a valid email + 8-char password → `201` with a
   working session; the browser is signed in as the owner, in the default team.
3. `GET /auth/setup` afterwards → `needs_setup: false`; the UI shows login.
4. `POST /auth/setup` again → `409`; no second account is created.
5. Env-var bootstrap and browser setup produce the same shape (one owner in
   the default team) — verified by `onboarding` unit tests.

## 8. Out of scope

Multi-step onboarding wizards · email verification · ~~invites~~ (**landed**
as [invitations-and-access-requests.md](invitations-and-access-requests.md); an
invitee chooses their own password against this file's `minPasswordLen`, so no
door into the panel is weaker than another) · password-strength policy and
rotation · SSO/OIDC
first-run (post-v1) · recovery of a lost sole-owner account (operator restores
from the DB / re-bootstraps).

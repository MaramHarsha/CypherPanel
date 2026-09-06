# Feature spec: Team invitations and access requests

> Two halves of one question — *how does a person get into a team?* — that the
> panel has so far answered only from the inside. Today a team admin can add an
> **existing** account by address ([teams-and-roles.md](teams-and-roles.md) §4)
> and nothing else: the invitee must already have been created, with a password
> chosen for them, by a panel admin. And a member who lacks the rank for an
> action has nowhere to ask for more — the 403 screen's "Request access" pill
> opens a `mailto:`.
>
> This slice adds the two missing directions:
>
> - an **Invitation** — a signed, expiring link that lets someone who is not in
>   the panel join one team at one role, choosing their own password;
> - an **Access Request** — a member of a team asking its owners for a higher
>   role, decided in the panel and recorded.
>
> Written 2026-09-05, just before implementing. It closes the "invitations by
> email" non-goal in [teams-and-roles.md](teams-and-roles.md) §7,
> [panel-mail.md](panel-mail.md) §8 and [first-run-setup.md](first-run-setup.md)
> §8, and it is the backend the design screens 13ab (invite modal), 12d/13aw
> (accept landing), 13ah (request access, both halves) and 13q (the 403 that
> names the fix) have been waiting on. Vocabulary per
> [glossary.md](../glossary.md).

## 1. The core idea: two records that grant nothing until they are spent

Both halves are the same shape the panel already uses for a join token and an
email change ([panel-mail.md](panel-mail.md) §3): **a row that describes a
future membership change, and a single atomic statement that spends it.**

An Invitation is a *bearer* permission — whoever holds the link may take it —
so it is short-lived (7 days), single-use, revocable, and only its hash is
stored. An Access Request is the mirror image: it is *not* bearer at all, it
carries no secret, and it grants nothing on its own; it is a durable ask that a
team owner answers with one of two verbs.

Neither adds desired state, a NATS subject, a work item or an agent path. Both
are authorization records in Postgres, read and written by the REST layer.

Three rules shape everything below:

1. **The grant rank is checked when the permission is created, not when it is
   spent.** An admin may invite at member or admin; only an owner may invite an
   owner — the same `requireGrantRank` rule that already governs
   `POST /teams/{id}/members`, lifted into `domain.CanGrantRole` so one
   implementation serves both. Accepting an invite performs no rank check of
   its own, because the person accepting has no rank yet.
2. **An invitation never touches an existing account's credentials.** If the
   invited address already belongs to a user, accepting requires that user's
   *current* password (and their second factor, if they have one) — see §4.
   Anything else would make "invite an address" an account-takeover primitive
   for anyone who can invite.
3. **A refusal that is not the caller's fault is a 404.** A team you cannot see
   does not exist ([teams-and-roles.md](teams-and-roles.md) §3), and an invite
   token that is unknown, expired, revoked or already spent is one
   undifferentiated 404 — the same discipline `auth.ErrInvalidEmailChange`
   already uses, so a guess learns nothing.

## 2. The resource model

```
TeamInvite:     id (inv_), team_id → teams CASCADE, email, role (member|admin|owner),
                token_hash (sha256 of the wire secret), invited_by → users SET NULL,
                invited_by_label (snapshot), expires_at (= created_at + 7d),
                accepted_at, revoked_at, created_at
                partial UNIQUE (team_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL

AccessRequest:  id (acr_), team_id → teams CASCADE, user_id → users CASCADE,
                requested_role, message (≤500), state (pending|granted|denied),
                decided_by → users SET NULL, decided_by_label (snapshot),
                decision_reason, decided_at, created_at
                partial UNIQUE (team_id, user_id) WHERE state = 'pending'
```

Four decisions worth stating:

**The wire token is `inv_….<secret>`, and only `sha256(secret)` is stored.**
The public half of the token is the row's own id, so a lookup is an indexed
primary-key read and the secret is compared in **constant time** before
anything is spent — a wrong guess against a real id can therefore never burn a
valid invite. This is `join_tokens`' shape, because it is `join_tokens`'
problem (ENGINEERING rules 21, 22).

**`invited_by` is `ON DELETE SET NULL`, beside a snapshot label.** The accept
landing says "sam@meridian.dev invited you to meridian studio"; that sentence
must survive Sam's account being deleted, exactly as an audit entry's actor
label does ([audit-log.md](audit-log.md) §2). The foreign key still exists —
unlike an audit row, an invite is live state and a dangling *live* reference is
worth avoiding — but the printable fact is a copy.

**One live invite per (team, address).** The partial unique index says it, and
the create path enforces it by *superseding*: re-inviting an address revokes
the outstanding invite for that team and issues a fresh one. A 409 would leave
an operator stuck behind a link they cannot see and did not send; superseding
also invalidates a link that went to the wrong place, which is the reason
someone re-invites.

**One open request per (team, user).** A second `POST` while one is pending is
a `409` naming the open request rather than a silent duplicate — the owner's
inbox must not fill with the same ask. A *decided* request is history: the
member may ask again, and the pending index does not stop them.

## 3. Authorization

| Route | Requires |
|---|---|
| `POST /teams/{id}/invites` | team **admin**; the invited role ≤ the caller's rank (owner needs owner) |
| `GET /teams/{id}/invites` | team **admin** |
| `DELETE /teams/{id}/invites/{inv}` | team **admin** (revoking is a *reduction*: no extra rank for an owner-role invite) |
| `GET /invites/{token}` | **public** (`security: []`), rate limited by client address |
| `POST /invites/{token}/accept` | **public**, rate limited; credentials still checked in full |
| `POST /teams/{id}/access-requests` | team **member**, asking strictly above their own rank |
| `GET /teams/{id}/access-requests` | team **admin** |
| `POST /access-requests/{id}/grant` | team **owner**, **session-only** |
| `POST /access-requests/{id}/deny` | team **owner**, **session-only** |

**Why grant and deny are `sessionOnly`.** An API token inherits its owner's
role ([api-tokens.md](api-tokens.md)); a `write`-able token belonging to an
owner could otherwise promote an account to owner — durable, panel-wide
privilege from one leaked CI credential. That is the same reasoning that made
deploy approval and the protection `PUT` session-only
([deploy-protection.md](deploy-protection.md) §5, threat-model §5.8). Issuing
and revoking an *invitation* is deliberately **not** session-only: it grants
nothing by itself, it is bounded at 7 days, it is revocable, and scripting team
setup from CI is a legitimate use.

**A non-member gets 404 on every team-scoped route here**, including
`POST /teams/{id}/access-requests`. Requesting access to a team you cannot see
is not a supported flow — it would turn the request collection into a tenancy
probe. The way into a team you are not in is an invitation, which is why both
halves are in one spec.

## 4. Accepting an invitation

`POST /api/v1/invites/{token}/accept {password, display_name?, totp_code?}`
resolves the token, then branches on one fact: does the invited address already
belong to an account?

**Unknown address → create the account.** The password is the invitee's own
choice, validated by the first-run policy (≥ 8 characters — the panel is
self-hosted and the operator owns the box either way, `core/onboarding`). The
new user gets panel role `member`; team rank comes from the invite.
`display_name` is optional and validated by the profile path — one rule for what
a display name may be — and a name that path rejects is *logged and dropped*
rather than failing the join: the invitation has already been spent by then, and
the person can set one from their profile.

**Known address → sign that person in.** `password` must be their **current**
password, and the call goes through `Authenticator.Login` — the same code path
as the sign-in screen, so it inherits per-address and per-account throttling,
the constant-time comparison, and the second-factor requirement. An account
with TOTP enabled therefore answers `401 {"totp_required": true}` until a code
is supplied, and an invitation is never a way around a second factor.

We choose *password login* over *require an active session* deliberately: the
accept route stays uniformly public and single-path, and an invite link opened
on a phone that has never signed in still works — which is the common case the
landing screen (12d/13aw) is drawn for.

Either way the tail is identical and ordered so a crash cannot grant twice:

1. **Spend the invite** — one atomic `UPDATE … SET accepted_at = now() WHERE id
   = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
   RETURNING *`. No row back means someone else spent it first, and the call
   ends in the same 404 an unknown token gets.
2. **Upsert the membership** at the invited role.
3. **Return a `LoginResponse`** — the session token and the user — so the
   landing page lands the person inside the panel rather than at a second form.

Spending first is what makes the route idempotent under a double-submit: the
second request finds nothing to spend. The membership upsert is idempotent by
construction, so a retry after a partial failure converges rather than
duplicating (ENGINEERING rule 12).

`GET /api/v1/invites/{token}` is the same lookup without the spend: it answers
`{team_name, inviter_label, email, role, expires_at, account_exists}` and 404
for anything not currently acceptable. `account_exists` is what lets the screen
say "Choose a password" or "Enter your password" honestly. It is a disclosure
to whoever holds a valid, unexpired invite for *that address* — strictly less
than the team membership the same token already grants.

## 5. Deciding an access request

`grant` applies the role change **through the existing member-role path**
(`teams.Service.ChangeMemberRole`), so this feature inherits the invariants that
already live there rather than restating them: the grant-rank rule, the
last-owner guard, and the store's single upsert. It then marks the request
`granted` with the decider snapshot. `deny` marks it `denied` with an optional
reason (≤500 characters) and changes no membership.

A request whose subject has since left the team is refused with `409` rather
than silently re-adding them: leaving is a decision, and an old ask must not
undo it.

## 6. Telling people: inbox, mail, audit

Four new **inbox kinds** (notification-inbox.md §3's taxonomy, extended):

| Kind | Audience | Written when |
|---|---|---|
| `access.requested` | the team's **owners** | a member asks |
| `access.granted` | the **requester** | an owner grants |
| `access.denied` | the **requester** | an owner denies |
| `invite.accepted` | the **inviter** | someone joins on their link |

These are the inbox's first **team-scoped** items: they belong to a team, not
to a project and not to the panel. `inbox_items` therefore gains a nullable
`team_id` (migration, additive — the panel-level kinds already made
`project_id` nullable). Two consequences matter:

- recipients are resolved from `team_members` narrowed by rank, the same join
  `ListApprovalInboxRecipients` uses, so the rule "never hold an item for a team
  you do not belong to" (notification-inbox.md §4) still holds by construction;
- `DeleteInboxItemsForTeamMember` — already called when a member is removed —
  now also drops that team's team-scoped items, so the rule keeps holding
  *after* someone leaves.

They are inbox kinds **only**, never a notifier or outbound-webhook
subscription: notifications.md §3's taxonomy is fed by terminal *observed
transitions* of resources, and "who is allowed in this team" is governance news
for named people, not an outcome to broadcast to a channel.

**Mail** goes out through **Panel Mail** ([panel-mail.md](panel-mail.md)) when
one is configured, and is skipped silently when it is not: the invitation to
the invitee, the request to the team's owners. It is composed in the service
beside the token that authorises it, so a CLI would send the same words
(CLAUDE.md rule 4), and every interpolated address is the one the panel parsed
and stored — never the string a request supplied (CWE-640, the discipline
panel-mail.md §5 sets). Delivery failure is logged, never fatal: the invite
exists and `accept_url` was already returned.

**`POST /teams/{id}/invites` always returns `accept_url` once.** A panel with no
SMTP is the common self-hosted case, and an invitation nobody can deliver is
worse than a link the operator pastes into Slack themselves. The response also
carries `mail_sent`, so the UI can say which of the two happened.

**Audit** ([audit-log.md](audit-log.md) §3) gains six verbs in the closed
vocabulary — `invite.created`, `invite.revoked`, `invite.accepted`,
`access.requested`, `access.granted`, `access.denied` — each recorded against
the **team**, so a team's timeline answers "who was let in, by whom, when". The
detail carries the address and the role; it never carries the token, and
`audit`'s secret-key stripping refuses a `token` key besides. An accept is
attributed to the account that joined (there is no principal on a public
route). A *valid* token with wrong credentials records one failure row — it
takes a real invite to produce, so it is not an unbounded write surface; an
unknown token records nothing at all, for the reason the throttled sign-in
records one row per episode rather than one per packet.

## 7. API surface (under `/api/v1`)

```
POST   /teams/{id}/invites          {email, role?}      → 201 Invite + accept_url + mail_sent
GET    /teams/{id}/invites          ?state=pending|all  → [Invite]
DELETE /teams/{id}/invites/{inv}                        → 204

GET    /invites/{token}                                 → InvitePreview          (public)
POST   /invites/{token}/accept      {password, display_name?, totp_code?}
                                                        → 200 LoginResponse      (public)

POST   /teams/{id}/access-requests  {requested_role, message?} → 201 AccessRequest
GET    /teams/{id}/access-requests  ?state=pending|all         → [AccessRequest]
POST   /access-requests/{id}/grant                             → 200 AccessRequest
POST   /access-requests/{id}/deny   {reason?}                  → 200 AccessRequest
```

An `Invite` never carries the token or its hash — only
`{id, team_id, email, role, state, invited_by_label, expires_at, created_at,
accepted_at, revoked_at}`, where `state` is `pending | accepted | revoked |
expired`, **derived** from the three timestamps rather than stored (a stored
`expired` would need a sweeper to stay true). An `AccessRequest` carries
`{id, team_id, user_id, user_email, requested_role, current_role, message,
state, decided_by_label, decision_reason, decided_at, created_at}`;
`current_role` is derived at read time so the owner's card can say
"member → admin" without a second call.

Error vocabulary: `400` invalid role/message/password, `401` bad credentials on
accept, `403` insufficient rank (or an API token on a session-only route),
`404` unknown team/invite/token/request **and** non-membership, `409` an
address that is already a member, a second open request, or a subject who has
left, `429` the throttle on the two public routes.

## 8. Security and bounds

- **Token entropy and storage.** The secret is `ids.Secret()` (~130 bits from
  the OS CSPRNG); only its SHA-256 is stored; it appears in exactly two places
  — the mail body and the `accept_url` of the create response — and in no log
  line, no list response, and no audit detail.
- **Single use, enforced by the database**, not by a read-then-write.
- **Expiry is 7 days**, evaluated in SQL (`expires_at > now()`) so the spend and
  the preview cannot disagree with each other or with a skewed process clock.
- **Both public routes are throttled by client address** with the sign-in
  limiter's shape (a failed lookup or a bad accept is a failure; success
  resets), answering `429` with `Retry-After` — the same envelope the login
  screen already counts down against (control-plane-hardening.md §5). A failure
  of *ours* is not a failure of theirs: only `ErrInvalidInvite` costs a strike,
  so a database that is down answers `500` and is logged rather than telling a
  legitimate invitee their link is spent and rate-limiting them for it.
- **The token never reaches the panel's own log.** It travels in a URL — the two
  public routes take it as a path segment, and the mailed link opens a SPA route
  this same binary serves — so the request log would otherwise keep it, and
  `GET /panel/logs` hands that log to a team owner (threat-model §5.8). The
  logging middleware redacts the secret half of an `/invite/…` or
  `/api/v1/invites/…` path, keeping the public invite id so a line stays
  correlatable. It is done in the middleware because a URL is logged before any
  handler decides what it means. (A reverse proxy in front of the panel still
  logs the raw line; that is the operator's own log, and the seven-day
  single-use bound is what limits it.)
- **No enumeration.** Unknown / expired / revoked / spent are one 404;
  `GET /invites/{token}` is the only public read and it reveals nothing an
  address-specific invite holder did not already have.
- **No privilege escalation.** Every rank comparison is server-side against the
  closed role set; an invite cannot exceed its issuer's rank; accepting cannot
  change a role that already exists (the upsert takes the invited role, and a
  person who is *already* a member is refused with 409 rather than silently
  re-ranked — a demotion or promotion belongs to the member-role route, which
  has the last-owner guard).
- **Removing a member revokes the invitations they issued for that team.** An
  invite outlives its issuer's session, but not their membership — otherwise a
  removed admin keeps a 7-day back door in an envelope.
- **Threat model.** This adds one unauthenticated *mutating* route
  (`/invites/{token}/accept`) — the second in the panel, after the GitHub
  webhook — and one unauthenticated read. Both are bearer-token gated,
  throttled and single-use; the accept route can create an account only when
  an authorized admin issued an invite for that exact address.

## 9. Acceptance (testable)

1. A team admin invites `new@example.test` at `admin`; the response carries an
   `accept_url` exactly once, and `GET /teams/{id}/invites` lists the invite
   with `state: pending` and **no** token anywhere in the body.
2. An admin inviting at `owner` is refused `403`; an owner may.
3. `GET /invites/{token}` on that link returns the team name, the inviter
   label, the address and the role; `POST …/accept {password}` creates the
   account, adds the membership at `admin`, and returns a working session.
4. The same accept replayed returns `404`, and the membership is not doubled.
5. A revoked invite, an expired invite and a wrong secret against a real id all
   return `404`, and the wrong secret does **not** consume the valid invite.
6. Re-inviting the same address supersedes: the first link then returns 404 and
   the second works.
7. Accepting for an address that already has an account requires that account's
   current password (wrong one → `401`), and a TOTP-enabled account answers
   `totp_required` until a code is supplied.
8. A team member requests `admin` with a message; the team's owners hold an
   `access.requested` inbox item and the member does not; a second request
   while it is open returns `409`.
9. An owner grants it: the member's role is `admin`, the request reads
   `granted` with the decider's label, the requester holds an `access.granted`
   item, and an `access.granted` audit row exists against the team.
10. `grant`/`deny` with an API token — even the owner's, with every ability —
    return `403`; with a session they succeed.
11. A member cannot list a team's invites or access requests (`403`), and a
    non-member gets `404` on every route in §7.
12. A removed member's team-scoped inbox items are gone, and any invitations
    they issued for that team read `revoked`.
13. After the whole flow, `grep` of the panel's own log finds no invitation
    secret, and `GET /api/v1/audit?team_id=…` carries `invite.created`,
    `invite.accepted`, `access.requested` and `access.granted` — and no token.

## 10. Out of scope this slice

- **A `viewer` role.** The design boards say "viewer" and "developer"; the API
  says `member`, `admin`, `owner`. Adding a fourth, *read-only* rank changes
  every rank comparison in the panel and belongs to granular RBAC (V1.x,
  feature-matrix "Roles / permissions") behind its own ADR — not to a slice
  about how people get in. The screens render the roles that exist.
- **Team-enforced two-factor.** 12d's footnote ("this team enforces two-factor")
  needs a team-level policy the panel does not have; per-account TOTP is
  unchanged and is still honoured on accept.
- **Password reset by email.** Deliberately still absent
  ([panel-mail.md](panel-mail.md) §8, threat-model §5.10): an invitation for a
  *known* address never resets its password, which is precisely what keeps that
  decision intact.
- **Bulk invites, invite-only signup links, domain-based auto-join, seat
  limits, invitation reminders and a mail queue with retries** (panel-mail.md
  §8 keeps the queue).
- **Access requests for a team you are not in** (§3), and requests for
  *panel* roles — the panel role is a panel-owner decision on `/users/{id}`.
- **The UI.** This slice is API-first (CLAUDE.md rule 4); the screens named at
  the top consume it in the web slice. **Release gate:** the mailed link and the
  `accept_url` both point at `/invite/<token>`, a *public* SPA route that does
  not exist yet — until `web/src/routes/invite.$token.tsx` (12d/13aw) lands, the
  panel's own router answers that path with its not-found page, and the
  documented no-SMTP fallback ("a link the operator hands over") dead-ends the
  same way; the only working path is `POST /api/v1/invites/{token}/accept` by
  hand. Ship the landing route in the same release as this backend, or leave
  invitations unadvertised until it does.

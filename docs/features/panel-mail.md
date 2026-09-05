# Feature spec: Panel mail and email changes

> Two halves of one thing. The panel has never been able to send an email in its
> own name — SMTP exists only as per-notifier configuration, owned by a project,
> for telling people about that project's events ([notifications.md](notifications.md)).
> This adds **Panel Mail**: one SMTP transport owned by the panel, for account
> mail the panel itself must send. The first consumer is the second half: an
> **Email Change** flow, which is why `Settings → Profile` still shows the
> sign-in address as read-only.
>
> Written 2026-08-21, just before implementing. Vocabulary per
> [glossary.md](../glossary.md); both terms were added there first (CLAUDE.md
> rule 5). Reuses `core/notify`'s stdlib `net/smtp` sender rather than adding a
> second mail stack — the direction [teams-and-roles.md](teams-and-roles.md) §7
> already recorded.

## 1. Why this shape

Three things in the tree already answer most of the design, so this feature
copies rather than invents:

| Question | Existing answer it copies |
|---|---|
| Where does panel-wide, operator-configured, admin-gated credential state live? | `backup_targets` — panel-scoped rows, sealed keys, `requirePanelRole(admin)` |
| How is "there is exactly one of these" expressed? | `plane_ca` — `id INTEGER PRIMARY KEY DEFAULT 1` with a singleton `CHECK` |
| How is a secret written but never read back? | `notifiers` — sealed `config_ct`/`config_nonce`, API returns a `config_hint` built only from non-secret fields |
| How is a single-use, short-lived token minted and spent? | `join_tokens` — `id.secret` wire form, `HashToken(secret)` at rest, and an atomic `UPDATE … WHERE consumed_at IS NULL AND expires_at > now()` as the race guard |

The one thing with no precedent is trusting a mailbox, which §5 addresses.

## 2. Panel Mail

### 2.1 Storage

A singleton row, because a panel has one outbound identity:

```
panel_mail(
  id            INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  config_ct     BYTEA NOT NULL,
  config_nonce  BYTEA NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

The config is one JSON blob, canonicalised then sealed as a unit with the master
`secret.Box` — the notifier discipline exactly, so a typo cannot be silently
dropped and a partial write cannot half-update a credential:

```json
{ "smtp_host": "", "smtp_port": 587, "username": "", "password": "", "from": "", "tls": "starttls" }
```

`password` is the only secret; the rest are sealed with it because the blob is
sealed as a unit, not because they are sensitive.

**Why the database and not `CYPHERD_*` env config.** Three reasons, in the
repo's own terms: it is operator-configured at runtime and wants a **Test**
button (`backup_targets` and `notifiers` both settled this); it is desired state
the panel reads, not process configuration it boots from (ADR-005); and it is
covered by whatever backs up Postgres, which is the panel's own recovery story.
The cost is that losing `CYPHERD_MASTER_KEY` loses the SMTP password along with
the CA key and every other sealed secret — which is already true of the panel
and is stated in `install/install.sh`.

### 2.2 API

```
GET    /api/v1/panel/mail        → PanelMailSettings              (panel admin)
PUT    /api/v1/panel/mail        → PanelMailSettings              (panel admin)
DELETE /api/v1/panel/mail        → 204                            (panel admin)
POST   /api/v1/panel/mail/test   → 202                            (panel admin)
```

`PanelMailSettings` carries `configured`, `config_hint`, `updated_at`, and —
when something is configured — `smtp_host`, `smtp_port`, `username`, `from` and
`tls`. **Everything except the password is read back.** The first cut returned
only the hint, which made the settings form write-only: changing a port meant
retyping the host, the username and the from address from memory, and an
operator who mistyped one had silently reconfigured the panel. The password is
the one field that never comes back; `configured` plus `config_hint` say that
one is set without saying what it is, which is the notifier contract.

`config_hint` is `smtp.acme.com → ops@acme.com`, built from non-secret fields by
the same helper shape as `notify.ConfigHint`. `PUT` replaces wholesale; there is
no partial-secret merge, for the same reason notifiers refuse one.

**Transport security is chosen, not guessed.** `tls` is `starttls` (the default),
`implicit` or `none`:

| mode | port | behaviour |
|---|---|---|
| `starttls` | 587 | Upgrades the connection and **refuses to send** if the server will not offer STARTTLS. |
| `implicit` | 465 | TLS from the first byte. |
| `none` | any | Cleartext. Only defensible for a relay the operator controls. |

Inferring the mode from the port is what makes a misconfigured panel fail at the
moment it matters. The refusal in `starttls` mode is deliberate: the stdlib's
`smtp.SendMail` upgrades only when the server advertises STARTTLS and otherwise
sends the credential in the clear, which is a downgrade an operator who chose
STARTTLS did not agree to. Settings sealed before this field existed read back as
`starttls`, because that is how they were already being sent.

`POST …/test` sends a fixed message to the configured `from` address and reports
the SMTP error verbatim on failure. It persists nothing — a test that wrote
state would be a second way to configure the panel.

### 2.3 Sending

`core/notify` already owns an SMTP sender; it is unexported and takes a
project-scoped `domain.Notifier`. This adds a narrow exported seam over the same
code — `notify.SendMail(cfg EmailConfig, to []string, subject, body string)` —
and the panel mail service calls it. No new dependency, no second mail stack,
and notifier delivery keeps its existing path.

## 3. Email Change

### 3.1 Flow

1. Signed in, on `Settings → Profile`, the operator opens **Change email** and
   supplies the **new address and their current password**.
2. `POST /api/v1/auth/email/change` (session only) validates, mints an Email
   Change, and mails the confirmation link **to the new address**.
3. It also mails a notice **to the old address** — see §5.
4. The operator opens the link, which lands on `/settings/profile?confirm=<token>`
   in the panel and calls `POST /api/v1/auth/email/confirm` (session only).
5. The address is swapped, the Email Change is consumed, and every **other**
   session is revoked.

### 3.2 Storage

```
email_changes(
  id           TEXT PRIMARY KEY,                          -- ec_…
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  new_email    TEXT NOT NULL,
  token_hash   BYTEA NOT NULL,
  expires_at   TIMESTAMPTZ NOT NULL,
  consumed_at  TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

`join_tokens`' shape, for the same reasons. The wire token is `id + "." + secret`
so the lookup is by public id and only `HashToken(secret)` is stored. TTL is
**30 minutes** — long enough to walk to another device, short enough that a
forwarded link goes stale.

Single-use is enforced by the database, not by application logic:

```sql
UPDATE email_changes SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *
```

The secret is compared with `ConstantTimeEqual` **before** the consume, so a
wrong guess against a real id cannot burn a valid change — the ordering
`core/enroll` is explicit about. Every failure returns one undifferentiated
error.

### 3.3 Reading and abandoning a pending change

```
GET    /api/v1/auth/email/change → PendingEmailChange   (session only)
DELETE /api/v1/auth/email/change → { cancelled }        (session only)
```

`GET` answers `{ new_email, requested_at, expires_at }`, or 404 when nothing is
pending — the ordinary case, and an answer rather than a failure. It exists so
the confirm step can show *old → new* instead of asking someone to trust a link,
and so a profile screen can say when the link dies. It never carries the token:
holding a session is enough to **start or abandon** a move, never to complete
one (§5).

`DELETE` is the "this wasn't me" path. It spends every outstanding change for
the account at once and reports how many died, so `0` ("nothing to undo") is
distinguishable from `1` ("undone"). Killing them all is the point: a second
request made in the same breath must not survive the cancel of the first.

It deliberately does **not** require the current password, where requesting a
change does. The asymmetry is intentional — the person who can undo a move they
did not ask for is the person holding the session, and making them find a
password first only keeps a hijacked request alive longer. The blast radius of
an unauthorised cancel is that a legitimate change must be requested again.

## 4. Authorization

Both routes are `sessionOnly`. Changing the sign-in address is credential
management, and `rest.go`'s rationale applies unchanged: an API token may never
reach these routes, because that is exactly how a leaked token becomes durable
account takeover.

**Confirmation requires the token *and* a live session.** This is the load-bearing
decision of the feature. A mailbox alone cannot move an account, and a session
alone cannot either — the request needed the current password, and the confirm
needs a secret that only ever went to the new address. The cost is that the link
must be opened in a browser signed in to the panel; the confirm screen says so.

## 5. Security

This feature adds mailbox-as-identity to the trust model, which
[threat-model.md](../security/threat-model.md) did not previously contain. A
scenario lands there in the same PR, per that document's own header.

- **A stolen mailbox is not enough.** §4 — the confirm needs a live session too.
- **A stolen session is not enough.** The request needs the current password,
  keeping "possession of a session never weakens a credential" true.
- **The old address is always told.** A change request mails the old address a
  notice naming the new one. It is the only signal the rightful owner gets if
  both the session and the password have already been lost, and it costs nothing
  when the change is legitimate.
- **Other sessions are revoked on success**, on the same reasoning as a password
  change: the address that can recover the account has moved.
- **Rate limiting.** Both routes are brute-force surfaces — the confirm most
  obviously — so both take the same limiters `Login` does, keyed by the client
  address *and* by the account
  ([control-plane-hardening.md](control-plane-hardening.md) §5). A wrong
  current password or a wrong confirmation secret spends the budget, and the
  `429` carries `Retry-After` and `retry_after_seconds`.
- **Enumeration.** Requesting a change to an address that already exists returns
  409. This differs from login, which deliberately hides whether an address
  exists: the caller here is authenticated, rate-limited, and probing one address
  at a time about their own account, and silently doing nothing would be a worse
  failure than the disclosure is a risk. Recorded as a deliberate difference.
- **Secrets never logged.** The SMTP password is sealed at rest, absent from
  every response, and absent from error strings — the cross-cutting rule in
  threat-model §6.
- **No open redirect.** The confirmation link is built from the panel's own
  advertised base URL, never from a request header.

## 6. UI

**`Settings → Mail`** — a new tab, in the shape every other "connection with
credentials" screen uses (`2m` notifiers, `6d` registries, `9l` add connection):
host, port, TLS mode, username, password, from address; the password write-only;
a **Send test email** action; the saved state shown as the masked hint. This
screen is not in the design canvas — it is inferred from those analogues and
flagged as such.

**`Settings → Profile`** — the Email field becomes editable, with the canvas's
own qualifier `· re-verified on change` (card `13i`). It does **not** join the
profile form's dirty/Save path: a sign-in-address change is a re-auth-gated
action, so it opens its own dialog modelled on the change-password dialog
(`9i`), which already has the right shape.

When Panel Mail is not configured, the Change email control says so and points
at the Mail tab rather than failing at submit — a dead end is a bug
(ui-principles §11).

## 7. Acceptance (testable)

1. With no Panel Mail configured, `GET /panel/mail` reports `configured: false`
   and a change request is refused with a message naming the Mail tab.
2. `PUT` then `GET` returns a hint and never the password; a second `GET` after a
   restart still returns the hint (it survives sealing).
3. A panel `member` gets 403 on every `/panel/mail` route.
4. A change request with the wrong current password → 401; with an address
   already in use → 409; with a malformed address → 400.
5. The confirmation token: wrong secret → error and the change is **not**
   consumed; correct secret → applied; replay → error; after 30 minutes → error.
6. On success the address is changed, other sessions are gone, and the old
   address received a notice.
7. Confirming with an API token instead of a session → 403.

## 8. Out of scope

DKIM/SPF verification of the configured sender · Invitations by email · password reset by email (the panel has no anonymous
recovery path, and adding one is a larger decision than this) · per-person
notification digests (the profile page's other placeholder, which this transport
unblocks but does not implement) · DKIM/SPF guidance · a mail queue with retries
— a failed send reports its error and the operator retries.

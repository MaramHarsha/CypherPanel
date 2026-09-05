# Feature spec: Audit log

> Something is gone and nobody knows who removed it. A deploy went out on a
> Friday that was supposed to be frozen. An account has two-factor turned off
> and its owner did not turn it off. Today the panel's answer to all three is a
> `slog` line in a container's stdout, if it was even logged — and the UI has
> been promising better for a while: `ConfirmDestructive` renders *"this is
> audit-logged with your name"* on every destructive dialog, the 404 screen says
> *"the audit log remembers"*, the throttled sign-in screen says *"every failure
> is in the audit log"*, and none of it was true.
>
> The **audit log** makes it true: a first-class, immutable row for every
> sensitive action — who did what to which resource, from where, and whether it
> worked — queryable per team.
>
> Matrix row: **Audit log · V1.x** ([feature-matrix.md](../product/feature-matrix.md)),
> moved to landed by this change set. Written 2026-09-05, just before
> implementing. Vocabulary per [glossary.md](../glossary.md). Prior art:
> Dokploy's `schema/audit-log.ts` — the idea of one flat, queryable table rather
> than per-resource history, and nothing else (CLAUDE.md rule 1).
>
> It discharges four open commitments:
> [threat-model.md](../security/threat-model.md) §5.1 ("full audit log of every
> desired-state mutation, so a compromise is reconstructable"), §5.3 and §8.1
> ("a new server enrollment is an **audited**, surfaced event" — until now one
> `slog` line), [deploy-protection.md](deploy-protection.md) §10, which deferred
> approval and break-glass *decisions* to "a general audit log (V1.x)", and the
> UI copy above.

## 1. The core idea: a ledger, not a history table

An audit entry is **evidence**. Three consequences follow, and between them they
decide the whole design.

**It must outlive what it describes.** The most valuable entry in the log is the
one that says a project was deleted — and a foreign key from `audit_events` to
`projects` would delete exactly that entry along with the project. `ON DELETE
SET NULL` is no better: it would erase the `team_id` that authorizes the read,
so the member who deleted a project would make their own action invisible.
`audit_events` therefore has **no foreign keys at all**. Every id in it is a
snapshot of an identifier that was real when the action happened; a dangling one
means the thing is gone, which is information rather than corruption.

**It must not be rewritten.** Names are copied in at write time and never
re-resolved. Renaming an application does not rewrite its history, and an entry
stays attributable by name after the account that caused it is deleted — which
is the guarantee canvas 14k needs when it says *"audit entries stay"*.

**It must not be writable by its readers.** There is no `POST /audit`. A row is
minted by the handler that performed the action, in the same request, and the
only mutation the table ever sees is the retention purge (§8).

This is also why the feature adds **no path to the agent**: no proto change, no
NATS subject, no reconciler. Every action worth auditing is one an operator
takes against the control plane, and the one exception — a server enrolling —
already arrives at the plane as a gRPC call (§4).

```
handler performs the action
        │
        ├──▶ response to the caller
        │
        └──▶ audit.Record  ──▶  INSERT audit_events
                                   (ownership chain resolved in the same
                                    statement from whichever link is known)
                                          │
GET /api/v1/audit  ◀── visibility ────────┘
   (viewer's teams · panel rows for an admin · always their own actions)
```

## 2. The resource model

One table, `audit_events` (migration `0032_audit_events.sql`), and one domain
type, `domain.AuditEvent`:

| column | meaning |
|---|---|
| `id` | `aud_…` |
| `at` | database `now()` — never the caller's clock, because it is compared against other rows' stamps |
| `action` | the dotted verb (§3) |
| `outcome` | `success` \| `failure` |
| `actor_kind` | `user` \| `token` \| `agent` \| `system` \| `anonymous` |
| `actor_user_id`, `actor_token_id` | snapshots; a token records its OWNER too |
| `actor_label` | the email as it read at the time |
| `resource_kind`, `resource_id`, `resource_name` | what was acted on, with a name snapshot |
| `team_id`, `project_id`, `environment_id` | the ownership chain as it stood; NULL `team_id` = panel-level |
| `detail` | JSONB: identifiers, key NAMES, counts, reasons — never a secret value (§6) |
| `trace_id`, `client_ip` | the provenance the hardening middleware already stamps on the request |

**Actor kinds are a closed set** because "who did this" has exactly five honest
answers:

- **`user`** — a person, in an interactive session.
- **`token`** — a personal access token acting for its owner. The owner is
  recorded as well: a token is a *way to act*, not a separate identity. The
  token id is there so the answer to a leak is "revoke `tok_…`", not "revoke
  something belonging to Priya".
- **`agent`** — an enrolled agent acting for its server (enrollment). Nobody was
  signed in.
- **`system`** — the panel itself: a signed inbound webhook that triggered a
  deploy, a sweeper. Attributable to a mechanism, honestly labelled.
- **`anonymous`** — an unauthenticated caller. This is what a refused sign-in
  is, and its label is the address it *claimed* to be, which is the useful fact
  without ever asserting that address signed in.

**Failure is an outcome, not a verb.** `auth.login` with `outcome: failure` is
the refused sign-in. The alternative — an `auth.login_failed` verb — doubles the
vocabulary and makes "show me everything that was refused" a list of verbs to
remember instead of one predicate. The reason goes in `detail.reason`.

**Six indexes**, and each one answers a question the screens actually ask:
`(at DESC, id DESC)` for the page and the purge, `(team_id, at)`,
`(project_id, at)` and `(actor_user_id, at)` for the three filters that narrow
before they sort, `(resource_id, at)` for one resource's timeline, and
`(action text_pattern_ops, at)` so the `deploy.%` family prefix is an index scan
rather than a sequential one.

For those indexes to be *used*, the `visible` CTE in `ListAuditEvents` is
declared `AS NOT MATERIALIZED`. It is referenced twice — once for the page, once
for the cursor lookup — and from PostgreSQL 12 a multiply-referenced CTE is
materialized by default: the caller's whole visible set would be built, spooled
and top-N sorted before `LIMIT`, on every page, growing with the retention
window, and cheap for any authenticated caller to loop. Measured on 200k rows
(PG16): materialized, a page is a `Seq Scan` over 200,000 rows into a sort,
152 ms; inlined, it is an `Index Only Scan using idx_audit_events_at`, 0.14 ms,
and the cursor lookup is a primary-key scan. The keyword is load-bearing, not
decoration.

## 3. The vocabulary

`core/audit/actions.go` holds the **closed** set of verbs, validated on write:
`Record` refuses an action outside it. A typo'd verb would produce a row that
the very filter built to find it cannot find, and since every call site uses a
constant, the check can only ever fire on a programmer error — which is exactly
when it should be loud.

A verb is `family.past_tense_event`. The families are the nouns an operator
reasons about, and `?action=deploy` matches a whole family by prefix, so a
filter menu can offer three coarse choices without enumerating sixty verbs.

| family | verbs |
|---|---|
| `auth` | `login`, `logout`, `password_changed`, `email_change_requested`, `email_change_confirmed`, `email_change_cancelled`, `totp_enabled`, `totp_disabled`, `session_revoked` |
| `token` | `created`, `revoked` |
| `user` | `created`, `role_changed`, `deleted` |
| `team` | `created`, `renamed`, `deleted`, `member_added`, `member_role_changed`, `member_removed` |
| `server` | `created`, `updated`, `deleted`, `enrolled` |
| `deploy_key` | `created`, `deleted` |
| `project` | `created`, `updated`, `transferred`, `deleted` |
| `environment` | `created`, `renamed`, `deleted`, `template_installed` |
| `application` | `created`, `updated`, `deleted`, `env_var_set`, `env_var_removed` |
| `database` | `created`, `updated`, `deleted`, `stopped`, `started`, `password_reset`, `restore_requested` |
| `deploy` | `started`, `rolled_back` |
| `protection` | `policy_set`, `approved`, `rejected`, `break_glass_opened` |
| `backup_target` / `backup_schedule` / `backup` | `created`, `updated`, `deleted` / `created`, `updated`, `deleted` / `run_requested` |
| `shared_variable` | `created`, `updated`, `deleted` |
| `notifier` / `webhook_endpoint` | `created`, `updated`, `deleted` / `created`, `updated`, `deleted`, `secret_rotated` |
| `panel` | `setup_completed`, `mail_updated`, `mail_deleted`, `dns_updated`, `dns_deleted`, `tls_updated` |

Two deliberate choices in that table:

- **`project.transferred` has its own verb** while a rename is
  `project.updated`. Moving a project between teams moves who can see everything
  inside it — including, from that moment on, the project's own audit trail
  (§5). That is not the same class of event as a rename, and an operator
  reviewing access changes should not have to read details to find it.
- **An env-var change names the APPLICATION, not a synthetic `env_var`
  resource**, with the key in `detail`. `?resource_id=app_…` then returns the
  application's whole story — created, rewired, deployed, deleted — in one
  timeline, which is how the question is actually asked.

`panel.setup_completed` is entry #1 in every panel's log. That is what makes
canvas 15a's empty-state copy — *"the log itself is never empty — first-run
setup is entry #1"* — true rather than aspirational.

## 4. Writing an entry

`core/audit.Service.Record` is the only way a row is minted. It validates the
verb and the outcome, defaults the actor kind, bounds every snapshot string,
sanitises the detail (§6), mints the id and inserts.

The REST layer calls it through one helper, `a.audit(r, audit.Entry{…})`, so a
call site is a single statement after the action it describes. Three decisions
live in that helper:

- **It never fails the request.** The action has already happened. Answering 500
  because the *record* of it could not be written would turn a successful deploy
  into a failed one and invite a retry that does it twice. A failed write is an
  error-level log line carrying the trace id — the loudest thing available that
  does not lie to the caller. (§9 lists the alternative we rejected and why.)
- **It is synchronous.** A detached goroutine would order entries by scheduling
  luck, and "audit-logged with your name" is only true if the row is there when
  the response is.
- **It runs on a context detached from the request's cancellation**
  (`context.WithoutCancel` plus a five-second timeout). A client that hangs up
  the instant its `DELETE` returns must not take the record of that `DELETE`
  with it.

**The ownership chain is completed in SQL, inside the INSERT.** A handler
supplies the most specific link it knows — usually an environment id it already
had — and `InsertAuditEvent` fills in the rest:

```sql
team_id = COALESCE(@team_id,
                   (SELECT team_id FROM projects WHERE id = @project_id),
                   (SELECT p.team_id FROM projects p JOIN environments e … WHERE e.id = @environment_id))
```

Resolving there rather than in Go makes the snapshot atomic with the write:
there is no window in which a project could move to another team between the
lookup and the insert. The exception is a handler that **destroys its own
chain** — deleting a project, a team or an environment — which reads the parent
first and passes `TeamID` explicitly. Without that, the entry recording a
project deletion would lose its `team_id` and become invisible to precisely the
person who made it.

The same obligation applies to an action taken *near* a resource rather than to
it, where no id in the request is a link in the chain. An approval decision is
addressed by deployment id, and a backup schedule by schedule id; both handlers
therefore resolve and pass the chain they already had to compute for
authorization (the deployment's `project_id`, the database's `environment_id`).
Skipping it is not a missing nicety: `team_id` resolves to `NULL`, the row
becomes **panel-scoped**, and the team that owns the deploy cannot read who
approved it while every panel admin can — the exact inversion §5 exists to
prevent. `TestProtectionDecisionsAreScopedToTheirProject` and
`TestBackupScheduleActionsAreScopedToTheDatabasesTeam` assert the in-team read,
and `TestAPanelAdminOutsideTheTeamCannotReadItsDecisions` the other side.

**Preview environments** are the other automated writer. A preview environment
is created by a pull request and destroyed by a close or the TTL sweeper, with
no operator in the loop, so `core/previews` takes a consumer-defined
`AuditRecorder` (`previews.WithAudit`) and emits `environment.created` and
`environment.deleted` with a `system` actor labelled *preview automation*,
carrying the PR number, the preview id and the reason it went away (*pull
request closed* / *ttl expired*). The teardown reads the environment **before**
deleting it, because it destroys its own chain. The manual `DELETE
/previews/{id}` is audited by its handler instead, so that row carries the
operator's name, trace id and address. Without this, the two environment verbs
would have been true only of the environments a person made by hand.

**Agent enrollment** is the one writer outside `core/api/rest`. `EnrollmentServer`
takes a consumer-defined `AuditRecorder` and writes `server.enrolled` with an
`agent` actor labelled by the hostname and the peer address as `client_ip`. Read
next to the `server.created` row the operator produced when they minted the join
token, the two are the whole story of a host joining the fleet — which is what
threat-model §5.3 asks for and what a bare log line never gave.

## 5. Reading it: scope IS the authorization

Neither read route carries a role gate. `core/audit` resolves what the caller
may see from **their own record** — never from anything in the request — and
every query parameter narrows *inside* that:

- a **panel owner** sees every row (the same superadmin bypass
  `teams.RoleInTeam` already grants — one answer to "who is a superadmin", not
  two);
- **anyone** sees the rows of the teams they belong to;
- a **panel admin** additionally sees **panel-scoped** rows — a server, a user,
  the mail/DNS/TLS settings, which belong to no team. Those are the actions that
  role performs, so it is the role that must be able to review them;
- **everyone** sees their **own** actions, whatever scope those landed in, so
  "what did I do?" never depends on still being in the team.

The predicate lives in one place, `ListAuditEvents`' `visible` CTE, and
`Service.Get` mirrors it for a single row so the two can never disagree.

**A filter cannot widen visibility.** `?team_id=` for a team the caller does not
belong to returns an **empty page**, not 403 — a refusal would confirm the team
exists. `GET /audit/{id}` for an entry outside scope returns **404**, the same
answer as one that never existed. Same posture as every project-scoped route in
[teams-and-roles.md](teams-and-roles.md) §3.

The cursor is resolved against the visible set too: `?before=` naming an entry
the caller cannot see yields an empty page rather than silently restarting at the
newest row.

## 6. Security & bounds

**No secret values, ever.** `detail` carries identifiers, key *names*, counts and
reasons. Every call site was written to pass only those — an env-var change
records `{"key": "DATABASE_URL"}`, a token creation records its abilities and
not its secret, a mail settings change records the host and not the password.
As a second line of defence `Record` strips a small denylist of whole key names
(`value`, `password`, `secret`, `plaintext`, `token`, `private_key`, `api_key`,
`passphrase`, `credential`) and logs a warning, so a future call site cannot
quietly make the audit log the one place a sealed value appears in plaintext.
Matching is on the **whole key**: `key` is the name of an env var and belongs in
the detail; `value` is its content and never does.

**Bounds.** An audit row records an action; it is not a place to put data. Each
detail string is truncated to 1 KiB, the encoded detail is capped at 4 KiB (over
that the map is replaced with `{"detail_dropped": "too large"}` and the row is
still written — the *who* matters more than the *extras*), the actor label is
capped at 320 bytes (an email at its RFC maximum) and a resource name at 200. A
truncated string keeps a `…` so a clipped name never reads as the whole one.

The cut lands on a **rune boundary**, and the ellipsis's three bytes are
budgeted inside the cap. This is a security property, not tidiness: snapshots
are caller-supplied — the address typed at a failed sign-in, a webhook URL —
Postgres refuses a string that is not valid UTF-8 (`invalid byte sequence for
encoding "UTF8"`), and a refused `INSERT` is a *missing* entry, because the
write helper only logs its failures. A byte-boundary cut would therefore have
let an unauthenticated caller keep their own failed sign-ins out of the log by
sending an over-long multibyte "email". Invalid UTF-8 that arrives from any
other source is coerced rather than passed on, for the same reason.
(`TestTruncateBounds`, `TestRecordTruncatesOnRuneBoundaries`,
`TestStoreAuditRoundtripsMultibyteSnapshots`.)

**A refusal that never reaches the database is recorded once per episode.** A
throttled sign-in is turned away by the in-memory limiter without a query, so
auditing every refused packet would let an anonymous caller drive unbounded
durable writes at their own request rate — the very work the login throttle
exists to bound (control-plane-hardening.md §5). `Limiter.Refuse` reports the
*transition* into a throttle episode, and only that attempt is recorded; the
episode reopens once the key is allowed again. Canvas 13t needs the throttling
to be visible, not counted.

**Personal data.** The log holds email addresses — including, for a refused
sign-in, an address supplied by whoever failed to sign in — and client IP
addresses, both visible to team members through the API. That is the point of an
audit log, and it is the reason retention has a default rather than being
unbounded (§8): `CYPHERD_AUDIT_RETENTION` is also the panel's erasure horizon.
The label is a snapshot and is never re-resolved, so deleting an account removes
it from the panel but not from the history of what it did — deliberately, since
the alternative is that deleting an account erases the evidence of its actions.

**Nothing here weakens an existing check.** The log records what happened; it
never decides whether it was allowed. Authorization stays where the action is.

## 7. API surface (under `/api/v1`)

```
GET /audit?team_id&project_id&resource_id&action&actor&outcome&since&before&limit
                                              → { events: [AuditEvent], next_before }
GET /audit/{id}                               → AuditEvent | 404
```

`AuditEvent` is `{id, at, action, outcome, actor{kind,user_id,token_id,label},
resource{kind,id,name}, team_id, project_id, environment_id, detail, trace_id,
client_ip}`.

- **`action`** matches an exact verb or a whole family by prefix.
- **`actor`** matches a user id **or** the email label recorded at the time, so
  `priya@example.com` works without first resolving it to a `usr_…`.
- **`since`** is an RFC 3339 lower bound — what canvas 15a's *"Widen to 7 days"*
  sets.
- **`before`** is a seek cursor on `(at, id)` descending; `limit` defaults to 50
  and is capped at 200. A full page always returns `next_before`, so the last
  page of an exactly-divisible log costs one extra request that comes back
  empty — cheaper than counting the whole log on every request.

There is **no write route**, and no route to delete or amend an entry. Retention
is the only thing that removes one.

## 8. Retention

`CYPHERD_AUDIT_RETENTION` (default `2160h` — 90 days) is how long entries are
kept. `0` keeps them forever, which is the right answer for an operator with a
compliance requirement and the wrong default for a panel nobody prunes.

One owned goroutine, started by the wiring beside the session purge, sweeps
hourly and once at boot — a panel restarted more often than the interval would
otherwise never purge at all. Each sweep deletes in **bounded batches of 1000**
oldest-first, looping until a short batch comes back, so a long backlog drains
in steps instead of taking one lock over the whole table; a cancelled context
ends the drain without reporting a failure, because shutdown is not an error.
With retention disabled `RunRetention` returns immediately rather than ticking
over a cutoff that can never match.

## 9. Alternatives considered

**A middleware that audits every mutating request.** One call site instead of
sixty. Rejected: it can record a method and a path but not a *verb*, a resource
name, or the fact that a `PATCH` was a team transfer — and a log of
`PATCH /api/v1/projects/{id} 200` is a worse access log, not an audit log.

**Failing the request when the audit write fails.** Genuinely tempting for a
security control, and the honest reason not to is ordering: the row is written
*after* the action, so a 500 would report a failure for something that already
happened, and the retry it invites would do it twice. The failure is loud in the
panel's own log (`GET /panel/logs`) with the trace id that ties it to the
request.

**Foreign keys with `ON DELETE SET NULL`.** Discussed and rejected in §1: it
erases the scope that authorizes the read, so the entry recording a deletion
becomes invisible to the person who caused it.

## 10. Screens it serves

- **3g, the audit log page** (sitemap): the list this API backs — filters for
  actor, action family and time window, and a cursor.
- **13p, the 404 screen**: `auditLogHref` has a destination at last, so the
  third action (*"Audit log"*) can be shown, and *"the audit log remembers"* is
  a statement about a real place.
- **13t, the throttled sign-in**: *"every failure is in the audit log"* — a
  throttled attempt is `auth.login` with `outcome: failure` and
  `detail.reason: throttled`, alongside the wrong-password rows that led to it.
  One row per throttle *episode*, not per refused packet (§6).
- **14k, export & leave**: *"audit entries stay"* — guaranteed by §1's no-foreign-keys
  rule, proven by `TestStoreAuditEntriesOutliveTheirResources`.
- **15a, the audit filter-miss empty state**: `?actor=&since=` are the filters
  it clears and widens, and `panel.setup_completed` is why *"the log itself is
  never empty"* holds.
- **Every destructive dialog** (`ConfirmDestructive`): *"audit-logged with your
  name"* is now true for delete, rollback, restore, revoke and rotate.

The web pages themselves are not in this change set (§12); the API and the rows
they need are.

## 11. Acceptance (testable)

1. Delete an application as a signed-in member → `GET /audit?action=application.deleted`
   returns a row naming the caller's email, the application's name, its
   environment, the response's `X-Request-Id` and the client address.
   (`TestDeletingAnApplicationIsAuditedWithTheCallersName`)
2. `PUT /applications/{id}/env/DATABASE_URL` with a value containing `hunter2` →
   the entry's detail is `{"key": "DATABASE_URL"}` and no rendering of the entry
   contains `hunter2`. (`TestEnvVarChangesAreAuditedByKeyNeverByValue`)
3. Sign in with a wrong password → one `auth.login` row, `outcome: failure`,
   `actor.kind: anonymous` labelled with the attempted address, no `actor.user_id`,
   and the password nowhere in it. (`TestFailedSignInIsRecordedAsAFailure`)
4. A member of team A cannot read team B's entries by listing, by id (404), or
   by `?team_id=` (empty page) — and can always read their own actions, even
   ones that landed in team B. (`TestMemberCannotReadAnotherTeamsAuditEvents`,
   `TestMemberAlwaysSeesTheirOwnAuditEvents`)
5. A panel admin sees panel-scoped rows (a server creation); a plain member does
   not; a panel owner sees every team's.
   (`TestPanelAdminSeesPanelScopedAuditEvents`, `TestPanelOwnerSeesEveryTeamsAuditEvents`)
6. An action taken with an API token records `actor.kind: token`, the token's id
   **and** its owner. (`TestAnAPITokensActionsNameTheTokenAndItsOwner`)
7. Store round-trip (real Postgres): an entry written with only an
   `environment_id` comes back with the project and team filled in; an entry
   whose application, project and actor account are then deleted is still
   readable, still named, still scoped.
   (`TestStoreAuditResolvesTheOwnershipChain`, `TestStoreAuditEntriesOutliveTheirResources`)
8. `?action=deploy` returns `deploy.started` and `deploy.rolled_back` and not
   `application.created`; `?outcome=failure` returns only refusals; walking
   `before` visits every entry exactly once. (`TestStoreAuditFilters`,
   `TestStoreAuditCursorPaging`, `TestAuditPagesWithACursor`)
9. Retention: with the horizon set, one sweep deletes only rows older than the
   cutoff and does it in batches of at most 1000; with `CYPHERD_AUDIT_RETENTION=0`
   nothing is ever deleted and the goroutine returns immediately.
   (`TestStoreAuditPurge`, `TestPurgeDrainsInBoundedBatches`,
   `TestPurgeIsANoOpWhenRetentionIsDisabled`)
10. A verb outside the vocabulary is refused rather than stored, and every entry
    any handler writes is in the vocabulary. (`TestRecordRefusesAnUnknownAction`,
    `TestEveryRecordedActionIsInTheVocabulary`)
11. An approval or rejection, and a backup schedule or run, is readable by a
    member of the team that owns it and invisible to a panel admin outside that
    team. (`TestProtectionDecisionsAreScopedToTheirProject`,
    `TestBackupScheduleActionsAreScopedToTheDatabasesTeam`,
    `TestAPanelAdminOutsideTheTeamCannotReadItsDecisions`)
12. A snapshot longer than its cap that is not ASCII is stored, valid, and
    within its byte cap — a login refused for an over-long multibyte address
    still produces a row. (`TestTruncateBounds`,
    `TestRecordTruncatesOnRuneBoundaries`, `TestStoreAuditRoundtripsMultibyteSnapshots`)
13. Five refused sign-ins inside one throttle window produce exactly one
    `detail.reason: throttled` row, and a second window produces a second.
    (`TestAThrottledSignInIsRecordedOncePerEpisode`,
    `TestRefuseIsTrueOncePerThrottleEpisode`)
14. A preview environment opened by a PR and torn down by its close or by the
    TTL sweeper produces `environment.created` and `environment.deleted` rows
    with a `system` actor, the reason, and a scope the owning team can read —
    and a failed audit write does not undo the teardown.
    (`TestPreviewLifecycleIsAudited`, `TestSweptPreviewIsAuditedWithItsReason`,
    `TestAFailingAuditWriteDoesNotFailTheTeardown`)

## 12. Out of scope this slice

The **web pages** (canvas 3g, and wiring `auditLogHref` on the 404 screen) —
this is the API-first half, and the UI lands with the design-system work ·
**exporting** the log (CSV/JSON download, or shipping it to a SIEM) ·
**streaming** it to the `/events` SSE channel · **tamper-evidence** beyond "no
write route and no update statement" (a hash chain or an append-only WORM store
is a real answer to a compromised plane, and it needs its own ADR — §5.1's
residual risk is reduced here, not closed) · **deploy cancellation**, which has
no route to audit yet · **scheduled-task CRUD**, whose mutations are not in the
vocabulary this slice defines (the preview lifecycle *is* in: both directions,
from `core/previews`) · **redacting** a deleted
account's label from historical entries (§6 explains why the snapshot stays; an
erasure verb would be a separate, deliberate decision) · **per-user
notification** of audit events — that is the inbox's job
([notification-inbox.md](notification-inbox.md)) · **retention per team** or a
legal-hold flag.

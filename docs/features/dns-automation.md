# Feature spec: DNS automation and domain ownership

> Today a domain is a **string an operator types**. Nothing checks they own it,
> nothing creates the record that makes it resolve, and nothing removes that
> record when the application goes away. `GET /applications/{id}/domain-check`
> reports what public DNS *currently* says, which is a diagnostic, not a
> control — an operator can type `google.com`, we will happily ask Let's Encrypt
> for a certificate, and the only thing that stops them is that the ACME
> challenge fails.
>
> This adds a **DNS Provider** — one Cloudflare connection owned by the panel —
> and makes two things true. A domain is only routed if it falls inside a
> **Zone** that connection can see, which is ownership proof by possession of
> the API token. And the **DNS Record** that makes it resolve is desired state
> the panel creates, updates and *deletes* on its own, so a deleted project
> leaves nothing behind in Cloudflare.
>
> Written 2026-08-21, just before implementing (CLAUDE.md rule 7). Vocabulary
> per [glossary.md](../glossary.md), which gains **DNS Provider**, **Zone** and
> **DNS Record** in the same PR. Builds on
> [routing-and-tls.md](routing-and-tls.md) (the route this makes resolvable) and
> reuses the sealed-credential discipline of [panel-mail.md](panel-mail.md).
> [feature-matrix.md](../product/feature-matrix.md) already carries the row
> "Cloudflare DNS automation (auto-create records on domain add)"; this spec
> **promotes it V1.x → V1** and corrects its Dokploy column (see §1.1).
>
> **Prior art, concept only** (CLAUDE.md rule 1 — never code). Dokploy has the
> closest thing: `utils/dns/{cloudflare,route53}.ts` behind a `DnsClient`
> interface (`listZones` / `listRecords` / `upsertRecord` / `updateRecord` /
> `deleteRecord`), which is good evidence that a two-provider seam is the right
> shape and is why `kind` exists here from day one. Two things we pointedly do
> **not** port: their DNS provider is never wired to a domain, so records are
> operator-driven CRUD with no lifecycle — the gap this feature exists to close
> — and their config masking round-trips a `"********"` sentinel back into a
> merge, where our discipline is write-only with no partial-secret merge
> (§3.1, the reason `notifiers` refuses one). Coolify has no equivalent: its
> Cloudflare support is Tunnel/`cloudflared` only.

## 1. Why this shape

Four decisions were taken before writing, and they are what the rest of this
document elaborates. They are recorded here so the reasoning is not
re-litigated in implementation work.

| Decision | Why |
|---|---|
| **One panel-wide connection**, not per-team or per-project | A Cloudflare account is an operator-level asset: you own the domain, not your team. It matches `panel_mail` and `plane_ca` — panel-scoped, `requirePanelRole(admin)`, sealed. §7 records the tenancy consequence honestly rather than hiding it. |
| **Verification gates routing, and only when a provider is connected** | An install with no DNS Provider behaves exactly as it does today; nothing breaks for an operator managing DNS in Route53 or by hand. Connect one, and a domain outside your zones stops being routable. |
| **DNS-only records (grey cloud)** | The proxied orange-cloud path puts Cloudflare's edge certificate in front of Traefik's and changes what the origin must serve. Grey cloud leaves [routing-and-tls.md](routing-and-tls.md)'s Let's Encrypt HTTP-01 story working **unchanged**, which is the whole reason it is the default. Proxying is §10. |
| **Records are desired state with tombstones**, not fire-and-forget API calls | `research/coolify.md` already records the lesson: *"previews leak unless lifecycle is owned by state, not events"*. A record created by an event and deleted by another event leaks on every missed event. §4 is how deletion is made reliable. |

### 1.1 What the matrix said, and what is true

The existing row read `❌ | ❌`. Coolify's column is right — its Cloudflare
integration is Tunnel only. Dokploy's was not: it *does* ship a Cloudflare and
Route53 client, just never connected to a domain's lifecycle. The row is now
`⚠️ (manual only)` with that evidence, because a matrix that overstates our
lead is worth less than one that is accurate.

**Why the control plane makes these calls, not the agent.** Cloudflare is an
API the *plane* has credentials for, and the record's content is a fact about
routing that the plane already owns. This is the same egress posture
[threat-model.md §5.11](../security/threat-model.md) was just written for, and
it adds no new path to the agent — ADR-001's "the control plane never talks to
a registry" is about image distribution, not about the plane's own integrations
(`core/notify` and `core/webhooks` already make outbound calls).

## 2. Vocabulary

- **DNS Provider** — the panel's connection to a DNS operator. Exactly one, and
  `cloudflare` is the only kind at v1. The `kind` column exists so a second
  provider is a new implementation of one interface, not a schema change.
- **Zone** — a domain the connected provider is authoritative for
  (`example.com`). Cloudflare's own term. Zones are **cached from the
  provider**, never operator-entered: an operator-entered zone list would be a
  second place to lie about ownership.
- **DNS Record** — one record CypherPanel manages, and therefore owns. The
  panel only ever touches records it created; a record an operator made by hand
  is never modified or deleted (§4.4).

## 3. Storage

### 3.1 The provider

A singleton, on `panel_mail`'s exact shape:

```
dns_providers(
  id            INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  kind          TEXT NOT NULL DEFAULT 'cloudflare',
  config_ct     BYTEA NOT NULL,
  config_nonce  BYTEA NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

The sealed config is `{ "api_token": "", "account_id": "", "account_name": "" }`.
The token is the only secret; the account is sealed with it because the blob is
sealed as a unit. `GET` reports `configured`, the account, and a `config_hint`
naming the zones — never the token, the `notifiers` discipline.

### 3.1.1 Which token, and why the account matters

Cloudflare has **two kinds of API token**, and the difference is not cosmetic:

| | User-owned | **Account-owned** |
|---|---|---|
| Made at | My Profile → API Tokens | Manage Account → API Tokens |
| Belongs to | a person | the account |
| Survives that person leaving | ❌ | ✅ |
| Verifies at | `/user/tokens/verify` | `/accounts/{id}/tokens/verify` |

[Cloudflare recommends account-owned tokens](https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/)
for durable integrations that "act as service principals with their own specific
set of permissions" — which is exactly what a panel is. A user-owned token dies
with the person who made it, and the failure mode is every domain silently
un-verifying months later.

So the config carries an optional **account id**, and it changes two calls:

- zones are listed as `GET /zones?account.id=<id>` — Cloudflare's filter is
  dotted, `account.id`, not `account_id`;
- the token verifies under its account rather than under `/user`. Calling the
  wrong one reports a perfectly good credential as broken.

**The operator does not have to find that id.** On save the panel calls
`GET /accounts`: exactly one → adopt it; several → return 400 **with the
choices**, and the UI offers a picker; none, or no permission to ask → proceed
unscoped, which is the correct behaviour for a user-owned token. An id the
token cannot see is refused by name, because the alternative is an empty zone
list with no explanation.

**How requests are built.** Every call is assembled from path *segments* and
query *values* against a parsed base URL, never by string formatting. Each
interpolated segment must be an identifier — `URL.JoinPath` resolves `..`
rather than escaping it, so an account id of `../../..` would otherwise pick a
different Cloudflare endpoint to send the panel's token to. A final check
refuses any request that would leave the pinned host.

**Permissions.** `Zone → Zone → Read` and `Zone → DNS → Edit`, with *Zone
Resources* narrowed to the zones CypherPanel should manage. The panel validates
on save (§5.1) and refuses a token that cannot list zones — a credential that
fails at first use is a dead end, and a dead end is a bug (ui-principles §11).

### 3.2 Zones

```
dns_zones(
  id            TEXT PRIMARY KEY,               -- dnz_…
  provider_zone_id TEXT NOT NULL,               -- Cloudflare's zone id
  name          TEXT NOT NULL UNIQUE,           -- example.com
  refreshed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

A **cache**, refreshed on save, on demand, and by the sweeper. It is never the
authority: verification failures always re-check against the provider before
refusing, so a zone added in Cloudflare five seconds ago does not need a manual
refresh to work.

**Activation is not ownership**, and conflating the two was this feature's first
real-account bug. Cloudflare zones are `initializing`, `pending`, `active` or
`moved`; a zone is `pending` from the moment you add the domain until its
nameservers are repointed at Cloudflare, which can be days. The first cut listed
zones with `status=active`, so an operator who had just added their domain was
told the panel **could see no zones at all** — while the domain sat plainly
visible in their Cloudflare dashboard.

So the status is *stored and reported*, never used as a filter. A zone you own
verifies a domain whatever its activation state. Whether the domain **resolves**
is a different fact, and the UI says which one you are looking at (§6). Only
`moved` is dropped, because that zone has genuinely left this provider.

### 3.3 Records

```
dns_records(
  id            TEXT PRIMARY KEY,               -- dnr_…
  application_id TEXT REFERENCES applications(id) ON DELETE SET NULL,
  zone_id       TEXT NOT NULL REFERENCES dns_zones(id),
  name          TEXT NOT NULL,                  -- app.example.com, the FQDN
  type          TEXT NOT NULL,                  -- 'A' at v1
  content       TEXT NOT NULL,                  -- the server's public address
  desired       TEXT NOT NULL,                  -- 'present' | 'absent'
  provider_record_id TEXT,                      -- set once observed created
  last_error    TEXT NOT NULL DEFAULT '',
  attempt       INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (zone_id, name, type)
)
```

**`ON DELETE SET NULL`, deliberately, and it is the load-bearing choice in this
document.** A cascade would delete the row the moment the application is
deleted — and with it the only record of what has to be removed from
Cloudflare, which is exactly how the orphan leaks. Instead the application
going away sets `application_id = NULL` and `desired = 'absent'`, and the row
survives as a **tombstone** until the reconciler has confirmed the record is
gone from the provider. Only then is the row deleted.

**`desired` is the whole state machine.** There is no `status` column: whether
a record exists is *observed* (`provider_record_id` is non-NULL), and what
should be true is `desired`. That is ADR-005 applied to a third-party API
instead of an agent.

### 3.4 The server's public address

```
ALTER TABLE servers ADD COLUMN public_address TEXT NOT NULL DEFAULT '';
```

The A record has to point somewhere, and the plane does not know a server's
public IP: the agent dials out (ADR-002), the heartbeat carries no address, and
a source address seen through NAT is not necessarily the address the internet
reaches. So the operator supplies it, on the server, once.

This is honest rather than clever. An operator who knows their VPS IP types it;
one behind a load balancer types the load balancer's. An application whose
server has no public address is reported as such — a named, actionable state,
not a silent failure (§6).

## 4. Behaviour

### 4.1 Verification

When a route's domain is set (`PATCH /applications/{id}`), the plane resolves
it against the zone list, **longest suffix wins**: `api.staging.example.com`
matches zone `staging.example.com` over `example.com` if both are present.

- No DNS Provider configured → **unverified, and not enforced**. Today's
  behaviour exactly: the domain is stored and routed, and the UI says nothing
  new. This is what keeps existing installs working.
- Provider configured, domain inside a zone → **verified**. A DNS Record is
  desired.
- Provider configured, domain outside every zone → **stored, unverified, and
  not routed**. The UI shows *verification pending in Cloudflare*, naming the
  zones that *are* connected, because "not verified" without "here is what you
  do have" is a dead end.

Verification is **derived, never stored as a flag**: `domain_verified` is
computed from the domain and the current zone list on every read. A stored flag
would go stale the moment a zone is added or the token is revoked, and a stale
security decision is worse than a recomputed one.

### 4.2 Routing is gated on it

`Scheduler.buildSpec` omits the router rule for an unverified domain, so the
application deploys and runs but is not published at that hostname. This is the
single point where "the domain will not work" is enforced, and it is enforced
in desired state rather than by refusing the write — which means fixing the
zone in Cloudflare and re-deploying is all it takes, with no data re-entry.

**Certificates follow routing.** No router rule means Traefik never requests a
certificate for that host, so an unverified domain cannot burn Let's Encrypt
rate limit on a name we cannot prove.

### 4.3 Record lifecycle

| Event | Effect |
|---|---|
| Domain set on an app, verified, server has a public address | A `dns_records` row, `desired = 'present'` |
| Domain changed | Old row → `desired = 'absent'`; new row → `desired = 'present'` |
| Domain cleared | Row → `desired = 'absent'` |
| Server's public address changed | Row's `content` updated; reconciler PATCHes the record |
| Application deleted | `application_id = NULL` (the FK), then reaped — see below |
| **Project or environment deleted** | The same, by the same one rule |
| DNS Provider disconnected | Records are **left alone**, not deleted — see §4.5 |

**Deletion is one rule, not three.** `application_id` is `ON DELETE SET NULL`,
and environments and projects cascade to applications — so deleting an
application, an environment, or a whole project all leave the *same* trace: a
record with no application. A record with no application has no reason to
exist, so the reconciler's first act on every tick is:

```sql
UPDATE dns_records SET desired = 'absent'
WHERE application_id IS NULL AND desired = 'present'
```

That is why deletion is reliable. It does not depend on a hook having fired for
the right entity, which is exactly the failure mode `research/coolify.md`
records ("previews leak unless lifecycle is owned by state, not events"). The
explicit per-entity tombstones still exist, because they make the common case
immediate rather than waiting for a tick — but correctness does not rest on
them.

### 4.4 The reconciler

`RunDNSSweeper`, beside `RunBackupSweeper` and `webhooks.RunRetrySweeper`, on
the same shape:

1. Take rows that are out of sync — `desired = 'present'` with no
   `provider_record_id`, `desired = 'absent'` with one, or a `content` that
   differs from what the provider last returned.
2. Converge each: create, update, or delete at Cloudflare.
3. On success: record `provider_record_id`, or delete the tombstone row.
4. On failure: bounded backoff in `next_attempt_at`, `last_error` set, the row
   stays out of sync and is retried. Errors are surfaced per application, never
   swallowed.

**Idempotence** (ENGINEERING rule 12). Creating a record that already exists is
not an error: Cloudflare's "record already exists" is resolved by looking the
record up by `(zone, name, type)` and adopting its id **only if its content is
ours**. Deleting a record that is already gone is a success.

**We only ever touch what we created.** A record that exists at Cloudflare but
has no `dns_records` row is never modified or deleted, however tempting.
Adoption on conflict is the one exception, and it is narrow: same zone, same
name, same type, same content. An operator's hand-made record with different
content produces a named conflict the operator has to resolve, because silently
overwriting someone's DNS is not a thing this panel does.

### 4.5 Disconnecting the provider

Deleting the DNS Provider does **not** delete records from Cloudflare. The
token is what proves ownership; removing it removes our ability to act, not our
obligation to be careful. Records are left exactly as they are, tombstones stop
converging, and the UI says how many records are now unmanaged. An operator who
wants them gone deletes the applications first, or removes them in Cloudflare.

Deleting the provider **does** immediately unverify every domain, which stops
routing at those hostnames. That is the correct blast radius for "the thing
that proved ownership is gone", and the UI warns with the count before the
operator confirms.

## 5. API

```
GET    /api/v1/panel/dns            → { configured, kind, config_hint, zone_count, account_id, account_name }
PUT    /api/v1/panel/dns            → the same, or 400 + { accounts } to choose one   (panel admin)
DELETE /api/v1/panel/dns            → 204                                             (panel admin)
POST   /api/v1/panel/dns/test       → 202                                             (panel admin)
GET    /api/v1/panel/dns/zones      → [ { id, name, refreshed_at } ]                  (panel admin)
POST   /api/v1/panel/dns/zones/refresh → [ … ]                                        (panel admin)

GET    /api/v1/applications/{id}/dns → { verified, zone, record, state, last_error }  (project member)
PATCH  /api/v1/servers/{id}          → { public_address }                             (panel admin)
```

The raw token is never returned by any route. `PUT` replaces wholesale; there
is no partial-secret merge, for the same reason notifiers refuse one.

`GET …/applications/{id}/dns` is the one project-scoped route: a member can see
whether *their* application's domain is verified and what the record is doing,
without being able to see the token, the other zones, or another project's
records.

### 5.1 Validation on save

`PUT /panel/dns` resolves the account (§3.1.1) and calls Cloudflare's zone list
before writing anything. A token that cannot list zones is refused with
Cloudflare's own message — the operator needs to know whether it is a bad token
or a missing permission, and paraphrasing it would make them guess. Nothing is
persisted on any failure.

Cloudflare answers a **malformed** token with HTTP 400, not 401, so classifying
on status alone reports a credential problem as an unreachable API and sends the
operator to look at their network. The client classifies on Cloudflare's own
error codes (6003, 6111, 9109, 10000) at any depth of the `error_chain`, and
surfaces the chained reason — "Invalid format for Authorization header" tells
them what to fix; "Invalid request headers" does not.

## 6. UI

**`Settings → DNS`** — a new tab beside Mail, in the same shape every
"connection with credentials" screen uses: the token write-only, a **Test
connection** action, the saved state as a masked hint, and the zone list
underneath so the operator can see exactly what the panel believes it can
manage. A **Refresh zones** action, because a zone added in Cloudflare should
be usable without waiting for a sweep.

**Application → Settings → Domain** — the domain field gains a verification
state directly beneath it:

- *Verified · example.com* — with the zone named.
- *Record created, but the zone is pending* — the record exists in Cloudflare
  and the domain still will not resolve until the registrar's nameservers point
  at Cloudflare. Owning the zone and serving traffic are different milestones,
  and saying only "verified" would leave someone waiting for a domain that
  cannot work yet.
- *Verification pending in Cloudflare* — with the connected zones listed and a
  link to the DNS tab. This is the state the operator sees when they type a
  domain they have not added to Cloudflare.
- *This server has no public address* — with a link to the server, when the
  domain is verified but the record cannot be written.
- *Record error* — Cloudflare's message, verbatim, with the next retry time.

**Server → Settings** gains **Public address**, with a qualifier explaining what
it is for: *· where DNS records for this server's apps will point*.

Status uses the existing vocabulary and the existing shape language
(ui-principles §5): verified is `running`-green and round, pending is
`degraded`-amber, error is `error`-red and square.

## 7. Security

- **The token is sealed** with the master `secret.Box`, unsealed only to call
  Cloudflare, never logged, never in a response, absent from error strings
  (threat-model §6, ENGINEERING rule 20). A panel compromise already implies
  it; nothing weaker does.
- **A new asset, ranked honestly.** A Cloudflare token with `DNS:Edit` can
  repoint any zone it covers — including MX records, which is mail
  interception. It belongs in threat-model §2's asset table above the SMTP
  credential and below the master key, and a scenario lands in
  [threat-model.md](../security/threat-model.md) in the same PR, per that
  document's own header rule.
- **Scope the token, and say so.** The UI tells the operator to issue a token
  limited to the zones CypherPanel should manage. We cannot enforce it — a
  token's scope is Cloudflare's to police — so the control is documentation
  plus the §4.4 rule that we only touch records we created.
- **Tenancy, stated plainly.** A panel-wide provider means any project member
  who can set a domain can cause a record to be created in *your* zones, under
  a name they choose. Three things bound it: the name must be inside a
  connected zone, the content is always the app's own server address (never
  operator-supplied), and the record is visible and attributable in the UI. It
  is nonetheless a real consequence of the panel-wide choice, and an operator
  running untrusted members should scope the token to a zone they do not mind
  sharing. **If per-team providers are ever added, this is the paragraph that
  motivated them.**
- **No SSRF surface added.** Unlike an outbound webhook the destination is not
  operator-supplied: it is Cloudflare's API base, a constant. The only
  operator-controlled input is a hostname and a token.
- **Enumeration.** `GET …/applications/{id}/dns` names the zone an application
  matched, which a project member can already infer from the domain they typed.
  It does **not** list other zones — that is panel-admin only.

## 8. Acceptance (testable)

1. With no DNS Provider configured, an application with any domain deploys and
   routes exactly as it does today; no route asserts verification.
2. `PUT` then `GET /panel/dns` returns a hint and a zone count, never the
   token; a bad token is refused with Cloudflare's message and persists nothing.
3. A panel `member` gets 403 on every `/panel/dns` route; a project member gets
   200 on `…/applications/{id}/dns` for their own app and 404 for another
   project's.
4. Provider configured, domain inside a zone → verified, and a `dns_records`
   row reaches `provider_record_id` set.
5. Provider configured, domain outside every zone → the app deploys, the router
   rule is **absent**, and the API reports pending.
6. Changing the domain leaves the old record `desired='absent'` and the
   reconciler deletes it from the provider.
7. **Deleting the project deletes every record beneath it from the provider**,
   and the tombstone rows are gone afterwards.
8. Deleting the DNS Provider deletes nothing at Cloudflare and unverifies every
   domain.
9. A record that already exists with our exact content is adopted, not
   duplicated; one with different content is a named conflict.
10. Reconciling is idempotent: running the sweeper twice over the same state
    makes no second call and changes nothing.

## 9. Out of scope this slice

Proxied (orange-cloud) records · DNS-01 challenges and wildcard certificates
(routing-and-tls.md §10 still owns that deferral; the token here is what will
unblock it) · providers other than Cloudflare, though `kind` exists for them ·
CNAME and other record types · per-team or per-project providers (§7) ·
records for managed databases or Compose Stacks (applications only) ·
automatic public-address discovery (§3.4) · importing existing Cloudflare
records into management.

## 10. Later

**Proxying** is the first follow-up and the reason `dns_records` has room for
it: a per-domain toggle, plus the origin-certificate story that comes with it.
**DNS-01** is the second, and it is what makes wildcard certificates possible —
one Cloudflare token serves both, which is why this feature was worth building
before that one.

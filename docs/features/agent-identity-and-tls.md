# Feature spec: Agent identity & TLS

> Three things that were each half-built, and are the same story told at three
> layers: an agent's certificate must renew itself, the panel must own the ACME
> account that lets a node serve HTTPS at all, and the join command must be able
> to complete on a host that has never seen a `cypher-agent` binary.
>
> Written 2026-09-05, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). The decisions this rests on are already made:
> [ADR-002](../adrs/ADR-002-agent-dial-home-no-ssh.md) (dial-home mTLS identity),
> [ADR-004](../adrs/ADR-004-traefik-file-provider.md) (Traefik file provider,
> certificates on the serving node), [ADR-005](../adrs/ADR-005-desired-state-reconciliation.md)
> (everything is desired state), [ADR-010](../adrs/ADR-010-agent-auto-update.md)
> (the plane names agent versions but stores no binaries).

## 1. Why this exists

Three defects, found by audit, that share a shape: something was designed, the
mechanism was built, and the last wire was never connected.

1. **Agent certificates expire and nothing renews them.** `pkg/pki` mints agent
   certificates with `CYPHERD_AGENT_CERT_TTL` (default 90 days).
   `EnrollmentService` has exactly one RPC — `Enroll` — and no code anywhere in
   `agent/`, `core/identity` or `core/api/grpc` mentions renewal. The bus
   requires a valid client certificate (`tls.RequireAndVerifyClientCert`), so on
   day 90 every agent's handshake fails, and it fails in the way the agent reads
   as *revocation* (`agent/cmd/cypher-agent/main.go`: "bus connection closed
   permanently (identity revoked?)"). Recovery is manual, per host: delete the
   state directory, issue a new join token, re-run the installer. The threat
   model has promised the fix since Phase 1 — §5.2 lists "short-lived agent
   certificates with rotation (renew over the authenticated channel)" with an
   open `[Phase 1: cert TTL decision]` tag, and §8 requirement 6 asserts agent
   certificates are "short-lived and revocable". Only the second half is true.

2. **TLS is off by default and the proxy lies about it.** The agent configures
   Traefik's Let's Encrypt resolver only when `CYPHER_ACME_EMAIL` is set on that
   host. Nothing sets it: not the panel's join command, not `install/agent.sh`,
   not the systemd unit it writes. `grep -rn CYPHER_ACME docs install deploy` hit
   one sentence in the README. Meanwhile the fragment writer emitted
   `tls.certResolver: le` for every route with `https: true` — which is the
   default the API and the UI both apply — plus a permanent HTTP→HTTPS redirect.
   So the standard outcome on a fresh install was: visitors permanently
   redirected to `:443`, answered with Traefik's self-signed default certificate,
   while the panel's overview printed "HTTPS ✓ auto-renews" from the `https` flag
   alone. Wrong at every layer, and worse than plain HTTP would have been.

3. **The join command cannot complete on a fresh host.** `install/agent.sh`
   downloaded the agent only when `CYPHER_AGENT_URL` was exported, and otherwise
   dead-ended at `no agent binary found`. The panel's `install_command` did not
   include that variable. `install/install.sh` — the plane's own installer — has
   defaulted `CYPHERD_URL` to the latest GitHub release all along; the agent
   installer never got the same treatment. The roadmap's Phase 1 acceptance
   ("fresh Ubuntu VM joins via `curl | sh` in under 60 seconds") passed only
   because CI injects the variable before pasting the command.

## 2. Scope

- A `Renew` RPC on `EnrollmentService`, an agent-side renewal loop, and an
  atomic on-disk swap (§3).
- A panel-owned ACME account (`GET`/`PUT /api/v1/panel/tls`), carried to every
  node in `DesiredState`, and a proxy that only promises HTTPS when it can keep
  the promise (§4).
- `Application.tls_state`, so the UI can say "serving over HTTP meanwhile"
  truthfully (§5).
- A join command and installer that complete unaided (§6).

Out of scope → §10.

## 3. Certificate renewal

### 3.1 The plane side

`EnrollmentService` gains one RPC, additively (ENGINEERING rule 18):

```proto
rpc Renew(RenewRequest) returns (RenewResponse);
```

It is served on the **existing** enrollment listener, which already accepts
client certificates (`tls.VerifyClientCertIfGiven`) because `ImageRelayService`
shares it. So renewal needs no new port, no new listener, and no new trust
relationship — it is the authenticated channel the agent already holds.

Authorization is the verified peer certificate's CommonName and nothing else.
`RenewRequest.server_id` is a *claim*: the plane refuses the call when it
disagrees with the peer identity, so an agent can never renew another server's
certificate and a mismatch is a loud `PermissionDenied` rather than a silent
re-issue under the caller's real name. An empty claim is accepted (the
certificate still decides), which keeps the field additive.

Three refusals, all `PermissionDenied`:

| Condition | Why |
|---|---|
| No verified client certificate | The caller is not an agent. It may `Enroll`; it may not renew. |
| CN names a server that is gone or never enrolled | **Revocation.** Deleting a server both cuts its bus connection (`certAuth`) and denies it a fresh certificate, so the identity ages out instead of living forever. |
| `server_id` ≠ CN | An agent renews itself and nothing else. |

On success the plane signs the CSR with `CN = <caller's server id>` and the same
`CYPHERD_AGENT_CERT_TTL`, and returns the certificate, the CA, and the new
`not_after`. Nothing is consumed; calling twice issues two valid certificates and
the agent keeps the last one it stored.

**No server-row write, deliberately.** The obvious move — reuse
`MarkServerEnrolled` — re-stamps `enrolled_at`, which the panel renders as
"Enrolled <relative time>". A renewal is not a re-introduction, and a two-year-old
server claiming it joined this morning every sixty days is a lie the operator has
no way to see through. The agent version the request carries needs no write
either: heartbeats already refresh it every interval. The renewal's audit trail
is the plane's log line, which names the server and the new expiry.

**TTL rationale (closing the `[Phase 1: cert TTL decision]` tag).** 90 days is
kept. It is Let's Encrypt's number for the same reason: long enough that a
renewal outage is not an emergency, short enough that a stolen certificate is
not a permanent grant. What was missing was not a shorter TTL but a renewal
path — a shorter TTL without renewal is strictly worse, and with renewal the TTL
mostly stops mattering. Operators who want a tighter window can shorten
`CYPHERD_AGENT_CERT_TTL`; the renewal schedule is a *fraction* of the lifetime
(§3.2), so it shortens with it rather than becoming impossible.

### 3.2 The agent side

`agent/identity` grows three things.

**Expiry awareness.** `identity.NotAfter` / `identity.Lifetime` parse the stored
certificate. The agent logs `cert_not_after` at startup, so "when does this box
stop working" is answerable from a log line rather than from `openssl`.

**A renewal loop** (`identity.Renewer`), one owned goroutine with a defined
lifecycle and an injected clock (rules 7 and 9):

- **Renews at two thirds of the certificate's validity window.** A 90-day
  certificate renews on day 60, leaving 30 days of retries before anything goes
  dark. Expressed as a fraction rather than a fixed offset so shortening the TTL
  shortens the retry window instead of eliminating it.
- **Retries after a quarter of the time actually left**, floored at 30 s and
  capped at an hour. A plane that is down, mid-upgrade or unreachable is the
  expected failure, and for a 90-day certificate this is the hourly retry the
  window deserves (~720 attempts). A *constant* hour is only right for one TTL:
  live verification against a deliberately short `CYPHERD_AGENT_CERT_TTL` showed
  an hour's backoff can exceed the certificate's entire remaining life, so the
  identity would expire while the loop slept. A proportional retry always leaves
  at least three more attempts, whatever the lifetime.
- **Caps a single wait at six hours.** The renewal date can be two months out
  and a host's clock can jump — a VM resumed from a snapshot, an NTP step — so
  the loop re-evaluates rather than trusting one long timer set from a time that
  turned out to be wrong.
- **Generates a fresh key pair every time.** Rotating the certificate over the
  same key would bound how long a *certificate* is valid without bounding how
  long a leaked *key* stays useful, which is most of the point (threat-model
  §5.2). Only the CSR is transmitted, at renewal exactly as at enrollment.
- **Refuses a certificate that is no newer than the one in hand.** Installing it
  would leave the next renewal date in the past and the loop would spin. This is
  reachable only when the whole lifetime is minutes — short enough that the CA's
  five-minute clock-skew backdate dominates it — and the proportional retry above
  makes even that self-heal: the next attempt comes after wall-clock has moved,
  so the re-issue does advance.
- **Never treats failure as fatal.** The certificate in hand is still valid; the
  next attempt may find a plane that has come back.

**An atomic swap.** The identity directory holds `agent-cert.pem` +
`agent-key.pem`, and `identity.json` now *names* the pair in use. A renewal
writes the new pair into the free slot (`agent-cert.1.pem` / `agent-key.1.pem`,
alternating), fsyncs, and then renames `identity.json` — one atomic act that
commits the whole rotation. At every instant, the pair `identity.json` points at
is a complete, matching, present pair. The obvious alternative — write both
files under their existing names — needs two renames with a window between them
where the certificate on disk does not match the key; an agent restarting there
could never build a TLS keypair again, an outage requiring manual re-enrollment
on every affected host. Identities written before this change carry no file
names and default to the original ones, so an in-place upgrade needs no
migration.

### 3.3 Taking effect without a reconnect

**Design decision.** The obvious implementation of "the agent reconnects after
rotation" is to tear down the NATS connection and the gRPC clients and rebuild
them with the new material. We do the opposite, and it is strictly better: the
bus and relay TLS configs are built with `GetClientCertificate` (new:
`pki.ClientTLSConfigFunc`) backed by `identity.Keeper`, so **every handshake
reads whatever certificate is current at that moment**.

The consequences are exactly the ones the reconnect approach was trying to buy,
without its costs:

- The connection in flight keeps its already-negotiated session — nothing is
  dropped, no desired state is re-fetched, no work item is redelivered mid-run,
  no image transfer is interrupted.
- The next connection — nats.go reconnects forever after a plane restart, gRPC
  redials on demand — presents the renewed certificate automatically.
- There is no window in which the agent is disconnected, so there is no failure
  mode where a rotation *causes* an outage. A forced reconnect would have added
  one, in order to solve a problem that only exists at expiry.

Renewal needs the plane's gRPC address, which identities enrolled before
`plane_addr` existed do not carry. `CYPHER_PLANE_ADDR` supplies it; with neither,
the agent logs a warning naming its expiry rather than failing silently two
months later.

## 4. The panel's ACME account

**Decision: the ACME account is desired state, owned by the panel** — not
per-host configuration. One panel, one account, carried to every node inside the
desired set it already reads on connect. Turning TLS on becomes one action in
one place, and a server that joins tomorrow serves HTTPS with no extra step on
the host.

**Resource.** A singleton `panel_tls` row on `panel_mail`'s and
`dns_providers`' exact shape, with one deliberate difference: it is **not
sealed**. An ACME account email is published back by the CA in the account
record and a directory URL is well-known; the secret in ACME is the *account
key*, which Traefik generates and keeps on the serving node and which never
reaches the plane (ADR-004). Sealing public values would buy the illusion of
protection while making them unreadable to the desired-state builder that has to
send them to every agent. The row exists only when TLS is configured — a CHECK
constraint forbids an empty email — so "is there a resolver?" has one answer in
one place instead of two that can disagree.

**API.**

| Route | Auth | Notes |
|---|---|---|
| `GET /api/v1/panel/tls` | panel **owner** | Returns the account as stored, or `configured: false`. |
| `PUT /api/v1/panel/tls` | panel **owner** | Wholesale replacement. An empty `acme_email` clears it. |

Owner, not admin: this decides how every routed application on every server is
served to the public internet, and it registers an account in the operator's
name. There is no `DELETE` — "no email" and "no ACME" are the same statement, so
a second way to say it would be a second thing to keep consistent.

**Propagation.** `DesiredState` gains an additive `TLSSettings tls = 3`. A
change would otherwise reach an agent only on its next reconnect, which can be
days, so `PUT` also publishes a new `ResyncWork` item on `work.<server>.resync`
to every enrolled server: *re-read your desired state*. It carries no state —
the agent fetches the authoritative set, which is exactly what it does on
connect — so it is idempotent by construction (rule 12) and a redelivered nudge
costs one request and changes nothing. The nudge is best-effort: the setting is
already in Postgres, which is what makes it true (rule 15), so a failed publish
costs propagation latency, never correctness. The new subject sits under the
existing `work.<id>.>` grant, so per-agent authorization is unchanged (rule 14).

This also fixed a latent bug: the agent's sync path *merged* the reply into its
in-memory state. The sync reply is the complete desired set, so it now
**replaces** it — otherwise a re-sync would resurrect an application the plane
had removed.

**Replacing makes the reply's completeness load-bearing, so it is resolved
fail-closed.** Under ADR-005 an application missing from the reply means *remove
it*, and the agent's driver acts on that; the nudge re-syncs the whole fleet
while it is up, where the merge used to make a mid-life re-sync harmless. So
`DesiredStateFor` no longer logs-and-skips whatever it could not read: a store
read that did not answer, or sealed data that would not open, fails the **whole**
sync. The plane then sends no reply at all, the agent times out, keeps the set it
already holds and asks again — one sync cycle lost instead of a running container
on every node the blip touched. Only a permanent, application-scoped data
problem still omits one entry (its desired revision row is gone, or its config
snapshot will not parse): no retry can produce a spec for it, and holding the
whole node's desired state hostage to it would strand every other application on
that server. The same rule covers managed databases, whose reconciler also
removes what desired state does not name.

**The host override stays.** `CYPHER_ACME_EMAIL` / `CYPHER_ACME_CASERVER` now
override the panel's values per field, which is the escape hatch for a node that
must use a different account and what keeps CI hermetic. Its one cost is
recorded in §5.

**The fragment writer stops lying.** `certResolver` and the HTTP→HTTPS redirect
are emitted only when the node actually has a resolver. With none, the route is
plain HTTP on the `web` entrypoint: the app is reachable, the deploy is
unaffected, and the panel says so.

The router is **pinned** to `web` rather than left to Traefik's "attach to every
entrypoint" default, and so is the router of a route the operator declared
HTTP-only. Bound to `websecure` as well, an `https://` visitor completes a TLS
handshake against Traefik's self-signed default certificate and is served the
app under a browser warning — the same false HTTPS this section removes, arrived
at by a different route. A router with no TLS configuration has no business on
the TLS entrypoint. The two cases stay consistent: everything without a
`certResolver` is served on `web` only, and `:443` answers such a host with a
404 rather than a warning.

## 5. Route TLS state

`Application` gains a derived, never-stored `tls_state` (the `redeploy_pending`
pattern):

| Value | Meaning |
|---|---|
| *(omitted)* | No domain — there is no route to describe. |
| `https` | HTTPS requested and the panel has an ACME account: the node configures a resolver and obtains a certificate. |
| `http_only_no_resolver` | HTTPS requested, no ACME account. Served over plain HTTP. "Serving over HTTP meanwhile"; the deploy is unaffected. |
| `http_only` | HTTPS was not requested. Nothing is missing. |

The design brief named two values. Three exist because two would force the API
to lie: an application deliberately routed over HTTP is not missing a resolver,
and calling it `http_only_no_resolver` would put an untrue remedy in front of an
operator who chose plain HTTP.

**What it does not claim.** It reports the panel's *configuration*, not an
observed certificate. It cannot see a per-node `CYPHER_ACME_EMAIL` override
(that node may serve HTTPS while the panel reports `http_only_no_resolver` — an
understatement, and the safe direction), and `https` does not mean a certificate
was issued: issuance also needs DNS pointing at the server, which
`GET /applications/{id}/domain-check` answers. Reporting per-domain certificate
state *from the node* remains the later refinement
[routing-and-tls.md §10](routing-and-tls.md) already names. A read failure
answers "no resolver": with no answer, claiming HTTPS is exactly the false
certainty this feature removes (ui-principles §10).

## 6. A join command that completes

**`install/agent.sh`** now resolves the binary in three ordered cases:

1. `CYPHER_AGENT_URL` set → download it. Explicit operator intent — and what the
   panel's join command sends — wins over anything already installed, so joining
   also upgrades a stale binary.
2. A binary already at `/usr/local/bin/cypher-agent` → reuse it. A host prepared
   out of band, or re-joining, must not need a release server.
3. Otherwise → `https://github.com/<repo>/releases/latest/download/cypher-agent-linux-{arch}`,
   mirroring what `install/install.sh` has always done for `cypherd`.

The dead end is gone; its remedy text now appears when a *download* fails, which
is where it is useful.

**The panel's `install_command`** pins `CYPHER_AGENT_URL` to the panel's **own
version** when the panel is a release build (`updates.AgentAssetURL`), so a
server joins running the agent that matches its plane — the version pairing
ADR-010 assumes. A development build names no URL and the installer's default
takes over. The plane still stores and serves no binaries either way; it names a
version, which is precisely what ADR-010 permits.

## 7. Authorization & security summary

- Renewal is authorized by the verified client certificate alone; the request
  body is a claim that must agree with it. Revoked and unknown identities are
  refused, so revocation continues to mean revocation.
- The agent's private key is generated on the host and never transmitted — at
  renewal as at enrollment. No secret is added to any wire.
- `panel_tls` holds no secret (§4) and is therefore returned as stored; the
  ACME account key stays on the serving node (ADR-004).
- `GET`/`PUT /panel/tls` are owner-only. Every other principal gets 403; an
  unauthenticated caller gets 401.
- The renewal loop cannot be used to escalate: it re-issues the *same* CN with
  the same TTL, and cannot mint an identity the caller does not already hold.

Threat-model rows updated: §5.2 (the rotation control and its TTL decision are
now implemented and tested, and the open Phase 1 tag is closed) and §8
requirement 6.

## 8. Testing

- **Plane:** `core/enroll` — renew happy path (same CN, expiry moves, renewal
  recorded), revoked refused, unknown refused, CN mismatch refused, malformed
  CSR refused. `core/api/grpc` — the same set at the gRPC boundary, including
  "no client certificate ⇒ `PermissionDenied`".
- **Agent:** `agent/identity` with a fake clock — the schedule is two thirds of
  the lifetime; the loop waits rather than renewing early; on the date it renews,
  swaps atomically (reload from disk yields a usable, matching pair and the
  superseded slot is gone), and re-arms against the new expiry; a failed renewal
  keeps the running identity on disk and in memory and schedules a retry; the
  retry shrinks with the remaining lifetime and never drops below its floor; long
  waits are capped; a non-advancing certificate is refused.
- **Proxy:** an https route with no resolver names neither `certResolver` nor a
  redirect, is pinned to the `web` entrypoint (read back as a list, since an
  absent `entryPoints` key means *every* entrypoint — the opposite of the
  restriction — and greps the same as a restricted one) and still routes;
  `SetACME` from desired state flips the static config
  and the fragments and changes the container's static-hash so Traefik restarts
  into it; the host override wins per field; converge-twice is byte-identical and
  does not even touch the file (rule 13), in both TLS states.
- **Worker:** settings reach the Proxy from the sync reply, an empty account is
  applied rather than skipped, the resync nudge re-reads and is idempotent under
  redelivery, a re-sync removes applications the plane dropped, and a re-sync the
  plane cannot answer leaves the held desired set intact and NAKs.
- **Scheduler / store / API:** desired state carries the account and survives a
  read failure without inventing one; a desired set that cannot be resolved
  completely fails the sync instead of shrinking — proven for an unreadable
  application revision, an unreadable database revision and sealed env that will
  not open — while a missing revision row or an unparsable config snapshot omits
  only that one application; resync nudges only enrolled servers with
  distinct message ids; a real-Postgres round-trip of the singleton; HTTP tests
  for both routes (happy path, validation 400, non-owner 403, unauthenticated
  401) and for `tls_state` in all four shapes; join-command tests for the pinned,
  development-build and no-version cases.
- **CI:** `integration.yml` gains an assertion that a pasted join command on a
  container with no binary reaches for the project's latest release asset instead
  of dead-ending.

## 9. Acceptance

1. An agent whose certificate is two thirds through its life renews itself over
   mTLS and keeps running — no reconnect, no dropped desired state, no operator
   action.
2. Killing the plane during the renewal window does not lose the agent: it
   retries and succeeds when the plane returns, with weeks to spare.
3. A revoked (deleted) server is refused a renewal and cannot extend its access.
4. On a panel with no ACME account, an application with `https: true` is reachable
   over plain HTTP, its fragment names no resolver and binds only the `web`
   entrypoint, and the API reports `tls_state: http_only_no_resolver`.
5. Setting `PUT /panel/tls` makes every enrolled node configure a resolver within
   one reconcile — no per-host environment variable, no agent restart — and the
   same application then reports `tls_state: https`.
6. `curl -fsSL …/install/agent.sh | … sh`, pasted verbatim from the panel onto a
   fresh VM with no `cypher-agent` binary, joins the server.
7. A store failure while a fleet-wide resync is in flight costs one sync cycle
   and nothing else: no agent receives a desired set with a running application
   missing from it.

**Evidence (live, 2026-09-05).** Run against a real `cypherd` + `cypher-agent`
pair with `CYPHERD_AGENT_CERT_TTL=5m` (so the renewal point falls ~100 s after
issuance) and the Proxy image deliberately unresolvable, so only the fragments
and static config are exercised:

- Before any account: `traefik.yml` carries no `certificatesResolvers` and a
  freshly created `https` application reports `tls_state: http_only_no_resolver`.
- `PUT /panel/tls` → the agent logs one `re-reading desired state` (reason
  `panel tls changed`) within a second, `traefik.yml` gains the `le` resolver
  with the account and the staging directory URL, and the same application then
  reports `tls_state: https`. **Acceptance 4 and 5.**
- At exactly the logged `renew_at`, `agent certificate renewed`: a new serial,
  the same CommonName, a later `NotAfter`, `identity.json` committed to the
  alternate slot (`agent-cert.1.pem` / `agent-key.1.pem` at `0600` for the key),
  and the superseded pair gone from the directory. **Acceptance 1.**
- Across the whole run: **0 bus reconnects and 0 bus disconnects**, and the
  server kept reporting a live heartbeat-derived status. The rotation cost
  nothing. **§3.3.**
- Deleting the server then refused the *renewed* identity at the bus
  (`bus auth: refused connection from revoked or unknown identity`) and the agent
  exited — revocation survives rotation. **Acceptance 3** (renewal-side refusal
  is covered by `core/enroll` and `core/api/grpc` tests; a live agent cannot
  demonstrate it because the bus cuts it off first).

Acceptance 2 and 6 are not live-checked here: 2 needs a multi-day window
(covered by the fake-clock retry tests) and 6 needs a published release asset
(covered by the CI installer assertion and the join-command tests).

## 10. Out of scope

Reporting per-domain **certificate issuance** state from the node to the plane
(`acme.json` observation) — the `CERT ✓` / retry chip in design 13c still needs
it, and it stays where routing-and-tls.md §10 put it · DNS-01, wildcards, BYO
certificates · a per-project or per-application ACME account (one panel, one
account) · CA rotation and a CRL/OCSP story for agent certificates (revocation
remains enrollment-state-based) · notifying operators before an agent
certificate expires (`cert-expiry` remains a deferred notification event) ·
agent auto-update itself (ADR-010's implementation, alongside the release
pipeline) · multiple domains per Application.

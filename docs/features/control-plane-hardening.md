# Feature spec: Control-plane hardening and diagnostics

> Eight small things that together decide whether an operator can *trust* the
> panel and *diagnose* it. One is a blocker (an agent could read another
> agent's secrets off the bus); the rest are the diagnostic and honesty gaps
> the design canvases 13s, 13t, 13ai, 14j and 16a were drawn against: a 500
> with a trace id worth pasting, a 429 that says how long, a version the panel
> admits to, a bounded tail of its own log, and links that carry the right
> scheme when TLS lives in front.
>
> This spec serves the feature-matrix row "Login rate limiting & session
> management" (**V1**) and adds the row "Panel version, update check and
> diagnostics" (**V1**). It closes the audit items
> `nats-inbox-wildcard-cross-agent-read`, `login-rate-limiting`,
> `panel-public-url-https`, `expired-sessions-never-purged`,
> `panel-version-and-diagnostics-not-exposed`, `notify-project-name-bug` and
> `notify-validevents-not-delegated`.
>
> Written 2026-09-05, just before implementing. Vocabulary per
> [glossary.md](../glossary.md); rules per [ENGINEERING.md](../../ENGINEERING.md).

## 1. NATS inbox isolation (threat-model §5.2 — blocker)

**The bug.** The bus authorizer scoped every agent to its own `work.<id>.>`,
`state.<id>.>` and `logs.<id>.>` subjects, but granted *every* agent
`Subscribe` on the bare `_INBOX.>` wildcard so request/reply could work. The
plane answers a desired-state sync with `msg.Respond(data)` on the requester's
reply inbox, and that payload is the resolved `DesiredState` — `AppSpec.env`
and `DbSpec.env` in plaintext. Core NATS fans a publication out to every
matching subscription, so agent A subscribed to `_INBOX.>` received agent B's
sync reply. That is exactly the lateral movement §5.2 says must be impossible.

**The fix.** Reply inboxes become per-identity subjects, and the grant follows:

```
agent (CN = srv_x):  nats.CustomInboxPrefix(subjects.InboxPrefix("srv_x"))   → "_INBOX_srv_x"
plane authorizer:    Subscribe.Allow = [ work.srv_x.>, _INBOX_srv_x.> ]
plane's own client:  in-process, default permissions, default "_INBOX" prefix
```

`pkg/subjects` gains `InboxPrefix(serverID)` and `InboxForServer(serverID)`
so both sides build the prefix from one function (rule 14: subjects are
contracts, changed additively). The JetStream pull subscription and the JS API
requests an agent already makes (`CONSUMER.INFO`, `MSG.NEXT`) use the
connection's inbox prefix for their replies, so the existing work-item path is
covered by the same grant. Nothing else changes on the wire.

**The other half: the plane validates the reply subject.** The grant above
fixes who may *subscribe*; it does not fix where the plane *publishes*. A
requester chooses its own `msg.Reply`, and NATS does not permission-check a
publisher's reply subject under plain publish/subscribe allow lists — only
`Responses` permissions do that, and these grants are not those (verified
against nats-server v2.14.5, `server/client.go`: a reply is checked only when
`client.replies != nil`, which is set for `Responses` permissions alone). The
plane's own client is in-process and unrestricted, so an unvalidated
`msg.Respond` let any enrolled agent aim a trusted publication at an arbitrary
subject:

```
agent srv_beta:  PublishRequest("state.srv_beta.sync", reply="state.srv_alpha.deploy", nil)
plane:           publishes a DesiredState onto the plane's own deploy-event subject
```

The payload is always `resolve(<id from the request subject>)` — the requester's
*own* state, because the id comes from the permission-pinned subject and never
from the reply — so this was not a cross-agent read. It was an
arbitrary-subject publish primitive into plane-internal subjects
(`work.<other>.rollout`, `state.*.deploy`, `logs.*.>`). `RespondDesiredState`
therefore requires `msg.Reply` to start with `InboxPrefix(<requester>) + "."`
— the same scope the subscribe grant names — and otherwise drops the request
and logs at warn with the server id. The warn fires once per server id per
process: the cause is a static property of the agent build, the agent retries
forever, and the diagnostic ring (§4) is bounded.

**Compatibility.** An agent built before this change still uses the default
`_INBOX` prefix, which the plane no longer grants. Every request/reply it makes
is answered onto a subject it may not subscribe to, so it fails at the first
one — the JetStream work-consumer bind, before the desired-state sync — and
exits non-zero into its init system's restart loop, converging nothing. Its
heartbeats still publish, so the panel shows the server briefly green on each
restart until systemd gives up. **Agents must be upgraded in the same window as
the plane.** This is the one deliberate break in this change set and it is the
security fix itself; the `core/bus` package doc, threat-model §5.2,
`docs/dev/deployment.md` §Upgrades (the operator-facing copy), the roadmap and
`CLAUDE.md` record it.

**Proof.** `core/bus`: an agent enrolled as `srv_beta` subscribing to
`_INBOX.>` or to `srv_alpha`'s inbox scope gets a permissions violation and
never receives `srv_alpha`'s sync reply (`TestAgentInboxIsIsolatedPerIdentity`);
a sync request naming `state.srv_alpha.deploy` as its reply is dropped, proven
by a marker published after it arriving *first* at the plane's deploy-event
consumer (`TestSyncReplySubjectMustBeInTheRequestersInbox` — it fails against
the unvalidated `msg.Respond`); the sync, work-fetch and status tests keep
passing with the prefix an agent actually uses (`agentConn` in the bus tests
connects exactly as `conn.ConnectBus` does).

## 2. Request ids and the fault envelope (canvas 13s)

Every HTTP response carries `X-Request-Id: trace_xxxx-xxxx-xxxx-xxxx` (8
random bytes, hex, grouped). The id is minted by the outermost middleware and
placed on the response header map before any handler runs, so `writeError`
can read it back without a signature change: **every `Error` envelope gains an
optional `trace_id`** equal to the header. An incoming `X-Request-Id` is
honoured only when the TCP peer is a trusted proxy (§6) and the value is a
short printable token; anything else is replaced, so a client cannot forge a
correlation id into the log.

The recovery middleware turns a handler panic into the same `Error` envelope
with 500 (not a bare "internal error" string), and the request log line for
**every 5xx is at error level** carrying `trace_id`, `method`, `route`
(the mux pattern) and `path`. Successful requests keep their info line, now
with `trace_id`, so a fault can be found from the id the operator pasted.
Middleware order becomes request-id → log → security headers → recover, so a
panic is observed by the log line as a 500 rather than vanishing.

The SSE endpoints' only error path is `writeError`, which now carries the id;
in addition their opening `connected` frame carries `{"trace_id": "..."}`, so a
stream that misbehaves after the headers are out is diagnosable from the same
id (the web client already ignores that frame's data, so this is additive).

## 3. Panel version and the update check (canvases 14j, 16a, 13ai)

```
GET /api/v1/panel/version   (any authenticated user)
{
  "version":   "v0.4.0",            // -X main.version; "dev" locally
  "commit":    "0c7a08b",           // -X main.commit;  "dev" locally
  "built_at":  "2026-09-05T10:00:00Z" | null,   // -X main.buildDate
  "go_version": "go1.25.12",
  "latest": null | { "version": "v0.5.0", "kind": "patch|minor|major",
                     "notes_url": "https://github.com/…/releases/tag/v0.5.0",
                     "published_at": "…" }
}
```

`latest` is `null` whenever there is nothing newer to say: the running build
is up to date, the check is off, the check has not completed or failed, or the
running version is not a release (`dev`). The key is always present and
explicitly `null` — "nothing newer" and "the panel did not say" are different
answers. A panel wired without a version source at all answers `503` rather
than inventing one. `kind` is the semver class of the
delta, which is what canvas 16a's badge and canvas 14j's "AVAILABLE" chip
render. Member-level: knowing the panel's version is not a secret to anyone
who can sign in, and the report-issue dialog (13ai) needs it for every user.

`cypherd version` prints the same three build values, mirroring
`cypher-agent version`. `release.yml` and the `Dockerfile` stamp
`main.commit` and `main.buildDate` beside the existing `main.version`.

**The check** lives in `core/updates`: one owned goroutine (`Checker.Run`,
cancelled with the process) polls a feed every 6 hours — first run shortly
after boot — and caches the answer in memory. Configuration:

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_UPDATE_CHECK` | `on` | `off` disables the outbound request entirely; `latest` is then always `null` |
| `CYPHERD_UPDATE_FEED_URL` | `https://api.github.com/repos/MaramHarsha/CypherPanel/releases/latest` | The feed; GitHub's releases/latest JSON shape (`tag_name`, `html_url`, `published_at`, `draft`, `prerelease`) |

Hardening, per threat-model §5.11's posture for the panel as an HTTP client
(now §5.13): 10 s timeout per request; response body capped at 256 KiB;
`http`/`https` only; at most 3 redirects, and **a redirect is refused when its
target is a loopback, private, link-local or unspecified address — checked
after resolving the hostname**, so the feed cannot bounce the plane into its
own network; drafts and pre-releases are ignored; a feed that fails leaves the
last good answer in place and logs at warn level with the feed host, never
the body. The check sends no data about the panel beyond the request itself
(a `User-Agent: cypherd/<version>` header).

**Once per version, owners are told.** The first time the checker sees a
version newer than the running one it writes **one** inbox item of the new
kind `panel.update_available` to every team owner and panel owner who has not
muted that kind. The dedupe key is `panel.update_available:<version>`, so a
restart (which re-sees the same release) writes nothing, and a later release
writes one more. The item is panel-level — it has no project — so
`inbox_items.project_id` becomes nullable (migration `0028`, reversible: the
down step deletes panel items and restores `NOT NULL`). Severity `info`, but
**not digested**: a rollup of "releases available today" would be nonsense.
The item carries no link (there is no in-panel changelog route yet; the
release notes URL is in the body).

`panel.update_available` is an *inbox* kind, not a notifier or webhook event:
`domain.InboxKinds()` = `EventTypes()` + the panel kinds, and preferences
validate against `ValidInboxKind`. Notifiers and endpoints keep subscribing to
`EventTypes()` only — nothing emits a panel kind to them.

## 4. Panel log tail (canvas 13ai's "attach the last 20 panel log lines")

```
GET /api/v1/panel/logs?tail=N     (panel owner, session only; 1 ≤ N ≤ 500, default 50)
{ "lines": ["time=… level=INFO msg=\"http request\" method=GET …", …], "capacity": 500 }
```

`core/logring` is a bounded ring of rendered log lines (500) fed by a
`slog.TextHandler`, installed in `main.go` beside the JSON stderr handler
through a fan-out handler. Lines are the structured records rendered as
`key=value` text, oldest first. Owner-only and session-only: the log names
resources and hosts, and an API token must never be able to lift it. Rule 20
already keeps secrets out of every log line; the HTTP test
`TestPanelLogsNeverCarrySealedValues` seals a value through the API and asserts
neither the plaintext nor its ciphertext appears in the tail.

## 5. Login throttling that tells the truth (canvas 13t)

- **`429` carries `Retry-After: <seconds>`** and the body gains
  `retry_after_seconds` — the seconds until the oldest failure in the window
  ages out. `auth.Limiter.RetryAfter(key)` computes it; `auth.RateLimitedError`
  carries it and still matches `errors.Is(err, auth.ErrRateLimited)`.
- **Two dimensions.** Login is throttled per client address (5 failures / 15
  min, as before) *and* per account (10 failures / 15 min, keyed by the
  lower-cased email). One attacker behind a shared proxy can no longer lock
  everyone out — the per-address counter only ever stops that address — and a
  distributed guess at one account is bounded by the account counter.
  Successful sign-in resets both.
- **Email change is throttled**, as [panel-mail.md](panel-mail.md) §5 has
  promised since it shipped: request and confirm both take the same two keys
  (client address; the caller's user id as the account) and answer `429` with
  the same headers. A wrong current password or a wrong confirmation secret
  counts as a failure.
  The client-address key is shared with sign-in deliberately: failures from one
  address against either surface spend one budget, and a successful sign-in or
  a correct current password clears it — the same rule Login already had.
- **Client address behind a proxy.** `CYPHERD_TRUSTED_PROXIES` is a
  comma-separated CIDR list (default empty). Only when the TCP peer is inside
  it does the panel read `X-Forwarded-For` (walking from the right past
  trusted hops to the first address that is not one) or, failing that,
  `X-Real-IP`. From any other peer the headers are ignored, so a client cannot
  pick its own throttle key.

## 6. One public base URL

`CYPHERD_PUBLIC_URL` (e.g. `https://panel.example.com`) — scheme + host, an
optional port, nothing else — overrides `AdvertisedConsoleURL()` wherever the
panel writes a link to itself: the email-change confirmation link, the GitHub
webhook URL shown at application creation, and the agent join command's
installer fetch and `CYPHER_PLANE_HTTP`. Unset, the old
`http://<CYPHERD_PUBLIC_HOST>:<port>` stays. A value with a path, query,
fragment, credentials or an unknown scheme fails config validation at boot; a
trailing slash is trimmed, because every link is built by concatenation. Only
the *console* URL moves: `AdvertisedNATSURL` and `AdvertisedEnrollAddr` keep
using `CYPHERD_PUBLIC_HOST`, since agents dial the host directly and not
through the browser-facing proxy. `install.sh` accepts it, writes it into
`cypherd.env` and prints it as the URL to open;
`deploy/cypherd.env.example` and `docs/dev/deployment.md` document it beside
the TLS-in-front instructions it exists for.

## 7. Expired sessions are deleted

A session past `expires_at` was invisible but never removed, so `sessions`
grew by one row per sign-in forever. `Authenticator.RunSessionPurge(ctx,
interval)` is an owned goroutine that calls `DeleteExpiredSessions(before =
now)` every sweep interval, deleting rows whose `expires_at <= before` and
logging the count when it is non-zero. The clock is injected on the
authenticator so the purge is testable without waiting.

## 8. Small correctness

- `notify.dispatch` resolves `GetProject` so `NotifyEvent.Project` is the
  project's name, not the environment's (the divergence
  [outbound-webhooks.md](outbound-webhooks.md) §4 recorded).
- `notify.validateMeta` validates events with `domain.ValidEventType` — the
  private duplicate map is gone, as outbound-webhooks.md §3 said it would be.
- `DELETE /deploy-keys/{id}` on a key still referenced answers `409` with the
  blocking applications by id and name — the `Error` envelope plus an
  `applications: [{id, name}]` array (`DeployKeyInUse` in the OpenAPI spec,
  `deploykeys.InUseError` in Go, still matching `errors.Is(err,
  store.ErrInUse)`), which
  [deploy-key-private-repos.md](deploy-key-private-repos.md) §3 promised. The
  RESTRICT foreign key stays the authority: the blocker list is read only after
  the store refuses, so a reference created in between is still refused.
- `GET /databases/{id}/connection-info` returns the server's public address as
  `host` for an exposed port (falling back to the hostname the agent reported,
  and to empty when neither is known — an honest "we do not know where this is"
  beats a plausible non-address), never the server id, which was not an address
  at all.

## 9. Authorization and security summary

| Surface | Auth | Notes |
|---|---|---|
| `GET /panel/version` | any authenticated principal (`read`) | no secret; version is in every heartbeat log anyway |
| `GET /panel/logs` | panel **owner**, **session only** | names hosts and resources; never secrets (rule 20, tested) |
| `X-Request-Id` | trusted proxies may supply; others get a fresh id | no log injection via a client-chosen id (token charset, ≤ 128) |
| update check | outbound, opt-out | no private-range redirects, bounded body, timeout, drafts/prereleases ignored |
| inbox `panel.update_available` | owners only, once per version | no project; excluded from team-removal sweeps by construction |
| `_INBOX_<cn>.>` | per agent identity | closes the §5.2 cross-agent read |

Threat-model changes in the same PR: §5.2 gains the inbox control and its
compatibility note; §5.8 gains trusted-proxy client addressing and per-account
throttling; a new §5.13 covers the update check; §9's table gains the rows.

## 10. Acceptance (each has a test)

1. An agent cannot subscribe to another identity's reply inbox or the bare
   `_INBOX.>`; sync and work fetch still work with the per-agent prefix.
   A sync request whose reply subject is outside the requester's inbox scope
   is dropped, not answered, so the plane cannot be aimed at another subject.
2. Every response carries `X-Request-Id`; a 500 body carries the same value
   as `trace_id`; a client-supplied id is ignored unless the peer is trusted.
3. A handler panic yields the `Error` envelope with 500 and an error-level log
   line carrying the trace id.
4. `GET /panel/version` returns version/commit/built_at/go_version and a
   `latest` computed from a fake feed; `kind` classifies patch/minor/major;
   a redirect to a private address is refused; `off` performs no request.
5. Seeing a newer version writes exactly one `panel.update_available` item per
   owner, and seeing it again writes none.
6. `GET /panel/logs` is owner+session only, bounds `tail`, and never contains a
   sealed value.
7. A throttled login answers 429 with `Retry-After` and `retry_after_seconds`;
   per-account throttling stops a distributed guess; a proxied client is keyed
   by the forwarded address only from a trusted peer.
8. Email-change request and confirm answer 429 after repeated failures.
9. `CYPHERD_PUBLIC_URL` is validated and drives the confirmation link, the
   webhook URL and the join command.
10. Expired sessions are deleted by the purge and live ones are kept.
11. `NotifyEvent.Project` is the project name; an unknown event key is refused
    through the domain taxonomy; the deploy-key 409 names the applications;
    connection info returns the server's address.

## 11. What changed while implementing

Recorded because the spec was written first (rule 32) and the code is what
ships:

- **The plane validates a sync request's reply subject** (§1). The spec
  originally treated the per-identity subscribe grant as the whole fix; it is
  half. NATS does not permission-check a publisher's reply subject under these
  grants, so without the check the plane's unrestricted in-process client was
  an arbitrary-subject publish primitive for any enrolled agent. Found in
  review, fixed at the plane with a regression test.
- **`latest` is explicit `null`, and a panel with no version source is `503`.**
  Omitting the key would have made "up to date" and "not wired" the same
  answer to a client.
- **The SSE `connected` frame carries the trace id** (§2). The spec said the
  streams needed nothing; one line makes a stream as diagnosable as a
  request, and the web client already discards that frame's data.
- **The deploy-key `409` has a schema** (`DeployKeyInUse`), not just prose:
  `error` + `trace_id` + `applications[]`. Additive, so a client reading only
  `error` is unaffected (rule 17).
- **`connection_info.host` can be empty.** The spec said "falling back to its
  hostname"; a server with neither a public address nor a reported hostname now
  answers with an empty string rather than something that is not an address.
  The OpenAPI schema carries that meaning — the field changed sense (it used to
  be the server *id*), so a bare `type: string` would have left the contract
  saying nothing about it (rule 19).
- **`X-Request-Id` is a declared response header**, not only prose. It is
  `components/headers/RequestId`, referenced from every shared error response
  and from the `/panel/*` responses, and stated once in `info.description` as a
  property of every response. A generated client or a human reading the spec
  could not otherwise learn the header exists (rule 19).
- **The email-change routes share the client-address budget with sign-in** (§5).
  Two separate address budgets would have doubled what one attacker gets from
  one address for no benefit.
- **Six pre-existing OpenAPI descriptions were quoted.** They were flow-style
  scalars containing commas (`{ description: Runs, newest first., content: … }`),
  which the orval 7→8 bump in the previous commit began rejecting — the spec is
  the source of truth (rule 19), so a spec the client generator cannot read is a
  bug regardless of who introduced it. No semantics changed.

## 12. Out of scope this slice

Built-in TLS/ACME for the panel listener (TLS stays in front) · a persisted
audit log (canvas 13t's footer is not asserted by the API) · an in-panel
changelog route and help menu (14j's UI) · the release *banner* component
(16a's UI) · email digests of `panel.update_available` · desired agent version
/ channel state (ADR-010's implementation) · `agent_min_version` on the
version endpoint (no compatibility floor exists yet to declare) · purging
consumed join tokens and email changes (rows stay as audit for now) ·
per-team throttling quotas · distributed rate limiting (single node, vision.md).

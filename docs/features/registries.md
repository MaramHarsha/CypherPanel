# Feature spec: Container registries

> Optional credentials for private container registries — ADR-008's path 3.
> An application may pull its image (or its private base image) through one,
> and a build may push its result to one. Nothing in the deploy path comes to
> depend on a registry: a single-server build still keeps the image in the
> local daemon and a multi-server one still travels over the mTLS relay.
>
> Written 2026-09-05, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why this exists, and what it must not become

[ADR-008](../adrs/ADR-008-no-registry-required.md) decided that CypherPanel
requires no container registry. That decision stands. What it did not cover is
the two things operators legitimately need a registry *for*:

1. **Pulling something private.** A `source.kind=image` application whose image
   lives in a private registry, and a Dockerfile whose `FROM` names a private
   base image. Today both fail with an unexplained `manifest unknown`.
2. **Pushing builds somewhere the operator already runs.** A team with a
   Harbor or a GHCR namespace wants the artefact CypherPanel built to land
   there too — for their own promotion pipeline, for a second panel, for
   retention.

The binding constraint is that neither may become load-bearing. The rollout
runs the image in the local daemon or the one the relay delivered; the pushed
copy is *additional*. A panel that stores no registry behaves exactly as it did
before this feature existed, and an application that names none pays no lookup
for one.

## 2. Resource model

A **Registry** is a team-scoped credential. Team-scoped rather than
panel-scoped because credentials for one customer's registry have no business
being usable by another team's applications, and the team is the boundary every
other shared credential here already uses (deploy keys, backup targets).

```
Registry:
  id            TEXT PK (reg_… prefix)
  team_id       TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE
  name          TEXT NOT NULL            -- operator label; UNIQUE (team_id, name)
  url           TEXT NOT NULL            -- host, optionally namespaced: ghcr.io, ghcr.io/acme, registry:5000
  username      TEXT NOT NULL DEFAULT '' -- empty for a bearer-token registry
  token_ct      BYTEA NOT NULL           -- sealed under the master key (secret.Box)
  token_nonce   BYTEA NOT NULL
  can_pull      BOOLEAN NOT NULL DEFAULT true
  can_push      BOOLEAN NOT NULL DEFAULT false
  last_test_at     TIMESTAMPTZ
  last_test_ok     BOOLEAN NOT NULL DEFAULT false
  last_test_detail TEXT NOT NULL DEFAULT ''
  created_at, updated_at TIMESTAMPTZ
```

`url` is a **host**, never a URL. A registry reference carries no scheme, and
accepting `https://ghcr.io` would produce image names nothing can pull — so it
is refused at validation rather than stored and discovered at the first deploy.

`can_pull` / `can_push` exist so a pull-only credential attached to a build that
wants to push is refused at configuration time rather than met as a `403` from
the registry after a five-minute build. Pull defaults on (the common case);
push defaults off (the larger grant).

`last_test_*` lets a list show whether a credential is known-good without
re-authenticating on every page render.

Applications gain three columns
([0036_registries.sql](../../core/store/migrations/0036_registries.sql)):

```
applications.source_registry_id     TEXT REFERENCES registries(id) ON DELETE RESTRICT
applications.build_push_registry_id TEXT REFERENCES registries(id) ON DELETE RESTRICT
applications.build_push_repository  TEXT NOT NULL DEFAULT ''
```

`ON DELETE RESTRICT` on both, not `SET NULL`: deleting a registry applications
depend on would break their next deploy silently, at the moment nobody is
looking. The API turns the resulting conflict into a `409`, and
`GET /registries/{id}/used-by` names the applications rather than counting them
— "3 applications" is not something an operator can act on.

`source_registry_id` answers one question: **where do this application's inputs
come from.** For `kind=image` that is the image itself; for a git kind it is
the private base image a `FROM` names. One field, one meaning, two uses.

## 3. Secret discipline

The token is sealed before it reaches the store and is never returned by any
route (ENGINEERING rule 20). The API answers `token_set: true` — that a
credential exists, never what it is — which is the notifier contract, reused.

It is unsealed in exactly one place: the scheduler, at work-build time, exactly
as the deploy key is. The plaintext exists only inside an mTLS-carried work item
(rule 23). Nothing is cached, so rotating or deleting a credential takes effect
on the next deploy rather than at an expiry nobody can see, and no `docker
login` state is written to a builder for a later build to inherit by accident.

The audit detail records that a token was **rotated**, never a value. A
connection test's failure detail is the registry's own words — but a failed
dial is unwrapped from its `*url.Error` first, because that type stringifies
the whole request URL and a bearer token can live in one.

### 3.1 Where the probe is allowed to connect

The connection test is the panel's most careful outbound request, because the
destination is chosen in a request body and — unlike a notifier's webhook —
cannot be held to a known set of hosts: pointing at an arbitrary registry *is*
the feature. Two independent defences, neither substituting for the other
(threat-model §5.14):

**The URL is built from components.** The value is held to one regular
expression covering the whole host — DNS labels, or a bracketed IPv6 literal,
with an optional port — and it is that *checked* value which becomes
`url.URL.Host`. A scheme, credentials before an `@`, a path, a query, a
fragment, a backslash, whitespace or a control character are outside the
alphabet, so there is nothing to strip and nothing to get subtly wrong. A
namespace (`ghcr.io/acme`) belongs to the image path and is dropped: the host
alone answers `/v2/`.

**The connection is guarded at dial time**, by `core/egress`, which refuses any
address that is not publicly routable — loopback, RFC1918, IPv6 unique-local,
link-local (where cloud metadata lives), unspecified and multicast, and their
IPv4-mapped forms. The check runs in the dialer's `Control` hook, on the
**resolved** IP the socket is about to use, so a name that answers publicly once
and privately the next time is refused the next time. A validation-time string
check cannot do that.

The guard is on the service's own client, so it covers **both** test routes. The
stored path reaches the same capability with one extra step, and two policies
for "test this credential" would only mean the weaker one is the one people
reach for.

The scheme is therefore always `https`. Every address the Docker daemon treats
as insecure-by-default is refused before it is dialled, so no case remains in
which a credential would go out in the clear.

**The cost, stated plainly:** a registry on the operator's own private network
cannot be *tested* from the panel. It can still be stored, attached and pulled
from, because the **agent** does the pulling and the agent is already on that
network. The refusal says exactly that — "save the registry anyway, the agent
that pulls is already on that network" — rather than reporting a connection
error, so nobody is left believing their credential is wrong.

## 4. Validation, and what it buys

Checked when a registry is attached to an application, not when it is spent:

- the registry exists;
- it belongs to the **same team** as the application — one project borrowing
  another team's push token is exactly the tenancy hole the boundary exists to
  close;
- it allows the thing it is attached for (`can_pull` / `can_push`).

A registry in another team answers the same `no such registry` as one that does
not exist, so an application's config screen is not a way to enumerate other
teams' credentials.

Two combinations are refused rather than stored and silently ignored, because
both are failures an operator cannot see: a `push_repository` with no
`push_registry_id`, and a push target on an image source, which is never built.

`push_repository` must be a legal lowercase OCI path. Uppercase is **refused,
not folded**: a registry treats `Acme/Web` as a name it has never heard of, and
lowercasing it here would push to a repository the operator did not type. Empty
means "the application's name, reduced to a legal path" — `My API` →
`my-api`, and an application whose name survives nothing falls back to its id.

## 5. On the wire

`work.proto` gains two messages and three fields, all additive (`buf breaking`
clean, so an agent that predates them is unaffected):

```proto
message RegistryAuth { string server_address = 1; string username = 2; string token = 3; }
message RegistryPush { string image = 1; RegistryAuth auth = 2; }

AppSpec.registry_auth = 16   // only when AppSpec.pull is true
BuildWork.source_auth = 11   // private base image
BuildWork.push        = 12   // ADR-008 path 3
```

The credential is a value on the work item rather than agent configuration: an
agent holds no registry credentials of its own, so revoking one on the plane
revokes it everywhere at the next work item, and a captured agent disk yields
nothing.

`AppSpec.registry_auth` is attached **only when the spec pulls**. An image that
arrived by local build or relay has nothing to authenticate to, and putting a
token on that work item would be a secret travelling for no reason.

A named registry the plane cannot resolve **fails the deploy**. Falling back to
an anonymous attempt would fail later at the daemon with a `manifest unknown`
nobody can act on, turning a revoked credential into a mystery instead of a
message.

## 6. Agent side

Two different headers, because the daemon uses two:

- **Pull** (`POST /images/create`) reads `X-Registry-Auth`: base64url of one
  auth object.
- **Build** (`POST /build`) reads `X-Registry-Config`: base64url of a **map**
  from host to auth object, because one build may pull from several
  registries. Only the registry the plane sent is in the map, so an unrelated
  `FROM` finds no entry and pulls anonymously, exactly as before.

Both encodings live in [`pkg/registryauth`](../../pkg/registryauth), shared by
plane and agent, because two implementations of one header is how a pull starts
failing on half the fleet. The credential rides in a header rather than the
query so it stays out of anything that logs a URL — the daemon's own request log
included.

**Push** happens on the builder after a successful build: the local image is
tagged with the target reference and that reference is pushed. A failed push
**fails the deployment**. Warning and rolling out anyway would report success
for an image that is not where the operator was told it would be, and the next
thing to look for it finds nothing.

## 7. API

All routes are panel-scoped (a project-scoped API token may not reach them —
a credential is a team's, not one project's), and all mutations require the
`admin` ability.

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/registries` | member | filtered to your teams; never refuses |
| `POST /api/v1/registries` | team admin | `team_id` optional when you are in exactly one team |
| `POST /api/v1/registries/test` | team admin | proves a credential before it is saved |
| `GET /api/v1/registries/{id}` | team member | 404 for another team's |
| `PATCH /api/v1/registries/{id}` | team admin | partial; omitted token is not rotated |
| `DELETE /api/v1/registries/{id}` | team admin | `409` while applications use it |
| `POST /api/v1/registries/{id}/test` | team admin | records `last_test_*` |
| `GET /api/v1/registries/{id}/used-by` | team member | names what would break |

Both test routes take team-admin rank rather than membership because they make
an outbound request to a host the caller chose. A failed test is a `200` with
`ok: false` — the request succeeded, the connection did not, and conflating the
two costs the caller the distinction.

The test probes `GET /v2/`, the OCI distribution spec's own liveness-and-auth
endpoint, rather than the catalog: many registries disable catalog listing for
non-admin credentials, and a working credential must not look broken because it
cannot enumerate every repository on the host. Where it is allowed to connect,
and why the answer is not "anywhere", is §3.1.

With no registry service wired, every route answers `501`, which is what a
panel that never had this feature looked like.

## 8. Audit

`registry.created`, `registry.updated`, `registry.deleted`, on the
`registry` resource kind, scoped to the team. The update detail records
`token_rotated: true|false` and nothing else about the credential.

## 9. Deliberately out of scope

- **Registry-backed image distribution as the default.** ADR-008 says no
  registry is required; making one the transport for multi-server deploys would
  supersede it, and that needs its own ADR.
- **Per-application registry mirrors / pull-through caches.** Configuration of
  the daemon, not of an application.
- **Docker Hub rate-limit handling.** A credential helps, but the retry policy
  belongs with the deploy pipeline's own backoff.
- **Reading a credential back.** There is no route that returns a token, and
  there will not be one.

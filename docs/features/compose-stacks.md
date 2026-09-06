# Feature spec: Compose Stacks

> A **Compose Stack** is a multi-container Resource defined by a compose file
> the operator supplies and the panel runs as-is. It is the honest home
> [ADR-007](../adrs/ADR-007-template-format.md) named for "I have a compose
> file and want it run" — the ~130 catalog entries that need a command
> override, a host mount or privileged access, and everything an operator
> already has a compose file for.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. The problem this has to solve without breaking ADR-005

[ADR-005](../adrs/ADR-005-desired-state-reconciliation.md) says every feature is
expressible as desired state, and that there is no "run this thing" verb.
[ADR-007](../adrs/ADR-007-template-format.md) restated it: adding one "would
reintroduce exactly the imperative path ADR-005 exists to remove". A naive
Compose Stack — `POST /stacks/{id}/up`, the agent obeys — is precisely that
verb, and it would be the worst place to make an exception, because a
reconciler that takes orders on the side stops being able to converge from
desired state alone.

The resolution is that **the compose file itself is the desired state**, and
`docker compose up -d` is not a command in the imperative sense — it is a
convergence. Given the same file it makes the host match and does nothing on a
second run, which is the reconciler contract stated in Docker's vocabulary
instead of ours. So the plane stores a file, ships a file, and never sends a
command; the agent runs one fixed invocation of its own against whatever file
desired state currently names.

The distinction is the same one `work.proto` already draws: "declarative
workload config … is desired state and permitted; a general host/privileged/
cross-container exec verb is not." The plane does not choose the command here.
It chooses the file.

## 2. Resource model

A Compose Stack sits beside Application and Managed Database as a third
Resource in an Environment.

```
ComposeStack:
  id                  TEXT PK (cs_… prefix)
  environment_id      TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE
  name                TEXT NOT NULL              -- UNIQUE (environment_id, name)
  runtime_server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE RESTRICT
  desired_revision_id TEXT                       -- nil until the first deploy
  -- Routing (§5). Empty domain means the stack publishes nothing through the Proxy.
  route_domain        TEXT NOT NULL DEFAULT ''
  route_service       TEXT NOT NULL DEFAULT ''   -- which compose service answers
  route_port          INTEGER NOT NULL DEFAULT 0
  route_https         BOOLEAN NOT NULL DEFAULT true
  -- Observed (ADR-005), same vocabulary as an Application (ui-principles §5).
  status, status_detail, observed_revision_id, status_observed_at
  created_at, updated_at

ComposeRevision:
  id            TEXT PK (csr_… prefix)
  stack_id      TEXT NOT NULL REFERENCES compose_stacks(id) ON DELETE CASCADE
  compose_yaml  TEXT NOT NULL   -- the file, verbatim, as the operator wrote it
  created_at
```

Env vars are sealed rows keyed by stack, exactly as an Application's are, and
reach compose as an env file the agent writes `0600` and removes on every exit
path. Compose interpolates `${VAR}` from it, which is the mechanism a compose
file already expects — so nothing about the file has to be rewritten to carry a
secret, and the secret never appears in the file the plane stores.

**Deliberately NOT a Deployment.** A stack has no build and no distribute
stage: its images are pulled by the daemon on the target. The pipeline's
three-stage machinery — the queue, the builder routing, the relay, the
per-stage events — would be ceremony around a single converge. So deploying a
stack creates a **ComposeRevision**, points `desired_revision_id` at it and
publishes converge work; the revision list is the history, and rollback
re-points `desired_revision_id` at an older revision. This is the same shape
`POST /applications/{id}/restart` uses
([deployment-control.md](deployment-control.md) §3) and for the same reason:
not everything that changes a host is a deploy.

## 3. What the panel refuses, and why

The file is run as-is. Two directives are still refused at validation, because
each one silently breaks something the panel promises:

- **`build:`** — there is no builder on a target host. ADR-008's build story is
  a builder-role agent producing an image that travels by local daemon or mTLS
  relay; a compose `build:` would run a build wherever the stack happens to be
  scheduled, outside all of it. Refused with "compose stacks run images; build
  it as an Application, or reference a built image".
- **`container_name:`** — it is absolute, so the same stack in `staging` and
  `production` on one server collides on the name and the second one fails at
  create time with a message that names Docker, not the environment. Compose
  already generates predictable per-project names.

Everything else — `privileged`, host mounts, `network_mode: host`, `cap_add`,
`pid: host` — is **allowed**, because those are precisely the capabilities this
resource exists to provide (the feature matrix names "a command override, a
host mount, or privileged access" as the reason ~130 templates need it).
Allowing them is a real widening of what a project member can reach, so it is
paid for with rank and recorded as an accepted risk rather than pretended away
— see §7 and threat-model §5.15.

Beyond those, validation is structural only: it must parse as YAML, and it must
declare at least one service. The panel does not reimplement the Compose
Specification; a file compose rejects fails at converge with compose's own
words, which are better than any paraphrase.

## 4. Convergence

The agent owns one fixed invocation:

```
docker compose --project-name cypher-<stack id> --file <spec>.yml \
  --env-file <env> up --detach --remove-orphans --wait
```

- `--project-name` is derived from the stack id, so two stacks never collide
  and the project is findable again after an agent restart with no local state
  — identity is on the host, as everywhere else in the reconciler.
- `--remove-orphans` is what makes the file authoritative: a service deleted
  from the file is removed from the host, which is absence-means-remove *inside*
  the stack.
- `--wait` makes the converge honest: it returns when the containers are
  running (and healthy, where the file declares a healthcheck), so the status
  the agent reports is an observation rather than "we asked".

**Absence-means-remove across stacks** is the outer half: a `cypher-`-prefixed
compose project on the host that no `ComposeSpec` in `DesiredState` names is
brought down. Volumes are **never** removed by convergence, the same rule
managed databases follow — a stack that disappears from desired state because
of a plane-side mistake must not take its data with it. Deleting the stack
through the API removes them, and only when the caller says so.

**Converge-twice is a no-op** because `up -d` is: with the file unchanged,
compose reconciles nothing. That is checked in the agent tests the same way the
Application reconciler's is.

`docker compose` absent from the host is reported as exactly that — a status
detail naming the missing plugin — rather than an opaque exec failure. Every
host the panel's own installer touched has it, because `get.docker.com` ships
`docker-compose-plugin`.

## 5. Routing

A compose file's own Traefik labels **cannot** work here, and it is worth being
explicit about why rather than letting an operator discover it: the managed
Proxy runs with the **file provider only** and no docker provider, deliberately,
because a docker provider would mean mounting the Docker socket into Traefik —
and [ADR-004](../adrs/ADR-004-traefik-file-provider.md) keeps Traefik's API and socket access
off the table.

So a stack routes the way everything else does: it names **which service and
port** answers, and the plane emits the same route fragment an Application gets,
pointed at that service's container on the environment network. Domain
verification, TLS state and the certificate resolver all apply unchanged,
because it is the same fragment writer.

A stack with no `route_domain` publishes nothing through the Proxy. Its services
still reach each other, and anything it publishes with `ports:` is published by
compose as the file asks.

## 6. Status

Observed, per ADR-005, in the same six-word vocabulary as an Application
(ui-principles §5): `running` when every service compose was asked for is up,
`degraded` when some are, `error` when the converge failed (carrying compose's
own last words), `stopped` before the first deploy. The plane records what the
agent reports and never guesses.

`compose.crashed` / `compose.recovered` are **not** added to the event taxonomy
in this spec. `app.crashed` fires on a transition of one container with one
identity; a stack is several, and "what does crashed mean for three of five
services" is an alerting-policy question that
[deployment-control.md](deployment-control.md) §7 already put out of scope. The
status is visible and the inbox is not lied to; paging on it waits for the
alerting feature.

## 7. API and authorization

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/environments/{id}/compose-stacks` | member | |
| `POST /api/v1/environments/{id}/compose-stacks` | **team admin** | carries the first compose file |
| `GET /api/v1/compose-stacks/{id}` | member | |
| `PATCH /api/v1/compose-stacks/{id}` | **team admin** when the file or route changes; member otherwise | |
| `DELETE /api/v1/compose-stacks/{id}` | **team admin** | `?delete_volumes=true` to reclaim data |
| `POST /api/v1/compose-stacks/{id}/deploy` | member | new revision from the current file, then converge |
| `GET /api/v1/compose-stacks/{id}/revisions` | member | the history |
| `POST /api/v1/compose-stacks/{id}/rollback` | member | re-point at a revision |
| `GET /api/v1/compose-stacks/{id}/logs` | member | SSE, `?since=` as everywhere |
| `GET/PUT/DELETE /api/v1/compose-stacks/{id}/env/{key}` | member | keys only on read (rule 20) |

**Writing the file is team admin; deploying one is a member.** That split is
the whole authorization story. A compose file can ask for `privileged: true`
and `/:/host`, which is root on the node — a capability an Application cannot
express, so it must not be reachable at the rank that deploys an Application.
Once an admin has written and reviewed the file, redeploying it grants nothing
new, so a member (and a CI token with `deploy`) can do that, exactly as they can
for an application.

Every mutation is audited: `compose_stack.created`, `.updated`, `.deleted`,
`compose_stack.deployed`, `.rolled_back`. The detail records that the file
changed, never its content — a compose file can contain an inline secret an
operator put there, and the audit log is not the place for it to become
permanent.

## 8. Deliberately out of scope

- **Compose `build:`.** §3. It belongs to the builder-role architecture, not to
  a target host.
- **Per-service routes.** One stack routes one service. Several domains
  pointing into one stack is a real request and a bigger design (it needs a
  route table per stack, and a UI that can show it); a stack that needs it can
  publish ports today.
- **Reading a stack's compose file back through the template importer.**
  ADR-007 left "whether the Compose Stack eventually shares the importer"
  explicitly undecided, and it stays undecided.
- **Swarm-mode `deploy:` keys.** ADR-006 is docker-only; compose ignores them
  outside Swarm and so does the panel.
- **Scheduled tasks, backups and preview environments on a stack.** Each hangs
  off a single container's identity today. They are not refused by accident —
  they are not offered.

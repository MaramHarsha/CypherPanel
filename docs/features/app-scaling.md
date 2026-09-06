# Feature spec: Application replicas and placement

> Running one Application as **N containers across the servers a team already
> has**, load-balanced by the Proxy on each node, with an optional rule that
> moves N on its own. Manual replica count first; the autoscale rule is
> designed here and ships on top of metrics-and-usage, which does not exist
> yet.
>
> This is roadmap step 1 of the post-v1 scale-out ladder, and it takes step 3
> as far as it can go without step 2 (cloud provisioning) — deliberately not
> one inch further.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What is actually being added, and in what order

Today an Application is one container on one Server. `runtime_replicas` has
existed in the schema since migration `0002` and the API has refused anything
but `1` ever since — *"runtime.replicas must be 1 (multiple replicas are
post-v1)"*. This spec is what that sentence was waiting for.

Three stages, each useful on its own and each shippable alone. That ordering
is not project management, it is the argument: every stage after the first
costs something an operator can decline.

1. **N replicas on one server** (§2, §3, §7, §8). Multi-core use for a
   single-threaded runtime, survival of one container dying, and a rollout
   that replaces containers one group at a time. Needs no new networking, no
   new ports, no new address to configure, and no image movement. This is the
   whole feature for the personas who run one or two VPSes.
2. **Replicas across servers** (§4, §5, §6). Capacity beyond one host, and
   survival of a *server* dying. Costs an operator-declared internal address
   per server, one more listening port on each node, and an image transfer
   the relay does not do today.
3. **The autoscale rule** (§9). Costs a metrics pipeline that does not exist,
   and it is the only part of this that acts without a person.

### 1.1 Where this strains the vision, and how it is resolved

[vision.md](../vision.md) puts *"a Kubernetes distribution or general cluster
manager"* on the explicitly-out-of-scope list, and a placement strategy over a
fleet with an autoscaler attached is precisely the silhouette of one. The line
is worth taking seriously rather than waving at.

The resolution is that this adds **no scheduler**. There is no pod, no node
selector, no taint, no affinity rule, no eviction, no rebalancing loop, and no
extension point where one could be added without another spec. Placement is
two strategies (§4) over servers the operator registered by hand, computed
when the replica count or the eligible set changes and then **stored and left
alone**. The control plane still never runs a workload
(non-negotiable 5), it is still one control-plane node
(the multi-region exclusion is untouched), and the anti-persona — enterprise
platform teams wanting fleet management — gets nothing here they would
recognise as their tool.

The spread strategy is deliberately dumb for that reason, and §4.3 records what
was rejected to keep it that way.

[ADR-006](../adrs/ADR-006-docker-only-at-launch.md) is the other thing to
check. Its consequences say replica scaling before the Swarm driver *"is
limited to what standalone Docker can express (N containers behind the local
proxy)"*. This spec goes one step past what that sentence pictured — the
containers are behind the local Proxy on **several** nodes — but it does not
re-litigate the ADR: everything orchestrator-shaped stays inside
`agent/driver/docker` and `agent/proxy`, there is no `if swarm` anywhere, and
the plane-side model (a replica count, a placement, a rule) is driver-neutral
by construction. When the Swarm driver lands, the same stored placement
becomes a service replica count on a Swarm server and nothing above the driver
changes. That is the second-implementation check ADR-006 asked for, arriving
early.

## 2. A replica count is desired state, not an action

The whole feature is one integer in Postgres and a reconciler that already
knows how to make reality match. Nothing here is a verb the agent obeys
(hard rule 3, [ADR-005](../adrs/ADR-005-desired-state-reconciliation.md)).

```sql
-- 0040_app_scaling.sql
ALTER TABLE applications
  ADD COLUMN placement_strategy TEXT NOT NULL DEFAULT 'spread';   -- 'spread' | 'pinned'
-- runtime_replicas already exists (0002) and is already 1 everywhere.

CREATE TABLE application_placements (
  application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  server_id      TEXT NOT NULL REFERENCES servers(id)      ON DELETE RESTRICT,
  replica_index  INTEGER NOT NULL,          -- 1..N, stable, plane-assigned
  image_ready    BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (application_id, replica_index)
);
```

`ON DELETE RESTRICT` on `server_id` for the same reason `registries` uses it:
a server cannot vanish from under an application that is placed on it, and the
`409` names the applications rather than leaving an operator to guess.

**Replica index is plane-assigned and stable.** Index 1 is the application's
first replica, wherever it lives; the panel displays a replica as
`<app name>-<index>` (`web-1`, `web-2`, `web-3`), so an operator can say "web-2
moved to helsinki" and mean something. Indexes are dense: scaling 3 → 5 adds 4
and 5, scaling 5 → 3 removes 5 then 4. Removing from the top is the only rule
that makes a scale-down predictable, and predictability is worth more here than
any cleverness about which container looks least busy.

**The container name carries the index, and index 1 keeps today's name.**
`containerName` becomes `cypher-<app>-<rev>[-<restart token>][-r<index>]`, with
the `-r<index>` segment omitted for index 1. That is not cosmetic: without it,
upgrading the agent would make every existing container in every fleet read as
drift and get recreated on the next reconcile, turning a version bump into a
fleet-wide rolling restart. Identity still comes from labels, never the name
(`agent/driver/driver.go`), and a new label `cypherpanel.replica-index` joins
the management set so a driver can discover its own replicas on a host it has
never seen.

On the wire, `AppSpec` is already per-application-per-server, so it gains the
indexes **this** server should run rather than a count:

```proto
// In AppSpec (additive, field numbers never reused — ENGINEERING 18):
repeated uint32 replica_indexes = 18;  // empty = [1], which is every app today
repeated string peer_upstreams  = 19;  // §5: host:port of this app's Proxy peers
```

An agent that predates this ignores both fields and runs exactly one container
under exactly the name it runs today. That is what makes the rollout of this
feature itself safe.

## 3. What the panel refuses, and why

Two refusals, both `400`, both naming the offending thing. They are refusals
rather than warnings because in each case there is no correct behaviour to fall
back to — only two different ways to be wrong.

**An application with a volume mount cannot have more than one replica.** Two
containers sharing one named volume on one host is one filesystem with two
writers, which is how a SQLite file or an embedded index gets corrupted. The
same volume *name* on two hosts is worse: two different volumes that silently
diverge while the panel calls them one thing. The message names the volume and
says the app is stateful. This is the design's *"Databases don't scale this
way; replicas are for stateless apps, and the panel says so when an app mounts
a volume"* said in the place it can actually be enforced.

**An application publishing raw host ports cannot have more than one replica.**
Two containers cannot bind the same host port; the alternative is the plane
allocating a different port per replica, which breaks the one contract a raw
port publish has — that the port is the number the operator wrote down.

A **Managed Database** has no scale surface at all: no route, no button, no
field. Replication is an engine-specific operation with its own failover,
promotion and split-brain semantics, and a panel that offered "replicas: 3" for
Postgres would be offering three independent databases with one name. See
[managed-databases.md](managed-databases.md); if replication is ever built it
is that spec's problem, under the engine's own vocabulary.

A **Compose Stack** has none either. Its file *is* the desired state
([compose-stacks.md](compose-stacks.md) §1), so scaling a stack is editing the
file — and the panel adding a replica count beside a file that also declares
one would create two places to say the same thing, with no rule for which
wins.

## 4. Placement

### 4.1 The eligible set

Replicas are placed on servers of the application's own team that are enrolled,
`running`, and not role `builder` (a builder runs no Applications and no Proxy
— [builder-role-and-relay.md](builder-role-and-relay.md) §1). For a placement
that spans more than one server, each of those servers must also have an
internal address (§5.2); one that does not is excluded, and the exclusion is
reported by name rather than silently dropping the app to fewer replicas.

`applications.runtime_server_id` stays exactly as it is — not patchable, still
the application's **home server**: replica index 1 lives there, DNS points
there (§5.3), and it is the anchor the builder relay already reasons about. The
migration backfills one `application_placements` row per existing application
from it, so the day the feature merges, every desired set on every node is byte
for byte what it was the day before.

### 4.2 The two strategies

- **`spread`** (default) — replicas are dealt round-robin over the eligible
  servers, **least-loaded first**. "Least loaded" is the count of replicas
  already placed there by this panel, not a live metric: see §4.3.
- **`pinned`** — the operator names an ordered list of servers and replicas
  are dealt over exactly those. A pinned server that stops being eligible is an
  error the operator must resolve, not something the plane routes around; that
  is what pinning *means*.

### 4.3 Placement is computed once and then stored

The tempting design is to compute placement on every desired-state sync from
live load. It is wrong, and the reason is worth writing down: desired state
that is recomputed from a moving input is not desired state. Replicas would
migrate between hosts every time a metrics tick moved, each migration is a
container start, a health gate, an image transfer and a route change, and the
system would spend its life converging on a target that moved while it
converged.

So the planner runs on **changes to the inputs it owns** — the replica count,
the strategy, the pin list, and the eligible set — writes rows, and stops. A
placement is stable until something a person or a rule did changes it.

The one exception is loss: a server that leaves the eligible set (deleted,
un-enrolled, or `error` beyond the offline grace) has its replicas **re-placed
onto the remaining eligible servers**, because the alternative is desired state
that names a host nobody is converging. It is a re-placement, not a
rebalancing: replicas already running elsewhere are never moved, so a server
coming back does not cause a second round of churn. There is no periodic
rebalancer, and adding one is a decision, not a refinement.

## 5. Getting traffic to N replicas

### 5.1 The fragment

Nothing about routing leaves the file provider ([ADR-004](../adrs/ADR-004-traefik-file-provider.md)):
the Docker provider stays banned, and this is expressible without it. The
per-application fragment's service gains more than one server:

```yaml
http:
  routers:
    <app_id>:                       # public, as today (web / websecure)
      rule: Host(`app.example.com`)
      service: <app_id>
    <app_id>-peer:                  # §5.2 — local replicas only
      rule: Host(`app.example.com`)
      entryPoints: [peer]
      service: <app_id>-local
  services:
    <app_id>:
      loadBalancer:
        servers:                    # local replicas, then one entry per peer node
          - url: http://172.18.0.5:8080
          - url: http://172.18.0.6:8080
          - url: http://10.0.1.7:7080
        healthCheck: { path: /, interval: 5s, timeout: 2s }
    <app_id>-local:
      loadBalancer:
        servers: [ ... local only ... ]
        healthCheck: { path: /, interval: 5s, timeout: 2s }
```

The **active health check is what makes the design's promise true** — *"one
down means traffic shifts, not an outage"*. The plane also removes a dead
replica from desired state once it observes it, but that path is a heartbeat
plus a sync cycle wide; Traefik ejecting a failing server takes one interval.
The two are not redundant: the health check handles seconds, the plane handles
minutes and is the only one that can decide to start a replacement.

The health check is written only for `health.kind = http`, because Traefik's
load-balancer health check is HTTP. A `tcp` or `none` health kind gets no
active check and relies on the plane's observation alone — stated here rather
than discovered later.

### 5.2 The peer entrypoint

A container's IP on `cypher-<environment_id>` is reachable from its own host
and nowhere else. Standalone Docker has no overlay network — that is Swarm, and
ADR-006 says not yet — so a node's Proxy cannot dial a container on another
node.

**Decision: a node reaches another node's replicas through that node's own
Proxy, on a dedicated `peer` entrypoint whose routers serve local replicas
only.** Traefik gains a third entrypoint on `:7080` (`CYPHER_PEER_PORT`),
published bound to the server's internal address only — never `0.0.0.0`. The
`-peer` router serves `<app>-local`, so a request that arrives on `peer` is
answered locally and never forwarded again: **one hop, loop-free by
construction** rather than by a header convention that has to be right.
`passHostHeader` is Traefik's default, so the peer's own `Host` rule matches
without anything being rewritten.

This needs the plane to know where a server is reachable *from other servers*,
which is not what `public_address` means:

```sql
ALTER TABLE servers ADD COLUMN internal_address TEXT NOT NULL DEFAULT '';
```

Operator-supplied, for exactly the reason [dns-automation.md](dns-automation.md)
§3.4 gives for `public_address`: the agent dials out (ADR-002), the heartbeat
carries no address, and a source address seen through NAT is not the address a
peer would use. Empty means "cannot host a replica of an application that is
spread across servers", reported as a named, actionable state.

**Cost, stated plainly:** traffic between nodes is plain HTTP on that network.
If an operator sets `internal_address` to a public IP, replica traffic crosses
the public internet unencrypted, and the panel says so on the server screen
rather than assuming a private network exists. The considered alternative —
mTLS on the peer entrypoint using the agent's own certificate — was rejected
for now because it puts the agent's identity key material inside the container
that terminates public traffic, so a Traefik compromise would become an agent
compromise; doing it properly means a second certificate from the agent CA with
its own usage, which is a change to
[agent-identity-and-tls.md](agent-identity-and-tls.md) and needs its own spec.

Rejected alternatives for the same problem:

- **Publish each replica's port on the host and point peers at
  `address:port`.** Rejected twice over: it reopens exactly the inbound app
  ports [routing-and-tls.md](routing-and-tls.md) §4 deliberately avoids, it
  needs plane-side port allocation, and — decisively — a host port cannot be
  held by two containers, so the surge-then-flip rollout of §7 becomes
  impossible and every deploy gets a gap.
- **One shared network across all hosts (WireGuard, or a Docker overlay).**
  Rejected: an overlay is Swarm, and a WireGuard mesh is a second piece of
  infrastructure to install, key, and debug — "installing CypherPanel should
  feel like adding a tool, not adopting a platform".
- **DNS round-robin only, each node serving its own replicas.** Rejected as
  *sufficient*, kept as *complementary*: a client whose chosen A record is dead
  hangs until it retries, which is not "traffic shifts, not an outage". It also
  cannot balance within a node's share.

### 5.3 Ingress and TLS stay on one node

Every node holding replicas writes the same spreading fragment, so which node
receives public traffic is a DNS decision rather than a configuration one. In
practice **DNS points at the home server, and that is where the certificate
is**: Let's Encrypt HTTP-01 answers on whichever node the validator reached, and
with several A records for one host that is a coin flip per attempt against a
rate limit. Certificates live on the serving node and never on the plane
(ADR-004), and DNS-01 wildcards are a non-goal
([routing-and-tls.md](routing-and-tls.md) §10), so there is no way today to
give every node a certificate for the same domain.

The honest consequence, which the panel must state and this spec must not
oversell: **replicas do not make the home server redundant.** They buy capacity,
they survive a replica dying, and they survive a *non-home* server dying. If the
home server is down, the domain is down even though replicas elsewhere are
healthy. Real ingress redundancy needs DNS-01 wildcards or an external load
balancer, and both are named in §13.

## 6. Getting the image to a server that has never run it

A scale-out onto a new server is not a config edit: that host has no
`cypher/<app>:<revision>` image, and ADR-008 means there may be no registry to
pull it from. The existing relay moves an image builder → plane → target, but
its authorization is per *deployment*
([builder-role-and-relay.md](builder-role-and-relay.md) §5) and a scale-out is
not a deployment.

**Decision: the placement row is the authorization.** A placement written for a
server that lacks the image is created `image_ready = false`, and:

- `PullImage` is authorized when the caller's CN has a placement row for that
  application at that revision with `image_ready = false`;
- `PushImage` is authorized for a server whose placement for the same
  application is already `image_ready = true` — every one of them holds the
  image locally, so the source of a scale-out transfer is a peer, not
  necessarily the builder;
- the plane picks one ready server as the source and publishes the existing
  `PushImageWork` / `DistributeWork` pair; on the target's `STAGE_DISTRIBUTE`
  success the row flips to `image_ready = true` and only then does the spec
  reach that node.

The relay session key widens from a deployment id to a transfer key, which is
two additive fields (`ImageChunk.transfer_id`, `PullImageRequest.transfer_id`)
and no renumbering. A deploy that fans out to M servers opens M sessions, one
per target, because the plane may not buffer an image tar (ADR-008: never on
control-plane disk) and therefore cannot fan one stream out to many. **That is
a real cost: deploying a 400 MB image to four servers moves 1.6 GB through the
plane.** It is bounded, it is transient, and an operator who dislikes it has
the answer ADR-008 path 3 already gives — configure a registry, and every
target pulls it directly.

A scale-out is deliberately **not** a Deployment: no revision is minted, no
deployment row appears, `desired_revision_id` does not move, and a freeze
window does not gate it — the same argument
[deployment-control.md](deployment-control.md) §3 makes for restart. A freeze
exists to stop new code shipping and a scale-out ships none; refusing to add
capacity during a freeze would make the freeze the outage.

## 7. Rolling out, scaling out, and draining

**Per node, a rollout is surge-then-flip, exactly as today.** All of that
node's new-revision replicas start alongside the old ones, every one passes its
health gate, the fragment is rewritten once, and only then are the old
containers drained. The alternative — replacing replicas one at a time and
letting the fragment carry a mix — was rejected because it makes two revisions
of an application serve one node's traffic for the whole roll, and a client can
see an old and a new API response in the same session. The cost of surge is
peak memory: a rollout of an application with N replicas on one host needs
headroom for 2N, and the panel shows that arithmetic beside the replica
stepper rather than letting an operator discover it through an OOM kill.

**Across nodes, mixed revisions during a rollout are unavoidable, and the spec
says so instead of pretending.** Each node converges independently; there is no
coordinated flip, and inventing one would be a distributed transaction this
architecture deliberately does not have. For the seconds-to-a-minute a
multi-server rollout takes, some replicas serve the old revision and some the
new. Single-server applications — every application that exists today — are
unaffected, and the panel states the window on the Scale screen only when
replicas span more than one server.

**A multi-server rollout has no atomic rollback either.** The deployment
succeeds when *every* placed server reports the new revision serving, and fails
on the first `STAGE_ROLLOUT` failure from any of them — at which point some
nodes are serving the new revision and the failed one is still serving the old.
The recovery is the rollback that already exists: re-point desired state and let
every node converge. Naming this is better than a "partially deployed" status
nobody can act on differently.

**Scale-in drains before it stops.** The driver removes the departing index
from the fragment *first*, waits the existing drain timeout so Traefik stops
sending and in-flight requests finish, and only then stops and removes the
container. Doing it in the other order drops requests, and "scale-in draining"
is a day-one requirement the roadmap wrote down before this spec existed.

Two mechanical details that are easy to get wrong: work-item message ids must
become per-server (`<deployment>.rollout.<server_id>`, and the same for
distribute and push) or JetStream's dedup window silently swallows every fan-out
after the first; and `ConvergeApp` must publish to every placed server rather
than to `app.Runtime.ServerID`.

## 8. What is observed, and what the application's status means

`AppStatus` gains a repeated per-replica observation (additive), reported by the
node that owns those replicas:

```proto
message ReplicaStatus {
  uint32 index = 1;
  string container_id = 2;   // short id, for the panel and for §11
  string revision_id = 3;
  string state = 4;          // the ui-principles §5 vocabulary
  string detail = 5;
  google.protobuf.Timestamp started_at = 6;
}
```

The plane upserts these into `application_replicas` (keyed application + index)
and sweeps rows for indexes no longer desired. That table is what the PLACEMENT
card reads: `DESIRED 3, RUNNING 3`, a card per replica with its server and its
state.

**The application's aggregate status becomes derived, and needs no new
vocabulary.** Running iff every desired index is running; `degraded` if some but
not all; `error` if none; `deploying` while a deployment is in flight —
precisely the reading [compose-stacks.md](compose-stacks.md) §6 already gives
`degraded` for a partially-up stack. For a one-replica application this is
identical to what `HandleAppStatus` records today.

That has a consequence worth stating out loud, because it is the point of the
whole feature: **losing one replica of three is a degradation, not a crash.**
`app.crashed` fires only on `running` → `error`
([deployment-control.md](deployment-control.md) §5), so a partial loss pages
nobody, which is correct — the application is still serving — and the operator
sees amber in the panel and an inbox item. An operator who wants to be paged for
partial loss is asking for an alerting policy, which §13 keeps out of scope.

## 9. The autoscale rule

Everything above ships without this. This section is the design, and its
implementation is gated on **metrics-and-usage**: there is no metrics collection
in the repository today — no container stats sampling in the agent, no
Prometheus endpoint on the Proxy, nothing. Writing an autoscaler before the
signal exists would be writing a controller that reads zeros.

### 9.1 The rule

One row per application (`0041_app_autoscale.sql`, when metrics land):

```sql
CREATE TABLE application_autoscale (
  application_id        TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
  enabled               BOOLEAN NOT NULL DEFAULT FALSE,
  min_replicas          INTEGER NOT NULL,
  max_replicas          INTEGER NOT NULL,
  signal                TEXT    NOT NULL,   -- cpu | memory | requests_per_second | p95_latency_ms
  up_threshold          NUMERIC NOT NULL,
  up_for_seconds        INTEGER NOT NULL,
  down_threshold        NUMERIC NOT NULL,
  down_for_seconds      INTEGER NOT NULL,
  cooldown_seconds      INTEGER NOT NULL DEFAULT 300,
  updated_at            TIMESTAMPTZ NOT NULL
);
```

It reads as the sentence the design screen writes: *"Keep between 2 and 6
replicas. Add one when p95 latency stays above 400ms for 3 min"* / *"Remove one
when below 150ms for 15 min"*. Four signals, all of them things the platform can
already see from outside the workload — *"cpu · memory · requests/s · p95
latency — all measured at the proxy and agent, nothing to instrument in your
app"*. CPU and memory come from the agent's container stats; requests/s and p95
latency come from the Proxy's own metrics for that application's service. An
application whose signal has no data does not scale, in either direction, and
says so.

**One rule, one signal, one replica at a time.** Two rules on one application
means writing a conflict-resolution policy, and every autoscaler that has one
is remembered for it. A step of one is what makes the cooldown a real brake
rather than a suggestion.

### 9.2 The three brakes, and why they are asymmetric

- **Bounds.** `min_replicas` ≥ 1 and `max_replicas` is a hard ceiling. Nothing
  provisions a server: *"It never provisions hardware without a rule you
  wrote."*
- **Cooldown**, default 300s: *"one scaling action at a time, never a flap."*
  It runs after **every** action including a manual one, so a person and the
  rule cannot fight inside one window.
- **Asymmetric windows.** `down_for_seconds` must be ≥ `up_for_seconds` and the
  API refuses otherwise; the defaults are 3 min up and 15 min down. Scaling up
  late costs latency for a few minutes; scaling down early costs an outage
  under the load that has not actually left yet. The two mistakes do not cost
  the same, so the timings are not the same — the same reasoning
  [disk-management.md](disk-management.md) §2 uses for its retain set.
- **Deadband.** `down_threshold` must be strictly below `up_threshold`, so the
  two conditions can never both be true.

### 9.3 The controller

One goroutine on the plane with an owner, a cancellation path and observable
failure (ENGINEERING rule 7), evaluating enabled rules on a fixed interval.

**It writes desired state and nothing else.** It calls the same internal scale
path the API calls, with a `system` actor and the rule that fired as the
reason; the placement planner, the reconcilers, the relay and the Proxy do not
know it exists. That is exactly the roadmap's framing — *"a controller that
edits desired state from agent metrics"* — and it is why an autoscaler does not
strain ADR-005: it is a writer of desired state on the same footing as a person
clicking Apply.

At the ceiling with the signal still above the threshold, it writes one inbox
item and one `app.scale_ceiling` event **on the transition into that state**,
never per evaluation — the rule
[disk-management.md](disk-management.md) §5 states for `server.disk_low`, for
the same reason: a channel that repeats itself gets muted, taking the next real
alert with it. The design screen's *"suggest cloud burst"* is a link to a
feature that does not exist and is not proposed here (§13).

Like a scale-out by hand, an autoscale action is not gated by a freeze window
(§6), and it never crosses `max_replicas` regardless of what the signal says.

## 10. API and authorization

| Route | Ability | Rank | Notes |
|---|---|---|---|
| `PUT /api/v1/applications/{id}/scale` | `deploy` | member | `{replicas, strategy, servers[], reason}`; `400` for a volume or host-port app (§3); `409` before the first deploy |
| `GET /api/v1/applications/{id}/placement` | `read` | member | desired count, strategy, and the per-replica observed rows of §8 |
| `GET /api/v1/applications/{id}/autoscale` | `read` | member | the rule, or absent |
| `PUT /api/v1/applications/{id}/autoscale` | `deploy` | member | §9; `400` for an inverted deadband or a down-window shorter than the up-window |
| `DELETE /api/v1/applications/{id}/autoscale` | `deploy` | member | removes the rule; the replica count stays where it is |
| `PATCH /api/v1/servers/{id}` | `admin` | admin | gains `internal_address` (§5.2) |

Scaling is `deploy`-ability at member rank for the same reason restarting is:
it is a production action with a visible effect, and the credential that can
ship code can change how much of it runs. It is a **dedicated route rather than
a section of `PATCH /applications/{id}`** because that patch replaces sections
wholesale and has always documented replicas as not patchable, because scaling
carries its own audit action and its own refusals, and because the autoscale
controller needs one code path with a `system` actor that is not "pretend to be
a user editing an application".

`AppRuntime.replicas` loses the words *"Fixed at 1 for v1"* and gains its real
range; `Application` gains `placement_strategy` and the derived status stays in
the ui-principles §5 vocabulary. OpenAPI is edited first and the handlers follow
it (ENGINEERING rule 19).

## 11. Audit, events, and the history feed

The design's History screen is not a new store. *"Same feed lands in the audit
log"* is the design, so the feed **is** the audit log filtered to this
application — `GET /api/v1/audit?resource_id=<app>` already exists, and the
scaling actions join the closed vocabulary in `core/audit/actions.go`:

- `application.scaled` — from, to, strategy, the actor, and the reason. A
  person's reason is the note they typed (*"expecting the newsletter bump"*); a
  rule's reason is the rule that fired (*"p95 above 400ms for 3m"*).
- `application.replica_replaced` — a `system` actor, written when a replica's
  container id changes **at an unchanged revision with no deployment in
  flight**. That condition is what keeps a rollout — which replaces every
  container by design — out of the feed; without it a deploy of six replicas
  would write six "replaced" entries and the feed would be useless. The
  precedent for a `system` actor is preview environments in
  [audit-log.md](audit-log.md).

Two keys join the **subscribable** taxonomy in `core/domain/notify.go`, which is
one edit that notifiers, outbound webhooks and the inbox all gain at once:
`app.scaled` (info) and `app.scale_ceiling` (warning).

One correction to the design copy, because the spec may not promise what does
not exist: there is no inbox **digest**. Items land in the inbox one per event,
as everything else does. A digest is a notification-inbox feature and belongs to
that spec.

## 12. What this costs, collected in one place

Because a spec that only lists benefits is not a design document.

- **Peak memory during a rollout doubles** for the node being rolled (§7).
- **A deploy to M servers moves the image M times through the plane** (§6).
- **One more listening port per node**, on a network the operator asserts is
  private, carrying plaintext HTTP (§5.2).
- **Ingress is still one node**, so replicas do not remove the home server as a
  single point of failure (§5.3).
- **Two revisions serve at once** during a multi-server rollout (§7).
- **A new operator-supplied field** (`internal_address`) that, left unset,
  silently means "this server cannot join a spread placement" — which is why it
  is reported by name rather than by absence.
- **The status derivation changes** for every application, including
  one-replica ones, so it has to be identical for them or the whole fleet's
  status flickers.

## 13. Deliberately out of scope

- **Cloud burst and cloud provisioning** (design screens 11c / 13at). Renting a
  server for a spike is roadmap step 2, it spends the operator's money
  automatically, and it needs a provider credential, a cost cap and a return
  path. Nothing here provisions anything; the ceiling notification links to a
  feature that must be argued in its own spec.
- **Autoscale implementation**, until metrics-and-usage lands (§9). The schema
  and the brakes are designed here so the metrics spec knows what it owes.
- **Per-replica CPU, memory and request share** on the placement card — the
  `cpu 41% · 212 MB` and `34%` figures in the design. Same dependency; the card
  ships with the replica, its server and its state, and gains the numbers when
  there are numbers.
- **More than one ingress node.** Needs DNS-01 wildcard certificates (a
  [routing-and-tls.md](routing-and-tls.md) §10 non-goal) or an external load
  balancer. Named, not attempted.
- **mTLS between peer Proxies** (§5.2) — needs a second certificate from the
  agent CA, so it is a change to agent identity, not a flag here.
- **Rebalancing.** No periodic loop moves a running replica to a less loaded
  server (§4.3). Re-placement on server loss is the only movement.
- **Sticky sessions.** An application that needs them needs shared session
  state instead, and offering a cookie-affinity toggle would make the panel
  complicit in the version that breaks under a scale-in.
- **Replicas for Managed Databases and Compose Stacks** (§3), and multi-signal
  or multi-rule autoscaling (§9.1).
- **Alerting policy** — thresholds, repeat suppression, escalation. `app.scaled`
  and `app.scale_ceiling` are events, not an alerting system; the muting that
  exists is the inbox preference list.

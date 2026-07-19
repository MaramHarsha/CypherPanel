# Feature spec: Builder role & multi-server image relay

> How a build lands on a server that didn't build it. The `--role` flag splits
> build from run duties across agents, and built images move builder → control
> plane → target as a **transient mTLS gRPC relay** — bounded buffers, never on
> plane disk, no registry required ([ADR-008](../adrs/ADR-008-no-registry-required.md)).
> This closes the last code gap in the Phase 2 vertical slice
> ([roadmap.md](../roadmap.md)).
>
> Written 2026-07-19, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). A previous scaffolding attempt was removed at
> `f1df8ae` for the reasons its commit message records; this spec is the
> contract the real implementation is built and tested against — including the
> two-daemon integration scenario (§8) that attempt lacked.

## 1. Roles

A server's **role** is what its agent volunteered at start and determines
which work its agent performs:

| Role      | Builds images | Runs Applications + Proxy |
|-----------|---------------|---------------------------|
| `all`     | ✅            | ✅                        |
| `builder` | ✅            | ❌                        |
| `worker`  | ❌            | ✅                        |

- Set by `cypher-agent run --role=all|builder|worker` (default `all` —
  single-server setups are unchanged, and every existing agent keeps its
  exact current behavior).
- Reported in every `Heartbeat` (new additive field) and recorded on the
  server row. A server that has never heartbeated has role `all` by default.
- **`builder`**: the agent constructs only the builder and worker loop — no
  docker reconciler, no Proxy (nothing binds :80/:443 on a builder host), no
  runtime-log streamer. Its desired set is always empty (no Application
  targets it), so there is nothing to reconcile.
- **`worker`**: the agent constructs everything except nothing — it keeps the
  full runtime stack but the scheduler never routes build work to it. (The
  existing "builder is nil" guard stays as defense in depth but is not the
  contract; routing is.)

The role is the agent's assertion, not an enrollment property: moving a host
between roles is a flag change and restart, no re-enrollment. A compromised
agent lying about its role gains nothing — role only *attracts* build work;
every relay operation is authorized per-deployment against the recorded
builder/target (§5).

## 2. Scheduler routing

### Build placement

When a deployment needs a build, the scheduler picks the **builder server**:

1. **Target first (local path).** If the app's server has role `all`, it
   builds its own image — builder = target, zero transfer, exactly today's
   behavior (ADR-008 path 1).
2. **Else a dedicated builder.** The first `running` server with role
   `builder` (falling back to any other `running` `all` server). "First" is
   deterministic (creation order); load-aware placement is a Later concern.
3. **Else fail fast.** No eligible builder → the deployment fails with
   `no builder available: target server has role worker and no builder-role
   server is running`. Nothing queues forever.

`BuildWork` is published on `work.<builder_server_id>.build`, and the chosen
builder is persisted on the deployment row (`builder_server_id`, §4) — it
pins event and relay authorization and survives plane restarts.

### Pipeline stages

Builder = target: `queued → building → rolling_out → succeeded` — the
`distributing` stage is skipped entirely, as today.

Builder ≠ target, on the builder's `STAGE_BUILD` success event:

1. Deployment advances to `distributing`.
2. The scheduler publishes **two** work items:
   - `PushImageWork` on `work.<builder_id>.push` — "stream this image to the
     plane relay for this deployment";
   - `DistributeWork` on `work.<target_id>.distribute` — "obtain this image
     from the plane relay, then report".
3. On the **target's** `STAGE_DISTRIBUTE` success event, the deployment
   advances to `rolling_out` and the rollout work item is published as
   always. A `STAGE_DISTRIBUTE` failure from either side fails the
   deployment.

`STAGE_DISTRIBUTE` already exists in `DeployEvent` (reserved for this since
the deploy spec); `distributing` already exists in `DeploymentStatus`.

## 3. The relay: mTLS gRPC, transient, bounded

ADR-008: "streamed builder → control plane → target over the existing mTLS
gRPC channel as a transient relay — bounded buffers, never written to
control-plane disk." NATS/JetStream is explicitly **not** the transport:
image tars are far larger than any sane memory stream budget, and a bounded
stream silently corrupts what it evicts (the removed attempt's fatal flaw).

### Service

A new `ImageRelayService` (additive proto, same package) served on the
existing enrollment gRPC listener:

```proto
service ImageRelayService {
  // Builder-side: client-streams the docker-save tar for one deployment.
  rpc PushImage(stream ImageChunk) returns (PushImageResponse);
  // Target-side: server-streams that tar back out.
  rpc PullImage(PullImageRequest) returns (stream ImageChunk);
}
message ImageChunk { string deployment_id = 1; bytes data = 2; }  // ≤1 MiB data
message PullImageRequest { string deployment_id = 1; }
message PushImageResponse {}
```

The listener's TLS gains `VerifyClientCertIfGiven` against the agent CA:
`Enroll` still works without a certificate (first contact, join-token gated),
while both relay RPCs **require** a verified client certificate and take the
caller's server ID from its CN — the same identity NATS trusts (ADR-002).

### Plane rendezvous

The plane holds a per-deployment **relay session**: whichever side arrives
first waits (bounded, §6) for the other; then chunks flow pusher → puller
through an `io.Pipe`. gRPC/HTTP-2 flow control gives end-to-end backpressure,
so plane memory per transfer is a handful of chunks regardless of image size,
and nothing is ever buffered to disk. When the pull stream completes (or
either side errors), the session is torn down; there is no replay — retry is
a fresh session (§6).

### Agent sides

- **Builder** (`.push` work): `docker save` via the engine API
  (`GET /images/{name}/get`), chunked into the `PushImage` stream. If the
  plane answers "relay not needed" (deployment no longer `distributing` —
  e.g. the target already loaded the image on a previous attempt), the item
  acks as success.
- **Target** (`.distribute` work): if the engine already has the image
  (`HasImage`) the item succeeds immediately — this is the idempotency anchor
  for redelivery and crash recovery. Otherwise `PullImage`, streamed into
  `docker load` (`POST /images/load`), then `STAGE_DISTRIBUTE` success.

Agents dial the relay at the plane address they enrolled against: `enroll`
now persists `--plane` into the identity state (additive JSON field).
Already-enrolled agents lack it — set `CYPHER_PLANE_ADDR` or re-enroll; the
agent refuses relay work with a clear error otherwise. Single-server (`all`)
setups never need it.

## 4. Data model

Two additive migrations:

- `servers.role text NOT NULL DEFAULT 'all'` — recorded from heartbeats,
  exposed read-only in the server DTO (`role`).
- `deployments.builder_server_id text NULL` — set when the scheduler routes a
  build to a server other than the target; `NULL` means builder = target.

`domain.Server.Role string`, `domain.Deployment.BuilderServerID *string`.
No API write surface: role is agent-asserted, builder placement is
scheduler-decided.

## 5. Authorization

Per-deployment, from persisted rows — never from what the caller claims:

- `PushImage` — caller CN must equal the deployment's `builder_server_id`
  (or the app's server when `NULL`), and the deployment must be
  `distributing`.
- `PullImage` — caller CN must equal the app's `runtime.server_id`, same
  status check.
- `HandleDeployEvent` — the existing "only the app's own server" check
  widens exactly as far as the persisted builder: `STAGE_BUILD` and
  `STAGE_DISTRIBUTE` events are also accepted from
  `deployment.builder_server_id`; rollout/status events remain
  target-server-only. Blast radius of a compromised builder stays: its own
  builds (threat-model §5.2).
- NATS grants are **unchanged** — `work.<id>.>` already covers the two new
  suffixes, and no image byte ever crosses NATS.
- The image tar transits the plane but is user data in motion, not at rest:
  no plane persistence, no size accounting against plane disk (§5.9 stays
  true).

## 6. Failure modes & idempotency

| Failure | Behavior |
|---|---|
| No eligible builder at deploy time | Deployment fails immediately with a clear detail; queued successor promotes. |
| Rendezvous peer never arrives | Session times out (3 min); the waiting side's work item NAKs and redelivers; `maxDeliveries` then fails the stage → deployment fails. |
| Transfer interrupted (either side drops) | Both streams error; both work items redeliver; the retry is a fresh session. `docker load` of a truncated tar fails cleanly — a partial load never yields the tag, so `HasImage` stays false and the retry is honest. |
| Target crashed after `docker load`, before ack | Redelivered `.distribute` finds `HasImage` true → immediate success. |
| Builder redelivered `.push` after transfer done | Plane sees the deployment past `distributing` → "not needed" → ack. |
| Plane restart mid-`distributing` | `Recover` republishes **both** work items (same msg IDs — deduped inside the window, idempotent beyond it). Sessions are memory-only and simply re-form. |
| Both work items are lost forever | Cannot happen short of WORK-stream retention expiry (48 h) — same guarantee as every other stage. |

Work-item handling keeps the worker's existing discipline: `InProgress()`
heartbeats during long transfers, Term at `maxDeliveries`, per-deployment
idempotency keys (`<dep>.push`, `<dep>.distribute`).

## 7. Observability

- Push/pull progress lines go to the deployment's build-log subject
  (`logs.<server>.build.<deployment>`), so the existing deployment-log SSE
  shows the distribute stage with no new API surface.
- Deployment records surface the stage as they already do (`distributing`
  status, failure details name the side that failed).

## 8. Acceptance (all live, two real daemons)

Verified with two agents against **separate Docker daemons** (the target in
dind) — a shared daemon would short-circuit the relay via the image cache and
prove nothing:

1. App targeting a `worker`-role server + one `builder`-role server: deploy
   succeeds; the image exists on the target daemon, was never pulled from any
   registry, and the app serves through the target's Proxy.
2. Single-server `all` regression: pipeline identical to today, no
   `distributing` stage, no relay connections.
3. Kill the target agent mid-distribute → restart → deployment completes
   (redelivery + `HasImage` idempotency).
4. No builder available → deployment fails fast with the §2 message.
5. Plane RSS during a relay stays flat (bounded-buffer proof); nothing under
   the plane's data directory grows.

## 9. Out of scope (unchanged decisions, not gaps)

- **External registry push/pull** (ADR-008 path 3) — optional alternative,
  own spec when scheduled.
- Load-aware builder selection, multiple concurrent builders per deployment.
- Resumable (mid-stream) transfers — retry-from-zero is the resume mechanism
  at v1 fleet sizes; a superseding note on ADR-008 if real fleets disagree.
- BuildKit remote builders / cache export (`cypher-builder` box in
  architecture.md's target diagram) — the role split is the foundation; the
  BuildKit upgrade is its own later feature.

# Feature spec: Builder role split & multi-server image relay

> Separates the build and run concerns onto distinct agents via a `--role` flag
> and delivers built images to target servers through a NATS-based transient
> relay — no registry required (ADR-008). This is the multi-server path that
> completes the Phase 2 vertical slice's acceptance gate 5.
>
> Written 2026-07-19, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Agent roles

A server's **role** determines which work items it may process:

| Role      | Builds | Runs apps | Runs Proxy |
|-----------|--------|-----------|------------|
| `both`    | ✅      | ✅         | ✅          |
| `builder` | ✅      | ❌         | ❌          |
| `docker`  | ❌      | ✅         | ✅          |

Default: `both` (backward compatible — single-server deploys are unchanged).

The role is set at agent start via `--role` (or `CYPHER_ROLE` env) and
reported in every heartbeat. The plane records it on the server row and uses
it to route work.

### Agent-side behavior by role

- **`both`** (default): builds images, runs the docker driver + Proxy,
  streams logs. Exactly the current behavior — no change.
- **`builder`**: constructs the builder; skips the docker driver, Proxy, and
  runtime-log streamer. Only processes `.build` and relay-upload work.
- **`docker`**: constructs the docker driver + Proxy + log streamer; sets
  `builder = nil` in the worker. Processes `.rollout`, `.remove`, `.distribute`
  (relay download). Build work items for this server are never published
  (the scheduler routes them elsewhere).

## 2. Scheduler routing

### Build routing

When starting a deployment, the scheduler selects a **builder server**:

1. If the target server's role is `both`, use it (local build, no relay).
2. Otherwise, pick a server with role `builder` or `both` that is `running`.
   If multiple exist, prefer the one with the fewest active builds (simple
   load spread; not a full scheduler — that's a Later concern).
3. If no builder is available, fail the deployment with a clear error.

The `BuildWork` subject is published on `work.<builder_server_id>.build` —
not the target server.

### Distribute stage

After a successful build, if the builder ≠ target:

1. Deployment status advances to `distributing`.
2. The scheduler publishes `DistributeWork` on `work.<target_server_id>.distribute`
   containing the image tag and the builder server id.
3. The plane opens the relay channel (§3).
4. On successful distribution, the deployment advances to `rolling_out` and
   the rollout work item is published as before.

If builder = target (role `both` on a single server): the distribute stage is
skipped entirely, same as today.

## 3. NATS-based image relay protocol

ADR-008 mandates that images stream "builder → control plane → target over
the existing mTLS channel" without touching plane disk. The relay uses NATS
subjects on a transient, memory-backed stream:

```
relay.upload.<deployment_id>     builder → plane (chunked docker save)
relay.download.<deployment_id>   plane → target (republished chunks)
```

### Stream config

A **RELAY** stream: memory-backed, `MaxAge: 5m`, `MaxBytes: 256 MiB`,
`LimitsPolicy`. Short-lived: each relay session publishes, the target
consumes, and the retention window prunes automatically. The plane's memory
budget for one concurrent relay is bounded by the chunk size × pipeline
depth.

### Protocol

1. **Builder side** (after build succeeds):
   - `docker save <image>` piped into a writer that chunks the output into
     1 MiB messages published to `relay.upload.<deployment_id>`.
   - A sentinel message (empty payload) marks end-of-stream.
   - The builder publishes a `DeployEvent(STAGE_DISTRIBUTE, SUCCEEDED)` when
     the upload finishes, or `FAILED` on error.

2. **Plane relay** (in `bus.go`):
   - Subscribes to `relay.upload.<deployment_id>` and republishes each
     message to `relay.download.<deployment_id>`. No buffering beyond NATS's
     own in-flight window — bounded memory, zero disk.
   - The relay is started by the scheduler when it transitions to
     `distributing` and torn down on completion.

3. **Target side** (on `.distribute` work item):
   - Subscribes to `relay.download.<deployment_id>`.
   - Pipes received chunks into `docker load`.
   - On the sentinel (empty payload): closes the pipe, waits for `docker load`
     to finish, publishes `DeployEvent(STAGE_DISTRIBUTE, SUCCEEDED/FAILED)`.

### Authorization

The builder's NATS identity already allows publishing to
`logs.<builder_id>.>` (its log scope). The relay upload subject
`relay.upload.<deployment_id>` needs a new publish grant for the builder and
a subscribe grant for the plane. The target needs a subscribe grant for
`relay.download.<deployment_id>`. These are scoped per-agent in the same
`certAuth.Check` method that already manages state/work/logs grants.

## 4. Data model changes

### Server

```sql
ALTER TABLE servers ADD COLUMN role TEXT NOT NULL DEFAULT 'both';
```

### Proto additions

```protobuf
// In work.proto:
message DistributeWork {
  string deployment_id = 1;
  string app_id = 2;
  string image = 3;              // tag to docker-load
  string source_server_id = 4;   // builder that has the image
}

// In agent.proto, Heartbeat:
string role = 6;                  // "docker" | "builder" | "both"
```

## 5. Security

- Image data travels exclusively over mTLS NATS (rule 23).
- The relay is transient and memory-only: no image blob ever touches the
  plane's disk (ADR-008, threat-model §5.9).
- The per-agent NATS authorization ensures a builder can only upload for
  deployments the scheduler authorized, and a target can only download its
  own relay subjects.
- A compromised builder's blast radius stays its own builds: it cannot
  publish rollout work or status observations for another server's apps
  (threat-model §5.2).

## 6. Acceptance (testable)

1. **Two-agent CI scenario**: Agent A (`--role=builder`), Agent B
   (`--role=docker`). Deploy → build on A → relay to B → rollout on B → app
   reachable through B's Proxy.
2. Kill the relay mid-transfer → restart → the scheduler re-publishes the
   distribute work item → relay completes on retry.
3. Single-server (`--role=both`) deploys remain unchanged — no relay, no
   performance regression.
4. Builder server unavailable → deployment fails with a clear error, not a
   hang.

## 7. Non-goals for this slice

External registry push/pull (the optional path 3 of ADR-008 — schema fields
exist, implementation is a fast follow) · multi-builder load balancing beyond
simple fewest-active · resumable relay (a failed relay restarts from scratch;
acceptable at v1 image sizes) · relay compression (images are already
compressed layers; double-compressing wastes CPU) · parallel relay to multiple
targets (one target per deployment in the slice; replicas > 1 is Later).

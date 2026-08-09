# Feature spec: Application deploy (Phase 2 vertical slice)

> The roadmap Phase 2 contract, specified. One Application, end to end, done properly: git source → build on a builder-role agent → image distribution (ADR-008) → health-gated rollout by the `docker` driver (ADR-006) → Traefik route with TLS (ADR-004) → logs streamed → webhook deploys → rollback. Everything here is desired state (ADR-005); "deploy succeeded" is only assertable from observed state.
>
> Written 2026-07-18, just before implementation (CLAUDE.md rule 7). Vocabulary per [glossary.md](../glossary.md).

## 1. Resource model

The slice introduces the organizational spine plus the Application resource:

- **Project** — created by the operator; groups environments. (Team is implicit in Phase 2 — one team exists conceptually; the `team_id` column lands with real teams in Phase 3, additive migration.)
- **Environment** — named context in a project (`production` created by default). Holds resources.
- **Application** — a resource in an environment, built from a git repository, owned end to end.
- **Build** — producing an image from a source revision. **Deployment** — a recorded transition of the Application from one desired revision to another.
- **Revision** — an immutable record (image reference + config snapshot) a Deployment points at; what rollback re-points to.

### Application desired state (the schema the reconciler consumes)

```
Application:
  id, environment_id, name
  source:   { kind: github|git_url, repo, branch, deploy_key_id? }
  build:    { kind: dockerfile, dockerfile_path (default ./Dockerfile), context (default .) }
  runtime:  { server_id, port (container port), env_vars (sealed), replicas: 1 (fixed in slice) }
  route:    { domain?, https: true (LE HTTP-01), path_prefix? }   # domain optional: empty ⇒ raw service
  health:   { kind: http|tcp|none (default http), path (default /), interval, timeout, retries }  # gates rollout
  ports:    [ { host_port, container_port, protocol: tcp|udp } ]  # raw host publishes, current-state
  desired_revision_id  → Revision
```

Env var values are sealed with the master key at rest (same `secret.Box` as the CA key) and masked in every API response (ENGINEERING rule 20).

## 2. Lifecycle & states

Application status uses the fixed vocabulary ([ui-principles §5](../product/ui-principles.md)): `running · deploying · stopped · error · degraded · unknown`.

```
create ──▶ (no revision yet: stopped)
   │ deploy (manual or webhook)
   ▼
BUILD ──▶ DISTRIBUTE ──▶ ROLLOUT ──▶ ROUTE      status: deploying
   │            │            │          │
   └── fail ────┴────────────┴──────────┘──▶ error (previous revision keeps serving)
                                        └──▶ success: running
rollback = deploy of a previous Revision (same pipeline, build skipped)
delete  ──▶ desired absence: container removed, route fragment removed, images GC'd
```

**Zero-downtime invariant (vision non-negotiable 4).** During ROLLOUT the old container keeps serving until the new one passes its health check; then the proxy route flips (atomic fragment rewrite, ADR-004) and the old container drains and stops. If health never passes, the new container is discarded and the Application reports `error` with the build/runtime log attached — the old revision never stopped serving. Dokploy's stale-container failure is definitionally impossible: `running` is only reported when the agent *observes* the new revision serving ([community-pain-points](../../research/community-pain-points.md) finding 3).

**Crash recovery.** Kill agent or plane mid-deploy → on reconnect the reconciler compares desired revision vs observed containers and converges (ADR-005). No resumable-script logic exists anywhere; the roadmap acceptance test asserts exactly this.

## 3. Pipeline mechanics (work items + reconciliation)

The scheduler decomposes a Deployment into **work items** on `work.<server_id>.…` — durable JetStream subjects (at-least-once ⇒ every consumer idempotent, keyed by deployment id; ENGINEERING rules 12–13). The **WORK stream is file-backed** (unlike Phase 1's memory-only STATE stream): work must survive a plane restart, with explicit retention limits (threat-model §8 req 9).

1. `work.<builder>.build` — clone at commit SHA (shallow), `docker build`, tag `cypher/<app_id>:<revision_id>`. Progress + logs stream on `logs.build.<deployment_id>` (verbose by default — no hidden `BUILDKIT_PROGRESS` toggle; matrix row).
2. **Distribute** (ADR-008): builder = target ⇒ no-op. Otherwise `docker save` streamed builder→plane→target over gRPC (chunked, resumable), `docker load` on the target. Never touches plane disk.
3. `work.<target>.rollout` — the docker driver converges: pull-nothing (image is local), start new container on the app's deterministic network (explicit name `cypher-<environment_id>` — never Docker auto-naming, [coolify.md](../../research/coolify.md) lesson 5), health-gate, flip Traefik fragment, drain + stop old, report `state.<server>.deploy` with observed outcome.
4. Route: the agent owns `/etc/cypherpanel/traefik/apps/<app_id>.yml` — atomic write (temp + rename), validated before activation, deleted with the app (ADR-004). TLS via Let's Encrypt HTTP-01 for the slice; the cert lives on the serving node.

**Deployment concurrency:** per-Application serialization — a new deploy queues behind (or supersedes) a running one; never two rollouts of one app in flight (semantics per Dokploy's `concurrency.ts`, mechanism ours).

**Image GC (threat-model §5.9):** the agent retains images referenced by the current + previous N (default 3) revisions — the rollback set — and prunes the rest. Principled GC, not heuristic pruning.

## 4. API surface (API-first; all under `/api/v1`)

```
POST   /projects                    {name}                    → Project (+ default production env)
GET    /projects · GET/DELETE /projects/{id}
POST   /environments/{id}/applications   {name, source, build, runtime, route, health}
GET/PATCH/DELETE /applications/{id}      (PATCH = config change ⇒ new revision on next deploy)
POST   /applications/{id}/deploy         {ref?}               → Deployment (202; progress via stream)
GET    /applications/{id}/deployments    · GET /deployments/{id}
POST   /deployments/{id}/rollback        → new Deployment pointing at that revision
GET    /deployments/{id}/logs            (SSE: build then rollout log stream)
GET    /applications/{id}/logs           (SSE: runtime logs, follow)
POST   /webhooks/github/{app_webhook_id} (HMAC-verified; branch-filtered → deploy)
```

Status changes stream over the existing SSE channel; deployments are listable with their work-item transitions (the UI's live progress is these records, not a spinner — ui-principles §3).

**Permissions:** Phase 2 has the single owner account — every authenticated route requires the session; the webhook route authenticates by per-app HMAC secret instead. Object-level authz slots in with teams (Phase 3) — handlers already resolve resources through the environment→project chain so the team check is one added predicate.

## 5. Agent-side contracts

- `agent/driver/driver.go` — the Reconciler interface (THE seam, ADR-006): `Reconcile(ctx, desired []AppSpec) (observed []AppStatus, err)`. Server-level draining (evacuating a host) is a scale-out concern, not part of the slice. Written against the desired-state schema; nothing Docker-shaped leaks out of `driver/docker/`.
- `agent/proxy/` — the Traefik Proxy behind its own driver interface (Caddy later). Fragment writing exists; the Proxy lifecycle, upstream networking, and Let's Encrypt are specified in [routing-and-tls.md](routing-and-tls.md).
- `agent/builder/` — enabled by `--role=builder`; BuildKit via the Docker daemon for the slice (Railpack/Nixpacks are separate matrix rows, not the slice).
- `agent/stream/` — log tailing (`docker logs --follow`) → `logs.runtime.<app_id>`; build logs from the builder → `logs.build.<deployment_id>`. Bounded retention on the plane (JetStream limits), drains later (matrix V1.x).
- The `reconciler-development` skill (`.claude/skills/`) is written alongside the driver interface — the carried-over Phase 1 deliverable.

### Raw TCP/UDP ports and non-HTTP services (feature-matrix V1)

Not every app is an HTTP service. An Application may publish raw host ports and
skip the HTTP proxy entirely — a game server, message broker, or database-as-app.
Three orthogonal knobs make this work, and every existing HTTP app is unchanged
because each defaults to today's behavior:

- **`ports: [{host_port, container_port, protocol}]`** — publishes container
  ports to the host on `tcp` or `udp` (empty protocol defaults to `tcp`). The
  agent maps these onto the container's `ExposedPorts` + `HostConfig.PortBindings`
  (mirroring the managed-database `expose_port`). Ports are **current state**
  (like volumes and resource limits), not per-revision — rollback keeps them.
  Validation bounds each port to 1–65535 and rejects duplicate host-port +
  protocol bindings; ≤20 per app.
- **`route.domain` is now optional.** A non-empty domain gives the app its
  Traefik fragment as before; an **empty domain means no proxy route** — the app
  is reached only through its published ports. The reconciler writes no fragment
  for a routeless app and removes a stale one if a domain is cleared, all while
  preserving the converge-twice invariant.
**Known limitation — host ports and redeploys.** The zero-downtime sequence
starts the new revision *alongside* the old one, and both carry the same fixed
`PortBindings`, so the second bind fails with address-in-use: an app that
publishes host ports cannot currently be redeployed or rolled back. The health
gate never passes, the new container is discarded, and the old revision keeps
serving — no outage and no data loss, but the update does not land. Fixing it
means accepting a brief handover gap for this class of app (a fixed host port
cannot be held by two containers at once), which changes the zero-downtime
invariant for them and therefore needs its own decision rather than a quiet
behaviour change. Recorded here because it constrains what the bundled catalog
can ship: no template declares host ports until it is resolved.

- **`health.kind`** selects the rollout gate: `http` (default — GET `health.path`,
  today's behavior), `tcp` (dial the container port), or `none` (liveness-only,
  for raw UDP services with no readiness signal). **The health probe is always
  internal** (agent → container), independent of any public route — which is why
  routing and health decompose cleanly rather than entangling. The zero-downtime
  sequence (start new → gate → flip/settle → drain old) holds for `tcp` too.

### Deploy from a container image (feature-matrix V1)

`source.kind = image` runs a prebuilt OCI reference with **no build stage**:
`Deploy` records the reference as an already-built revision and goes straight
to rollout — the same path a rollback takes. No builder is selected and nothing
is distributed. The reference is stored in its own `source_image` column rather
than overloading `source_repo`, because a git remote and an OCI reference are
different vocabularies; git fields are cleared and deploy keys are rejected for
image sources.

The spec carries `pull` (per revision, in the config snapshot — so rolling back
to an image revision still pulls correctly after the app was re-pointed at a
git source). The reconciler resolves it **only in the create branch**, so a
converged app never touches the registry and converge-twice stays
zero-mutation.

**Mutable vs immutable references.** A **digest** (`repo@sha256:…`) is
immutable: if it is already local, those are provably the right bits and the
pull is skipped. A **tag** is mutable — `acme/web:latest` can point somewhere
new since the last deploy — so every new revision re-fetches it. Skipping that
would start the new container from the stale cached image and then report
success, which is exactly the stale-container failure ADR-005 exists to make
impossible. A pull that fails leaves the old revision serving and reports
`error`; it never silently falls back to the cached image.

**Reclaiming what a pull created.** A pulled image cannot carry our labels —
those are baked in by whoever built it — so it is tagged into the managed
`cypher/<app>:<revision>` namespace at rollout and the container runs from that
alias. The floating registry reference the pull arrived under is then dropped;
leaving it would keep every layer alive after the app was deleted.

Only a reference **our own pull created** may be dropped: one that already
existed is the operator's, and untagging it would be the driver reaching
outside its managed set. That is knowable only at pull time, so it is recorded
immediately — on the **image**, as a marker reference (`cypher-pull/<encoded
reference>:<app_id>`), before anything that could lose it. A container label was
the wrong home for it: everything after the pull (tagging, create, start, the
health gate, the route flip) can fail, and the last four discard the container,
so the record would vanish in exactly the cases it exists for. The image
survives all of them, and GC retries the removal from the marker alone — no
spec to consult, no container to read, so it still converges for a rollout that
never created one and for an app deleted before the retry succeeded. The marker
always outlives the reference it names, and is removed once that reference is
gone. Residual: a crash in the instant between the pull and the marker leaves an
unrecorded reference, which is then treated exactly like the operator's — never
reclaimed, and never wrongly removed.

### Persistent volumes (feature-matrix V1)

An Application may declare volume mounts (`volumes: [{name, path}]`), stored as a
JSONB column and carried on `AppSpec` as current state (not per-revision — like
env vars and resource limits, so rollback keeps your data mounts). Each mount is
a **named Docker volume** with a deterministic, plane-computed name
(`cypher-appvol-<app_id>-<name>`, app-id-labelled) bound at the absolute `path`
inside the container. Semantics:

- The agent `EnsureVolume`s each volume **only in the container-create branch**
  (idempotent, and skipped on a no-change converge, so the converge-twice
  invariant holds), then binds `<volume>:<path>`.
- Volumes **persist across container recreation** (that is the point) and are
  **never touched by desired-state GC** (GC prunes only images/containers).
- On app delete they are **kept** (conservative — accidental data loss is worse
  than an orphaned volume, matching the database `delete_volume` default);
  explicit reclaim is a follow-on.
- Validation: safe lowercase-alphanumeric-dash `name`, absolute non-`..` `path`,
  unique names and paths, ≤20 per app.

## 6. Acceptance (from roadmap, restated testably)

1. `git push` → webhook → new version live with zero dropped requests (asserted by a request loop across the flip).
2. Kill the agent mid-deploy → restart → reconciler converges to the desired revision with no manual step.
3. Rollback restores the previous revision in seconds (no build).
4. Entire flow drivable by REST alone (curl transcript in the integration test).
5. Build on a second (builder-role) agent reaches a different target server via relay — the multi-server path exercised in CI's two-agent scenario.

## 7. Non-goals for this slice

Preview environments (Phase 3) · managed databases (Phase 3) · Compose Stacks & templates (Phase 4, ADR-007) · Nixpacks/Railpack/static builds (post-slice matrix rows) · replicas > 1 (Later; ADR-006 note) · DNS-01 wildcards, custom certs, redirects/middleware (V1.x) · multiple git providers beyond GitHub App + raw URL/deploy key (V1.x) · per-resource CPU/memory limits UI (schema field exists, enforcement V1) · deployment queue fairness across apps (only per-app serialization is in the slice).

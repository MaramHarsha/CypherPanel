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
  route:    { domain, https: true (LE HTTP-01), path_prefix? }
  health:   { path (default /), interval, timeout, retries }   # gates rollout
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

- `agent/driver/driver.go` — the Reconciler interface (THE seam, ADR-006): `Reconcile(ctx, desired []AppSpec) (observed []AppStatus, err)` plus `Drain`. Written against the desired-state schema; nothing Docker-shaped leaks out of `driver/docker/`.
- `agent/proxy/` — Traefik fragment writer behind its own driver interface (Caddy later).
- `agent/builder/` — enabled by `--role=builder`; BuildKit via the Docker daemon for the slice (Railpack/Nixpacks are separate matrix rows, not the slice).
- `agent/stream/` — log tailing (`docker logs --follow`) → `logs.runtime.<app_id>`; build logs from the builder → `logs.build.<deployment_id>`. Bounded retention on the plane (JetStream limits), drains later (matrix V1.x).
- The `reconciler-development` skill (`.claude/skills/`) is written alongside the driver interface — the carried-over Phase 1 deliverable.

## 6. Acceptance (from roadmap, restated testably)

1. `git push` → webhook → new version live with zero dropped requests (asserted by a request loop across the flip).
2. Kill the agent mid-deploy → restart → reconciler converges to the desired revision with no manual step.
3. Rollback restores the previous revision in seconds (no build).
4. Entire flow drivable by REST alone (curl transcript in the integration test).
5. Build on a second (builder-role) agent reaches a different target server via relay — the multi-server path exercised in CI's two-agent scenario.

## 7. Non-goals for this slice

Preview environments (Phase 3) · managed databases (Phase 3) · Compose Stacks & templates (Phase 4, ADR-007) · Nixpacks/Railpack/static builds (post-slice matrix rows) · replicas > 1 (Later; ADR-006 note) · DNS-01 wildcards, custom certs, redirects/middleware (V1.x) · multiple git providers beyond GitHub App + raw URL/deploy key (V1.x) · per-resource CPU/memory limits UI (schema field exists, enforcement V1) · deployment queue fairness across apps (only per-app serialization is in the slice).

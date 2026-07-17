# Research: Dokploy — Extraction Map

> Purpose: **where to look in `../dokploy` when porting a capability, and what verdict to apply**. We extract logic, schemas, and edge-case knowledge — never code (ADR-001). The reference repo is read-only. Verified against the source on 2026-07-17.

## ⚠️ License caution — read before porting anything

The repo contains `LICENSE.MD` **and** `LICENSE_PROPRIETARY.md`, plus a `routers/proprietary/` directory. Parts of this codebase (cloud/enterprise features — SSO/SCIM area) are **not** open-licensed. Before extracting from any file, confirm which license governs it. When in doubt: extract the *behavioral idea* from docs/UI, not from the source.

## Stack (for orientation while reading)

pnpm monorepo (biome for lint): `apps/dokploy` (Next.js + tRPC web/app server), `packages/server` (the real domain layer — DB schema, services, utils), `apps/monitoring` (**Go** metrics service), `apps/schedules` (cron runner), `apps/api` (lightweight external API). Drizzle ORM + PostgreSQL, Redis + BullMQ queues, Docker Swarm as the orchestration substrate, Traefik configured via generated files.

## Architecture in one paragraph

A well-factored TypeScript monolith: tRPC routers (`apps/dokploy/server/api/routers/`) call services (`packages/server/src/services/`) over a clean Drizzle schema (`packages/server/src/db/schema/`). Its strengths are the data model, the builder abstraction, and product polish (rollbacks, volume backups, granular notifications). Its weaknesses: everything — builds included — executes on the manager node, and Swarm assumptions thread through the Docker utilities. Notably, when they needed something lightweight (metrics), they wrote it in Go: `apps/monitoring` validates ADR-001.

## Extraction map

| Area | Where in `../dokploy` | Verdict | Notes |
|---|---|---|---|
| **Data model** | `packages/server/src/db/schema/*.ts` (~48 entities: application, compose, deployment, rollbacks, preview-deployments, volume-backups, registry, schedule, notification, domain, certificate, mount, port, redirects, forward-auth, audit-log…) | **Port as blueprint** | The single most valuable artifact in either repo. Entity-by-entity reference for our sqlc schema — translate terms via [glossary.md](../docs/glossary.md) (their "destination" = backup target!). |
| Builders | `packages/server/src/utils/builders/{nixpacks,railpack,heroku,paketo,static,docker-file,compose,drop}.ts` | **Adapt** | Clean per-builder abstraction → `agent/builder/`. `drop.ts` = drag-and-drop upload deploys (Later, per feature matrix). |
| Git providers | `packages/server/src/utils/providers/` + `services/{github,gitlab,gitea,bitbucket}.ts` + per-provider schema | **Adapt** | Full OAuth/App flows per provider, incl. webhook validation. |
| Traefik config | `packages/server/src/utils/traefik/` | **Port logic** | They also generate config *files* (validates ADR-004) — but centrally; ours moves to the agent. Their middleware/redirect/forward-auth config shapes map to our V1.x rows. |
| Docker layer | `packages/server/src/utils/docker/`, `utils/cluster/` | **Adapt with care** | This is where Swarm coupling lives — exactly what `agent/driver/` must isolate. Read to enumerate what a `swarm` driver needs (ADR-006 input). |
| Backups & restore | `utils/backups/`, `utils/restore/`, `utils/volume-backups/` | **Adapt** | In-panel restore and volume backups are differentiators the feature matrix keeps (P2 persona). |
| Deployment queue | `apps/dokploy/server/queues/` (BullMQ: `deployments-queue.ts`, `concurrency.ts`) | **Avoid mechanism, keep semantics** | ADR-003 replaces BullMQ; but `concurrency.ts` encodes per-application serialization semantics our scheduler must reproduce. |
| Monitoring | `apps/monitoring/` (Go: `containers/`, `database/`, `middleware/`) | **Study/Port** | Already Go. Container + server metrics collection patterns → `agent/stream/`. Closest thing to existing cypher-agent code in either repo. |
| Schedules | `apps/schedules/`, `utils/schedules/` | **Absorb** | Separate cron-runner app; ours folds into `core/scheduler`. |
| Notifications | `utils/notifications/` (build-error, build-success, database-backup, docker-cleanup, restart, server-threshold, volume-backup) | **Port event catalog** | Their *event taxonomy* is more granular than Coolify's — use it as our `core/notify` event list. |
| Templates | `packages/server/src/templates/` (`github.ts`, `processors.ts`) | **Adapt** | Remote-registry model (fetch from a templates repo) vs Coolify's in-repo YAML — ADR-007 weighs these; likely hybrid. |
| API surface | `apps/dokploy/server/api/routers/*.ts` (45+ routers) + `openapi.json` | **Reference** | Router-by-router checklist of operations + permission checks for our REST design. |
| Terminal | `apps/dokploy/server/wss/` | **Adapt concept** | Websocket → `docker exec` bridging; ours is a gRPC stream through the agent (ADR-002). |
| Rollbacks | `schema/rollbacks.ts` + `routers/rollbacks.ts` | **Adapt** | Cheap for us by construction (ADR-005) — their UX around it is the extractable part. |
| SSO/SCIM, AI | `schema/{sso,scim,ai}.ts`, `routers/proprietary/` | **Later / license caution** | See warning above. AI is Out for v1 regardless. |

## Lessons

1. **The schema-first layering is why Dokploy ships fast** — routers thin, services cohesive, schema canonical. Our REST → domain → store layering copies the discipline.
2. **Manager-node execution is their ceiling** — builds compete with the panel and workloads for the same CPU. Builder-role agents are our structural answer.
3. **Swarm assumptions creep** — service-mode Docker calls appear in generic-looking utils. The `driver` interface must be policed in review (project-structure.md rule 2).
4. **Granular notification events** (per-event-type files) age better than a single "send notification" chokepoint.

## License

Mixed — open core with proprietary components (`LICENSE.MD`, `LICENSE_PROPRIETARY.md`). **Check per-file/per-directory before extracting.**

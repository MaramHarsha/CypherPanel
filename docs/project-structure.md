# CypherPanel — Project Structure

> Where code and docs live, and the rules for adding new things. The architecture behind this layout is in [architecture.md](architecture.md).

## Code layout

```
cypherpanel/
├── go.work                      # Go workspace tying the modules together
├── Makefile                     # build / test / dev orchestration
├── docker-compose.dev.yml       # local dev: postgres + hot-reload plane
│
├── core/                        # ── control plane (single binary: cypherd) ──
│   ├── cmd/cypherd/             # main: serves API, UI assets, scheduler, embedded NATS
│   ├── api/
│   │   ├── rest/                # HTTP handlers, OpenAPI spec, middleware
│   │   └── grpc/                # agent-facing services (proto in /proto)
│   ├── auth/                    # sessions, API tokens, OIDC, RBAC policy
│   ├── domain/                  # resource model: project, application, compose stack,
│   │   │                        #   managed database, environment, server, deployment,
│   │   │                        #   backup, certificate
│   │   └── reconcile/           # desired-vs-observed diffing, work planning
│   ├── scheduler/               # queue producers, cron, retry/backoff policy
│   ├── store/                   # Postgres access (sqlc), migrations/
│   ├── bus/                     # JetStream setup, subject contracts (work.*, state.*, logs.*)
│   └── notify/                  # email, Discord, Telegram, Slack, webhooks
│
├── agent/                       # ── data plane (single binary: cypher-agent) ──
│   ├── cmd/cypher-agent/
│   ├── conn/                    # dial-home, mTLS, join-token enrollment
│   ├── driver/                  # orchestrator drivers (the modularity seam)
│   │   ├── docker/              # standalone Docker reconciler
│   │   ├── swarm/               # Swarm reconciler (pending ADR-006)
│   │   └── driver.go            # common Reconciler interface
│   ├── proxy/                   # Traefik dynamic-config writer (Caddy later)
│   ├── builder/                 # BuildKit / Railpack / Compose (enabled by --role=builder)
│   ├── stream/                  # log tailing, metrics, event publishing
│   └── exec/                    # terminal session bridging
│
├── proto/                       # gRPC contracts shared by core & agent (buf-managed)
├── pkg/                         # shared Go libs: types, compose-spec parsing, ids
│
├── web/                         # ── UI (static build, embedded into cypherd) ──
│   ├── src/
│   │   ├── api/                 # generated client from OpenAPI — never hand-written
│   │   ├── features/            # projects/, applications/, databases/, servers/,
│   │   │                        #   deployments/, backups/, settings/
│   │   ├── components/          # shared UI (shadcn-based)
│   │   └── routes/
│   └── package.json
│
├── templates/                   # one-click catalog (declarative YAML — data, not code)
├── install/                     # curl|sh installers: control plane, agent join
└── docs/                        # see below
```

Load-bearing decisions: `agent/driver/` is where "eliminate orchestration coupling" physically lives; `proto/` + `core/api/rest/` enforce API-first; `templates/` is declarative data so both reference catalogs can be ported mechanically.

## Documentation layout

```
docs/
├── architecture.md        # canonical system design
├── vision.md              # why / who / non-negotiables / out of scope
├── tech-stack.md          # what we use; ADRs say why
├── roadmap.md             # phases, acceptance gates, open decisions
├── glossary.md            # canonical vocabulary + reference-repo term mapping
├── project-structure.md   # this file
├── adrs/                  # ADR-NNN-slug.md — immutable; supersede, don't edit
├── product/               # personas, feature-matrix, ui-principles
│                          #   (+ design-system.md in Phase 4, from real components)
├── dev/                   # ci.md — CI/CD workflow inventory
├── security/              # threat-model.md (written at Phase 1 start, before agent code)
└── features/              # one spec per feature, written JUST BEFORE implementing it
                           #   (3–8 pages: lifecycle, states, API, permissions, non-goals)
research/                  # extraction maps into ../coolify and ../dokploy sources
.claude/skills/            # project skills, created just-in-time (pattern proven in both
                           #   reference repos): reconciler-development (Phase 1–2),
                           #   adding-a-database-engine (Phase 3),
                           #   frontend-design + adding-a-template (Phase 4)
```

## Placement rules

1. **New code** goes in the existing module whose responsibility it matches; new top-level directories require an ADR.
2. **Orchestrator- or proxy-specific logic** may only exist inside a `driver/` or `proxy/` implementation. If a feature needs `if swarm`, the design is wrong.
3. **One topic, one home** (docs): a subject is documented in exactly one file; everything else links to it. No stub files, ever — a doc exists only when its content does.
4. **ADRs are immutable.** Changed circumstances → new ADR with `Supersedes: ADR-NNN`.
5. **Feature specs precede features.** No Phase 2+ feature is implemented without its `docs/features/<name>.md`.

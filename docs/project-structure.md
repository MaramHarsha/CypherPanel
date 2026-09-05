# CypherPanel — Project Structure

> Where code and docs live, and the rules for adding new things. The architecture behind this layout is in [architecture.md](architecture.md).

## Code layout

Directories marked *(planned)* do not exist yet; they arrive with the roadmap
phase that needs them.

```
cypherpanel/
├── go.work                      # Go workspace tying the modules together
├── Makefile                     # build / test / dev orchestration
├── docker-compose.dev.yml       # local dev: postgres + hot-reload plane
│
├── core/                        # ── control plane (single binary: cypherd) ──
│   ├── cmd/cypherd/             # main: wiring + run loop, serves API + embedded NATS
│   ├── cmd/coolify-import/      # build-time only: Coolify compose templates → catalog
│   │                            #   YAML, refusing what the schema cannot express
│   │                            #   (dev/template-import.md). Never a runtime path.
│   ├── api/
│   │   ├── rest/                # HTTP handlers, middleware, OpenAPI spec, SSE log streams
│   │   │   └── console/         # interim Phase 1 console (replaced by web/ in Phase 4)
│   │   └── grpc/                # agent-facing services (proto in /proto)
│   ├── audit/                   # the audit log: the closed verb vocabulary, the
│   │                            #   single write path (validation, secret-key
│   │                            #   stripping, bounds) and the read path whose
│   │                            #   visibility comes from the viewer's own
│   │                            #   record, never the request (audit-log.md)
│   ├── auth/                    # sessions, token hashing; OIDC/RBAC (planned)
│   ├── config/                  # fail-closed env config for cypherd
│   ├── domain/                  # resource model (Server, User, JoinToken; Phase 2:
│   │                            #   Project, Environment, Application, Revision, Deployment)
│   ├── enroll/                  # agent-facing enrollment: join token → signed cert
│   ├── guard/                   # boot-time safety checks (disk headroom, threat-model §5.9)
│   ├── identity/                # plane CA lifecycle: create, seal, load
│   ├── paneltls/                # the panel's ACME account: one setting, carried to
│   │                            #   every node as desired state so the Proxy can
│   │                            #   obtain certificates (agent-identity-and-tls.md)
│   ├── secret/                  # AES-256-GCM sealing with the master key
│   ├── servers/                 # operator-facing server lifecycle
│   ├── projects/               # operator-facing project/environment lifecycle (Phase 2)
│   ├── protection/              # deploy protection: the freeze-window arithmetic,
│   │                            #   the admission gate the scheduler consults, and
│   │                            #   the approval / break-glass decisions
│   │                            #   (deploy-protection.md)
│   ├── applications/           # application lifecycle: config, sealed env, webhook secret (Phase 2)
│   ├── status/                  # heartbeat consumption, liveness → status transitions
│   ├── scheduler/               # deploy pipeline: work-item producers, deployment state
│   │                            #   machine, observed-outcome assertion (Phase 2)
│   ├── store/                   # Postgres access (sqlc: db/, queries/), migrations/
│   ├── bus/                     # embedded NATS: mTLS, per-agent authz (subjects AND
│   │                            #   per-identity reply inboxes), revocation;
│   │                            #   memory STATE + file-backed WORK streams (Phase 2)
│   ├── logring/                 # bounded in-memory tail of the panel's own slog output,
│   │                            #   served by GET /panel/logs (control-plane-hardening.md §4)
│   ├── updates/                 # what build is running + an opt-out release-feed check;
│   │                            #   never self-updates (ADR-010, control-plane-hardening.md §3)
│   └── notify/                  # (planned, Phase 3+) email, Discord, Telegram, webhooks
│
├── agent/                       # ── data plane (single binary: cypher-agent) ──
│   ├── cmd/cypher-agent/
│   ├── conn/                    # dial-home mTLS connection with reconnect
│   ├── identity/                # join-token enrollment, on-disk key/cert storage,
│   │                            #   and certificate renewal (expiry awareness, the
│   │                            #   renewal loop, the atomic swap)
│   ├── heartbeat/               # periodic status publishing
│   ├── worker/                  # work-item consumer + reconcile loop; the Bus seam
│   │                            #   over nats.go, desired-state sync on connect (Phase 2)
│   ├── driver/                  # orchestrator drivers (the modularity seam)
│   │   ├── docker/              # standalone Docker reconciler (Phase 2)
│   │   │   ├── engine/          # Docker Engine API client (unix socket, no SDK)
│   │   │   └── prober/          # HTTP health prober gating rollout
│   │   ├── swarm/               # (planned, V1.x — ADR-006) Swarm reconciler
│   │   └── driver.go            # common Reconciler interface + management labels
│   ├── proxy/                   # Traefik Proxy driver: lifecycle (EnsureProxy), env-network
│   │                            #   attachment, file-provider fragments, LE config (ADR-004)
│   ├── builder/                 # git clone + Dockerfile image build (Phase 2; Railpack/
│   │                            #   Nixpacks/Compose are later matrix rows)
│   ├── stream/                  # container log tailing → logs.* (Phase 2)
│   └── exec/                    # (planned, Phase 3+) terminal session bridging
│
├── proto/                       # gRPC/protobuf contracts shared by core & agent (buf-managed)
├── pkg/                         # shared Go libs
│   ├── ids/                     # prefixed IDs + token secrets
│   ├── pki/                     # CA, CSR signing, TLS config construction
│   ├── proto/                   # generated protobuf code (from /proto)
│   └── subjects/                # NATS subject contracts (work.*, state.*)
│
├── web/                         # ── UI (static build, embedded into cypherd) ──
│   ├── public/                  # copied to dist verbatim: favicon set, web manifest
│   ├── src/
│   │   ├── api/                 # generated client from OpenAPI — never hand-written
│   │   ├── features/            # projects/, applications/, databases/, servers/,
│   │   │                        #   deployments/, backups/, settings/
│   │   ├── components/          # shared UI (shadcn-based)
│   │   └── routes/
│   └── package.json
│
├── templates/                   # (planned, Phase 4) one-click catalog (declarative YAML)
├── install/                     # curl|sh installers — agent.sh (agent join, served by the
│                                #   plane at /install/agent.sh); control-plane installer
│                                #   arrives with the first release (GoReleaser, Phase 2)
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
├── assets/                # images referenced by the README and docs (the mark,
│                          #   the lockup); the app's own copies live in web/public
├── adrs/                  # ADR-NNN-slug.md — immutable; supersede, don't edit
├── product/               # personas, feature-matrix, ui-principles, web-ui-design
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

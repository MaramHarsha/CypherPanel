# CypherPanel — Tech Stack

> Rationale for the load-bearing choices lives in [adrs/](adrs/). This document is the reference for *what* we use; ADRs cover *why*.

## Runtime stack

| Layer | Choice | Notes |
|---|---|---|
| Control plane (`cypherd`) | **Go**, single static binary | Embeds API, scheduler, NATS, UI assets ([ADR-001](adrs/ADR-001-go-single-binary-control-plane.md)) |
| Agent (`cypher-agent`) | **Go**, single static binary | Shares `pkg/` + proto stubs with core; linux/amd64 + linux/arm64 |
| Database | **PostgreSQL 16+** | Single state of record. No Redis anywhere in the system. |
| Queue / event bus | **NATS JetStream**, embedded in-process | `work.*`, `state.*`, `logs.*` ([ADR-003](adrs/ADR-003-embedded-nats-jetstream.md)) |
| Client API | **REST**, OpenAPI 3.1 spec is the source of truth | Spec-first; generated TS client for the UI, generated Go client for the CLI |
| Agent API | **gRPC over mTLS** | Streaming for logs, terminal, exec ([ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md)) |
| Builds | **BuildKit** (Dockerfile), **Railpack/Nixpacks** (auto-detect), Compose parser | Runs only on builder-role agents |
| Proxy | **Traefik v3** via file provider; Caddy as a later driver | ([ADR-004](adrs/ADR-004-traefik-file-provider.md)) |
| TLS | Let's Encrypt HTTP-01 default, DNS-01 for wildcards; custom certs supported | Cert material stays on the serving node |
| Web UI | **React 19 + Vite + TanStack Router/Query**, Tailwind CSS + shadcn/ui | Static build embedded via `go:embed`; no SSR server |
| Real-time UI | **SSE** for status/logs; WebSocket only for interactive terminal | SSE reconnects trivially through proxies |

## Development toolchain

| Concern | Tool |
|---|---|
| Go version | 1.25+ (`go.work` workspace: `core`, `agent`, `pkg`) — floor set by grpc-go's minimum |
| SQL | **sqlc** — hand-written SQL, generated type-safe Go; migrations via **goose** (reversible, see ENGINEERING.md) |
| Protobuf | **buf** — lint + breaking-change detection in CI |
| Go lint/format | gofmt + **golangci-lint** (config committed at repo root) |
| JS package manager | **pnpm**; UI lives in `web/` only |
| TS API client | Generated from the OpenAPI spec (orval or openapi-typescript) — hand-written fetch calls are forbidden |
| E2E tests | Playwright (introduced in roadmap Phase 4) |
| CI | GitHub Actions: lint → unit → integration (dockerized Postgres) → buf breaking → build matrix — full phased workflow inventory in [dev/ci.md](dev/ci.md) |

## Version and dependency policy

- Pin direct dependencies; renovate/dependabot updates land as ordinary PRs with passing CI.
- New runtime dependencies in `core` or `agent` require justification in the PR description — every one fights the footprint budget in [vision.md](vision.md).
- The reference repos (`../coolify`, `../dokploy`) are **read-only inputs**, never dependencies.

# ADR-001: Go, single-binary control plane

- **Status:** Accepted
- **Date:** 2026-07-17

## Context

Both reference stacks impose heavy runtimes. Coolify requires PHP-FPM, Laravel Horizon, Redis, Soketi, and Postgres on every install (~2 GB+ footprint). Dokploy requires a Node.js SSR server (Next.js), Redis, and Postgres — and runs deployments on the same node. Our vision commits to hard budgets: control plane < 300 MB RSS idle, one binary + one database to install.

Language choice also determines how much reference code we can reuse: TypeScript would let us lift Dokploy's tRPC routers nearly verbatim; PHP would do the same for Coolify.

## Decision

The control plane (`cypherd`) and the agent (`cypher-agent`) are written in **Go**. `cypherd` is a single static binary that embeds the API server, the scheduler/reconciler, the NATS JetStream event bus (ADR-003), and the compiled web UI assets. The only external dependency is PostgreSQL.

## Alternatives considered

- **TypeScript/Node (Dokploy lineage).** Maximum code reuse from Dokploy. Rejected: re-inherits the Node runtime weight, needs a process manager, and offers no credible single-binary story.
- **PHP/Laravel (Coolify lineage).** Rejected for the same reasons, more severely.
- **Rust.** Comparable footprint and better raw performance. Rejected on velocity: the Docker/BuildKit/NATS ecosystem and contributor pool in Go are substantially stronger for this domain.

## Consequences

- We extract **logic and schemas** from the reference repos, never code. Porting is design work, not translation (see `research/coolify.md` and `research/dokploy.md`).
- Precedent inside the references themselves: Dokploy's own newest component, `dokploy/apps/monitoring`, is written in Go — they reached the same conclusion for anything that must be light.
- The web UI remains TypeScript (React + Vite) but is compiled to static assets and embedded via `go:embed`; no Node server exists at runtime.
- Core and agent share Go modules (`pkg/`, generated proto stubs), keeping the wire contract in one language.

# CypherPanel

An open-source, resource-efficient alternative to cPanel/WHM. Go control plane, Next.js UI, and a lightweight per-server agent — designed to run where cPanel won't (its installer refuses below 2GB RAM; our per-server agent targets <50MB idle).

See [plan.md](plan.md) for the full architecture, [task.md](task.md) for current progress, and [upcoming-features.md](upcoming-features.md) for the post-MVP roadmap.

## Layout

```
cmd/core/       CypherCore — central API server (Gin, REST /api/v1)
cmd/agent/      CypherAgent — per-server daemon (gRPC/mTLS client)
internal/       Shared Go packages (config, auth, audit, paths, platform, ...)
proto/          gRPC contracts between Core and Agent (source of truth)
migrations/     PostgreSQL schema migrations (golang-migrate format)
web/            Next.js UI (lands in Phase 2)
```

## Development (any OS)

The product runs on Linux servers, but development works from Windows, macOS, or Linux:

```sh
docker compose up -d --wait   # PostgreSQL + Redis + NATS JetStream
make migrate-up               # apply database schema
make build-local              # native binaries into bin/local/
bin/local/cypher-core         # http://localhost:8080/healthz

cd web && npm install && npm run dev   # CypherUI at http://localhost:3000
```

The UI proxies `/api/*` to CypherCore (set `CYPHER_CORE_API_URL` if not on localhost:8080), so there is no CORS setup. Regenerate the typed API client after backend API changes: `make openapi && cd web && npm run gen:api`.

Create the first admin user:

```sh
bin/local/cypher-core create-admin --username admin --password 'choose-a-strong-one'
```

Production binaries are always cross-compiled for Linux: `make build` (amd64 + arm64, CGO disabled).

## Contributing rules that keep this portable

- **No hardcoded filesystem paths.** Use `filepath.Join` and the `internal/paths` layout layer; distro differences (Debian vs RHEL config locations) are data, not code.
- Linux-only syscall code lives behind interfaces in `internal/platform` with `_linux.go` build tags — everything else must compile and unit-test on any OS.
- Shell scripts, templates, and configs are LF-only (enforced via `.gitattributes`).

## License

Apache-2.0 — see [LICENSE](LICENSE).

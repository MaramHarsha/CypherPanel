---
name: go-backend-conventions
description: How Go code is written in the CypherPanel repo — handler structure, config, error style, the no-hardcoded-paths rule, and cross-platform build discipline. Use when writing or reviewing any Go code in cmd/ or internal/.
---

# Go Backend Conventions

## Structure

- Binaries live in `cmd/core` and `cmd/agent`; all shared code in `internal/` packages. Nothing outside `gen/` (generated) and `docs/` (generated) is exported for external import.
- One `internal/` package per concern (`auth`, `audit`, `config`, `jobs`, `paths`, `pki`, `platform`, `store`, `agentrpc`, `api`, `hoststats`). Keep new code in the package whose concern it extends; create a new package only for a genuinely new concern.
- Gin handlers are methods on a handler struct holding its dependencies (see `internal/api/auth_handler.go`); dependencies are passed via the `api.Deps` struct into `api.NewRouter`. No global state, no `init()` wiring.

## Configuration — no hardcoded anything

- **Every** endpoint, path, secret, TTL, and port comes from `internal/config` (env-driven with documented defaults). Feature code never calls `os.Getenv` directly.
- Production mode (`CYPHER_ENV=production`) must hard-fail on missing security-critical config (see `LoadCore`/`LoadAgent`); development mode may fall back with a loud `slog.Warn`.
- **No hardcoded filesystem paths.** Construct paths with `filepath.Join`, never string concatenation with `/`. Agent-side system paths resolve through `internal/paths` (distro-family layouts + `CYPHER_PATH_*` overrides) — if a path isn't there, add it to the `Layout` struct, don't inline it.

## Cross-platform rule

- `CGO_ENABLED=0` always. Production binaries are cross-compiled for Linux (`make build`); development happens on any OS.
- Linux-only syscall code (useradd, systemd, cgroups, /proc) lives behind interfaces in `internal/platform` (or build-tagged files like `internal/hoststats/sample_linux.go`), with a `!linux` stub returning `platform.ErrUnsupported` or zero values. Business logic must compile and unit-test on Windows/macOS.

## Errors & logging

- Wrap errors with package-prefixed context: `fmt.Errorf("store: scanning user: %w", err)`. The prefix is the package name.
- Sentinel errors as package vars (`store.ErrNotFound`, `auth.ErrInvalidToken`, `platform.ErrUnsupported`); compare with `errors.Is`/`errors.As`.
- Logging is `log/slog` with key-value fields (`slog.Info("agent registered", "server_id", id)`). Handlers log server-side errors but return generic messages to clients — never leak internals in API responses.

## Servers & shutdown

- Long-running processes use `signal.NotifyContext` (SIGINT/SIGTERM) and shut down gracefully (`srv.Shutdown`, `grpcSrv.GracefulStop`, `nc.Drain()`); see `cmd/core/main.go`.
- Retry loops (agent registration, result reporting) use bounded exponential backoff and respect `ctx.Done()`.

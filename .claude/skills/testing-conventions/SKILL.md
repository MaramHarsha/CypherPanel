---
name: testing-conventions
description: How CypherPanel is tested — what runs natively on any OS, what needs the docker-compose stack, and what needs a Linux container. Use when writing tests or deciding how to verify a change.
---

# Testing Conventions

## Test tiers (match the tier to the code)

1. **Unit tests, any OS** — platform-neutral logic: auth (hashing, JWT round-trip/expiry/tamper), paths layouts, jobs encode/decode, config parsing. Standard `go test ./...`; must pass on Windows/macOS/Linux with no services running. This is the tier CI's `test` job runs.
2. **Integration, docker-compose stack** — anything needing Postgres/Redis/NATS (store queries, token refresh flow, publish/consume). Bring up with `docker compose up -d --wait`, apply `make migrate-up` first.
3. **Agent E2E, Linux container** — anything exercising `internal/platform` Linux implementations or full task execution. Pattern: cross-compile the agent (`CGO_ENABLED=0 GOOS=linux`), run it in `debian:stable-slim` with `CYPHER_AGENT_CORE_ADDR=host.docker.internal:9090` and the NATS URL pointed at the host, then drive it through the real API and assert on DB rows / container state (e.g. `getent passwd`).

## Writing unit tests

- Table-driven where there are multiple cases; plain sequential tests where a scenario is a flow (see `internal/auth/tokens_test.go`).
- Use `t.Setenv` for env-dependent config/paths tests — never mutate globals.
- Test failure modes, not just happy paths: expired/tampered tokens, malformed hashes, salt randomness are the model (`internal/auth/password_test.go`).
- Constructors that take infra clients accept nil in tests when the tested paths don't touch them (see `newTestTokenService`) — but if you find yourself faking much, the logic probably belongs in a platform-neutral function you can test directly.

## Structural testability rules

- Platform-neutral logic must be testable without Linux — that's why syscall code sits behind `internal/platform` interfaces. If a change can't be unit-tested on Windows, first ask whether the pure logic can be extracted.
- Every agent task handler must have its idempotency verified (run twice, both succeed) — at minimum in the container E2E tier.
- CI (`.github/workflows/ci.yml`) runs build + vet + test + linux amd64/arm64 cross-compile + buf lint on every push/PR. A change that passes tests but breaks `GOOS=linux go build` is broken — run the cross-compile locally when touching build-tagged files.

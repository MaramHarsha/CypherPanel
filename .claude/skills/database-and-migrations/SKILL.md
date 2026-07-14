---
name: database-and-migrations
description: pgx hand-written SQL patterns and golang-migrate workflow for CypherPanel. Use when adding tables, queries, store methods, or schema migrations.
---

# Database & Migrations

## Ground rules

- **No ORM. Explicitly: no GORM.** All SQL is hand-written and lives in `internal/store`, executed via `pgxpool` (plan.md Section 3 — reflection-based mapping and N+1 patterns don't survive millions-of-rows tables).
- One file per aggregate in `internal/store` (`store.go` = users, `servers.go`, `tasks.go`). Store types hold a `*pgxpool.Pool`; constructors are `NewXxx(pool)`.

## Query & scan conventions

- Row scanning goes through a package-private `scanXxx(row pgx.Row)` helper that maps `pgx.ErrNoRows` → `store.ErrNotFound` and wraps other errors with context.
- Nullable SQL → Go: use `COALESCE(col::text, '')` for optional FKs read as strings, `NULLIF($n, '')::uuid` / `::inet` for optional writes, and pointer types (`*time.Time`) for nullable timestamps. Follow the existing patterns in `internal/store/store.go`.
- Writes that must be idempotent use `ON CONFLICT ... DO UPDATE` (see `Servers.UpsertByHostname`) or status-guarded updates (see `Tasks.SetResult`, which only transitions from `pending` so redelivered results are no-ops).
- Return `store.ErrNotFound` (never nil-nil) when zero rows affected/found matters to the caller.

## Migrations (golang-migrate)

- Paired files in `migrations/`: `NNNNNN_name.up.sql` + `NNNNNN_name.down.sql`, zero-padded sequential numbers. Every up has a working down.
- **Never edit a shipped migration.** Schema changes are always a new migration (`000003_server_stats` altered `servers` rather than touching `000001_init`).
- Apply with `make migrate-up` (which runs `go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest` — the `postgres` build tag is required or the driver is missing).
- UUIDs: `uuid PRIMARY KEY DEFAULT gen_random_uuid()` (Postgres built-in, no extension). Timestamps: `timestamptz NOT NULL DEFAULT now()`. Status columns get `CHECK (status IN (...))` constraints.

## Scale expectations

- Tables that will reach millions of rows (accounts, domains, DNS records, audit_log, tasks) get indexes on every FK used in lookups and partial indexes for hot filters (see `tasks_status_idx ... WHERE status = 'pending'`).
- High-cardinality time-series data (per-account metrics history) does **not** go in Postgres — only latest-state snapshots (see the `servers` stats columns from migration 000003). History belongs in the future time-series store.

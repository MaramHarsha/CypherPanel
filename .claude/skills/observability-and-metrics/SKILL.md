---
name: observability-and-metrics
description: What goes to the time-series store vs Postgres, metric/log naming, audit retention, and the Metrics API surface (plan.md §16). Use when adding metrics, structured logs, or the metrics endpoints.
---

# Observability & Metrics

> **Status: partially grounded.** Structured logging (`slog`), the `audit_log` table, and latest-snapshot host stats exist today; the time-series store and Metrics API (§16) are Phase 6. Verify/expand against code as they land, updating in the same PR.

## The cardinal rule: time-series data does NOT go in Postgres

- High-cardinality, high-write metrics history (per-account/domain CPU, RAM, disk I/O, bandwidth over time) goes to a **purpose-built time-series store (Prometheus / VictoriaMetrics)** — never Postgres tables. Postgres index bloat from metrics writes is the fastest way to blow the resource budget (plan.md Section 8).
- Postgres holds **current state only** — e.g. the latest host snapshot columns on `servers` (migration 000003), not a history table. If you're about to add a `*_history` table with a timestamp and a metric value, stop: that's a time-series store's job.

## Metrics API (§16)

- `GET /api/v1/metrics/{scope}` for `server` / `account` / `domain` scopes (RBAC + resource-scoped per [[auth-and-rbac]]), reading from the time-series store, plus a raw Prometheus `/metrics` scrape endpoint for operators' own monitoring.
- Metric names follow Prometheus conventions (`cypher_<subsystem>_<unit>`, base units, labels for scope IDs — but keep label cardinality bounded).

## Structured logging

- `log/slog` with key-value fields (already the standard — see [[go-backend-conventions]]). Consistent field names across the codebase (`server_id`, `account_id`, `task_id`, `error`). Never log secrets, tokens, passwords, or full payloads that may contain them.

## Audit log

- The `audit_log` table is the durable record of privileged actions (see [[auth-and-rbac]]). Phase 6 adds query/dashboard surfaces and **retention policies** — audit rows are append-only; retention is archival/pruning by age, never in-place edits.

# CypherPanel — Engineering Handbook

> Binding rules for all code in `cypherpanel/`. These are checkable; violations block merge. "Why" for architectural rules lives in the referenced ADRs. Product-facing UI rules live in [docs/product/ui-principles.md](docs/product/ui-principles.md).

## Go

1. `gofmt` + `golangci-lint` clean; the lint config in the repo is the arbiter, not personal taste.
2. `context.Context` is the first parameter of any function that blocks, does I/O, or can be canceled. No contexts in structs.
3. Errors are wrapped with `%w` and context at every boundary (`fmt.Errorf("enrolling agent %s: %w", id, err)`); match with `errors.Is`/`errors.As`, never string comparison. Sentinel errors are exported from the package that owns them.
4. Structured logging via `slog` only. No `fmt.Println`, no unstructured strings. Every log line carries the resource IDs it concerns.
5. No global mutable state. Dependencies enter through constructors (`NewScheduler(store, bus, clock)`); composition over inheritance-simulation.
6. Interfaces are defined by the **consumer**, kept small, and accepted as parameters; concrete types are returned.
7. Every goroutine has an owner: a defined lifecycle, a cancellation path, and a way its failure is observed. No fire-and-forget.
8. No panics across package boundaries; `panic` only for programmer-error invariants in `main`/init paths.
9. Time and randomness are injected (`clock`, `rand` interfaces) in anything that needs deterministic tests.
10. No `TODO` without an issue link. No commented-out code. No placeholder implementations — if it can't be finished in this PR, it isn't in this PR.
11. Orchestrator- or proxy-specific behavior exists only inside `agent/driver/*` or `agent/proxy/*` implementations ([project-structure.md](docs/project-structure.md) rule 2).

## Reconciliation & messaging (ADR-003, ADR-005)

12. Every work item carries an idempotency key; every consumer is safe under redelivery.
13. Every reconciler ships an idempotency test: converging twice equals converging once.
14. NATS subjects are contracts — documented in `core/bus`, changed only additively.
15. The bus carries transient traffic only; anything that must survive a restart is in Postgres before it is published.

## Data & compatibility

16. Migrations are reversible and additive-first (add column → backfill → switch → drop later). A migration that can't be rolled back needs an ADR-level justification.
17. Published REST API changes are backward compatible: additive only; breaking changes require a new version path.
18. Protobuf: never reuse or renumber field numbers; `buf breaking` runs in CI and its failure is final.
19. The OpenAPI spec is the source of truth; handlers and clients are generated/validated from it, not vice versa.

## Security

20. Secrets never appear in logs, error messages, or API responses (mask by default — see Coolify's `ApiSensitiveData` idea, [research/coolify.md](research/coolify.md)).
21. Token and secret comparisons are constant-time.
22. Join tokens are single-use and short-lived; agent identity thereafter is its mTLS cert (ADR-002).
23. Anything crossing the agent↔plane boundary is mTLS; there is no plaintext internal traffic.

## Web (`web/`)

24. TypeScript `strict`; no `any` that isn't quarantined with a comment and an issue.
25. API access only through the generated client — hand-written `fetch` calls are forbidden.
26. Server state lives in TanStack Query; no ad-hoc `useEffect` data fetching.
27. Every page satisfies the four-state page contract ([ui-principles.md §1](docs/product/ui-principles.md)) before it's reviewable.

## Testing

28. Every exported function/behavior has tests; table-driven where variants exist. Bug fixes land with the failing test that proves them.
29. Integration tests run against real Postgres (dockerized) — no mocked stores in integration scope.
30. No skipped tests on `main`.

## Process

31. Architectural change → ADR first ([docs/adrs/](docs/adrs/)); ADRs are immutable, supersede instead of editing.
32. Features are implemented against a spec in `docs/features/` written just-in-time (3–8 pages) — no spec, no feature.
33. Docs change in the same PR as the behavior they describe.
34. `../coolify` and `../dokploy` are read-only references; we port logic and schemas, never code — and for Dokploy, check the license per directory first ([research/dokploy.md](research/dokploy.md)).

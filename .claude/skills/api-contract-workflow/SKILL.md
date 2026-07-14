---
name: api-contract-workflow
description: Adding or changing a CypherPanel REST endpoint end to end — versioning, OpenAPI annotations, spec regeneration, and client generation. Use for any change to internal/api routes or handlers.
---

# API Contract Workflow

## Versioning & routing

- Every REST route lives under `/api/v1` (registered in `internal/api/router.go`). Breaking changes require a `/api/v2` group, never an in-place change to v1 semantics.
- Only `/healthz` and `/api/v1/openapi.json` plus the auth login/refresh endpoints are unauthenticated. Everything else goes in the `authed` group; admin-only routes in the `RequireRole(RoleRootAdmin)` group (see the auth-and-rbac skill).

## OpenAPI is generated, never hand-edited

- The spec is generated from swaggo annotations on handlers (`swag/v2`, OpenAPI 3.1) into `docs/` and served live at `GET /api/v1/openapi.json`.
- **Every new/changed handler gets annotations** (`@Summary`, `@Tags`, `@Param`, `@Success`/`@Failure`, `@Router`, `@Security BearerAuth` for authed routes) — copy the style in `internal/api/auth_handler.go`.
- After editing annotations run `make openapi` and commit the regenerated `docs/`. A handler whose behavior changed but whose annotations didn't is a review-blocking defect — a drifted spec is worse than no spec.

## Handler conventions

- Request/response DTOs are unexported structs next to the handler with `json` tags and `binding:"required"` where applicable; bind via `c.ShouldBindJSON`, return 400 with a human-readable message on failure.
- Response bodies are `gin.H`/DTOs with snake_case keys. Errors: `{"error": "message"}` — generic wording client-side, detailed `slog` logging server-side.
- Async operations return `202 Accepted` with the task ID (see `TasksHandler.Create`), never block until an agent finishes.

## Downstream clients

- The CypherUI TypeScript client (Phase 2+) is generated from this spec (`openapi-typescript`) — frontend code never hand-writes fetch paths. Future SDKs (Go/Node/Python, plan.md §14) are also generated; treat any hand-maintained parallel client as a bug.

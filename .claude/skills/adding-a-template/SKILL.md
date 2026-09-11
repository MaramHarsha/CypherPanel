---
name: adding-a-template
description: Add or update a bundled CypherPanel application template. Use when changing core/templates/catalog YAML entries, catalog metadata, resource wiring, generated secrets, database placeholders, routes, health checks, volumes, or published ports.
---

# Add a catalog template

1. Read `docs/features/template-catalog.md`, especially the v1 schema and placeholder grammar. Do not invent fields or add a second runtime path.
2. Confirm every image and setting from the upstream project's official documentation. Pin a stable image tag; never use `latest`. Do not copy source code from another catalog.
3. Add one lowercase, slug-named YAML file under `core/templates/catalog/`. Keep descriptions factual and under 200 characters.
4. Model databases with `resources.databases`; wire applications with `{{db.<name>.<field>}}`. Generate credentials with `{{secret.N}}` (16–64 bytes). Use `{{domain}}` only for the single `route: true` application.
5. Declare persistent data as named volumes with absolute container paths. Declare non-HTTP ports explicitly. Never add host mounts, commands, capabilities, devices, or arbitrary Compose features.
6. Validate the catalog with `cd core && go test ./templates/...`, then run `go test ./...`. If the template changes the API shape, update `core/api/rest/openapi.yaml` and regenerate with `make generate-web`.
7. Review the diff for secrets, floating image tags, accidental host paths, malformed placeholders, and unrelated generated files.

For a stack the v1 schema cannot express, stop and explain the unsupported requirement. Do not approximate it with unsafe fields.

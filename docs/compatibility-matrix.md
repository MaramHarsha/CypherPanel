# CypherPanel Compatibility Matrix

CypherCore (control plane) and CypherAgent (per-server daemon) upgrade at
different times in a fleet, so **mixed versions are normal** during a rolling
upgrade. What keeps that safe is the gRPC contract's **add-only rule** (see the
`grpc-proto-contracts` skill): new fields are additive, and neither side removes
or renumbers fields. The values here are the single source of truth, defined in
[`internal/version`](../internal/version/version.go).

## Supported ranges

| Component | This release | Accepts |
|-----------|--------------|---------|
| CypherCore | `0.1.0` | Agents `>= 0.1.0` |
| CypherAgent | `0.1.0` | Any Core that accepts it |
| Plugin API | `v1` | Manifests with `api_version: v1` |

## Enforcement

- **At registration**, Core checks the Agent's reported version
  (`RegisterRequest.agent_version`) against `MinAgent`. An Agent **older** than
  the minimum is **refused** with `FailedPrecondition` and a clear reason,
  rather than being allowed to fail mysteriously mid-operation.
- **Development builds** (`dev`, empty, or an unrecognised format) are allowed
  but logged, so local/testing agents are never locked out.
- Plugin manifests are validated against `api_version: v1` when loaded
  (`internal/plugins`).

## Upgrade order

1. **Upgrade Core first.** Because the proto contract is add-only, an older
   Agent keeps working against a newer Core (it just doesn't use new fields).
2. **Then roll Agents forward.** A Core will refuse an Agent below `MinAgent`,
   so raising `MinAgent` in a release is the mechanism for dropping support for
   very old Agents — do it only in a release that documents the break here.

## Bumping versions at release time

- Update `Core` (and, when dropping old-Agent support, `MinAgent`) in
  `internal/version/version.go`, and update the table above in the same PR.
- Migrations are **forward-only** in production (`database-and-migrations`
  skill); a shipped migration is never edited — a fix is a new migration.
- Production recovery is via the mandatory pre-upgrade backup, not a blind
  `down` replay.

---
name: upgrade-and-compatibility
description: The cypherctl upgrade/rollback workflow, migration replay rules, pre-upgrade backups, and Core/Agent/plugin version compatibility (plan.md §13). Use when working on upgrades, migrations at release time, or version negotiation.
---

# Upgrade & Compatibility

> **Status: design-intent (pre-implementation).** Grounded in plan.md §13. This is a **release-candidate gate — it must exist before the first production update ships.** Verify against code as it lands, updating in the same PR. Read [[database-and-migrations]] and [[grpc-proto-contracts]].

## Upgrade workflow (`cypherctl upgrade` / `rollback`)

- Track the installed version in a `system_version` record. Upgrades **replay migrations sequentially** from the current version forward (the same paired `.up.sql`/`.down.sql` files from [[database-and-migrations]]) — never skip or apply out of order.
- **Mandatory pre-upgrade backup.** An upgrade takes a backup (DB + config) *before* touching anything, so `rollback` is always possible. No backup → no upgrade.
- `rollback` restores the pre-upgrade backup and reverts to the prior version; it's a first-class command, not a manual recovery scramble.

## Compatibility matrix (Core ↔ Agent ↔ plugin)

- Maintain `docs/compatibility-matrix.md`: which Core versions work with which Agent and plugin API version ranges. In a fleet, Core and Agents upgrade at different times (rolling), so mixed versions are normal — the proto contract's add-only rule ([[grpc-proto-contracts]]) is what makes this safe.
- **Enforce ranges at registration**: when an Agent (or plugin) registers/loads, Core checks its version against the supported range and refuses or warns on mismatch, rather than failing mysteriously mid-operation.

## Migration discipline at release time

- Migrations are forward-only in production; a shipped migration is never edited (a fix is a new migration). Downs exist for dev/rollback but production recovery is via the pre-upgrade backup, not blind `down` replay.
- Data migrations that could be slow on million-row tables are designed to run online / batched, not as a table-locking step that takes the panel down during upgrade.

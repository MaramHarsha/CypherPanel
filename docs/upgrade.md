# Upgrading CypherPanel

CypherPanel upgrades are **backup-first and forward-only**. The installed
version is recorded in the `system_version` table; the database schema version
is tracked by golang-migrate. See also the compatibility matrix
([`docs/compatibility-matrix.md`](compatibility-matrix.md)).

## The golden rule: no backup, no upgrade

An upgrade **always takes a backup first** (database + config) so `rollback` is
possible. If the backup step fails, the upgrade aborts before touching anything.

## Upgrade procedure

1. **Back up** (mandatory, before anything else):
   ```sh
   pg_dump "$CYPHER_DATABASE_URL" > backup-$(date +%F).sql
   cp -a /etc/cypherpanel /etc/cypherpanel.bak   # config + secrets
   ```
2. **Replace binaries** with the new cross-compiled static builds
   (`cypher-core`, `cypher-agent`). Upgrade **Core first**, then roll Agents
   forward — the add-only gRPC contract keeps a mixed fleet working, and Core
   refuses an Agent below `MinAgent`.
3. **Replay migrations sequentially** (never skip or reorder):
   ```sh
   make migrate-up      # golang-migrate applies each new .up.sql in order
   ```
4. **Restart** `cypher-core`; it records the new version in `system_version`.
5. Verify: `cypher-core version` and `GET /healthz`.

## Rollback

1. Stop `cypher-core`.
2. Restore the pre-upgrade backup:
   ```sh
   psql "$CYPHER_DATABASE_URL" < backup-YYYY-MM-DD.sql
   rm -rf /etc/cypherpanel && mv /etc/cypherpanel.bak /etc/cypherpanel
   ```
3. Reinstall the previous binaries and restart.

Production recovery is via the pre-upgrade backup — **not** a blind `migrate
down` replay (downs exist for development). Shipped migrations are never edited;
a fix is always a new migration.

## Checking versions

```sh
cypher-core version
# cypher-core 0.1.0 (minimum supported agent 0.1.0)
```

The recorded database version:
```sql
SELECT version, updated_at FROM system_version;
```

> A fully automated `cypherctl upgrade` / `rollback` that performs these steps
> (including the mandatory backup) is tracked as a post-MVP deliverable
> (plan.md §14); this document is the manual procedure it will encode.

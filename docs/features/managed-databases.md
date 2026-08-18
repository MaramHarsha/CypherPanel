# Feature spec: Managed Databases

> Provisions and operates database engine instances (PostgreSQL, MySQL,
> MariaDB, MongoDB, Redis, Valkey) as first-class resources inside the
> existing organizational spine (Project → Environment → Resource). The
> panel owns the full lifecycle — provisioning, credential management,
> health, backups to S3-compatible targets, restore — with the same
> desired-state reconciliation contract that Applications use
> ([ADR-005](../adrs/ADR-005-desired-state-reconciliation.md)).
>
> Written 2026-07-20, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Research input from
> [coolify.md](../../research/coolify.md) (8 engine models) and
> [dokploy.md](../../research/dokploy.md) (per-engine schema files).
>
> **Scope note (this branch):** provisioning and lifecycle (§1–6) ship here,
> proven live across PostgreSQL, Redis, and MySQL. Backup and restore (§7) are
> a follow-up: the first cut could not work at runtime (docker-exec stream
> framing corrupts dumps; the exec seam had no stdin for restore), so per rule
> 10 it is deferred rather than shipped non-functional, to be rebuilt against a
> live S3 (MinIO) test. Its schema, API, and proto are deferred with it; the
> stripped first-cut code is recoverable from this branch's history at commit
> `1d83f0a`.

## 1. Resource model

A **Database** is a resource that lives in an Environment, just like an
Application. It runs as a Docker container managed by the same
`agent/driver` reconciler that manages Application containers, using the
same management labels (`cypherpanel.managed`, `cypherpanel.db-id`,
`cypherpanel.revision-id`) for desired-state GC.

```
Database:
  id                TEXT PK (db_… prefix)
  environment_id    TEXT NOT NULL → environments(id) ON DELETE CASCADE
  name              TEXT NOT NULL, UNIQUE(environment_id, name)
  engine            TEXT NOT NULL   -- 'postgresql' | 'mysql' | 'mariadb' | 'mongodb' | 'redis' | 'valkey'
  version           TEXT NOT NULL   -- engine-specific tag ('16', '8.4', '11', '7.0', '7.2', '8.0')

  -- runtime placement
  server_id         TEXT NOT NULL → servers(id) ON DELETE RESTRICT

  -- resource limits (noisy-neighbor control, feature-matrix V1 row)
  cpu_limit         REAL            -- fractional cores (e.g. 1.5); NULL = no limit
  memory_limit_mb   INTEGER         -- MiB; NULL = no limit

  -- persistence
  volume_name       TEXT NOT NULL   -- deterministic: cypher-db-<id>
  data_path         TEXT NOT NULL   -- engine-specific mountpoint (e.g. /var/lib/postgresql/data)

  -- networking
  expose_port       INTEGER         -- if set, Traefik TCP entrypoint or host-published port; NULL = private only
  network           TEXT NOT NULL   -- cypher-<environment_id> (same env network as apps)

  -- credentials (sealed at rest, threat-model §5.1)
  root_password_ct      BYTEA       -- NULL for Redis/Valkey (auth is optional)
  root_password_nonce   BYTEA
  root_user             TEXT NOT NULL DEFAULT ''  -- 'postgres', 'root', '' for Redis/Valkey

  -- desired state tracking (ADR-005)
  desired_revision_id   TEXT        -- points at the current config snapshot
  desired_state         TEXT NOT NULL DEFAULT 'running'  -- 'running' | 'stopped': operator intent, authoritative for the scheduler
  status                TEXT NOT NULL DEFAULT 'stopped'   -- agent observation only, never used as intent
  status_detail         TEXT NOT NULL DEFAULT ''
  observed_revision_id  TEXT NOT NULL DEFAULT ''
  status_observed_at    TIMESTAMPTZ

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
```

### Engine matrix

| Engine     | Default image             | Default version | `data_path`                      | Health command                            | Root user  |
|------------|---------------------------|-----------------|----------------------------------|-------------------------------------------|------------|
| postgresql | `postgres`                | `16`            | `/var/lib/postgresql/data`       | `pg_isready -U $user`                     | `postgres` |
| mysql      | `mysql`                   | `8.4`           | `/var/lib/mysql`                 | `mysqladmin ping -u root -p$pass`         | `root`     |
| mariadb    | `mariadb`                 | `11`            | `/var/lib/mysql`                 | `mariadb-admin ping -u root -p$pass`      | `root`     |
| mongodb    | `mongo`                   | `7.0`           | `/data/db`                       | `mongosh --eval "db.adminCommand('ping')"` | `root`    |
| redis      | `redis`                   | `7.2`           | `/data`                          | `redis-cli ping`                          | *(none)*   |
| valkey     | `valkey/valkey`           | `8.0`           | `/data`                          | `valkey-cli ping`                         | *(none)*   |

Valkey is protocol-compatible with Redis 7.2 and uses the same
desired-state spec (different image). It is recommended as the default
for license-sensitive users (glossary.md; feature-matrix.md).

### Revisions

A **DatabaseRevision** is an immutable snapshot of the database's
configuration at a point in time, exactly mirroring the Application
`Revision` pattern. This enables rollback of *configuration* (version
upgrade, resource limit change) — not of data.

```
DatabaseRevision:
  id              TEXT PK (dbr_… prefix)
  database_id     TEXT NOT NULL → databases(id) ON DELETE CASCADE
  config_snapshot JSONB NOT NULL  -- full spec at creation
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
```

## 2. Credential management

Database root passwords are generated server-side (`crypto/rand`, 32
bytes, base64url-encoded) and sealed with the master-key Box before
storage — identical to deploy-key and env-var sealing (threat-model
§5.1). The plaintext password is returned **exactly once** in the create
response; subsequent GET responses return `"root_password": "[sealed]"`.

For Redis and Valkey, the password is optional. If the operator sets
`require_password: true` at creation, a password is generated and the
container receives `--requirepass $pass`. Otherwise, no auth.

The `root_user` is engine-determined and not operator-configurable at v1
(always `postgres` / `root` / empty).

### Initial database *(added 2026-08-17)*

`initial_database` names an application database the engine creates on first
boot. It is optional, immutable after creation, and empty by default.

Without it a managed database offers only what the engine ships with: for
PostgreSQL the `postgres` maintenance database, and for MySQL, MariaDB and
MongoDB nothing at all. That is enough for an application that creates its own
schema inside a database somebody else made, and not enough for the common
case — most self-hosted software expects to be handed a database that already
exists. It is the single largest blocker in the template catalog
([the import report](../dev/template-import-report.md) counts 43 upstream
templates refused for exactly this), and the reason
[template-catalog.md](template-catalog.md) §3 recorded it as a follow-up.

It is a **creation-time** field because the engine images only honour it on an
empty data directory: `POSTGRES_DB` and friends are read by the entrypoint's
first-boot initialization and ignored on every subsequent start. Accepting a
change later would mean either lying (the setting silently does nothing) or
running `CREATE DATABASE` ourselves — an imperative poke at a running server,
which is exactly what ADR-005 forbids. So `PATCH` rejects it, and the field is
carried in the revision snapshot like every other part of the spec.

| Engine | Environment variable | Notes |
|---|---|---|
| postgresql | `POSTGRES_DB` | Without it the engine default `postgres` applies |
| mysql | `MYSQL_DATABASE` | Nothing exists without it |
| mariadb | `MARIADB_DATABASE` | Nothing exists without it |
| mongodb | `MONGO_INITDB_DATABASE` | Advisory: Mongo creates a database on first write regardless |
| redis · valkey | *(none)* | Numbered, not named — the field is rejected |

The value must be a plain SQL identifier — `[A-Za-z_][A-Za-z0-9_]*`, ≤63
characters, the tightest of the engines' own limits. It is interpolated into no
SQL we write; the engine's entrypoint uses it. The restriction exists so that
neither can it become quoting-dependent inside the engine's own init script,
nor can a name valid on one engine break on another.

Template installs always request one (`template-catalog.md` §4), which is what
makes `{{db.<name>.database}}` resolve to a database that exists.

## 3. Lifecycle

A Database goes through a lifecycle that parallels Applications but is
simpler — there is no build stage:

```
create → provision → running
                   ↕ (config change → re-provision)
         stopped ← delete
```

### Status vocabulary (ui-principles §5)

| Status      | Meaning |
|-------------|---------|
| `stopped`   | Birth state, or explicitly stopped |
| `provisioning` | Agent creating/recreating the container |
| `running`   | Container healthy, accepting connections |
| `error`     | Container failed or unreachable |
| `unknown`   | Heartbeat stale — same logic as Applications |

### Provisioning flow

1. **API creates** the Database row + a DatabaseRevision with the sealed
   config snapshot. Observed `status` = `stopped` (nothing runs yet), but
   `desired_state` = `running` — the two are distinct, and the scheduler keys
   only on intent. Create then calls the scheduler to reconcile.
2. **Scheduler** sees `desired_state = running` with a desired revision and
   emits a `DbProvisionWork` item on `work.<server_id>.db.provision`. (Stop
   sets `desired_state = stopped` → the scheduler emits `DbRemoveWork` keeping
   the volume; start sets it back. Work-item MsgIDs are unique per intent
   event so a start-after-stop is never dropped by publish deduplication.)
3. **Agent receives** the work item, which contains the full `DbSpec`
   (engine, version, image, env, volume, network, health command,
   resource limits).
4. **Agent reconciles**: creates the named volume if missing → creates
   the Docker network if missing → runs the container with the
   engine-specific env (`POSTGRES_PASSWORD`, `MYSQL_ROOT_PASSWORD`,
   `MONGO_INITDB_ROOT_PASSWORD`, `--requirepass`) → runs health check
   until passing → reports `DbStatus` on
   `state.<server_id>.db.<db_id>`.
5. **Plane observes** the status and updates the Database row's observed
   state. Provisioning succeeds only from the observed status, never from
   work-item completion (ADR-005).

### Config changes (version upgrade, resource limits)

The operator PATCHes the Database → a new DatabaseRevision is created →
scheduler detects the revision drift → emits `DbProvisionWork` with the
updated spec. The agent stops the old container, starts the new one with
the updated config, health-checks it, and reports. Volume is preserved
across container replacements (it's a named volume, not ephemeral).

### Delete

`DELETE /databases/{id}` sets a `pending_delete` flag. The scheduler
emits `DbRemoveWork` on `work.<server_id>.db.remove`. The agent stops
the container and removes it. **The named volume is NOT deleted by
default** — operators must explicitly confirm volume deletion via a
`?delete_volume=true` query parameter to prevent accidental data loss.
Once the agent reports removal, the plane deletes the Database row.

## 4. API surface (under `/api/v1`)

```
POST   /environments/{id}/databases             → Database (+ root_password once)
GET    /environments/{id}/databases             → [Database]
GET    /databases/{id}                          → Database
PATCH  /databases/{id}                          → Database
DELETE /databases/{id}[?delete_volume=true]     → 204
POST   /databases/{id}/stop                     → 202
POST   /databases/{id}/start                    → 202

-- Connection info
GET    /databases/{id}/connection-info          → {host, port, user, password_hint}
```

Connection info returns the internal hostname
(`<container_name>.<network>`) for same-environment apps, and the
external `host:port` if `expose_port` is set. The password is never
returned in connection-info — only its sealed indicator. The operator
retrieves it from the create response or resets it.

### Password reset

```
POST   /databases/{id}/reset-password           → {root_password: "new"}
```

Generates a new password, seals it, updates the Database row, creates a
new DatabaseRevision (the password is part of the sealed config), and
triggers re-provisioning. The new password is returned exactly once.

## 5. Proto contract

### New messages in `work.proto`

```protobuf
// DbSpec is a Database's desired state as one server's reconciler sees it.
message DbSpec {
  string db_id = 1;
  string environment_id = 2;
  string revision_id = 3;
  string engine = 4;          // 'postgresql', 'mysql', etc.
  string image = 5;           // 'postgres:16', 'mysql:8.4', etc.
  string volume_name = 6;     // 'cypher-db-<id>'
  string data_path = 7;       // engine-specific mount target
  string network = 8;         // 'cypher-<environment_id>'
  map<string, string> env = 9; // POSTGRES_PASSWORD, etc. — decrypted, mTLS only
  string health_cmd = 10;     // shell command for Docker HEALTHCHECK
  uint32 expose_port = 11;    // 0 = private only
  double cpu_limit = 12;      // fractional cores; 0 = no limit
  uint32 memory_limit_mb = 13; // 0 = no limit
}

// DbProvisionWork commands an agent to converge a database container.
message DbProvisionWork {
  string idempotency_key = 1; // database_id + revision_id
  DbSpec spec = 2;
}

// DbRemoveWork commands absence of a database container.
message DbRemoveWork {
  string idempotency_key = 1;
  string db_id = 2;
  bool   delete_volume = 3;   // explicit opt-in for data destruction
}

// DbStatus is one Database's observed state, reported on
// state.<server_id>.db.<db_id>.
message DbStatus {
  string db_id = 1;
  string revision_id = 2;
  string state = 3;           // running, provisioning, stopped, error
  string detail = 4;
  google.protobuf.Timestamp observed_at = 5;
}
```

### DesiredState extension

`DesiredState` gains a `repeated DbSpec db_specs = 2;` alongside the
existing `repeated AppSpec specs = 1;`. The agent reconciles both sets
on (re)connect — databases whose `db_id` is absent from `db_specs` are
removed (same absence-means-remove contract as apps).

### New NATS subjects (additive, rule 14)

| Subject                              | Direction    | Payload          |
|--------------------------------------|-------------|------------------|
| `work.<server>.db.provision`         | plane → agent | `DbProvisionWork` |
| `work.<server>.db.remove`            | plane → agent | `DbRemoveWork`    |
| `state.<server>.db.<db_id>`          | agent → plane | `DbStatus`        |
| `state.*.db.>`                       | plane wildcard | consume all db status |

All sit under the existing `work.<server>.>` / `state.<server>.>`
authorization scope — no new per-agent grants needed.

## 6. Agent-side reconciliation

The Database reconciler lives alongside the Application reconciler in
`agent/driver/docker/`. It reuses the same Docker client, label
conventions, and network-creation logic. Key differences:

- **No build stage** — the image is a public registry tag pulled
  directly by the Docker daemon (unlike Applications where the image is
  pre-built or relayed).
- **Named volumes** — created with `docker volume create` if absent,
  never removed unless explicitly instructed.
- **Health check** — engine-specific shell command passed as a
  `HEALTHCHECK` instruction at container create. The reconciler polls
  `docker inspect` for the health status.
- **Resource limits** — translated to Docker
  `HostConfig.NanoCPUs` / `HostConfig.Memory`.
- **Environment injection** — engine-specific env vars
  (`POSTGRES_PASSWORD`, `MYSQL_ROOT_PASSWORD`, etc.) are injected at
  container create. These travel only over mTLS (rule 23) and are never
  logged (rule 20).

### Convergence contract

The Database reconciler satisfies the same idempotency invariant as the
Application reconciler (rule 13): converging twice equals converging
once. A `DbProvisionWork` for a container that already matches the
desired spec reports success without recreation. A container whose image
or config differs is stopped, removed, and recreated with the same
named volume.

## 7. Backup and restore

> Rebuilt from the stripped first cut (recoverable at `1d83f0a`) with two
> corrections that made the original non-functional, plus a security fix:
>
> - **Transport (both bugs).** The dump/restore data moves through the Docker
>   **archive API** (`/containers/{id}/archive`, i.e. `docker cp`), not through
>   the exec stdout/stdin stream. The dump is written to a file *inside* the
>   container and copied out as a tar (no multiplexed-stream framing to corrupt
>   the gzip); a restore copies the dump file *into* the container and the
>   restore command reads it by shell redirection (no exec stdin needed). The
>   exec seam only runs a command to completion and reports its exit code.
> - **Security (threat-model §8 req 4).** Work items carry the **engine**, not
>   a shell command. The agent derives the dump/restore command from its own
>   engine matrix — the wire never carries an exec verb. S3 credentials are
>   unsealed on the plane and travel only over mTLS (rule 23).
>
> Validated against a live MinIO/S3 target.

### Backup targets (S3-compatible)

A **BackupTarget** is a reusable S3-compatible storage destination.
Multiple databases can reference the same target.

```
BackupTarget:
  id                TEXT PK (bt_… prefix)
  name              TEXT NOT NULL
  endpoint          TEXT NOT NULL  -- S3-compatible endpoint URL
  bucket            TEXT NOT NULL
  region            TEXT NOT NULL DEFAULT ''
  access_key_ct     BYTEA NOT NULL  -- sealed (threat-model §5.1)
  access_key_nonce  BYTEA NOT NULL
  secret_key_ct     BYTEA NOT NULL  -- sealed
  secret_key_nonce  BYTEA NOT NULL
  path_prefix       TEXT NOT NULL DEFAULT ''  -- key prefix inside bucket
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
```

### Backup schedules

```
DatabaseBackup:
  id              TEXT PK (bak_… prefix)
  database_id     TEXT NOT NULL → databases(id) ON DELETE CASCADE
  target_id       TEXT NOT NULL → backup_targets(id) ON DELETE RESTRICT
  schedule        TEXT NOT NULL DEFAULT ''  -- cron expression; '' = manual only
  retention_count INTEGER NOT NULL DEFAULT 7  -- keep last N backups
  enabled         BOOLEAN NOT NULL DEFAULT true
  last_run_at     TIMESTAMPTZ
  last_status     TEXT NOT NULL DEFAULT ''  -- succeeded | failed | running
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
```

**Scheduled firing.** A plane-side cron evaluator (`RunBackupSweeper`, the same
sweeper pattern as previews and the heartbeat-stale sweep, on the shared
`CYPHERD_SWEEP_INTERVAL` — 30 s by default, finer than cron's minute
resolution) walks every enabled schedule with a non-empty `schedule` and fires
`RunBackup` when its next cron time — measured from `last_run_at`, or
`created_at` if it has never run — has passed. `RunBackup` advances
`last_run_at` when it starts, so a schedule fires at most once per due window:
no catch-up storms, and no double-fire before a run completes. An empty
`schedule` is manual-only (API `run` endpoint); a run missed while the plane
was down fires on the next sweep after restart, not as a backlog.

### Backup execution

Per-engine dump commands (adapted from Coolify's
`DatabaseBackupJob.php`):

| Engine     | Dump command                                                    | Output format |
|------------|----------------------------------------------------------------|---------------|
| postgresql | `pg_dumpall -U $user`                                          | SQL           |
| mysql      | `mysqldump -u root -p$pass --all-databases`                    | SQL           |
| mariadb    | `mariadb-dump -u root -p$pass --all-databases`                 | SQL           |
| mongodb    | `mongodump --archive --gzip`                                   | Archive       |
| redis      | Copy `/data/dump.rdb` after `BGSAVE`                           | RDB           |
| valkey     | Copy `/data/dump.rdb` after `BGSAVE`                           | RDB           |

The scheduler emits `DbBackupWork` (engine + S3 coordinates, no shell
command) to the agent hosting the database. The agent:

1. Runs the engine's dump command inside the container, writing to a file
   there (`sh -c '<dump> > /tmp/cypher-backup'`; Redis/Valkey `redis-cli save`
   → `/data/dump.rdb`; Mongo `mongodump --archive=/tmp/cypher-backup --gzip`),
   and checks the exec **exit code**.
2. Copies that file out via the archive API and gzips it (SQL dumps; Mongo
   and RDB are already compact but re-gzipping is uniform and cheap).
3. Uploads to the backup target with key
   `<path_prefix>/<db_id>/<timestamp>.gz` using the unsealed S3 credentials.
4. Reports `DbBackupEvent` to the plane with success/failure + the object key
   + size, and removes the in-container temp file.
5. **Retention** (retention is desired state the plane owns, not agent
   guesswork): after a successful upload the plane computes the sweep set —
   *succeeded* backups beyond `retention_count` — and sends a `DbBackupPruneWork`
   listing those object keys to the database's host. The agent deletes each
   object (S3 `DELETE` is idempotent, so redelivery and a partly-applied prior
   sweep both converge) and reports a `DbBackupPruneEvent` of what it removed.
   The plane then deletes the `BackupRecord` rows **only for the confirmed-gone
   keys** — a `BackupRecord` represents an object that exists in S3, so a key the
   agent could not delete keeps its row and is swept again on the next backup
   (self-healing, no silent orphans; ADR-005: act on observation, not intent).
   Failed/running records carry no object and are left as diagnostic history.

### Backup API

```
POST   /backup-targets                           → BackupTarget
GET    /backup-targets                           → [BackupTarget]
GET    /backup-targets/{id}                      → BackupTarget
PATCH  /backup-targets/{id}                      → BackupTarget
DELETE /backup-targets/{id}                      → 204 (RESTRICT if referenced)

POST   /databases/{id}/backups                   → DatabaseBackup config
GET    /databases/{id}/backups                   → [DatabaseBackup]
PATCH  /databases/{id}/backups/{bak_id}          → DatabaseBackup
DELETE /databases/{id}/backups/{bak_id}          → 204

POST   /databases/{id}/backups/{bak_id}/run      → 202 (trigger now)
GET    /databases/{id}/backups/{bak_id}/history   → [BackupRecord]
POST   /databases/{id}/restore                   → 202 {backup_record_id}
```

### Restore

`POST /databases/{id}/restore` with `{backup_record_id, confirm: true}`
triggers a restore work item (engine + S3 coordinates, no shell command).
The agent:

1. Downloads the backup from S3 and decompresses it.
2. Copies the dump file *into* the container via the archive API.
3. Runs the engine's restore command reading that file
   (`sh -c '<restore> < /tmp/cypher-restore'`; Mongo
   `mongorestore --archive=/tmp/cypher-restore --gzip --drop`; Redis/Valkey
   copy `dump.rdb` into `/data` and restart the container so it reloads).
4. Health-checks the container and reports success/failure.

Restore is destructive — the API requires an explicit `"confirm": true`
field to proceed.

## 8. Migration (SQL)

`0006_managed_databases.sql` — additive (ENGINEERING rule 16):

- `CREATE TABLE databases` (schema per §1)
- `CREATE TABLE database_revisions`
- `CREATE TABLE backup_targets`
- `CREATE TABLE database_backups`
- `CREATE TABLE backup_records` (history of completed backups)
- Indexes on `databases(environment_id)`, `databases(server_id)`,
  `database_revisions(database_id)`, `database_backups(database_id)`,
  `backup_records(database_backup_id, created_at DESC)`

No existing table is altered destructively.

## 9. Security

- **Root passwords** are AES-256-GCM sealed at rest with the master key
  (same `secret.Box` as CA key, deploy keys, env vars). Never in logs,
  error messages, or API responses after creation (rule 20).
- **S3 credentials** (backup targets) are sealed identically.
- **Environment injection** at container create travels only over mTLS
  (rule 23). Passwords in env vars are decrypted in the scheduler's
  memory, transported to the agent, and injected directly into the
  container's env — never on agent disk.
- **Volume isolation**: each database gets its own named Docker volume.
  Databases in different environments use different Docker networks
  (`cypher-<environment_id>`), preventing cross-environment access.
- **Expose port** enables TCP ingress. When set, the agent creates a
  Traefik TCP router (or a Docker published port as a fallback) —
  authenticated by the database engine's own auth, not by the panel.
  The API warns on create if `expose_port` is set without a password.
- **Delete safety**: volumes survive container deletion by default.
  Explicit `?delete_volume=true` required for data destruction.

## 10. Acceptance (testable)

1. Create a PostgreSQL database → response includes root password once →
   subsequent GET returns `"[sealed]"` → container healthy on the
   target server.
2. Create a MySQL, MariaDB, MongoDB, Redis, and Valkey database → each
   starts and passes its engine-specific health check.
3. Connect an Application in the same environment to the database using
   the internal hostname → connection succeeds.
4. PATCH the database version (e.g. PostgreSQL 15 → 16) → container is
   recreated with the new image, data survives on the named volume.
5. Configure a backup with an S3 target → trigger a manual backup →
   file appears in S3 with the correct key prefix → trigger restore →
   data is restored.
6. Scheduled backup fires on cron → retention prunes old backups beyond
   the configured count.
7. Delete the database without `?delete_volume=true` → container removed
   → volume preserved. Delete with `?delete_volume=true` → volume
   removed.
8. Kill the agent mid-provision → agent reconverges on restart →
   database reaches `running` state without manual intervention.
9. Root password never appears in any log line, API response (except
   create), or on-disk artifact outside the sealed store columns.

## 11. Non-goals for this slice

Custom database users/roles (panel manages root only; operators create
app-specific users via the database itself or the terminal feature) ·
read replicas · connection pooling (PgBouncer) · custom configuration
files (`postgresql.conf` overrides) · logical replication · multi-server
database clusters (single-node per database at v1) · backup encryption
at rest in S3 (rely on S3-side encryption) · point-in-time recovery
(PITR, WAL archiving) · database migration between servers (V1.x: same
desired-state reassignment as Application server moves).

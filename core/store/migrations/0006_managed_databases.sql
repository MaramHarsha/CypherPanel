-- +goose Up
-- Phase 3 resource model (docs/features/managed-databases.md §1): Managed
-- Database resources with revisions, backup targets, backup schedules, and
-- backup history. Additive — no existing table is altered destructively
-- (ENGINEERING rule 16).

CREATE TABLE databases (
    id                  TEXT        PRIMARY KEY,
    environment_id      TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    engine              TEXT        NOT NULL,  -- 'postgresql' | 'mysql' | 'mariadb' | 'mongodb' | 'redis' | 'valkey'
    version             TEXT        NOT NULL,

    -- Runtime placement. A server may not be deleted while it still hosts
    -- databases (RESTRICT) — the operator deletes the databases first.
    server_id           TEXT        NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,

    -- Resource limits (noisy-neighbor control, feature-matrix V1 row).
    cpu_limit           REAL,                  -- fractional cores; NULL = no limit
    memory_limit_mb     INTEGER,               -- MiB; NULL = no limit

    -- Persistence.
    volume_name         TEXT        NOT NULL,   -- deterministic: cypher-db-<id>
    data_path           TEXT        NOT NULL,   -- engine-specific mountpoint

    -- Networking.
    expose_port         INTEGER,               -- host-published TCP port; NULL = private only
    network             TEXT        NOT NULL,   -- cypher-<environment_id>

    -- Credentials sealed at rest (threat-model §5.1, same secret.Box as
    -- CA key, deploy keys, and env vars).
    root_user           TEXT        NOT NULL DEFAULT '',
    root_password_ct    BYTEA,                 -- NULL for password-less Redis/Valkey
    root_password_nonce BYTEA,
    require_password    BOOLEAN     NOT NULL DEFAULT false,

    -- Desired-state tracking (ADR-005). desired_state is the operator's
    -- intent (run vs stop) and is authoritative for the scheduler; status is
    -- the agent's observation and is never used as intent — keeping the two
    -- separate is what lets a freshly-created database (observed 'stopped',
    -- desired 'running') provision instead of being torn down.
    desired_revision_id  TEXT,
    desired_state        TEXT        NOT NULL DEFAULT 'running', -- 'running' | 'stopped'
    status               TEXT        NOT NULL DEFAULT 'stopped',
    status_detail        TEXT        NOT NULL DEFAULT '',
    observed_revision_id TEXT        NOT NULL DEFAULT '',
    status_observed_at   TIMESTAMPTZ,

    -- Soft-delete state: pending_delete is set on DELETE request; the row
    -- is removed only after the agent confirms container removal.
    pending_delete      BOOLEAN     NOT NULL DEFAULT false,
    delete_volume       BOOLEAN     NOT NULL DEFAULT false,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name)
);
CREATE INDEX idx_databases_environment ON databases (environment_id);
CREATE INDEX idx_databases_server ON databases (server_id);

-- Immutable revision snapshots (managed-databases.md §1). Config changes
-- create a new revision; rollback re-points desired_revision_id.
CREATE TABLE database_revisions (
    id              TEXT        PRIMARY KEY,
    database_id     TEXT        NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    config_snapshot JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_database_revisions_db ON database_revisions (database_id);

-- FK for desired_revision_id, added after both tables exist to avoid an
-- ordering cycle. SET NULL so pruning old revisions can't orphan.
ALTER TABLE databases
    ADD CONSTRAINT fk_databases_desired_revision
    FOREIGN KEY (desired_revision_id) REFERENCES database_revisions(id) ON DELETE SET NULL;

-- S3-compatible backup destinations. Credentials sealed at rest
-- (threat-model §5.1).
CREATE TABLE backup_targets (
    id               TEXT        PRIMARY KEY,
    name             TEXT        NOT NULL,
    endpoint         TEXT        NOT NULL,
    bucket           TEXT        NOT NULL,
    region           TEXT        NOT NULL DEFAULT '',
    access_key_ct    BYTEA       NOT NULL,
    access_key_nonce BYTEA       NOT NULL,
    secret_key_ct    BYTEA       NOT NULL,
    secret_key_nonce BYTEA       NOT NULL,
    path_prefix      TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backup schedule configuration. Each row ties a database to a backup target
-- with an optional cron schedule and retention policy.
CREATE TABLE database_backups (
    id              TEXT        PRIMARY KEY,
    database_id     TEXT        NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    target_id       TEXT        NOT NULL REFERENCES backup_targets(id) ON DELETE RESTRICT,
    schedule        TEXT        NOT NULL DEFAULT '',   -- cron expression; '' = manual only
    retention_count INTEGER     NOT NULL DEFAULT 7,
    enabled         BOOLEAN     NOT NULL DEFAULT true,
    last_run_at     TIMESTAMPTZ,
    last_status     TEXT        NOT NULL DEFAULT '',   -- succeeded | failed | running
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_database_backups_db ON database_backups (database_id);

-- Individual backup execution records (history).
CREATE TABLE backup_records (
    id                 TEXT        PRIMARY KEY,
    database_backup_id TEXT        NOT NULL REFERENCES database_backups(id) ON DELETE CASCADE,
    object_key         TEXT        NOT NULL DEFAULT '',  -- S3 key
    size_bytes         BIGINT      NOT NULL DEFAULT 0,
    status             TEXT        NOT NULL DEFAULT 'running',  -- running | succeeded | failed
    detail             TEXT        NOT NULL DEFAULT '',
    started_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_backup_records_backup ON backup_records (database_backup_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS backup_records;
DROP TABLE IF EXISTS database_backups;
DROP TABLE IF EXISTS backup_targets;
ALTER TABLE databases DROP CONSTRAINT IF EXISTS fk_databases_desired_revision;
DROP TABLE IF EXISTS database_revisions;
DROP TABLE IF EXISTS databases;

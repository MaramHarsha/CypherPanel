-- +goose Up
-- Phase 3, managed-databases.md §7: S3-compatible backup targets, per-database
-- backup schedules, and backup execution history. Additive (ENGINEERING rule
-- 16) — no existing table is altered.

-- Reusable S3-compatible storage destinations. Credentials are sealed at rest
-- with the master-key Box (threat-model §5.1), same as deploy keys and env
-- vars.
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

-- Backup schedule configuration: ties a database to a target with an optional
-- cron expression and a retention policy.
CREATE TABLE database_backups (
    id              TEXT        PRIMARY KEY,
    database_id     TEXT        NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    target_id       TEXT        NOT NULL REFERENCES backup_targets(id) ON DELETE RESTRICT,
    schedule        TEXT        NOT NULL DEFAULT '',   -- cron expression; '' = manual only
    retention_count INTEGER     NOT NULL DEFAULT 7,    -- keep last N successful backups
    enabled         BOOLEAN     NOT NULL DEFAULT true,
    last_run_at     TIMESTAMPTZ,
    last_status     TEXT        NOT NULL DEFAULT '',   -- succeeded | failed | running
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_database_backups_db ON database_backups (database_id);

-- Individual backup execution records (history). object_key is the S3 key the
-- upload landed at; retention prunes the oldest succeeded rows beyond the
-- schedule's retention_count.
CREATE TABLE backup_records (
    id                 TEXT        PRIMARY KEY,
    database_backup_id TEXT        NOT NULL REFERENCES database_backups(id) ON DELETE CASCADE,
    object_key         TEXT        NOT NULL DEFAULT '',
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

-- Named volumes join the backup machinery (volume-backups.md §3).
--
-- SIBLING TABLES, not a widened backup_records. The spec anticipated adding a
-- nullable application_id beside a nullable database_id, but the real column is
-- `database_backup_id NOT NULL REFERENCES database_backups(id)` — the SCHEDULE,
-- not the database. Making that nullable to admit a second kind of subject
-- would turn a clean table into a union type every existing query has to
-- re-examine, for no gain: the two histories are read separately and pruned
-- separately anyway.
--
-- +goose Up
CREATE TABLE volume_backups (
    id              TEXT        PRIMARY KEY,
    application_id  TEXT        NOT NULL REFERENCES applications(id)   ON DELETE CASCADE,
    target_id       TEXT        NOT NULL REFERENCES backup_targets(id) ON DELETE RESTRICT,
    schedule        TEXT        NOT NULL DEFAULT '',
    retention_count INTEGER     NOT NULL DEFAULT 7,
    enabled         BOOLEAN     NOT NULL DEFAULT true,
    last_run_at     TIMESTAMPTZ,
    last_status     TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One schedule per application: it covers every volume the application
    -- marks as backed up. Per-volume schedules were considered and rejected —
    -- an operator who wants two cadences for two directories of the same app is
    -- describing two applications.
    UNIQUE (application_id)
);
CREATE INDEX idx_volume_backups_app ON volume_backups (application_id);

CREATE TABLE volume_backup_records (
    id               TEXT        PRIMARY KEY,
    volume_backup_id TEXT        NOT NULL REFERENCES volume_backups(id) ON DELETE CASCADE,
    -- The operator's label, recorded per record: one run archives every flagged
    -- volume, and a failure belongs to the one that failed.
    volume_name      TEXT        NOT NULL,
    object_key       TEXT        NOT NULL DEFAULT '',
    size_bytes       BIGINT      NOT NULL DEFAULT 0,
    status           TEXT        NOT NULL DEFAULT 'running',  -- running | succeeded | failed
    detail           TEXT        NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_volume_backup_records ON volume_backup_records (volume_backup_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS volume_backup_records;
DROP TABLE IF EXISTS volume_backups;

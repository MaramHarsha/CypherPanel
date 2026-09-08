-- The control plane backing itself up (plane-disaster-recovery.md §9.2).
--
-- The singleton row exists only while disaster recovery is ARMED; disarming
-- deletes it, so "is this panel backing itself up?" is one question with one
-- answer — the shape panel_tls already uses for the same problem.

-- +goose Up
CREATE TABLE plane_dr_config (
    id              INTEGER PRIMARY KEY DEFAULT 1,
    target_id       TEXT NOT NULL REFERENCES backup_targets(id) ON DELETE RESTRICT,
    path_prefix     TEXT NOT NULL DEFAULT 'plane-state',
    schedule        TEXT NOT NULL DEFAULT '30 3 * * *',
    retention_count INTEGER NOT NULL DEFAULT 14,
    -- The PUBLIC half. The plane can only ever write: the private half is
    -- generated once, shown once, and never stored here — not in this table,
    -- not in the data directory, not in a log line, not in an audit detail.
    recipient       TEXT NOT NULL,
    -- generated | provided. A recipient the operator supplied is one they
    -- already hold the key for; a generated one was shown to them once.
    recipient_mode  TEXT NOT NULL DEFAULT 'generated',
    -- NULL means armed but NOT PROVEN: the panel has never watched anyone
    -- decrypt with the matching key, so it must not imply the operator has it.
    recipient_verified_at TIMESTAMPTZ,
    last_run_at     TIMESTAMPTZ,
    last_status     TEXT NOT NULL DEFAULT '',
    last_detail     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT plane_dr_singleton CHECK (id = 1),
    CONSTRAINT plane_dr_recipient_present CHECK (recipient <> '')
);

-- An INDEX of what is in the bucket, not the truth about it: the bucket is the
-- truth, and a row here whose object was deleted out of band is a row that
-- describes something gone.
CREATE TABLE plane_snapshots (
    id             TEXT PRIMARY KEY,
    object_key     TEXT NOT NULL UNIQUE,
    panel_version  TEXT NOT NULL DEFAULT '',
    schema_version BIGINT NOT NULL DEFAULT 0,
    size_bytes     BIGINT NOT NULL DEFAULT 0,
    sha256         TEXT NOT NULL DEFAULT '',
    row_count      BIGINT NOT NULL DEFAULT 0,
    recipient      TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'running',
    detail         TEXT NOT NULL DEFAULT '',
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    pruned_at      TIMESTAMPTZ
);

CREATE INDEX idx_plane_snapshots_started ON plane_snapshots(started_at DESC);

-- +goose Down
DROP TABLE plane_snapshots;
DROP TABLE plane_dr_config;

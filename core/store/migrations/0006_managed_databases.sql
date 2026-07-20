-- +goose Up
-- Phase 3 resource model (docs/features/managed-databases.md §1): Managed
-- Database resources with revisions. (Backup targets/schedules/history — §7 —
-- are a follow-up with their own migration.) Additive — no existing table is
-- altered destructively (ENGINEERING rule 16).

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

-- Backup targets, schedules, and history (managed-databases.md §7) are a
-- separate follow-up (first cut stripped at commit 1d83f0a) and land with their
-- own migration once the agent-side execution is built and live-tested.

-- +goose Down
ALTER TABLE databases DROP CONSTRAINT IF EXISTS fk_databases_desired_revision;
DROP TABLE IF EXISTS database_revisions;
DROP TABLE IF EXISTS databases;

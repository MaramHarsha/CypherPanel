-- Database restores as observable operations (managed-databases.md
-- §"Restoring", canvas 10d).
--
-- A restore takes the database offline and cannot be cancelled halfway without
-- leaving a half-applied dump behind. The API answered it with an empty 202,
-- so the only honest thing a screen could say was "we asked". This records the
-- operation itself: which backup, how far along, and how it ended.
--
-- +goose Up
CREATE TABLE database_restores (
    id               TEXT        PRIMARY KEY,
    database_id      TEXT        NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    -- The record restored FROM. ON DELETE SET NULL rather than CASCADE: the
    -- retention sweep deletes old backup records, and losing the history of a
    -- restore because the thing it restored has aged out would delete the more
    -- interesting half.
    backup_record_id TEXT        REFERENCES backup_records(id) ON DELETE SET NULL,
    status           TEXT        NOT NULL DEFAULT 'running',
    -- Where a running restore has got to, in the agent's own vocabulary.
    step             TEXT        NOT NULL DEFAULT '',
    bytes_done       BIGINT      NOT NULL DEFAULT 0,
    bytes_total      BIGINT      NOT NULL DEFAULT 0,
    detail           TEXT        NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    CONSTRAINT database_restores_status CHECK (status IN ('running', 'succeeded', 'failed')),
    CONSTRAINT database_restores_step CHECK (step IN ('', 'fetching', 'stopping', 'applying', 'restarting'))
);

-- The screen asks one question — "is this database being restored right now?" —
-- so the index answers exactly that.
CREATE INDEX idx_database_restores_db ON database_restores (database_id, started_at DESC);

-- `databases.status` gains the word "restoring", but no CHECK is added here:
-- the column has never had one, the vocabulary is enforced in core/domain, and
-- introducing a constraint in this migration would make it fail on any row a
-- previous version happened to leave outside the list.

-- +goose Down
UPDATE databases SET status = 'unknown' WHERE status = 'restoring';
DROP TABLE IF EXISTS database_restores;

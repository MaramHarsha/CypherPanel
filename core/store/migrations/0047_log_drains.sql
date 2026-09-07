-- Log drains (log-drains.md §3): the panel's outbox for log lines.
--
-- The panel keeps a WINDOW of runtime logs, not an archive — 24 hours, 512 MiB,
-- oldest-first discard. That is the right size for "what happened at 03:00" and
-- the wrong size for "what did this endpoint return last Tuesday". The fix is
-- not a bigger window, which is the silent disk fill both reference platforms
-- are known for; it is handing the lines to something whose job is keeping them.

-- +goose Up
CREATE TABLE log_drains (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,
    -- NULL means every project. Nothing finer than a project: every shipped
    -- line already carries its environment as a label, so a sink filters
    -- better than we can, and scope answers a TENANCY question — whose logs
    -- leave the panel — which stops at the project.
    project_id   TEXT REFERENCES projects(id) ON DELETE CASCADE,
    -- An S3 drain names an existing Backup Target rather than carrying a second
    -- sealed S3 credential: rotating a key in two places is how the second one
    -- is found at 02:00 on the night the batch fails.
    target_id    TEXT REFERENCES backup_targets(id) ON DELETE RESTRICT,
    -- The WHOLE config is sealed, not just its secret field, because which half
    -- of a Loki config is a secret changes per deployment: X-Scope-OrgID is a
    -- tenant id at one site and an access boundary at another.
    config_ct    BYTEA NOT NULL,
    config_nonce BYTEA NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    -- Observed, written by the shipper; never operator-set.
    last_shipped_at TIMESTAMPTZ,
    last_error      TEXT NOT NULL DEFAULT '',
    last_error_at   TIMESTAMPTZ,
    -- What the retention window discarded before this drain could ship it. A
    -- drain down a week resumes at the oldest message still held, having lost
    -- the rest, and this is where it says so.
    dropped_lines   BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_log_drains_enabled ON log_drains(enabled);

-- +goose Down
DROP TABLE log_drains;

-- What a server has left on the disk its daemon actually uses
-- (disk-management.md §4).
--
-- Zero means "not reported" — an agent that predates this, or a host where the
-- figure could not be read. The plane reads that as unknown and never as full,
-- so a node that cannot answer is silent rather than alarming.
--
-- +goose Up
ALTER TABLE servers
    ADD COLUMN disk_total_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN disk_free_bytes  BIGINT NOT NULL DEFAULT 0,
    -- Whether this server is currently below the threshold. Stored, because the
    -- alert fires on the TRANSITION: a heartbeat arrives every few seconds, and
    -- a channel that repeats itself gets muted, taking the next real alert with
    -- it (the same rule app.crashed follows).
    ADD COLUMN disk_low BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE servers
    DROP COLUMN disk_low,
    DROP COLUMN disk_free_bytes,
    DROP COLUMN disk_total_bytes;

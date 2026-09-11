-- Which subsystem made a server degraded (agent-updates.md §7).
--
-- The agent has keyed its health by subsystem since ADR-010, but only the
-- collapsed status word crossed the wire, so the panel could report a host
-- amber and say nothing about why. An operator's only recourse was to read the
-- agent's log ON THE HOST — in an architecture whose first decision (ADR-002)
-- is that there is no SSH.
--
-- Named for the wire field rather than reusing `status_detail`, which
-- `applications` already has as a human sentence: one API vocabulary word must
-- not mean two shapes.
--
-- JSONB and replaced wholesale: this is OBSERVED state with one writer, it is
-- never queried by field, and a row per subsystem would need its own lifecycle
-- (when does an entry that stopped being reported get deleted?) for two
-- reporters. Empty is healthy.
--
-- +goose Up
ALTER TABLE servers
    ADD COLUMN subsystem_health JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE servers
    DROP COLUMN subsystem_health;

-- Agent version channels (agent-updates.md §2, ADR-010).
--
-- Two rows and a selector, rather than a desired version per server: a
-- forty-host fleet would otherwise be forty decisions that must agree, and
-- "promote" would be a bulk edit. A server's desired version is its channel's,
-- so promotion is ONE write and joining canary is one dropdown — while what
-- crosses the wire is still per-server desired state, which keeps ADR-010 §6's
-- "staged rollout is a later refinement on the same primitive" true rather than
-- aspirational.
--
-- BOTH ROWS SHIP EMPTY, and empty means no instruction. Upgrading a panel must
-- not start replacing binaries across a fleet nobody asked it to touch — that
-- is the exact trust wound the feature matrix records against Coolify.
-- Defaulting to the panel's own version would inherit it on purpose.
--
-- +goose Up
CREATE TABLE agent_channels (
    channel         TEXT PRIMARY KEY CHECK (channel IN ('stable', 'canary')),
    desired_version TEXT        NOT NULL DEFAULT '',
    -- '' derives the artifact prefix from the version, which is every fleet
    -- using the project's own releases. A mirror mirrors the prefix.
    artifact_base   TEXT        NOT NULL DEFAULT '',
    -- rollback permits a version below the running one. It is per channel
    -- because it is a property of the instruction, not of the host.
    rollback        BOOLEAN     NOT NULL DEFAULT false,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by      TEXT
);

INSERT INTO agent_channels (channel) VALUES ('stable'), ('canary');

ALTER TABLE servers
    ADD COLUMN agent_channel       TEXT NOT NULL DEFAULT 'stable'
        CHECK (agent_channel IN ('stable', 'canary')),
    -- The observed half, from heartbeats. Phase is a string rather than an enum
    -- so an agent newer than this plane can report a phase it does not know
    -- without its whole heartbeat being dropped.
    ADD COLUMN agent_update_phase  TEXT NOT NULL DEFAULT '',
    ADD COLUMN agent_update_target TEXT NOT NULL DEFAULT '',
    ADD COLUMN agent_update_detail TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE servers
    DROP COLUMN agent_update_detail,
    DROP COLUMN agent_update_target,
    DROP COLUMN agent_update_phase,
    DROP COLUMN agent_channel;
DROP TABLE agent_channels;

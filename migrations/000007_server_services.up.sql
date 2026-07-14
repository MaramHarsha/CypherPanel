-- Latest managed-service health per server, refreshed each heartbeat.
-- Current-state snapshot (not history) — an array of {name, state} objects.

ALTER TABLE servers
    ADD COLUMN services jsonb NOT NULL DEFAULT '[]'::jsonb;

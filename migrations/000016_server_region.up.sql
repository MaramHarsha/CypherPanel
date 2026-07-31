-- Multi-region fleet grouping (plan.md §17). A server declares its region at
-- registration (CYPHER_AGENT_REGION); the panel groups and filters the fleet
-- by it, and data-residency queries scope to it.
--
-- Empty string rather than NULL keeps every existing row valid and the
-- filtering SQL free of NULL special-casing — "" reads as "unassigned".

ALTER TABLE servers ADD COLUMN region text NOT NULL DEFAULT '';

CREATE INDEX idx_servers_region ON servers (region) WHERE region <> '';

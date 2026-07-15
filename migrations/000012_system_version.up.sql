-- Installed-version tracking (plan.md §13, Upgrade & Migration Framework). A
-- single row records the CypherCore version currently running against this
-- database, so upgrades/rollbacks have a definitive "from" version. The schema
-- migration number itself is tracked separately by golang-migrate.

CREATE TABLE system_version (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    version    text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

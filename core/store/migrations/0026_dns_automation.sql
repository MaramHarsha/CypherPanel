-- +goose Up
-- DNS automation and domain ownership (docs/features/dns-automation.md).
--
-- Additive and reversible (ENGINEERING rule 16): three new tables plus one
-- column that every existing row defaults correctly into.

-- dns_providers is a singleton on panel_mail's exact shape: a panel has one DNS
-- connection, and "exactly one row" is a constraint the database can hold
-- rather than a rule the application has to remember. kind exists from day one
-- so a second provider is a new implementation of one interface, not a schema
-- change (§2) — Dokploy's two-client seam is the evidence that it will be
-- wanted. The config is one sealed JSON blob, the _ct/_nonce convention.
CREATE TABLE dns_providers (
    id           INTEGER     PRIMARY KEY DEFAULT 1,
    kind         TEXT        NOT NULL DEFAULT 'cloudflare',
    config_ct    BYTEA       NOT NULL,
    config_nonce BYTEA       NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dns_providers_singleton CHECK (id = 1)
);

-- dns_zones is a CACHE of what the provider says we can manage, never a place
-- an operator types a zone name (§3.2). An operator-entered zone list would be
-- a second place to lie about ownership, which is the one thing this feature
-- exists to prevent.
CREATE TABLE dns_zones (
    id               TEXT        PRIMARY KEY,
    provider_zone_id TEXT        NOT NULL,
    name             TEXT        NOT NULL UNIQUE,
    refreshed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- dns_records is desired state for a third-party API, which is ADR-005 applied
-- somewhere other than an agent: `desired` is what should be true, and
-- provider_record_id being non-NULL is the observation that it is.
--
-- ON DELETE SET NULL is the load-bearing choice in this migration (§3.3). A
-- cascade would delete this row the instant the application is deleted, taking
-- with it the only record of what still has to be removed from Cloudflare —
-- which is exactly how the orphan leaks that research/coolify.md warns about.
-- Instead the row survives as a TOMBSTONE (application_id NULL, desired
-- 'absent') until the reconciler has confirmed the record is gone, and only
-- then is it deleted.
CREATE TABLE dns_records (
    id                 TEXT        PRIMARY KEY,
    application_id     TEXT        REFERENCES applications(id) ON DELETE SET NULL,
    zone_id            TEXT        NOT NULL REFERENCES dns_zones(id) ON DELETE RESTRICT,
    name               TEXT        NOT NULL,
    type               TEXT        NOT NULL DEFAULT 'A',
    content            TEXT        NOT NULL,
    -- 'present' | 'absent'. There is deliberately no status column: what should
    -- be true is `desired`, and what IS true is provider_record_id.
    desired            TEXT        NOT NULL DEFAULT 'present',
    provider_record_id TEXT,
    last_error         TEXT        NOT NULL DEFAULT '',
    attempt            INT         NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dns_records_desired CHECK (desired IN ('present', 'absent')),
    -- One managed record per name per type per zone. A second row for the same
    -- name would race the reconciler against itself.
    UNIQUE (zone_id, name, type)
);

-- The sweeper's working set: rows that are out of sync, cheapest first.
CREATE INDEX idx_dns_records_pending ON dns_records (next_attempt_at)
    WHERE (desired = 'present' AND provider_record_id IS NULL)
       OR (desired = 'absent'  AND provider_record_id IS NOT NULL);
CREATE INDEX idx_dns_records_application ON dns_records (application_id);

-- Where DNS records for this server's applications point. The plane cannot
-- learn this itself: the agent dials out (ADR-002), the heartbeat carries no
-- address, and a source address seen through NAT is not necessarily the one the
-- internet reaches (§3.4). Empty means "not set", which is a named, actionable
-- state rather than a silent failure.
ALTER TABLE servers ADD COLUMN public_address TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE servers DROP COLUMN public_address;
DROP INDEX IF EXISTS idx_dns_records_application;
DROP INDEX IF EXISTS idx_dns_records_pending;
DROP TABLE IF EXISTS dns_records;
DROP TABLE IF EXISTS dns_zones;
DROP TABLE IF EXISTS dns_providers;

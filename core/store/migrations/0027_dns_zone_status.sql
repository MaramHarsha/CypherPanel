-- +goose Up
-- A zone's activation status (dns-automation.md §3.2).
--
-- The first cut of this feature listed zones with Cloudflare's status=active
-- filter, which quietly excluded every zone that had been added but whose
-- nameservers had not been repointed yet. The operator sees their domain in
-- Cloudflare and the panel says it can see no zones — the exact dead end
-- ui-principles §11 forbids, and the first thing a real account hit.
--
-- Ownership does not depend on activation: a zone in your account is yours
-- whether or not it has finished setting up. What DOES depend on it is whether
-- the domain resolves, so the status is stored and surfaced rather than used
-- as a filter.
--
-- Additive and reversible (ENGINEERING rule 16); existing rows default to the
-- status they were only ever allowed to have.
ALTER TABLE dns_zones ADD COLUMN status TEXT NOT NULL DEFAULT 'active';

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN status;

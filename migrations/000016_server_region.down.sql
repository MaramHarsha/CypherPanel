DROP INDEX IF EXISTS idx_servers_region;
ALTER TABLE servers DROP COLUMN IF EXISTS region;

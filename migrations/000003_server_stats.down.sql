ALTER TABLE servers
    DROP COLUMN IF EXISTS load_1m,
    DROP COLUMN IF EXISTS memory_total_bytes,
    DROP COLUMN IF EXISTS memory_used_bytes,
    DROP COLUMN IF EXISTS disk_total_bytes,
    DROP COLUMN IF EXISTS disk_used_bytes;

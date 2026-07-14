ALTER TABLE accounts
    DROP COLUMN IF EXISTS ssl_status,
    DROP COLUMN IF EXISTS ssl_expires_at;

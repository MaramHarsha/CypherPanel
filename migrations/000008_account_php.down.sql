ALTER TABLE accounts
    DROP COLUMN IF EXISTS php_version,
    DROP COLUMN IF EXISTS php_settings;

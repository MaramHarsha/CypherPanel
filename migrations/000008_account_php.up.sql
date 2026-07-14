-- Per-account PHP runtime: version branch and validated php.ini overrides
-- (MultiPHP INI editor, plan.md §4B). Settings are an allowlisted map applied
-- as pool-level php_admin_value.

ALTER TABLE accounts
    ADD COLUMN php_version  text NOT NULL DEFAULT '',
    ADD COLUMN php_settings jsonb NOT NULL DEFAULT '{}'::jsonb;

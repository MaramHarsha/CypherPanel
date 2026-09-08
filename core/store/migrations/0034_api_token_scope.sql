-- Per-project API token scope (api-tokens.md §"Narrowing a token").
--
-- Abilities already say what a token may DO. They say nothing about WHERE, so a
-- deploy token minted for one project can deploy every project its owner can
-- reach. A CI pipeline needs exactly one project, and handing it the whole
-- account is the gap between what the credential is for and what it can do.
--
-- Nullable, because unscoped is the existing behaviour and every token issued
-- before this migration keeps it. ON DELETE CASCADE: a token that names a
-- deleted project would otherwise be scoped to nothing and reach nothing, which
-- is a confusing way to say "revoked" — deleting it outright is honest.
--
-- +goose Up
ALTER TABLE api_tokens ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE CASCADE;
CREATE INDEX idx_api_tokens_project ON api_tokens (project_id) WHERE project_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_api_tokens_project;
ALTER TABLE api_tokens DROP COLUMN project_id;

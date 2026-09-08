-- +goose Up
-- control-plane-hardening.md §3: the inbox gains a panel-level kind
-- (panel.update_available) that belongs to no project. project_id becomes
-- nullable for exactly those rows; every project-scoped write still sets it.
-- Loosening a constraint is additive and reversible (ENGINEERING rule 16): the
-- down step removes the panel-level rows and restores NOT NULL.
ALTER TABLE inbox_items ALTER COLUMN project_id DROP NOT NULL;

-- +goose Down
DELETE FROM inbox_items WHERE project_id IS NULL;
ALTER TABLE inbox_items ALTER COLUMN project_id SET NOT NULL;

-- +goose Up
-- Phase 3, preview-environments.md: per-Application preview opt-in, and the
-- Preview resource that tracks one live PR preview and the first-class rows it
-- created. Additive (ENGINEERING rule 16).

ALTER TABLE applications ADD COLUMN preview_enabled     BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE applications ADD COLUMN preview_base_domain TEXT    NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN preview_ttl_hours   INTEGER NOT NULL DEFAULT 72;

-- One live preview per (source application, PR number). The child environment
-- and cloned application are ordinary rows; deleting the source application
-- cascades the preview, and deleting the environment (its own cascade removes
-- the cloned app) cascades the preview too. preview_app_id is SET NULL so the
-- destroy path can drop the app first without violating the link.
CREATE TABLE previews (
    id             TEXT        PRIMARY KEY,
    source_app_id  TEXT        NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    environment_id TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    preview_app_id TEXT        REFERENCES applications(id) ON DELETE SET NULL,
    pr_number      INTEGER     NOT NULL,
    pr_branch      TEXT        NOT NULL,
    domain         TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'creating',  -- creating | running | error | destroying
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_app_id, pr_number)
);
CREATE INDEX idx_previews_source ON previews (source_app_id);
CREATE INDEX idx_previews_expires ON previews (expires_at);

-- +goose Down
DROP TABLE IF EXISTS previews;
ALTER TABLE applications DROP COLUMN preview_ttl_hours;
ALTER TABLE applications DROP COLUMN preview_base_domain;
ALTER TABLE applications DROP COLUMN preview_enabled;

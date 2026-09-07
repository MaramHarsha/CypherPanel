-- Revision promotion (revision-promotion.md §5): ship the artifact that was
-- tested, rather than rebuilding one that should be the same.
--
-- One additive column. The promoted revision carries the TARGET application's
-- own canonical image tag — not the source's — and this column records where
-- the artifact came from, which is what makes the distribute stage know to go
-- fetch it rather than build it.

-- +goose Up
ALTER TABLE revisions ADD COLUMN promoted_from_revision_id TEXT REFERENCES revisions(id) ON DELETE SET NULL;

CREATE INDEX idx_revisions_promoted_from ON revisions(promoted_from_revision_id)
    WHERE promoted_from_revision_id IS NOT NULL;

-- +goose Down
ALTER TABLE revisions DROP COLUMN promoted_from_revision_id;

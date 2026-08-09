-- +goose Up
-- Deploy from container image (feature-matrix V1; unblocks the ADR-007
-- template catalog, whose templates declare image-based Applications).
-- source_kind gains 'image'; the reference lives in its own column rather
-- than overloading source_repo, because the two are different vocabularies
-- (git remote vs OCI reference) and the API presents them as such.
-- Additive (ENGINEERING rule 16): existing rows default to empty.

ALTER TABLE applications ADD COLUMN source_image TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE applications DROP COLUMN source_image;

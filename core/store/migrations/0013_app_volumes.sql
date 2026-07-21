-- +goose Up
-- Persistent volume mounts on applications (feature-matrix V1: "Volumes /
-- mounts"). Stored as a JSONB array of {name, path}; the deterministic Docker
-- volume name is derived at spec-build time. Additive (ENGINEERING rule 16).

ALTER TABLE applications ADD COLUMN volumes JSONB NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE applications DROP COLUMN volumes;

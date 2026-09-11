-- +goose Up
-- Per-application CPU/memory limits (feature-matrix V1: noisy-neighbor control
-- on shared servers). Mirrors the database columns; NULL = no limit. Additive
-- (ENGINEERING rule 16).

ALTER TABLE applications ADD COLUMN cpu_limit       REAL;
ALTER TABLE applications ADD COLUMN memory_limit_mb INTEGER;

-- +goose Down
ALTER TABLE applications DROP COLUMN memory_limit_mb;
ALTER TABLE applications DROP COLUMN cpu_limit;

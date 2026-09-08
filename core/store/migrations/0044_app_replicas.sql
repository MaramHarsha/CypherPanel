-- Replica observations (app-scaling.md §8).
--
-- A JSONB column rather than a sibling table, because the whole set is always
-- written by ONE reporter in ONE message: the node that owns the replicas
-- reports all of them together, so there is no partial write to reconcile and
-- no per-index row for a scale-down to sweep. Replacing the document wholesale
-- IS the sweep.
--
-- It is observation, not desired state: runtime_replicas says how many should
-- run, and this says what the agent last saw. The two are allowed to differ
-- (ADR-005), and the gap is exactly what the panel draws as "DESIRED 3,
-- RUNNING 2".

-- +goose Up
ALTER TABLE applications ADD COLUMN replica_status JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE applications DROP COLUMN replica_status;

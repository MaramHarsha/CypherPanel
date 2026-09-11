-- +goose Up
-- Observed Application status (ADR-005): what the agent actually reports,
-- never what the pipeline intends. status uses the ui-principles §5
-- vocabulary (running · deploying · stopped · error · degraded · unknown);
-- 'stopped' is the birth state (no desired revision yet). observed_revision_id
-- is the revision actually serving per the last report — deliberately NOT a
-- foreign key: it is an observation, and a pruned revision must not block or
-- distort status writes. Additive (ENGINEERING rule 16).

ALTER TABLE applications
    ADD COLUMN status TEXT NOT NULL DEFAULT 'stopped',
    ADD COLUMN status_detail TEXT NOT NULL DEFAULT '',
    ADD COLUMN observed_revision_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN status_observed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE applications
    DROP COLUMN status,
    DROP COLUMN status_detail,
    DROP COLUMN observed_revision_id,
    DROP COLUMN status_observed_at;

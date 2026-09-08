-- Maintenance mode (app-access-control.md §7), the third front-door capability.
--
-- Current state like the other two, and for the same reason: if it were
-- snapshotted onto a revision, a rollback would silently lift a maintenance
-- page. A control that changes because someone re-pointed a revision is not a
-- control.
--
-- maintenance_since exists because the failure mode of this feature is not a
-- bug — it is maintenance left ON, a Friday migration and a Monday of silence.
-- The panel's job is to make the on-state impossible to miss, so the UI shows
-- how long it has been on and needs a stamp to say it from. Turning it on twice
-- deliberately does not reset the clock (the UPDATE is written to keep it), so
-- an idempotent PUT cannot hide the age of an outage.
--
-- +goose Up
ALTER TABLE applications
    ADD COLUMN maintenance_mode  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN maintenance_since TIMESTAMPTZ;

-- +goose Down
ALTER TABLE applications
    DROP COLUMN maintenance_since,
    DROP COLUMN maintenance_mode;

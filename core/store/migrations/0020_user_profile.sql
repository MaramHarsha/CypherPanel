-- +goose Up
-- Who a person is to their teammates, and the clock they read timestamps in
-- (canvas 7a/13i). The panel has only ever known an address and a role, so a
-- name in a member list or an audit line had to be an email; this is the field
-- that lets it be a name instead.
--
-- Additive with defaults (ENGINEERING rule 16): every existing account reads as
-- '' and keeps behaving exactly as it does today — the UI falls back to the
-- address when the name is empty, and an empty timezone means UTC, which is
-- what the panel already prints (ui-principles §10).
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN timezone     TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN timezone;
ALTER TABLE users DROP COLUMN display_name;

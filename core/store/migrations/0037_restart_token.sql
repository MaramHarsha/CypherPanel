-- Restarting an application as desired state (deployment-control.md §3).
--
-- The imperative version of a restart — a verb the agent obeys — is what
-- ADR-005 does not allow: a reconciler that takes orders on the side stops
-- being able to converge from desired state alone. So a restart is a token
-- the spec carries and the container is labelled with. A new token is a
-- difference in desired state, which the existing recreate path already knows
-- how to close; converging twice after it still mutates nothing.
--
-- Empty is the birth value and means "no restart has been asked for", which is
-- what every existing application has.
--
-- +goose Up
ALTER TABLE applications ADD COLUMN restart_token TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE applications DROP COLUMN restart_token;

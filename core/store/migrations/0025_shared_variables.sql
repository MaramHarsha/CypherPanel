-- +goose Up
-- Phase 4, shared-variables.md §2: project shared variables — one SEALED value
-- defined once for a whole project, or narrowed to a single environment of it,
-- referenced from any application's env vars as {{shared.KEY}}. Values go
-- through the same secret.Box as app env vars and are never returned, so the
-- storage shape is the familiar sealed pair (ENGINEERING rule 20).
--
-- Additive and reversible (ENGINEERING rule 16): one new table plus three
-- columns that every existing row defaults correctly into.

CREATE TABLE shared_variables (
    id         TEXT NOT NULL PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- NULL = the whole project. Scope is a property of the VARIABLE, not of
    -- the reference, which is what lets a value be promoted from project to
    -- environment scope without editing any referencing application (§3).
    environment_id TEXT REFERENCES environments(id) ON DELETE CASCADE,
    key            TEXT  NOT NULL,          -- [A-Za-z_][A-Za-z0-9_]*
    value_ct       BYTEA NOT NULL,          -- sealed value (AES-256-GCM)
    value_nonce    BYTEA NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- NULLS NOT DISTINCT is load-bearing (§2): under the default semantics a
    -- NULL environment_id is not equal to itself, so two project-scoped rows
    -- with the same key would both be accepted and resolution would become
    -- order-dependent. Requires PostgreSQL 15+, which our 16+ floor
    -- (docs/tech-stack.md) guarantees.
    UNIQUE NULLS NOT DISTINCT (project_id, environment_id, key)
);
CREATE INDEX idx_shared_variables_project ON shared_variables (project_id);
CREATE INDEX idx_shared_variables_environment ON shared_variables (environment_id);

-- Which shared keys a sealed app env var references, in CLEARTEXT (§2). Key
-- names are already returned by GET /applications/{id}/env, so this discloses
-- nothing new — and it is what makes the used-by count and the drift marker
-- plain SQL instead of a read path that has to unseal values.
ALTER TABLE app_env_vars ADD COLUMN shared_refs TEXT[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_app_env_vars_shared_refs ON app_env_vars USING GIN (shared_refs);

-- The drift marker is DERIVED from two timestamps, not a mutable flag (§5):
-- startRollout stamps the deployment at the instant buildSpec froze that
-- environment onto the wire, and the plane copies the stamp onto the
-- application only when that rollout is OBSERVED running — so a failed deploy
-- can never mark an application clean.
ALTER TABLE deployments  ADD COLUMN env_resolved_at TIMESTAMPTZ;
ALTER TABLE applications ADD COLUMN env_applied_at  TIMESTAMPTZ;

-- +goose Down
ALTER TABLE applications DROP COLUMN env_applied_at;
ALTER TABLE deployments  DROP COLUMN env_resolved_at;
DROP INDEX IF EXISTS idx_app_env_vars_shared_refs;
ALTER TABLE app_env_vars DROP COLUMN shared_refs;
DROP TABLE IF EXISTS shared_variables;

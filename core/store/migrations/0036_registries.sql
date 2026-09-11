-- Private container registries (registries.md; ADR-008 path 3).
--
-- ADR-008's whole point is that no registry is required: a single-server build
-- keeps the image in the local daemon, and a multi-server one travels over the
-- mTLS relay. A registry is for the two cases neither covers — pulling a
-- private base image, and pushing builds somewhere the operator already runs.
--
-- Team-scoped rather than panel-scoped: credentials for one customer's registry
-- have no business being usable by another team's applications, and the team is
-- the boundary every other shared credential here already uses.
--
-- +goose Up
CREATE TABLE registries (
    id           TEXT        PRIMARY KEY,
    team_id      TEXT        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    -- Host, optionally with a namespace: ghcr.io, ghcr.io/acme, registry:5000.
    url          TEXT        NOT NULL,
    username     TEXT        NOT NULL DEFAULT '',
    -- The token, sealed under the master key. Read back only to authenticate a
    -- pull or a push, never to an API response (rule 20).
    token_ct     BYTEA       NOT NULL,
    token_nonce  BYTEA       NOT NULL,
    -- What this credential is allowed to be used for. A pull-only credential
    -- attached to a build that wants to push is refused at validation rather
    -- than discovered as a 403 from the registry mid-deploy.
    can_pull     BOOLEAN     NOT NULL DEFAULT true,
    can_push     BOOLEAN     NOT NULL DEFAULT false,
    -- The last test's outcome, so the list can show whether a credential is
    -- known-good without re-authenticating on every page render.
    last_test_at     TIMESTAMPTZ,
    last_test_ok     BOOLEAN     NOT NULL DEFAULT false,
    last_test_detail TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, name)
);
CREATE INDEX idx_registries_team ON registries (team_id);

-- An image-sourced application may pull through a registry credential, and a
-- built application may push its result to one.
--
-- ON DELETE RESTRICT on both: deleting a registry that applications depend on
-- would break their next deploy silently, at the moment nobody is looking. The
-- API turns the resulting conflict into a 409 that names them.
ALTER TABLE applications
    ADD COLUMN source_registry_id TEXT REFERENCES registries(id) ON DELETE RESTRICT,
    ADD COLUMN build_push_registry_id TEXT REFERENCES registries(id) ON DELETE RESTRICT,
    -- Repository within the push registry, e.g. "acme/web". Empty means the
    -- application's own name is used.
    ADD COLUMN build_push_repository TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE applications
    DROP COLUMN build_push_repository,
    DROP COLUMN build_push_registry_id,
    DROP COLUMN source_registry_id;
DROP TABLE IF EXISTS registries;

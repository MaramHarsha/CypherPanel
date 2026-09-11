-- +goose Up
-- Phase 2 resource model (docs/features/application-deploy.md §1): the
-- organizational spine (project → environment) plus the Application resource
-- and its deploy history (revisions, deployments). Additive — no Phase 1 table
-- is altered destructively (ENGINEERING rule 16). team_id is deliberately
-- absent: real teams land in Phase 3 as an additive column with a backfilled
-- default (the spec's stated migration path).

CREATE TABLE projects (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE environments (
    id         TEXT        PRIMARY KEY,
    project_id TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX idx_environments_project ON environments (project_id);

CREATE TABLE applications (
    id             TEXT        PRIMARY KEY,
    environment_id TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT        NOT NULL,

    -- source
    source_kind          TEXT NOT NULL,             -- 'github' | 'git_url'
    source_repo          TEXT NOT NULL,
    source_branch        TEXT NOT NULL DEFAULT 'main',
    source_deploy_key_id TEXT,

    -- build
    build_kind            TEXT NOT NULL DEFAULT 'dockerfile',
    build_dockerfile_path TEXT NOT NULL DEFAULT './Dockerfile',
    build_context         TEXT NOT NULL DEFAULT '.',

    -- runtime. A server may not be deleted while it still runs applications
    -- (RESTRICT) — the operator moves or deletes the apps first.
    runtime_server_id TEXT    NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    runtime_port      INTEGER NOT NULL,
    runtime_replicas  INTEGER NOT NULL DEFAULT 1,

    -- route
    route_domain      TEXT NOT NULL,
    route_https       BOOLEAN NOT NULL DEFAULT true,
    route_path_prefix TEXT NOT NULL DEFAULT '',

    -- health (gates rollout)
    health_path             TEXT    NOT NULL DEFAULT '/',
    health_interval_seconds INTEGER NOT NULL DEFAULT 10,
    health_timeout_seconds  INTEGER NOT NULL DEFAULT 5,
    health_retries          INTEGER NOT NULL DEFAULT 3,

    -- webhook: public id lives in the URL; the HMAC secret is sealed at rest
    -- (threat-model §5.1), same secret.Box as the CA key.
    webhook_id            TEXT NOT NULL UNIQUE,
    webhook_secret_ct     BYTEA NOT NULL,
    webhook_secret_nonce  BYTEA NOT NULL,

    -- desired_revision_id is set on first deploy; rollback re-points it.
    desired_revision_id TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name)
);
CREATE INDEX idx_applications_environment ON applications (environment_id);
CREATE INDEX idx_applications_server ON applications (runtime_server_id);

-- Per-application environment variables, each value sealed individually so a
-- DB read yields no plaintext (threat-model §5.1).
CREATE TABLE app_env_vars (
    application_id TEXT  NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    key            TEXT  NOT NULL,
    value_ct       BYTEA NOT NULL,
    value_nonce    BYTEA NOT NULL,
    PRIMARY KEY (application_id, key)
);

-- An immutable revision: the image + a config snapshot the deployment points
-- at. Rollback re-points desired_revision_id at an older row (ADR-005).
CREATE TABLE revisions (
    id              TEXT        PRIMARY KEY,
    application_id  TEXT        NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    image           TEXT        NOT NULL DEFAULT '',  -- set once the build produces it
    source_commit   TEXT        NOT NULL DEFAULT '',
    config_snapshot JSONB       NOT NULL,             -- the spec at creation time
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_revisions_application ON revisions (application_id);

-- desired_revision_id references revisions; added after both tables exist to
-- avoid an ordering cycle. SET NULL so pruning old revisions can't orphan.
ALTER TABLE applications
    ADD CONSTRAINT fk_applications_desired_revision
    FOREIGN KEY (desired_revision_id) REFERENCES revisions(id) ON DELETE SET NULL;

-- A deployment: a recorded transition to a revision, with its lifecycle status.
CREATE TABLE deployments (
    id             TEXT        PRIMARY KEY,
    application_id TEXT        NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    revision_id    TEXT        NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
    status         TEXT        NOT NULL DEFAULT 'queued', -- queued|building|distributing|rolling_out|succeeded|failed
    trigger        TEXT        NOT NULL DEFAULT 'manual', -- manual|webhook|rollback
    detail         TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ
);
CREATE INDEX idx_deployments_application ON deployments (application_id, created_at DESC);

-- +goose Down
DROP TABLE deployments;
ALTER TABLE applications DROP CONSTRAINT fk_applications_desired_revision;
DROP TABLE revisions;
DROP TABLE app_env_vars;
DROP TABLE applications;
DROP TABLE environments;
DROP TABLE projects;

-- Compose Stacks (compose-stacks.md): the third Resource in an Environment,
-- beside Application and Managed Database.
--
-- The compose FILE is the desired state (ADR-005) — the plane stores it, ships
-- it, and never sends a command; `docker compose up -d` is the agent's own
-- fixed invocation, and it is a convergence rather than an imperative verb.
--
-- +goose Up
CREATE TABLE compose_stacks (
    id                  TEXT        PRIMARY KEY,
    environment_id      TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    -- ON DELETE RESTRICT, like an application's: a server cannot be deleted out
    -- from under the workloads it is running.
    runtime_server_id   TEXT        NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    -- nil until the first deploy. Rollback re-points it; there is no Deployment
    -- row, because a stack has no build and no distribute stage to track.
    desired_revision_id TEXT,
    -- Routing goes through the panel's own file fragments, never the compose
    -- file's Traefik labels: the managed Proxy runs the file provider only
    -- (ADR-004), so a stack names WHICH service and port answers and the plane
    -- emits the same fragment an Application gets. Empty domain = no route.
    route_domain        TEXT        NOT NULL DEFAULT '',
    route_service       TEXT        NOT NULL DEFAULT '',
    route_port          INTEGER     NOT NULL DEFAULT 0,
    route_https         BOOLEAN     NOT NULL DEFAULT true,
    -- Observed state (ADR-005), in the ui-principles §5 vocabulary. 'stopped'
    -- is the birth state, before anything has been deployed.
    status               TEXT        NOT NULL DEFAULT 'stopped',
    status_detail        TEXT        NOT NULL DEFAULT '',
    observed_revision_id TEXT        NOT NULL DEFAULT '',
    status_observed_at   TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, name)
);
CREATE INDEX idx_compose_stacks_environment ON compose_stacks (environment_id);
CREATE INDEX idx_compose_stacks_server ON compose_stacks (runtime_server_id);

-- One immutable revision per deploy: the file as it was, so a rollback restores
-- exactly what ran rather than what the current row happens to say.
CREATE TABLE compose_revisions (
    id           TEXT        PRIMARY KEY,
    stack_id     TEXT        NOT NULL REFERENCES compose_stacks(id) ON DELETE CASCADE,
    compose_yaml TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_compose_revisions_stack ON compose_revisions (stack_id, created_at DESC);

-- Sealed env vars, keyed by stack. Compose interpolates ${VAR} from an env file
-- the agent writes 0600 and removes, so a secret never has to live in the
-- compose file the plane stores.
CREATE TABLE compose_env_vars (
    stack_id    TEXT  NOT NULL REFERENCES compose_stacks(id) ON DELETE CASCADE,
    key         TEXT  NOT NULL,
    value_ct    BYTEA NOT NULL,
    value_nonce BYTEA NOT NULL,
    PRIMARY KEY (stack_id, key)
);

-- +goose Down
DROP TABLE IF EXISTS compose_env_vars;
DROP TABLE IF EXISTS compose_revisions;
DROP TABLE IF EXISTS compose_stacks;

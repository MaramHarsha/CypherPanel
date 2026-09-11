-- Resource quotas (resource-quotas.md §8; permitted by ADR-012).
--
-- A quota is a GUARDRAIL, not a meter. It is denominated in bytes and counts,
-- there is no monetary concept anywhere in it, and it is set by an operator on
-- infrastructure they own. The panel already refuses work for governance
-- reasons — a freeze window, an approval gate, a registry still in use — and
-- this is that mechanism with a resource dimension where protection has a clock.

-- +goose Up
CREATE TABLE resource_quotas (
    id                 TEXT PRIMARY KEY,
    -- Two nullable owner columns with a CHECK, rather than a polymorphic
    -- (kind, id) pair: there are exactly two owners here, so the shape that
    -- gives real foreign keys and a real cascade is available. A quota is LIVE
    -- POLICY, and policy for a deleted project is meaningless — the opposite of
    -- an audit event, which carries no keys precisely because it must outlive
    -- what it names. The evidence that a quota existed is the audit row.
    project_id         TEXT REFERENCES projects(id) ON DELETE CASCADE,
    team_id            TEXT REFERENCES teams(id) ON DELETE CASCADE,
    -- NULL means this dimension is uncapped, so an operator who wants a disk
    -- cap only writes one number.
    memory_limit_bytes BIGINT,
    disk_limit_bytes   BIGINT,
    preview_limit      INTEGER,
    updated_by         TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT resource_quota_one_scope CHECK ((project_id IS NOT NULL) <> (team_id IS NOT NULL)),
    -- A cap of 0 is refused at the API: removing a quota is DELETE, and zero
    -- means "nothing may be deployed here", which is deploy protection's job
    -- and says so much more clearly.
    CONSTRAINT resource_quota_positive CHECK (
        (memory_limit_bytes IS NULL OR memory_limit_bytes > 0) AND
        (disk_limit_bytes IS NULL OR disk_limit_bytes > 0) AND
        (preview_limit IS NULL OR preview_limit > 0)
    )
);

CREATE UNIQUE INDEX idx_resource_quotas_project ON resource_quotas(project_id) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX idx_resource_quotas_team ON resource_quotas(team_id) WHERE team_id IS NOT NULL;

-- The last announced state per dimension, so the warning fires on the
-- TRANSITION rather than on every deploy. A warning repeated on every deploy is
-- a warning nobody reads by the second week.
CREATE TABLE quota_states (
    scope_kind TEXT NOT NULL,
    scope_id   TEXT NOT NULL,
    dimension  TEXT NOT NULL,
    state      TEXT NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_kind, scope_id, dimension)
);

-- +goose Down
DROP TABLE quota_states;
DROP TABLE resource_quotas;

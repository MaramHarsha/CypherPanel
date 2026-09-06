-- +goose Up
-- V1.x, deploy-protection.md §2: an Environment may declare WHO must approve a
-- deploy there and WHEN deploys are not allowed at all. Four plane-side tables,
-- no wire change: the gate is consulted once, where a Deployment is born, and
-- before any work item is published.
--
-- Additive and reversible (ENGINEERING rule 16): protection is off by default,
-- so an environment with no row here behaves exactly as it did before this
-- migration, and Down drops children before parents.

-- One policy row per protected Environment. Keyed by the environment itself:
-- "at most one policy per environment" is then an invariant the database
-- enforces, not a rule the service has to remember.
CREATE TABLE environment_protection (
    environment_id    TEXT        PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
    require_approval  BOOLEAN     NOT NULL DEFAULT false,
    -- member | admin | owner (domain.RoleRank). The minimum rank that may
    -- approve a deploy to this environment.
    min_approver_role TEXT        NOT NULL DEFAULT 'owner',
    -- The master switch over the windows below: turning freeze off keeps the
    -- declared windows so an operator can re-arm them without retyping.
    freeze_enabled    BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT environment_protection_role_valid
        CHECK (min_approver_role IN ('member', 'admin', 'owner'))
);

-- A weekly recurring window, evaluated on WALL CLOCK in its own zone, and
-- allowed to wrap the week (Fri 18:00 → Mon 08:00). Stored as day-of-week plus
-- minutes past local midnight rather than a timestamp, because "no deploys
-- after six on Friday" recurs — it is not an instant.
CREATE TABLE freeze_windows (
    id             TEXT        PRIMARY KEY,
    environment_id TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    start_dow      SMALLINT    NOT NULL,   -- 0=Sunday … 6=Saturday (time.Weekday)
    start_minute   INTEGER     NOT NULL,   -- 0–1439, minutes past local midnight
    end_dow        SMALLINT    NOT NULL,
    end_minute     INTEGER     NOT NULL,
    timezone       TEXT        NOT NULL,   -- IANA name, e.g. "Europe/Berlin"
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT freeze_windows_bounds CHECK (
        start_dow BETWEEN 0 AND 6 AND end_dow BETWEEN 0 AND 6
        AND start_minute BETWEEN 0 AND 1439 AND end_minute BETWEEN 0 AND 1439
    ),
    -- A window whose start equals its end is either empty or the whole week,
    -- and no reader can tell which. Refused at the column so neither reading
    -- can ever be stored.
    CONSTRAINT freeze_windows_not_degenerate CHECK (
        (start_dow, start_minute) <> (end_dow, end_minute)
    )
);
CREATE INDEX idx_freeze_windows_environment ON freeze_windows (environment_id);

-- Exactly one gate decision per Deployment — the natural key. required_role is
-- a SNAPSHOT of min_approver_role at park time, for the same reason revisions
-- snapshot their config: relaxing the policy while a deploy is parked must not
-- relax the deploy already parked.
CREATE TABLE deploy_approvals (
    deployment_id  TEXT        PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    environment_id TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- NULL for a webhook deploy: a push has no panel user behind it.
    requested_by   TEXT        REFERENCES users(id) ON DELETE SET NULL,
    required_role  TEXT        NOT NULL,
    state          TEXT        NOT NULL,   -- pending | approved | rejected
    decided_by     TEXT        REFERENCES users(id) ON DELETE SET NULL,
    decided_at     TIMESTAMPTZ,
    reason         TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT deploy_approvals_state_valid
        CHECK (state IN ('pending', 'approved', 'rejected')),
    CONSTRAINT deploy_approvals_role_valid
        CHECK (required_role IN ('member', 'admin', 'owner')),
    -- A decided row carries when it was decided; a pending one never does.
    CONSTRAINT deploy_approvals_decided_at_matches_state CHECK (
        (state = 'pending' AND decided_at IS NULL)
        OR (state <> 'pending' AND decided_at IS NOT NULL)
    )
);
CREATE INDEX idx_deploy_approvals_environment ON deploy_approvals (environment_id, state);

-- A bounded, recorded freeze override. Grants expire on their own and are never
-- revoked early, so the row is append-only: what it says happened, happened.
CREATE TABLE break_glass_grants (
    id             TEXT        PRIMARY KEY,
    environment_id TEXT        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- NULL once the opener's account is deleted. The row is append-only —
    -- what it says happened, happened — so it must outlive the person, which
    -- is also the shape ListBreakGlassGrants already reads (LEFT JOIN users,
    -- COALESCE the address to ''). Same choice as deploy_approvals above.
    opened_by      TEXT        REFERENCES users(id) ON DELETE SET NULL,
    reason         TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT break_glass_grants_reason_present CHECK (length(reason) BETWEEN 1 AND 500)
);
CREATE INDEX idx_break_glass_grants_environment ON break_glass_grants (environment_id, expires_at DESC);

-- +goose Down
-- The one thing this migration introduces OUTSIDE its own tables is the
-- `awaiting_approval` deployment status, and dropping the tables does not undo
-- it. A parked row left behind would be picked up by the pre-0031 queue queries
-- (`status NOT IN ('succeeded','failed')`), and because it sorts first by
-- created_at it would make the scheduler queue every later deploy for that
-- application forever, silently. So end them here: nothing can decide them any
-- more, and a failed deploy with a reason is a state an operator can act on.
UPDATE deployments
SET status = 'failed',
    detail = 'deploy protection was removed while this deploy was awaiting approval',
    finished_at = now()
WHERE status = 'awaiting_approval';

DROP TABLE IF EXISTS break_glass_grants;
DROP TABLE IF EXISTS deploy_approvals;
DROP TABLE IF EXISTS freeze_windows;
DROP TABLE IF EXISTS environment_protection;

-- +goose Up
-- Phase 3, teams-and-roles.md: Teams own projects (glossary: the tenancy
-- boundary); users belong to teams with a ranked role. The backfill enrolls
-- every existing user as an owner of a 'default' team and assigns every
-- existing project to it — pre-migration users were implicit superusers, so
-- defaulting to over-permission (not lockout) preserves their access exactly
-- (spec §5). Reversible (ENGINEERING rule 16).

CREATE TABLE teams (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE team_members (
    team_id    TEXT        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT        NOT NULL,  -- member | admin | owner (ranked)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX idx_team_members_user ON team_members (user_id);

INSERT INTO teams (id, name) VALUES ('tm_default', 'default');
INSERT INTO team_members (team_id, user_id, role)
    SELECT 'tm_default', id, 'owner' FROM users;

ALTER TABLE projects ADD COLUMN team_id TEXT REFERENCES teams(id);
UPDATE projects SET team_id = 'tm_default';
ALTER TABLE projects ALTER COLUMN team_id SET NOT NULL;
CREATE INDEX idx_projects_team ON projects (team_id);

-- +goose Down
ALTER TABLE projects DROP COLUMN team_id;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;

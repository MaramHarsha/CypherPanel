-- Project and environment lifecycle (projects-and-environments.md).
--
-- Three columns the redesign's project-settings screen cannot be built without,
-- and one on environments so a preview is distinguishable from a standing
-- environment without parsing its name.
--
-- +goose Up

-- A stable, human-readable handle for URLs and the CLI. Immutable once set:
-- renaming a project must not break a bookmark or a script, which is the whole
-- reason a slug exists separately from the name.
ALTER TABLE projects ADD COLUMN slug TEXT;

-- Backfill deterministically from the existing name, then de-duplicate. The
-- window function numbers same-slug rows oldest-first, so the longest-standing
-- project keeps the bare slug and later ones get -2, -3 — the same rule the
-- service applies to new projects, so backfilled and freshly created rows are
-- indistinguishable afterwards.
-- +goose StatementBegin
WITH slugged AS (
    SELECT
        id,
        team_id,
        NULLIF(trim(both '-' FROM regexp_replace(lower(name), '[^a-z0-9]+', '-', 'g')), '') AS base,
        created_at
    FROM projects
),
numbered AS (
    SELECT
        id,
        COALESCE(base, 'project') AS base,
        row_number() OVER (PARTITION BY team_id, COALESCE(base, 'project') ORDER BY created_at, id) AS n
    FROM slugged
)
UPDATE projects p
SET slug = CASE WHEN n = 1 THEN numbered.base ELSE numbered.base || '-' || n END
FROM numbered
WHERE p.id = numbered.id;
-- +goose StatementEnd

ALTER TABLE projects ALTER COLUMN slug SET NOT NULL;

-- Scoped to the team, not globally: two customers may both have a "website",
-- and a global unique would make the second one's slug depend on who signed up
-- first.
CREATE UNIQUE INDEX idx_projects_team_slug ON projects (team_id, slug);

-- Where "open this project" lands, and which environment a deploy targets when
-- none is named. ON DELETE SET NULL rather than RESTRICT: deleting the default
-- environment should leave the project without one, not refuse.
ALTER TABLE projects ADD COLUMN default_environment_id TEXT
    REFERENCES environments(id) ON DELETE SET NULL;

-- +goose StatementBegin
UPDATE projects p
SET default_environment_id = (
    SELECT e.id FROM environments e
    WHERE e.project_id = p.id
    ORDER BY (e.name = 'production') DESC, e.created_at, e.id
    LIMIT 1
);
-- +goose StatementEnd

-- Last time anything happened here: a deploy, a resource created or removed, a
-- setting changed. The projects list orders by it, and "2 h ago" on the mobile
-- card comes from it. Seeded from updated_at so an existing panel does not show
-- every project as brand new.
ALTER TABLE projects ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE projects SET last_activity_at = updated_at;
CREATE INDEX idx_projects_last_activity ON projects (last_activity_at DESC);

-- What kind of environment this is. Previews are created and destroyed by the
-- PR lifecycle and must never be renamed or deleted by hand, so the difference
-- has to be a column rather than a guess from the name.
ALTER TABLE environments ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard';
UPDATE environments SET kind = 'production' WHERE name = 'production';
-- A preview environment is one an open preview points at.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'previews') THEN
        UPDATE environments e SET kind = 'preview'
        WHERE EXISTS (SELECT 1 FROM previews p WHERE p.environment_id = e.id);
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE environments ADD CONSTRAINT environments_kind
    CHECK (kind IN ('production', 'standard', 'preview'));

-- +goose Down
ALTER TABLE environments DROP CONSTRAINT environments_kind;
ALTER TABLE environments DROP COLUMN kind;
DROP INDEX idx_projects_last_activity;
ALTER TABLE projects DROP COLUMN last_activity_at;
ALTER TABLE projects DROP COLUMN default_environment_id;
DROP INDEX idx_projects_team_slug;
ALTER TABLE projects DROP COLUMN slug;

-- name: CreateProject :one
INSERT INTO projects (id, name, team_id, slug) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at DESC;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1;

-- SlugTakenInTeam answers the collision question the service asks while
-- choosing a slug. Scoped to the team, matching the unique index.
-- name: SlugTakenInTeam :one
SELECT EXISTS (SELECT 1 FROM projects WHERE team_id = $1 AND slug = $2);

-- UpdateProject applies a partial edit. COALESCE keeps every omitted field, so
-- one statement serves rename, transfer and default-environment changes without
-- a read-modify-write race between them.
-- name: UpdateProject :one
UPDATE projects
SET name                   = COALESCE(sqlc.narg('name'), name),
    team_id                = COALESCE(sqlc.narg('team_id'), team_id),
    slug                   = COALESCE(sqlc.narg('slug'), slug),
    default_environment_id = CASE
                                 WHEN sqlc.narg('clear_default')::bool THEN NULL
                                 ELSE COALESCE(sqlc.narg('default_environment_id'), default_environment_id)
                             END,
    updated_at             = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- TouchProject records that something happened here. Called on the paths that
-- change what an operator would want to see on the projects list, so "2 h ago"
-- means what it says.
-- name: TouchProject :exec
UPDATE projects SET last_activity_at = now() WHERE id = $1;

-- TouchProjectForEnvironment reaches the project through an environment, which
-- is the id most callers actually hold.
-- name: TouchProjectForEnvironment :exec
UPDATE projects p SET last_activity_at = now()
FROM environments e
WHERE e.id = $1 AND p.id = e.project_id;

-- ProjectRollups is the projects list in one query: resource counts and the
-- worst status among them, per project. Rendering the list one project at a
-- time would be N+1 against the operator's whole portfolio.
--
-- "Worst" is ranked here rather than in Go so the ordering is the database's
-- job and cannot drift between callers: error beats degraded beats deploying
-- beats everything healthy.
-- name: ProjectRollups :many
WITH resources AS (
    SELECT e.project_id, a.status, 'application' AS kind
    FROM applications a JOIN environments e ON e.id = a.environment_id
    UNION ALL
    SELECT e.project_id, d.status, 'database' AS kind
    FROM databases d JOIN environments e ON e.id = d.environment_id
)
SELECT
    project_id,
    count(*) FILTER (WHERE kind = 'application')::bigint AS application_count,
    count(*) FILTER (WHERE kind = 'database')::bigint    AS database_count,
    count(*) FILTER (WHERE status = 'error')::bigint     AS error_count,
    -- Ranks the two observed vocabularies together: applications use
    -- deploying/degraded, managed databases use provisioning, and a project
    -- rollup has to speak for both.
    max(CASE status
            WHEN 'error'        THEN 5
            WHEN 'degraded'     THEN 4
            WHEN 'deploying'    THEN 3
            WHEN 'provisioning' THEN 3
            WHEN 'running'      THEN 1
            ELSE 0
        END)::int AS worst_rank
FROM resources
GROUP BY project_id;

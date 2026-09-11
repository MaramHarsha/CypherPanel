-- name: UpsertProjectQuota :one
INSERT INTO resource_quotas (id, project_id, memory_limit_bytes, disk_limit_bytes, preview_limit, updated_by)
VALUES ($1, $2, sqlc.narg('memory_limit_bytes'), sqlc.narg('disk_limit_bytes'), sqlc.narg('preview_limit'), $3)
ON CONFLICT (project_id) WHERE project_id IS NOT NULL DO UPDATE
SET memory_limit_bytes = EXCLUDED.memory_limit_bytes,
    disk_limit_bytes = EXCLUDED.disk_limit_bytes,
    preview_limit = EXCLUDED.preview_limit,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: UpsertTeamQuota :one
INSERT INTO resource_quotas (id, team_id, memory_limit_bytes, disk_limit_bytes, preview_limit, updated_by)
VALUES ($1, $2, sqlc.narg('memory_limit_bytes'), sqlc.narg('disk_limit_bytes'), sqlc.narg('preview_limit'), $3)
ON CONFLICT (team_id) WHERE team_id IS NOT NULL DO UPDATE
SET memory_limit_bytes = EXCLUDED.memory_limit_bytes,
    disk_limit_bytes = EXCLUDED.disk_limit_bytes,
    preview_limit = EXCLUDED.preview_limit,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: GetProjectQuota :one
SELECT * FROM resource_quotas WHERE project_id = $1;

-- name: GetTeamQuota :one
SELECT * FROM resource_quotas WHERE team_id = $1;

-- name: ListResourceQuotas :many
SELECT * FROM resource_quotas ORDER BY created_at;

-- name: DeleteProjectQuota :exec
DELETE FROM resource_quotas WHERE project_id = $1;

-- name: DeleteTeamQuota :exec
DELETE FROM resource_quotas WHERE team_id = $1;

-- ─── the meter ──────────────────────────────────────────────────────────────
--
-- Memory is DECLARED, not observed, and that is the one place the obvious
-- design is wrong: a workload that has not started uses nothing, so an observed
-- meter is zero for the very thing being admitted and could never refuse a new
-- application. Only a declared meter can answer "will this fit" before the
-- container exists.

-- name: ProjectDeclaredMemory :one
SELECT COALESCE((
    SELECT SUM(COALESCE(a.memory_limit_mb, 0) * GREATEST(a.runtime_replicas, 1))
    FROM applications a
    JOIN environments e ON e.id = a.environment_id
    WHERE e.project_id = $1
), 0)::BIGINT
+ COALESCE((
    SELECT SUM(COALESCE(d.memory_limit_mb, 0))
    FROM databases d
    JOIN environments e ON e.id = d.environment_id
    WHERE e.project_id = $1
), 0)::BIGINT;

-- Resources with NO declared limit make a memory quota a fiction, so they are
-- named rather than silently counted as zero — the runaway project this feature
-- exists to stop is precisely the one that never set a limit.
-- name: ProjectUnlimitedResources :many
SELECT a.name AS name, 'application' AS kind
FROM applications a
JOIN environments e ON e.id = a.environment_id
WHERE e.project_id = $1 AND a.memory_limit_mb IS NULL
UNION ALL
SELECT d.name AS name, 'database' AS kind
FROM databases d
JOIN environments e ON e.id = d.environment_id
WHERE e.project_id = $1 AND d.memory_limit_mb IS NULL;

-- Compose stacks are NOT in the memory meter and the screen says so: their
-- memory is declared inside their own file, and reading it out would meter a
-- six-service stack as two services' worth and LOOK complete.
-- name: ProjectComposeStackCount :one
SELECT COUNT(*)::BIGINT FROM compose_stacks c
JOIN environments e ON e.id = c.environment_id
WHERE e.project_id = $1;

-- name: ProjectObservedDisk :one
SELECT COALESCE(SUM(latest.total), 0)::BIGINT FROM (
    SELECT DISTINCT ON (d.resource_kind, d.resource_id)
           d.image_bytes + d.volume_bytes + d.container_bytes AS total
    FROM resource_disk_usage d
    WHERE (d.resource_kind = 'application' AND d.resource_id IN (
              SELECT a.id FROM applications a
              JOIN environments e ON e.id = a.environment_id WHERE e.project_id = $1))
       OR (d.resource_kind = 'database' AND d.resource_id IN (
              SELECT db.id FROM databases db
              JOIN environments e ON e.id = db.environment_id WHERE e.project_id = $1))
       OR (d.resource_kind = 'compose_stack' AND d.resource_id IN (
              SELECT c.id FROM compose_stacks c
              JOIN environments e ON e.id = c.environment_id WHERE e.project_id = $1))
    ORDER BY d.resource_kind, d.resource_id, d.bucket_start DESC
) latest;

-- name: ProjectLivePreviews :one
SELECT COUNT(*)::BIGINT FROM previews p
JOIN applications a ON a.id = p.source_app_id
JOIN environments e ON e.id = a.environment_id
WHERE e.project_id = $1 AND p.status <> 'destroying';

-- name: ListProjectsInTeam :many
SELECT id FROM projects WHERE team_id = $1;

-- ─── announced state ────────────────────────────────────────────────────────

-- name: GetQuotaState :one
SELECT * FROM quota_states WHERE scope_kind = $1 AND scope_id = $2 AND dimension = $3;

-- name: SetQuotaState :exec
INSERT INTO quota_states (scope_kind, scope_id, dimension, state, changed_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (scope_kind, scope_id, dimension) DO UPDATE
SET state = EXCLUDED.state, changed_at = now();

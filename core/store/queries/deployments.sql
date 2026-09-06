-- name: CreateDeployment :one
INSERT INTO deployments (id, application_id, revision_id, status, trigger)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: SetDeploymentBuilder :one
UPDATE deployments SET builder_server_id = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetDeployment :one
SELECT * FROM deployments WHERE id = $1;

-- name: UpdateDeploymentStatus :one
UPDATE deployments
SET status = $2, detail = $3, updated_at = now(),
    finished_at = CASE WHEN $2 IN ('succeeded', 'failed') THEN now() ELSE finished_at END
WHERE id = $1
RETURNING *;

-- name: ListDeploymentsByApplication :many
SELECT * FROM deployments WHERE application_id = $1 ORDER BY created_at DESC LIMIT $2;

-- The two queue queries below exclude 'awaiting_approval' as well as the two
-- terminal states (deploy-protection.md §3). A parked deploy has not finished,
-- but it holds no pipeline slot either: without the exclusion an approval
-- nobody got round to would sit at the head of its application's queue and
-- block every later deploy, and Scheduler.Recover would try to resume it on
-- boot. With it, approving simply re-enters the ordinary queue through
-- tryStart, and Recover needs no new case.

-- name: ListActiveDeployments :many
SELECT * FROM deployments
WHERE status NOT IN ('succeeded', 'failed', 'awaiting_approval')
ORDER BY created_at;

-- name: ListActiveDeploymentsByApplication :many
SELECT * FROM deployments
WHERE application_id = $1 AND status NOT IN ('succeeded', 'failed', 'awaiting_approval')
ORDER BY created_at;

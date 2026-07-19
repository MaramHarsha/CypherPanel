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

-- name: ListActiveDeployments :many
SELECT * FROM deployments
WHERE status NOT IN ('succeeded', 'failed')
ORDER BY created_at;

-- name: ListActiveDeploymentsByApplication :many
SELECT * FROM deployments
WHERE application_id = $1 AND status NOT IN ('succeeded', 'failed')
ORDER BY created_at;

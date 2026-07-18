-- name: CreateEnvironment :one
INSERT INTO environments (id, project_id, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironmentsByProject :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY created_at;

-- name: DeleteEnvironment :exec
DELETE FROM environments WHERE id = $1;

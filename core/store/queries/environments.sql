-- name: CreateEnvironment :one
INSERT INTO environments (id, project_id, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE id = $1;

-- name: ListEnvironmentsByProject :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY created_at;


-- name: DeleteEnvironment :exec
DELETE FROM environments WHERE id = $1;

-- name: RenameEnvironment :one
UPDATE environments SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: CreateEnvironmentOfKind :one
INSERT INTO environments (id, project_id, name, kind) VALUES ($1, $2, $3, $4) RETURNING *;

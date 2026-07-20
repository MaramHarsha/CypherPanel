-- name: CreateProject :one
INSERT INTO projects (id, name, team_id) VALUES ($1, $2, $3) RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at DESC;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1;

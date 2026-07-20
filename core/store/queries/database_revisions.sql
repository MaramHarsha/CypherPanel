-- name: CreateDatabaseRevision :one
INSERT INTO database_revisions (id, database_id, config_snapshot)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetDatabaseRevision :one
SELECT * FROM database_revisions WHERE id = $1;

-- name: GetLatestDatabaseRevision :one
SELECT * FROM database_revisions
WHERE database_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListDatabaseRevisions :many
SELECT * FROM database_revisions
WHERE database_id = $1
ORDER BY created_at DESC;

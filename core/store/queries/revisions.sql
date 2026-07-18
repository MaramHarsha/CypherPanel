-- name: CreateRevision :one
INSERT INTO revisions (id, application_id, source_commit, config_snapshot)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRevision :one
SELECT * FROM revisions WHERE id = $1;

-- name: SetRevisionImage :one
UPDATE revisions SET image = $2 WHERE id = $1 RETURNING *;

-- name: ListRevisionsByApplication :many
SELECT * FROM revisions WHERE application_id = $1 ORDER BY created_at DESC;

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

-- name: SetRevisionSourceCommit :one
UPDATE revisions SET source_commit = $2 WHERE id = $1 RETURNING *;

-- A promoted revision is created with its image ALREADY NAMED — the target
-- application's own canonical tag, which is exactly what a build would have
-- produced — and with a pointer back to where the artifact came from
-- (revision-promotion.md §5).
-- name: CreatePromotedRevision :one
INSERT INTO revisions (id, application_id, source_commit, config_snapshot, image, promoted_from_revision_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

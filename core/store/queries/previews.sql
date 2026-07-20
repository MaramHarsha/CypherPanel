-- name: CreatePreview :one
INSERT INTO previews (
    id, source_app_id, environment_id, preview_app_id,
    pr_number, pr_branch, domain, status, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: GetPreview :one
SELECT * FROM previews WHERE id = $1;

-- name: GetPreviewByPR :one
SELECT * FROM previews WHERE source_app_id = $1 AND pr_number = $2;

-- name: ListPreviewsBySourceApp :many
SELECT * FROM previews WHERE source_app_id = $1 ORDER BY created_at DESC;

-- name: SetPreviewStatus :exec
UPDATE previews SET status = $2, updated_at = now() WHERE id = $1;

-- name: ListExpiredPreviews :many
SELECT * FROM previews
WHERE expires_at IS NOT NULL AND expires_at < $1 AND status != 'destroying'
ORDER BY expires_at;

-- name: DeletePreview :exec
DELETE FROM previews WHERE id = $1;

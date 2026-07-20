-- name: CreateBackupTarget :one
INSERT INTO backup_targets (
    id, name, endpoint, bucket, region,
    access_key_ct, access_key_nonce,
    secret_key_ct, secret_key_nonce,
    path_prefix
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7,
    $8, $9,
    $10
)
RETURNING *;

-- name: GetBackupTarget :one
SELECT * FROM backup_targets WHERE id = $1;

-- name: ListBackupTargets :many
SELECT * FROM backup_targets ORDER BY created_at DESC;

-- name: UpdateBackupTarget :one
UPDATE backup_targets
SET name = $2, endpoint = $3, bucket = $4, region = $5,
    access_key_ct = $6, access_key_nonce = $7,
    secret_key_ct = $8, secret_key_nonce = $9,
    path_prefix = $10, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteBackupTarget :exec
DELETE FROM backup_targets WHERE id = $1;

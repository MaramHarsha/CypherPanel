-- name: CreateDeployKey :one
-- Create a new deploy key. The private key is sealed (threat-model §5.1).
INSERT INTO deploy_keys (
    id, name, public_key, fingerprint, private_key_ct, private_key_nonce
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: GetDeployKey :one
-- Get a deploy key by ID.
SELECT * FROM deploy_keys WHERE id = $1 LIMIT 1;

-- name: ListDeployKeys :many
-- List all deploy keys.
SELECT * FROM deploy_keys ORDER BY created_at DESC;

-- name: DeleteDeployKey :exec
-- Delete a deploy key (RESTRICT-gated by application references).
DELETE FROM deploy_keys WHERE id = $1;

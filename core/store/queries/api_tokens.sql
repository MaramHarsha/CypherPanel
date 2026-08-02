-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, abilities)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: APITokenByHash :one
-- Resolves a presented token to its owner AND its abilities in one round trip:
-- the middleware needs both to authorize the request.
SELECT sqlc.embed(u), t.id AS token_id, t.abilities
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = $1
  AND (t.expires_at IS NULL OR t.expires_at > now());

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1;

-- name: ListAPITokensByUser :many
SELECT id, user_id, name, last_used_at, expires_at, created_at, abilities
FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: GetAPIToken :one
SELECT id, user_id, name, last_used_at, expires_at, created_at, abilities
FROM api_tokens WHERE id = $1;

-- name: DeleteAPIToken :exec
DELETE FROM api_tokens WHERE id = $1;

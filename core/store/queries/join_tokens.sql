-- name: CreateJoinToken :one
INSERT INTO join_tokens (id, server_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetJoinToken :one
SELECT * FROM join_tokens WHERE id = $1;

-- ConsumeJoinToken atomically marks a token used, but only if it is still
-- unconsumed and unexpired. A concurrent second enrollment attempt with the
-- same token returns no row — this is the single-use guarantee (threat-model
-- §5.3), enforced by the database, not by application-level checking.
-- name: ConsumeJoinToken :one
UPDATE join_tokens
SET consumed_at = now()
WHERE id = $1
  AND consumed_at IS NULL
  AND expires_at > now()
RETURNING *;

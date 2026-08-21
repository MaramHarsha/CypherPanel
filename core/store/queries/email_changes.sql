-- name: CreateEmailChange :one
INSERT INTO email_changes (id, user_id, new_email, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEmailChange :one
SELECT * FROM email_changes WHERE id = $1;

-- Spending the change is one atomic statement: no row back means it was already
-- used or has expired, which is the only race-free answer.
-- name: ConsumeEmailChange :one
UPDATE email_changes
SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

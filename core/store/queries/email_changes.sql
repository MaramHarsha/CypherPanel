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

-- The pending change a user can still confirm, if any. Newest wins: requesting
-- a second change supersedes the first in the UI even though both rows live
-- until they expire.
-- name: PendingEmailChangeForUser :one
SELECT * FROM email_changes
WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;

-- Cancelling is "this wasn't me": every outstanding link for the user dies at
-- once, so a second request made by an attacker cannot survive the cancel of
-- the first. Marking them consumed rather than deleting keeps the record that
-- a change was attempted.
-- name: CancelPendingEmailChanges :execrows
UPDATE email_changes
SET consumed_at = now()
WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now();

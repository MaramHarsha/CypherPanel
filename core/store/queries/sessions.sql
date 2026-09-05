-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- DeleteExpiredSessions is the purge behind auth.RunSessionPurge
-- (control-plane-hardening.md §7). The cutoff is a parameter rather than
-- now() so the caller's injected clock decides what "expired" means and the
-- purge is testable without waiting.
-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= $1;

-- name: ListSessionsByUser :many
-- Live sessions only: an expired row is already unusable, so showing it would
-- invite the operator to "revoke" something that is not a way in.
SELECT id, user_id, expires_at, created_at
FROM sessions
WHERE user_id = $1 AND expires_at > now()
ORDER BY created_at DESC;

-- name: DeleteSessionForUser :execrows
-- Scoped by user_id so one account can never revoke another's session; the
-- affected-row count tells the caller whether the id was theirs.
DELETE FROM sessions WHERE id = $1 AND user_id = $2;

-- name: DeleteOtherSessionsForUser :execrows
-- "Sign out everywhere else": every session of this user except the one making
-- the request, identified by its token hash (never by an id the client sends).
DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2;

-- name: CreateServer :one
INSERT INTO servers (id, name)
VALUES ($1, $2)
RETURNING *;

-- name: GetServer :one
SELECT * FROM servers WHERE id = $1;

-- name: ListServers :many
SELECT * FROM servers ORDER BY created_at DESC;

-- name: MarkServerEnrolled :one
UPDATE servers
SET enrolled_at = now(),
    hostname = $2,
    agent_version = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RecordHeartbeat :one
UPDATE servers
SET status = $2,
    agent_version = $3,
    driver = $4,
    last_seen_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkStaleServersUnknown :many
UPDATE servers
SET status = 'unknown',
    updated_at = now()
WHERE enrolled_at IS NOT NULL
  AND status <> 'unknown'
  AND (last_seen_at IS NULL OR last_seen_at < $1)
RETURNING id;

-- name: DeleteServer :exec
DELETE FROM servers WHERE id = $1;

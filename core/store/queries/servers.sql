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
    role = $5,
    disk_total_bytes = $6,
    disk_free_bytes = $7,
    last_seen_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- SetServerDiskLow records whether a server is currently below the disk
-- threshold. Separate from the heartbeat write because it is a TRANSITION the
-- plane decides, not a measurement the agent reports (disk-management.md §5).
-- name: SetServerDiskLow :exec
UPDATE servers SET disk_low = $2, updated_at = now() WHERE id = $1;

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

-- ServerIsEnrolled backs the bus's connection-time revocation check
-- (threat-model §8 req 6): a certificate whose server row is gone or was
-- never enrolled is refused, however cryptographically valid it still is.
-- name: ServerIsEnrolled :one
SELECT EXISTS(
    SELECT 1 FROM servers WHERE id = $1 AND enrolled_at IS NOT NULL
) AS enrolled;

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

-- SetServerSubsystemHealth records WHICH subsystems the agent reported unhealthy.
-- Separate from the heartbeat write for the same reason the agent-update
-- columns are: it changes rarely while a heartbeat arrives every few seconds,
-- and the plane compares the previous value to decide whether to write at all.
-- name: SetServerSubsystemHealth :exec
UPDATE servers SET subsystem_health = $2, updated_at = now() WHERE id = $1;

-- SetServerAgentUpdate records the observed half of ADR-010. Separate from the
-- heartbeat write because it changes rarely while a heartbeat arrives every few
-- seconds, and because the plane compares the PREVIOUS phase to decide whether
-- a rollback is a transition worth announcing (agent-updates.md §7).
-- name: SetServerAgentUpdate :exec
UPDATE servers
SET agent_update_phase  = $2,
    agent_update_target = $3,
    agent_update_detail = $4,
    updated_at = now()
WHERE id = $1;

-- name: SetServerAgentChannel :one
UPDATE servers SET agent_channel = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ListAgentChannels :many
SELECT * FROM agent_channels ORDER BY channel;

-- name: GetAgentChannel :one
SELECT * FROM agent_channels WHERE channel = $1;

-- name: SetAgentChannel :one
UPDATE agent_channels
SET desired_version = $2,
    artifact_base   = $3,
    rollback        = $4,
    updated_at      = now(),
    updated_by      = $5
WHERE channel = $1
RETURNING *;

-- CountEnrolledServers counts servers whose agent actually joined. A row that
-- was created and never enrolled is a join command someone has not run yet, and
-- counting it would tell an operator they have a server when they have a token
-- (guided-onboarding.md §2).
-- name: CountEnrolledServers :one
SELECT count(*) FROM servers WHERE enrolled_at IS NOT NULL;

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

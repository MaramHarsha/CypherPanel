-- name: CreateLogDrain :one
INSERT INTO log_drains (id, name, kind, project_id, target_id, config_ct, config_nonce, enabled)
VALUES ($1, $2, $3, sqlc.narg('project_id'), sqlc.narg('target_id'), $4, $5, $6)
RETURNING *;

-- name: GetLogDrain :one
SELECT * FROM log_drains WHERE id = $1;

-- name: ListLogDrains :many
SELECT * FROM log_drains ORDER BY name;

-- name: ListEnabledLogDrains :many
SELECT * FROM log_drains WHERE enabled = true ORDER BY name;

-- name: UpdateLogDrain :one
UPDATE log_drains
SET name = $2, project_id = sqlc.narg('project_id'), target_id = sqlc.narg('target_id'),
    config_ct = $3, config_nonce = $4, enabled = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetLogDrainEnabled :one
UPDATE log_drains SET enabled = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteLogDrain :exec
DELETE FROM log_drains WHERE id = $1;

-- Observed health, written by the shipper. A batch accepted clears the error:
-- a drain that recovered must not keep showing why it once failed.
-- name: RecordLogDrainShipped :exec
UPDATE log_drains SET last_shipped_at = $2, last_error = '', last_error_at = NULL WHERE id = $1;

-- name: RecordLogDrainError :exec
UPDATE log_drains SET last_error = $2, last_error_at = $3 WHERE id = $1;

-- name: AddLogDrainDropped :exec
UPDATE log_drains SET dropped_lines = dropped_lines + $2 WHERE id = $1;

-- Naming what blocks a delete, rather than counting it: "in use by one or more
-- schedules" leaves an operator to guess which.
-- name: ListLogDrainsByTarget :many
SELECT * FROM log_drains WHERE target_id = $1;

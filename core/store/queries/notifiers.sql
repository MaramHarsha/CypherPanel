-- name: CreateNotifier :one
INSERT INTO notifiers (
    id, project_id, name, channel, config_ct, config_nonce, events, enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetNotifier :one
SELECT * FROM notifiers WHERE id = $1;

-- name: ListNotifiersByProject :many
SELECT * FROM notifiers WHERE project_id = $1 ORDER BY created_at DESC;

-- name: ListEnabledNotifiersForEvent :many
SELECT * FROM notifiers
WHERE project_id = @project_id AND enabled = true AND @event_type::text = ANY(events)
ORDER BY created_at;

-- name: UpdateNotifier :one
UPDATE notifiers
SET name = $2, config_ct = $3, config_nonce = $4, events = $5, enabled = $6, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteNotifier :exec
DELETE FROM notifiers WHERE id = $1;

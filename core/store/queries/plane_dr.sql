-- name: GetPlaneDRConfig :one
SELECT * FROM plane_dr_config WHERE id = 1;

-- name: SetPlaneDRConfig :one
INSERT INTO plane_dr_config (id, target_id, path_prefix, schedule, retention_count, recipient, recipient_mode)
VALUES (1, $1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE
SET target_id = EXCLUDED.target_id,
    path_prefix = EXCLUDED.path_prefix,
    schedule = EXCLUDED.schedule,
    retention_count = EXCLUDED.retention_count,
    recipient = EXCLUDED.recipient,
    recipient_mode = EXCLUDED.recipient_mode,
    updated_at = now()
RETURNING *;

-- Disarming DELETES the row, so "is this panel backing itself up?" has one
-- answer rather than a boolean beside a stale configuration.
-- name: DeletePlaneDRConfig :exec
DELETE FROM plane_dr_config WHERE id = 1;

-- name: SetPlaneDRRun :exec
UPDATE plane_dr_config
SET last_run_at = $1, last_status = $2, last_detail = $3, updated_at = now()
WHERE id = 1;

-- name: MarkPlaneDRRecipientVerified :exec
UPDATE plane_dr_config SET recipient_verified_at = $1, updated_at = now() WHERE id = 1;

-- name: CreatePlaneSnapshot :one
INSERT INTO plane_snapshots (id, object_key, panel_version, schema_version, recipient)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: FinishPlaneSnapshot :exec
UPDATE plane_snapshots
SET size_bytes = $2, sha256 = $3, row_count = $4, status = $5, detail = $6, finished_at = now()
WHERE id = $1;

-- name: ListPlaneSnapshots :many
SELECT * FROM plane_snapshots WHERE pruned_at IS NULL ORDER BY started_at DESC LIMIT $1;

-- Retention is a COUNT, not an age: an operator who backs up nightly and keeps
-- fourteen has two weeks, and one who backs up hourly has fourteen hours —
-- which is what they asked for either way.
-- name: ListPlaneSnapshotsBeyondRetention :many
SELECT * FROM plane_snapshots
WHERE pruned_at IS NULL AND status = 'succeeded'
ORDER BY started_at DESC
OFFSET $1;

-- name: MarkPlaneSnapshotPruned :exec
UPDATE plane_snapshots SET pruned_at = now() WHERE id = $1;

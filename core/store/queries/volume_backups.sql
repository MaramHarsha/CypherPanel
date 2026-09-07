-- name: UpsertVolumeBackup :one
INSERT INTO volume_backups (id, application_id, target_id, schedule, retention_count, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (application_id) DO UPDATE
SET target_id = EXCLUDED.target_id,
    schedule = EXCLUDED.schedule,
    retention_count = EXCLUDED.retention_count,
    enabled = EXCLUDED.enabled,
    updated_at = now()
RETURNING *;

-- name: GetVolumeBackupByApplication :one
SELECT * FROM volume_backups WHERE application_id = $1;

-- name: GetVolumeBackup :one
SELECT * FROM volume_backups WHERE id = $1;

-- name: DeleteVolumeBackup :exec
DELETE FROM volume_backups WHERE application_id = $1;

-- name: ListEnabledVolumeBackupSchedules :many
SELECT * FROM volume_backups WHERE enabled = true AND schedule <> '';

-- name: SetVolumeBackupLastRun :exec
UPDATE volume_backups SET last_run_at = $2, last_status = $3, updated_at = now() WHERE id = $1;

-- name: CreateVolumeBackupRecord :one
INSERT INTO volume_backup_records (id, volume_backup_id, volume_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetVolumeBackupRecord :one
SELECT * FROM volume_backup_records WHERE id = $1;

-- name: UpdateVolumeBackupRecord :exec
UPDATE volume_backup_records
SET object_key = $2, size_bytes = $3, status = $4, detail = $5, finished_at = now()
WHERE id = $1;

-- name: ListVolumeBackupRecords :many
SELECT * FROM volume_backup_records
WHERE volume_backup_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- Retention is per VOLUME, not per schedule: keeping "the last 7" across a
-- two-volume application would keep three of one and four of the other.
-- name: ListVolumeRecordsBeyondRetention :many
SELECT * FROM volume_backup_records
WHERE volume_backup_id = $1 AND volume_name = $2 AND status = 'succeeded'
ORDER BY created_at DESC
OFFSET $3;

-- name: DeleteVolumeBackupRecords :exec
DELETE FROM volume_backup_records WHERE id = ANY($1::text[]);

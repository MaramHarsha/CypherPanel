-- name: CreateDatabaseBackup :one
INSERT INTO database_backups (id, database_id, target_id, schedule, retention_count, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetDatabaseBackup :one
SELECT * FROM database_backups WHERE id = $1;

-- name: ListDatabaseBackups :many
SELECT * FROM database_backups WHERE database_id = $1 ORDER BY created_at DESC;

-- name: UpdateDatabaseBackup :one
UPDATE database_backups
SET schedule = $2, retention_count = $3, enabled = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteDatabaseBackup :exec
DELETE FROM database_backups WHERE id = $1;

-- name: SetDatabaseBackupLastRun :exec
UPDATE database_backups
SET last_run_at = $2, last_status = $3, updated_at = now()
WHERE id = $1;

-- name: ListEnabledBackupSchedules :many
SELECT * FROM database_backups
WHERE enabled = true AND schedule != ''
ORDER BY database_id;

-- name: CreateBackupRecord :one
INSERT INTO backup_records (id, database_backup_id, status, started_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetBackupRecord :one
SELECT * FROM backup_records WHERE id = $1;

-- name: ListBackupRecords :many
SELECT * FROM backup_records
WHERE database_backup_id = $1
ORDER BY created_at DESC;

-- name: UpdateBackupRecord :exec
UPDATE backup_records
SET object_key = $2, size_bytes = $3, status = $4, detail = $5, finished_at = $6
WHERE id = $1;

-- name: ListBackupRecordsBeyondRetention :many
-- The retention sweep set: succeeded backups older than the newest `keep`, each
-- of which owns an S3 object to delete. Failed/running rows carry no object and
-- are left as diagnostic history. The plane deletes these rows only after the
-- agent confirms the objects are gone (self-healing prune, ADR-005).
SELECT r.id, r.object_key FROM backup_records r
WHERE r.database_backup_id = $1
  AND r.status = 'succeeded'
  AND r.object_key <> ''
  AND r.id NOT IN (
    SELECT br.id FROM backup_records br
    WHERE br.database_backup_id = $1
      AND br.status = 'succeeded'
      AND br.object_key <> ''
    ORDER BY br.created_at DESC
    LIMIT $2
  )
ORDER BY r.created_at DESC;

-- name: DeleteBackupRecordsByObjectKeys :exec
-- Delete the rows for objects the agent has confirmed removed from S3. Object
-- keys embed the db id and a timestamp, so they are unique across schedules.
DELETE FROM backup_records WHERE object_key = ANY($1::text[]);

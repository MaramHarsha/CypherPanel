-- name: CreateDatabaseRestore :one
INSERT INTO database_restores (id, database_id, backup_record_id, step)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetDatabaseRestore :one
SELECT * FROM database_restores WHERE id = $1;

-- name: ListDatabaseRestores :many
SELECT * FROM database_restores
WHERE database_id = $1
ORDER BY started_at DESC
LIMIT $2;

-- The restore a database is in the middle of, if any. What the blocking popup
-- reopens onto when someone closes the tab and comes back.
-- name: RunningDatabaseRestore :one
SELECT * FROM database_restores
WHERE database_id = $1 AND status = 'running'
ORDER BY started_at DESC
LIMIT 1;

-- Progress only moves a running restore. A terminal event arriving after another
-- one already finished it — a redelivered message, or a duplicate from an agent
-- that reconnected — must not reopen it, which is why the WHERE clause pins the
-- status rather than the id alone.
-- name: AdvanceDatabaseRestore :one
UPDATE database_restores
SET step        = $2,
    bytes_done  = $3,
    bytes_total = $4
WHERE id = $1 AND status = 'running'
RETURNING *;

-- name: FinishDatabaseRestore :one
UPDATE database_restores
SET status      = $2,
    detail      = $3,
    finished_at = now()
WHERE id = $1 AND status = 'running'
RETURNING *;

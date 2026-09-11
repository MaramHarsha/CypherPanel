-- name: CreateScheduledTask :one
INSERT INTO scheduled_tasks (
    id, application_id, name, schedule, command, enabled
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetScheduledTask :one
SELECT * FROM scheduled_tasks WHERE id = $1;

-- name: ListScheduledTasksByApp :many
SELECT * FROM scheduled_tasks WHERE application_id = $1 ORDER BY created_at;

-- name: ListEnabledScheduledTasksByApp :many
SELECT * FROM scheduled_tasks WHERE application_id = $1 AND enabled = true ORDER BY created_at;

-- name: UpdateScheduledTask :one
UPDATE scheduled_tasks
SET name = $2, schedule = $3, command = $4, enabled = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteScheduledTask :exec
DELETE FROM scheduled_tasks WHERE id = $1;

-- name: CreateTaskRun :one
INSERT INTO scheduled_task_runs (
    id, task_id, started_at, finished_at, exit_code, status, output_tail
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListTaskRuns :many
SELECT * FROM scheduled_task_runs WHERE task_id = $1 ORDER BY started_at DESC LIMIT $2;

-- name: DeleteOldTaskRuns :exec
DELETE FROM scheduled_task_runs r
WHERE r.task_id = $1
  AND r.id NOT IN (
      SELECT keep.id FROM scheduled_task_runs keep
      WHERE keep.task_id = $1
      ORDER BY keep.started_at DESC
      LIMIT $2
  );

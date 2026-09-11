-- name: CreateDatabase :one
INSERT INTO databases (
    id, environment_id, name, engine, version,
    server_id, cpu_limit, memory_limit_mb,
    volume_name, data_path, expose_port, network,
    root_user, root_password_ct, root_password_nonce, require_password,
    status, status_detail, initial_database
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8,
    $9, $10, $11, $12,
    $13, $14, $15, $16,
    $17, $18, $19
)
RETURNING *;

-- name: GetDatabase :one
SELECT * FROM databases WHERE id = $1;

-- name: ListDatabasesByEnvironment :many
SELECT * FROM databases WHERE environment_id = $1 ORDER BY created_at DESC;

-- name: ListDatabasesByServer :many
SELECT * FROM databases WHERE server_id = $1 AND pending_delete = false ORDER BY created_at DESC;

-- name: ListPendingDeleteDatabases :many
SELECT * FROM databases WHERE pending_delete = true;

-- name: SetDatabaseDesiredRevision :one
UPDATE databases
SET desired_revision_id = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetDatabaseObservedStatus :exec
UPDATE databases
SET status = $2, status_detail = $3, observed_revision_id = $4,
    status_observed_at = $5, updated_at = now()
WHERE id = $1;

-- name: SetDatabaseStatus :exec
UPDATE databases SET status = $2, status_detail = $3, updated_at = now() WHERE id = $1;

-- name: SetDatabaseDesiredState :one
UPDATE databases SET desired_state = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetDatabasePendingDelete :exec
UPDATE databases SET pending_delete = true, delete_volume = $2, updated_at = now() WHERE id = $1;

-- name: DeleteDatabase :exec
DELETE FROM databases WHERE id = $1;

-- name: UpdateDatabaseConfig :one
UPDATE databases
SET name = $2,
    version = $3,
    cpu_limit = $4,
    memory_limit_mb = $5,
    expose_port = $6,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateDatabasePassword :exec
UPDATE databases
SET root_password_ct = $2, root_password_nonce = $3, updated_at = now()
WHERE id = $1;

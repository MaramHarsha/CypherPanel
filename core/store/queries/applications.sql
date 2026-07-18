-- name: CreateApplication :one
INSERT INTO applications (
    id, environment_id, name,
    source_kind, source_repo, source_branch, source_deploy_key_id,
    build_kind, build_dockerfile_path, build_context,
    runtime_server_id, runtime_port, runtime_replicas,
    route_domain, route_https, route_path_prefix,
    health_path, health_interval_seconds, health_timeout_seconds, health_retries,
    webhook_id, webhook_secret_ct, webhook_secret_nonce
) VALUES (
    $1, $2, $3,
    $4, $5, $6, $7,
    $8, $9, $10,
    $11, $12, $13,
    $14, $15, $16,
    $17, $18, $19, $20,
    $21, $22, $23
)
RETURNING *;

-- name: GetApplication :one
SELECT * FROM applications WHERE id = $1;

-- name: GetApplicationByWebhookID :one
SELECT * FROM applications WHERE webhook_id = $1;

-- name: ListApplicationsByEnvironment :many
SELECT * FROM applications WHERE environment_id = $1 ORDER BY created_at DESC;

-- name: ListApplicationsByServer :many
SELECT * FROM applications WHERE runtime_server_id = $1 ORDER BY created_at DESC;

-- name: SetApplicationDesiredRevision :one
UPDATE applications
SET desired_revision_id = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteApplication :exec
DELETE FROM applications WHERE id = $1;

-- name: CreateApplication :one
INSERT INTO applications (
    id, environment_id, name,
    source_kind, source_repo, source_branch, source_deploy_key_id,
    build_kind, build_dockerfile_path, build_context,
    runtime_server_id, runtime_port, runtime_replicas,
    route_domain, route_https, route_path_prefix,
    health_path, health_interval_seconds, health_timeout_seconds, health_retries,
    webhook_id, webhook_secret_ct, webhook_secret_nonce,
    preview_enabled, preview_base_domain, preview_ttl_hours,
    cpu_limit, memory_limit_mb, volumes,
    ports, health_kind, source_image
) VALUES (
    $1, $2, $3,
    $4, $5, $6, $7,
    $8, $9, $10,
    $11, $12, $13,
    $14, $15, $16,
    $17, $18, $19, $20,
    $21, $22, $23,
    $24, $25, $26,
    $27, $28, $29,
    $30, $31, $32
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

-- name: SetApplicationObservedStatus :exec
UPDATE applications
SET status = $2, status_detail = $3, observed_revision_id = $4,
    status_observed_at = $5, updated_at = now()
WHERE id = $1;

-- name: SetApplicationStatus :exec
UPDATE applications SET status = $2, status_detail = $3, updated_at = now() WHERE id = $1;

-- name: UpdateApplicationConfig :one
UPDATE applications
SET name = $2,
    source_kind = $3, source_repo = $4, source_branch = $5, source_deploy_key_id = $6,
    build_kind = $7, build_dockerfile_path = $8, build_context = $9,
    runtime_port = $10, runtime_replicas = $11,
    route_domain = $12, route_https = $13, route_path_prefix = $14,
    health_path = $15, health_interval_seconds = $16, health_timeout_seconds = $17, health_retries = $18,
    preview_enabled = $19, preview_base_domain = $20, preview_ttl_hours = $21,
    cpu_limit = $22, memory_limit_mb = $23, volumes = $24,
    ports = $25, health_kind = $26, source_image = $27,
    updated_at = now()
WHERE id = $1
RETURNING *;

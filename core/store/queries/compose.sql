-- name: CreateComposeStack :one
INSERT INTO compose_stacks (
    id, environment_id, name, runtime_server_id,
    route_domain, route_service, route_port, route_https
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetComposeStack :one
SELECT * FROM compose_stacks WHERE id = $1;

-- name: ListComposeStacksByEnvironment :many
SELECT * FROM compose_stacks WHERE environment_id = $1 ORDER BY created_at DESC;

-- name: ListComposeStacksByServer :many
SELECT * FROM compose_stacks WHERE runtime_server_id = $1 ORDER BY created_at DESC;

-- name: UpdateComposeStackConfig :one
UPDATE compose_stacks
SET name = $2, route_domain = $3, route_service = $4, route_port = $5, route_https = $6,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetComposeStackDesiredRevision :one
UPDATE compose_stacks
SET desired_revision_id = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetComposeStackStatus :exec
UPDATE compose_stacks SET status = $2, status_detail = $3, updated_at = now() WHERE id = $1;

-- name: SetComposeStackObservedStatus :exec
UPDATE compose_stacks
SET status = $2, status_detail = $3, observed_revision_id = $4,
    status_observed_at = $5, updated_at = now()
WHERE id = $1;

-- name: DeleteComposeStack :exec
DELETE FROM compose_stacks WHERE id = $1;

-- name: CreateComposeRevision :one
INSERT INTO compose_revisions (id, stack_id, compose_yaml)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetComposeRevision :one
SELECT * FROM compose_revisions WHERE id = $1;

-- name: ListComposeRevisions :many
SELECT * FROM compose_revisions WHERE stack_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- LatestComposeRevision is what a PATCH compares against to decide whether the
-- file actually changed, so an edit that only renames the stack does not mint a
-- revision nobody asked for.
-- name: LatestComposeRevision :one
SELECT * FROM compose_revisions WHERE stack_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: UpsertComposeEnvVar :exec
INSERT INTO compose_env_vars (stack_id, key, value_ct, value_nonce)
VALUES ($1, $2, $3, $4)
ON CONFLICT (stack_id, key) DO UPDATE
SET value_ct = EXCLUDED.value_ct, value_nonce = EXCLUDED.value_nonce;

-- name: ListComposeEnvVars :many
SELECT * FROM compose_env_vars WHERE stack_id = $1 ORDER BY key;

-- name: DeleteComposeEnvVar :exec
DELETE FROM compose_env_vars WHERE stack_id = $1 AND key = $2;

-- The same narrowing ListEnvVarKeys does, for a Compose Stack's variables.
-- name: ListComposeEnvVarKeys :many
SELECT key FROM compose_env_vars WHERE stack_id = $1 ORDER BY key;

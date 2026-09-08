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
    ports, health_kind, source_image,
    source_registry_id, build_push_registry_id, build_push_repository,
    github_installation_id
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
    $30, $31, $32,
    $33, $34, $35,
    $36
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

-- ListApplicationsByDeployKey names the applications still referencing a
-- deploy key, so a refused delete can say which (deploy-key-private-repos.md
-- §3; control-plane-hardening.md §8).
-- name: ListApplicationsByDeployKey :many
SELECT id, name FROM applications WHERE source_deploy_key_id = $1 ORDER BY name, id;

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
    source_registry_id = $28, build_push_registry_id = $29, build_push_repository = $30,
    github_installation_id = $31,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- BumpApplicationRestartToken is a restart: a new token is a difference in
-- desired state the reconciler closes by recreating the container
-- (deployment-control.md §3). Deliberately NOT part of UpdateApplicationConfig
-- — a restart must not carry an unrelated config edit along with it.
-- name: BumpApplicationRestartToken :one
UPDATE applications
SET restart_token = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- Access control is set on its own, never as part of the config update: it is
-- current state rather than a revision snapshot (app-access-control.md §3), and
-- folding it into UpdateApplicationConfig would let a config PATCH silently
-- clear an allowlist.
-- name: SetApplicationAllowlist :one
UPDATE applications
SET ip_allowlist_enabled = $2, ip_allowlist = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetApplicationMaintenance :one
-- Turning maintenance on twice keeps the ORIGINAL stamp: an idempotent PUT from
-- a migration script that retries must not reset the clock the panel shows.
UPDATE applications
SET maintenance_mode  = $2,
    maintenance_since = CASE
        WHEN $2 AND maintenance_since IS NOT NULL THEN maintenance_since
        WHEN $2 THEN now()
        ELSE NULL
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetApplicationPreviewPassword :one
UPDATE applications
SET preview_password_enabled = $2,
    preview_password_hash    = $3,
    preview_password_set_at  = CASE WHEN $3 = '' THEN NULL ELSE now() END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- ApplicationsByRouteDomain names every application already claiming a domain.
--
-- THE FAILURE THIS EXISTS TO STOP. Nothing refused two applications on the same
-- host, and Traefik's file provider does not either: it ends up with two
-- routers whose rules are both `Host(`example.com`)`, picks one, and the other
-- silently never serves. The operator sees a working deploy and a site that
-- stopped answering, with nothing anywhere saying why.
--
-- The team and server travel with the row because the refusal has to be
-- scoped: a conflict on the same server is a hard 409, and naming the other
-- application is only safe when the caller is in its team.
-- name: ApplicationsByRouteDomain :many
SELECT a.id, a.name, a.runtime_server_id, e.project_id, p.team_id
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN projects p ON p.id = e.project_id
WHERE lower(a.route_domain) = lower(sqlc.arg(route_domain))
  AND a.route_domain <> '';

-- ListRouteDomainsByServer is what a screen needs to warn "that one is taken"
-- before somebody submits. Hostnames only: an application name here would make
-- it an enumeration tool, which is precisely what the conflict refusal already
-- withholds from a caller outside the owning team.
-- name: ListRouteDomainsByServer :many
SELECT DISTINCT lower(route_domain) AS domain
FROM applications
WHERE runtime_server_id = $1 AND route_domain <> ''
ORDER BY domain;

-- SetApplicationWebhookSecret replaces the inbound push webhook's secret.
--
-- It exists because the original was returned exactly once, in the create
-- response, and the create dialog discarded it — so every application ever made
-- through the panel had a secret nobody held, the Overview told operators to
-- add a webhook to GitHub, and every delivery was refused 401. Push-to-deploy
-- was unreachable and unrecoverable: nothing could read the secret and nothing
-- could replace it.
-- name: SetApplicationWebhookSecret :one
UPDATE applications
SET webhook_secret_ct = $2, webhook_secret_nonce = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- ListServerWorkloads answers "what runs on this host" in one query.
--
-- The plane assembles desired state from exactly these three lists and no route
-- ever exposed them, so the panel could report a server degraded, or ask for
-- confirmation before removing it, without being able to say what was on it.
-- "What will I break" is the first question anyone asks about a host.
--
-- The project travels with each row because a workload without one is a name in
-- a list; the caller filters by what they may see.
-- name: ListServerWorkloads :many
SELECT a.id, 'application' AS kind, a.name, e.project_id, p.name AS project_name,
       a.status, p.team_id
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN projects p ON p.id = e.project_id
WHERE a.runtime_server_id = $1
UNION ALL
SELECT c.id, 'compose_stack', c.name, e.project_id, p.name, c.status, p.team_id
FROM compose_stacks c
JOIN environments e ON e.id = c.environment_id
JOIN projects p ON p.id = e.project_id
WHERE c.runtime_server_id = $1
UNION ALL
SELECT d.id, 'database', d.name, e.project_id, p.name, d.status, p.team_id
FROM databases d
JOIN environments e ON e.id = d.environment_id
JOIN projects p ON p.id = e.project_id
WHERE d.server_id = $1 AND d.pending_delete = false
ORDER BY kind, name;

-- name: CreateRegistry :one
INSERT INTO registries (id, team_id, name, url, username, token_ct, token_nonce, can_pull, can_push)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetRegistry :one
SELECT * FROM registries WHERE id = $1;

-- name: ListRegistriesByTeams :many
SELECT * FROM registries WHERE team_id = ANY(@team_ids::text[]) ORDER BY name;

-- UpdateRegistry is a partial edit: a NULL argument leaves the column alone, so
-- rotating a token does not mean re-sending the URL, and renaming does not mean
-- re-sending the token.
-- name: UpdateRegistry :one
UPDATE registries
SET name        = COALESCE(sqlc.narg('name'), name),
    url         = COALESCE(sqlc.narg('url'), url),
    username    = COALESCE(sqlc.narg('username'), username),
    token_ct    = COALESCE(sqlc.narg('token_ct'), token_ct),
    token_nonce = COALESCE(sqlc.narg('token_nonce'), token_nonce),
    can_pull    = COALESCE(sqlc.narg('can_pull'), can_pull),
    can_push    = COALESCE(sqlc.narg('can_push'), can_push),
    updated_at  = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: RecordRegistryTest :one
UPDATE registries
SET last_test_at = now(), last_test_ok = $2, last_test_detail = $3
WHERE id = $1
RETURNING *;

-- name: DeleteRegistry :exec
DELETE FROM registries WHERE id = $1;

-- The applications that would break if this registry went away: the ones that
-- pull through it, and the ones that push to it. Named rather than counted,
-- because "3 applications" is not something an operator can act on.
-- name: ApplicationsUsingRegistry :many
SELECT a.id, a.name, e.name AS environment_name, p.name AS project_name,
       (a.source_registry_id = $1) AS pulls,
       (a.build_push_registry_id = $1) AS pushes
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN projects p ON p.id = e.project_id
WHERE a.source_registry_id = $1 OR a.build_push_registry_id = $1
ORDER BY p.name, e.name, a.name;

-- name: UpsertStatusPage :one
INSERT INTO status_pages (id, project_id, slug, title, enabled, domain, https, route_server_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg('route_server_id'))
ON CONFLICT (project_id) DO UPDATE
SET slug = EXCLUDED.slug,
    title = EXCLUDED.title,
    enabled = EXCLUDED.enabled,
    domain = EXCLUDED.domain,
    https = EXCLUDED.https,
    route_server_id = EXCLUDED.route_server_id,
    updated_at = now()
RETURNING *;

-- name: GetStatusPageByProject :one
SELECT * FROM status_pages WHERE project_id = $1;

-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE id = $1;

-- name: GetStatusPageBySlug :one
SELECT * FROM status_pages WHERE slug = $1;

-- name: DeleteStatusPage :exec
DELETE FROM status_pages WHERE project_id = $1;

-- name: ListEnabledStatusPages :many
SELECT * FROM status_pages WHERE enabled = true ORDER BY slug;

-- Every enabled page with a domain and a serving node, for the desired-state
-- build. A page with no route_server_id is served at the panel origin only.
-- name: ListRoutableStatusPages :many
SELECT * FROM status_pages
WHERE enabled = true AND domain <> '' AND route_server_id IS NOT NULL;

-- name: CreateStatusPageComponent :one
INSERT INTO status_page_components (id, status_page_id, resource_kind, resource_id, label, position)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (status_page_id, resource_kind, resource_id) DO UPDATE
SET label = EXCLUDED.label, position = EXCLUDED.position
RETURNING *;

-- name: ListStatusPageComponents :many
SELECT * FROM status_page_components WHERE status_page_id = $1 ORDER BY position, id;

-- name: DeleteStatusPageComponentsNotIn :exec
DELETE FROM status_page_components
WHERE status_page_id = $1 AND NOT (id = ANY(@keep_ids::text[]));

-- name: ListAllTrackedComponents :many
SELECT c.* FROM status_page_components c
JOIN status_pages p ON p.id = c.status_page_id
WHERE p.enabled = true
ORDER BY c.status_page_id, c.position;

-- name: GetOpenStatusInterval :one
SELECT * FROM status_intervals WHERE component_id = $1 AND ended_at IS NULL;

-- name: OpenStatusInterval :one
INSERT INTO status_intervals (id, component_id, state, started_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CloseStatusInterval :exec
UPDATE status_intervals SET ended_at = $2 WHERE id = $1 AND ended_at IS NULL;

-- name: GetStatusInterval :one
SELECT * FROM status_intervals WHERE id = $1;

-- name: SetStatusIntervalMessage :exec
UPDATE status_intervals SET message = $2 WHERE id = $1;

-- Intervals overlapping the window, for the bars and the uptime figure. An
-- open interval (ended_at IS NULL) overlaps by definition.
-- name: ListStatusIntervalsSince :many
SELECT * FROM status_intervals
WHERE component_id = $1 AND (ended_at IS NULL OR ended_at >= $2)
ORDER BY started_at;

-- name: GetLastEvaluation :one
SELECT * FROM status_evaluations WHERE id = 'singleton';

-- name: StampEvaluation :exec
INSERT INTO status_evaluations (id, evaluated_at) VALUES ('singleton', $1)
ON CONFLICT (id) DO UPDATE SET evaluated_at = EXCLUDED.evaluated_at;

-- name: DeleteStatusIntervalsBefore :exec
DELETE FROM status_intervals si
WHERE si.id IN (
    SELECT s2.id FROM status_intervals s2
    WHERE s2.ended_at IS NOT NULL AND s2.ended_at < $1
    LIMIT $2
);

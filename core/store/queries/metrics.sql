-- Metrics ingest is one multi-row upsert per report, with the bucket identity
-- as the idempotency key: JetStream redelivery and the agent's own replay are
-- both no-ops (ENGINEERING rule 12).
--
-- ON CONFLICT DO UPDATE rather than DO NOTHING, taking the MAXIMUM of each
-- counter: a redelivered bucket is identical so max is a no-op, while a bucket
-- an agent re-sealed after a restart carries at least as much as the first
-- one. Taking the max cannot decrease a figure the operator already saw.

-- name: UpsertResourceMetric :exec
INSERT INTO resource_metrics (
    resource_kind, resource_id, bucket_start, server_id,
    cpu_core_ms, cpu_percent_peak, memory_byte_seconds, memory_bytes_peak,
    memory_limit_bytes, sample_count, covered_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (resource_kind, resource_id, bucket_start) DO UPDATE SET
    cpu_core_ms = GREATEST(resource_metrics.cpu_core_ms, EXCLUDED.cpu_core_ms),
    cpu_percent_peak = GREATEST(resource_metrics.cpu_percent_peak, EXCLUDED.cpu_percent_peak),
    memory_byte_seconds = GREATEST(resource_metrics.memory_byte_seconds, EXCLUDED.memory_byte_seconds),
    memory_bytes_peak = GREATEST(resource_metrics.memory_bytes_peak, EXCLUDED.memory_bytes_peak),
    memory_limit_bytes = EXCLUDED.memory_limit_bytes,
    sample_count = GREATEST(resource_metrics.sample_count, EXCLUDED.sample_count),
    covered_seconds = GREATEST(resource_metrics.covered_seconds, EXCLUDED.covered_seconds);

-- name: UpsertRequestMetric :exec
INSERT INTO request_metrics (
    resource_kind, resource_id, bucket_start, server_id,
    requests, redirect_count, status_2xx, status_3xx, status_4xx, status_5xx,
    response_bytes, latency_buckets, histogram_version, sample_rate
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (resource_kind, resource_id, bucket_start) DO UPDATE SET
    requests = GREATEST(request_metrics.requests, EXCLUDED.requests),
    redirect_count = GREATEST(request_metrics.redirect_count, EXCLUDED.redirect_count),
    status_2xx = GREATEST(request_metrics.status_2xx, EXCLUDED.status_2xx),
    status_3xx = GREATEST(request_metrics.status_3xx, EXCLUDED.status_3xx),
    status_4xx = GREATEST(request_metrics.status_4xx, EXCLUDED.status_4xx),
    status_5xx = GREATEST(request_metrics.status_5xx, EXCLUDED.status_5xx),
    response_bytes = GREATEST(request_metrics.response_bytes, EXCLUDED.response_bytes),
    latency_buckets = EXCLUDED.latency_buckets,
    histogram_version = EXCLUDED.histogram_version,
    sample_rate = EXCLUDED.sample_rate;

-- name: UpsertRequestPath :exec
INSERT INTO request_paths (
    resource_kind, resource_id, bucket_start, path,
    requests, status_5xx, latency_buckets, histogram_version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (resource_kind, resource_id, bucket_start, path) DO UPDATE SET
    requests = GREATEST(request_paths.requests, EXCLUDED.requests),
    status_5xx = GREATEST(request_paths.status_5xx, EXCLUDED.status_5xx),
    latency_buckets = EXCLUDED.latency_buckets,
    histogram_version = EXCLUDED.histogram_version;

-- name: UpsertResourceDisk :exec
INSERT INTO resource_disk_usage (
    resource_kind, resource_id, bucket_start, server_id,
    image_bytes, volume_bytes, container_bytes
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (resource_kind, resource_id, bucket_start) DO UPDATE SET
    image_bytes = EXCLUDED.image_bytes,
    volume_bytes = EXCLUDED.volume_bytes,
    container_bytes = EXCLUDED.container_bytes;

-- name: ListResourceMetrics :many
SELECT * FROM resource_metrics
WHERE resource_kind = $1 AND resource_id = $2 AND bucket_start >= $3
ORDER BY bucket_start;

-- name: ListRequestMetrics :many
SELECT * FROM request_metrics
WHERE resource_kind = $1 AND resource_id = $2 AND bucket_start >= $3
ORDER BY bucket_start;

-- name: ListRequestPaths :many
SELECT path,
       SUM(requests)::BIGINT AS requests,
       SUM(status_5xx)::BIGINT AS status_5xx
FROM request_paths
WHERE resource_kind = $1 AND resource_id = $2 AND bucket_start >= $3
GROUP BY path
ORDER BY requests DESC
LIMIT $4;

-- name: LatestResourceDisk :one
SELECT * FROM resource_disk_usage
WHERE resource_kind = $1 AND resource_id = $2
ORDER BY bucket_start DESC
LIMIT 1;

-- A server's own figure is the sum of the managed containers on it, which is
-- why there is no separate server-level sampler.
-- name: ListServerMetrics :many
SELECT bucket_start,
       SUM(cpu_core_ms)::BIGINT AS cpu_core_ms,
       MAX(cpu_percent_peak)::DOUBLE PRECISION AS cpu_percent_peak,
       SUM(memory_byte_seconds)::BIGINT AS memory_byte_seconds,
       MAX(memory_bytes_peak)::BIGINT AS memory_bytes_peak,
       MAX(covered_seconds)::INTEGER AS covered_seconds
FROM resource_metrics
WHERE server_id = $1 AND bucket_start >= $2
GROUP BY bucket_start
ORDER BY bucket_start;

-- ─── rollup ────────────────────────────────────────────────────────────────

-- name: ListUnrolledMetricDays :many
SELECT DISTINCT date_trunc('day', bucket_start)::DATE AS day
FROM resource_metrics
WHERE bucket_start >= $1 AND bucket_start < $2
ORDER BY day;

-- name: RollupResourceUsageDay :exec
INSERT INTO resource_usage_daily (
    resource_kind, resource_id, day,
    cpu_core_seconds, memory_byte_seconds, memory_bytes_peak, disk_bytes,
    requests, status_5xx
)
SELECT m.resource_kind, m.resource_id, $1::DATE,
       (SUM(m.cpu_core_ms) / 1000)::BIGINT,
       SUM(m.memory_byte_seconds)::BIGINT,
       MAX(m.memory_bytes_peak)::BIGINT,
       COALESCE((
           SELECT d.image_bytes + d.volume_bytes + d.container_bytes
           FROM resource_disk_usage d
           WHERE d.resource_kind = m.resource_kind AND d.resource_id = m.resource_id
             AND d.bucket_start >= $1::DATE AND d.bucket_start < ($1::DATE + 1)
           ORDER BY d.bucket_start DESC LIMIT 1
       ), 0)::BIGINT,
       COALESCE((
           SELECT SUM(r.requests) FROM request_metrics r
           WHERE r.resource_kind = m.resource_kind AND r.resource_id = m.resource_id
             AND r.bucket_start >= $1::DATE AND r.bucket_start < ($1::DATE + 1)
       ), 0)::BIGINT,
       COALESCE((
           SELECT SUM(r.status_5xx) FROM request_metrics r
           WHERE r.resource_kind = m.resource_kind AND r.resource_id = m.resource_id
             AND r.bucket_start >= $1::DATE AND r.bucket_start < ($1::DATE + 1)
       ), 0)::BIGINT
FROM resource_metrics m
WHERE m.bucket_start >= $1::DATE AND m.bucket_start < ($1::DATE + 1)
GROUP BY m.resource_kind, m.resource_id
ON CONFLICT (resource_kind, resource_id, day) DO UPDATE SET
    cpu_core_seconds = EXCLUDED.cpu_core_seconds,
    memory_byte_seconds = EXCLUDED.memory_byte_seconds,
    memory_bytes_peak = EXCLUDED.memory_bytes_peak,
    disk_bytes = EXCLUDED.disk_bytes,
    requests = EXCLUDED.requests,
    status_5xx = EXCLUDED.status_5xx;

-- The ownership chain, stamped after the numbers so a renamed project reads as
-- it did on the day. Applications only; stacks and databases get their own
-- statement below.
-- name: StampApplicationUsageOwnership :exec
UPDATE resource_usage_daily u
SET team_id = p.team_id, project_id = p.id, project_name = p.name,
    environment_id = e.id, environment_name = e.name, resource_name = a.name
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN projects p ON p.id = e.project_id
WHERE u.resource_kind = 'application' AND u.resource_id = a.id AND u.day = $1;

-- name: StampComposeUsageOwnership :exec
UPDATE resource_usage_daily u
SET team_id = p.team_id, project_id = p.id, project_name = p.name,
    environment_id = e.id, environment_name = e.name, resource_name = c.name
FROM compose_stacks c
JOIN environments e ON e.id = c.environment_id
JOIN projects p ON p.id = e.project_id
WHERE u.resource_kind = 'compose_stack' AND u.resource_id = c.id AND u.day = $1;

-- name: StampDatabaseUsageOwnership :exec
UPDATE resource_usage_daily u
SET team_id = p.team_id, project_id = p.id, project_name = p.name,
    environment_id = e.id, environment_name = e.name, resource_name = d.name
FROM databases d
JOIN environments e ON e.id = d.environment_id
JOIN projects p ON p.id = e.project_id
WHERE u.resource_kind = 'database' AND u.resource_id = d.id AND u.day = $1;

-- Deploy minutes are build-and-rollout time, from started_at when the row has
-- one and created_at otherwise (an upper bound, documented as such).
-- name: RollupDeployMinutes :exec
UPDATE resource_usage_daily u
SET deploy_count = s.n, deploy_seconds = s.secs
FROM (
    SELECT d.application_id AS app_id,
           COUNT(*)::INTEGER AS n,
           COALESCE(SUM(EXTRACT(EPOCH FROM (d.finished_at - COALESCE(d.started_at, d.created_at)))), 0)::BIGINT AS secs
    FROM deployments d
    WHERE d.finished_at IS NOT NULL
      AND d.finished_at >= $1::DATE AND d.finished_at < ($1::DATE + 1)
    GROUP BY d.application_id
) s
WHERE u.resource_kind = 'application' AND u.resource_id = s.app_id AND u.day = $1;

-- ─── usage reads ───────────────────────────────────────────────────────────

-- name: ListUsageForMonth :many
SELECT * FROM resource_usage_daily
WHERE day >= $1 AND day < $2
ORDER BY project_name, resource_name;

-- ─── retention ─────────────────────────────────────────────────────────────

-- name: DeleteResourceMetricsBefore :exec
DELETE FROM resource_metrics WHERE bucket_start < $1;

-- name: DeleteRequestMetricsBefore :exec
DELETE FROM request_metrics WHERE bucket_start < $1;

-- name: DeleteRequestPathsBefore :exec
DELETE FROM request_paths WHERE bucket_start < $1;

-- name: DeleteResourceDiskBefore :exec
DELETE FROM resource_disk_usage WHERE bucket_start < $1;

-- name: DeleteUsageDailyBefore :exec
DELETE FROM resource_usage_daily WHERE day < $1;

-- name: GetMetricsSettings :one
SELECT * FROM metrics_settings WHERE id = 1;

-- name: SetMetricsSettings :one
INSERT INTO metrics_settings (id, enabled, request_analytics, bucket_seconds)
VALUES (1, $1, $2, $3)
ON CONFLICT (id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    request_analytics = EXCLUDED.request_analytics,
    bucket_seconds = EXCLUDED.bucket_seconds,
    updated_at = now()
RETURNING *;

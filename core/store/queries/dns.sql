-- DNS automation (dns-automation.md). The provider is a singleton on
-- panel_mail's shape; zones are a cache; records are desired state.

-- name: GetDNSProvider :one
SELECT kind, config_ct, config_nonce, updated_at FROM dns_providers WHERE id = 1;

-- name: SetDNSProvider :exec
INSERT INTO dns_providers (id, kind, config_ct, config_nonce, updated_at)
VALUES (1, $1, $2, $3, now())
ON CONFLICT (id) DO UPDATE
SET kind = EXCLUDED.kind, config_ct = EXCLUDED.config_ct,
    config_nonce = EXCLUDED.config_nonce, updated_at = now();

-- name: DeleteDNSProvider :exec
DELETE FROM dns_providers WHERE id = 1;

-- ─── Zones (a cache of what the provider says we may manage) ────────────────

-- name: ListDNSZones :many
SELECT * FROM dns_zones ORDER BY name;

-- name: GetDNSZoneByName :one
SELECT * FROM dns_zones WHERE name = $1;

-- ReplaceDNSZones is how a refresh lands: the provider's answer is the whole
-- truth, so anything it no longer lists is no longer ours to manage. Zones with
-- records still attached are protected by dns_records' ON DELETE RESTRICT, so a
-- zone that still has managed records cannot silently vanish from the cache —
-- the refresh fails loudly instead, which is the safe direction.
-- name: DeleteDNSZonesNotIn :exec
DELETE FROM dns_zones WHERE name <> ALL(@names::text[]);

-- name: UpsertDNSZone :one
INSERT INTO dns_zones (id, provider_zone_id, name, status, refreshed_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (name) DO UPDATE
SET provider_zone_id = EXCLUDED.provider_zone_id, status = EXCLUDED.status, refreshed_at = now()
RETURNING *;

-- name: CountDNSZones :one
SELECT count(*) FROM dns_zones;

-- ─── Records (desired state) ────────────────────────────────────────────────

-- name: GetDNSRecord :one
SELECT * FROM dns_records WHERE id = $1;

-- name: GetDNSRecordByApplication :one
SELECT * FROM dns_records WHERE application_id = $1 AND desired = 'present';

-- name: UpsertDNSRecord :one
INSERT INTO dns_records (id, application_id, zone_id, name, type, content, desired, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $6, 'present', now())
ON CONFLICT (zone_id, name, type) DO UPDATE
SET application_id = EXCLUDED.application_id,
    content        = EXCLUDED.content,
    desired        = 'present',
    -- Re-arm: a row coming back to 'present', or changing content, is work
    -- again. Clearing the error and the attempt count is what makes a fixed
    -- misconfiguration retry immediately instead of waiting out the old
    -- backoff.
    last_error      = '',
    attempt         = 0,
    next_attempt_at = now(),
    updated_at      = now()
RETURNING *;

-- TombstoneDNSRecord is deletion, expressed as desired state. The row is NOT
-- removed: it is the only remaining record of what has to be deleted from the
-- provider, and dropping it here is exactly how an orphan leaks (§3.3).
-- name: TombstoneDNSRecord :exec
UPDATE dns_records
SET desired = 'absent', next_attempt_at = now(), attempt = 0, last_error = '', updated_at = now()
WHERE id = $1;

-- name: TombstoneDNSRecordsForApplication :exec
UPDATE dns_records
SET desired = 'absent', next_attempt_at = now(), attempt = 0, last_error = '', updated_at = now()
WHERE application_id = $1 AND desired = 'present';

-- TombstoneDNSRecordsForProject reaches every application under a project,
-- through environments, so deleting a project reaps its records too (§4.3).
-- name: TombstoneDNSRecordsForProject :exec
UPDATE dns_records r
SET desired = 'absent', next_attempt_at = now(), attempt = 0, last_error = '', updated_at = now()
FROM applications a
JOIN environments e ON e.id = a.environment_id
WHERE r.application_id = a.id AND e.project_id = $1 AND r.desired = 'present';

-- name: TombstoneDNSRecordsForEnvironment :exec
UPDATE dns_records r
SET desired = 'absent', next_attempt_at = now(), attempt = 0, last_error = '', updated_at = now()
FROM applications a
WHERE r.application_id = a.id AND a.environment_id = $1 AND r.desired = 'present';

-- ListDueDNSRecords is the sweeper's working set: rows whose observed state
-- does not match `desired` and whose backoff has elapsed.
-- name: ListDueDNSRecords :many
SELECT r.*, z.provider_zone_id, z.name AS zone_name
FROM dns_records r
JOIN dns_zones z ON z.id = r.zone_id
WHERE (r.next_attempt_at IS NOT NULL AND r.next_attempt_at <= $1)
  AND ((r.desired = 'present' AND r.provider_record_id IS NULL)
    OR (r.desired = 'absent'  AND r.provider_record_id IS NOT NULL))
ORDER BY r.next_attempt_at
LIMIT $2;

-- MarkDNSRecordCreated records the observation that the record now exists.
-- next_attempt_at goes NULL: converged rows are not work.
-- name: MarkDNSRecordCreated :one
UPDATE dns_records
SET provider_record_id = $2, last_error = '', attempt = 0, next_attempt_at = NULL, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteDNSRecordRow :exec
DELETE FROM dns_records WHERE id = $1;

-- name: FailDNSRecord :exec
UPDATE dns_records
SET last_error = $2, attempt = $3, next_attempt_at = $4, updated_at = now()
WHERE id = $1;

-- ─── Server public address ──────────────────────────────────────────────────

-- name: SetServerPublicAddress :one
UPDATE servers SET public_address = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- ListDNSRecordsForServerChange re-points every record whose application runs
-- on a server whose public address just changed (§4.3).
-- name: ListDNSRecordsForServer :many
SELECT r.* FROM dns_records r
JOIN applications a ON a.id = r.application_id
WHERE a.runtime_server_id = $1 AND r.desired = 'present';

-- TombstoneOrphanedDNSRecords is how deletion actually becomes reliable.
--
-- dns_records.application_id is ON DELETE SET NULL, and environments and
-- projects cascade to applications — so deleting an application, an environment
-- or a whole project all leave the SAME trace: a record with no application. A
-- record with no application has no reason to exist, so this one statement
-- reaps all three cases without a hook per case, and without depending on an
-- event that might not fire (§4.3, and research/coolify.md's orphan lesson).
-- name: TombstoneOrphanedDNSRecords :exec
UPDATE dns_records
SET desired = 'absent', next_attempt_at = now(), attempt = 0, last_error = '', updated_at = now()
WHERE application_id IS NULL AND desired = 'present';

-- ListApplicationsWantingDNS is what makes record CREATION state-owned rather
-- than event-owned.
--
-- Hooking the two application REST handlers was not enough: a template install
-- creates applications through templates.Install, a preview environment through
-- its own path, and each new caller would silently produce no DNS at all. The
-- first real use of this feature hit exactly that — a Grafana template with a
-- verified domain and no record, because no hook fired. Deriving the desired
-- set from the applications table on every sweep makes it impossible for a
-- creation path to be forgotten (§4.3, and research/coolify.md's lesson that
-- lifecycle must be owned by state, not events).
--
-- Applications whose server has no public address are excluded here rather than
-- filtered later: there is nothing to point a record at, and §6 reports that as
-- its own state.
-- name: ListApplicationsWantingDNS :many
SELECT a.id, a.route_domain, s.public_address
FROM applications a
JOIN servers s ON s.id = a.runtime_server_id
WHERE a.route_domain <> '' AND s.public_address <> ''
ORDER BY a.id;

-- Managed records per zone, counting only what the panel still wants to exist.
-- A tombstoned row (desired='absent') is on its way out and would overstate
-- what disconnecting the provider affects.
-- name: CountManagedRecordsByZone :many
SELECT zone_id, count(*)::bigint AS managed_records
FROM dns_records
WHERE desired = 'present'
GROUP BY zone_id;

-- The applications whose domains are verified through the connected provider,
-- which is exactly what stops being verified if it is disconnected. Ordered so
-- a confirmation dialog reads the same way twice.
-- name: ListApplicationsWithManagedDNS :many
SELECT r.application_id, a.name AS application_name, r.name AS domain, z.name AS zone_name
FROM dns_records r
JOIN dns_zones z ON z.id = r.zone_id
JOIN applications a ON a.id = r.application_id
WHERE r.desired = 'present' AND r.application_id IS NOT NULL
ORDER BY z.name, r.name;

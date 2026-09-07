-- name: CreatePanelUpgrade :one
INSERT INTO panel_upgrades (id, from_version, to_version, actor, rollback, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetActivePanelUpgrade :one
SELECT * FROM panel_upgrades WHERE finished_at IS NULL;

-- name: GetPanelUpgrade :one
SELECT * FROM panel_upgrades WHERE id = $1;

-- name: SetPanelUpgradePhase :exec
UPDATE panel_upgrades
SET phase = $2, detail = $3,
    finished_at = CASE WHEN $2 IN ('succeeded', 'rolled_back', 'failed') THEN now() ELSE finished_at END
WHERE id = $1;

-- name: AttachPanelUpgradeSnapshot :exec
UPDATE panel_upgrades SET snapshot_id = $2 WHERE id = $1;

-- Any request finding an expired lock clears it: a helper that died between
-- phases must not leave the panel read-only forever.
-- name: ExpireStalePanelUpgrades :exec
UPDATE panel_upgrades
SET phase = 'failed', detail = 'The upgrade helper stopped reporting and the lock expired.', finished_at = now()
WHERE finished_at IS NULL AND expires_at < $1;

-- name: ListPanelUpgrades :many
SELECT * FROM panel_upgrades ORDER BY started_at DESC LIMIT $1;

-- name: CreatePanelSnapshot :one
INSERT INTO panel_snapshots (id, version, path, size_bytes, expires_at)
VALUES ($1, $2, $3, $4, sqlc.narg('expires_at'))
RETURNING *;

-- name: GetPanelSnapshot :one
SELECT * FROM panel_snapshots WHERE id = $1;

-- name: ListPanelSnapshots :many
SELECT * FROM panel_snapshots ORDER BY created_at DESC;

-- name: SetPanelSnapshotRetention :one
UPDATE panel_snapshots SET expires_at = sqlc.narg('expires_at'), pinned = $2 WHERE id = $1 RETURNING *;

-- name: DeletePanelSnapshot :exec
DELETE FROM panel_snapshots WHERE id = $1;

-- A pinned snapshot is never swept, whatever its expiry says: pinning is the
-- operator saying "keep this one", and a sweep that overrode it would be the
-- panel deciding for them.
-- name: ListExpiredPanelSnapshots :many
SELECT * FROM panel_snapshots WHERE pinned = false AND expires_at IS NOT NULL AND expires_at < $1;

-- The pre-flight's disk check: the snapshot needs room for a dump of this,
-- twice, because a restore must be able to exist beside the thing it replaces.
-- name: DatabaseSizeBytes :one
SELECT pg_database_size(current_database())::BIGINT;

-- Running work is a COURTESY count, not a correctness requirement: builds and
-- restores execute on agents, and a plane that restarts mid-deploy does not
-- interrupt one. What waiting buys is attribution.
-- name: CountRunningWork :one
SELECT (
    (SELECT COUNT(*) FROM deployments WHERE status IN ('queued', 'building', 'distributing', 'rolling_out'))
  + (SELECT COUNT(*) FROM database_restores WHERE finished_at IS NULL)
)::BIGINT;

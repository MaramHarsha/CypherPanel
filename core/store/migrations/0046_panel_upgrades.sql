-- Guided panel upgrades and their fallback snapshots (panel-updates.md §§6, 7).
--
-- The partial unique index is the load-bearing line: it makes "at most one
-- active upgrade" a DATABASE INVARIANT rather than a check in a handler, so the
-- double-trigger that the reference platforms' pain-points row demands a lock
-- against cannot slip through a race between two requests.

-- +goose Up
CREATE TABLE panel_upgrades (
    id             TEXT PRIMARY KEY,
    from_version   TEXT NOT NULL,
    to_version     TEXT NOT NULL,
    -- preflight | waiting_for_quiesce | snapshotting | downloading | verifying
    -- | migrating | restarting | health_gate | succeeded | rolled_back | failed
    phase          TEXT NOT NULL DEFAULT 'preflight',
    detail         TEXT NOT NULL DEFAULT '',
    -- Who asked. `external` marks a version change this panel did not perform —
    -- a boot whose version differs from the last recorded one, which is what a
    -- compose install's upgrades look like.
    actor          TEXT NOT NULL DEFAULT '',
    rollback       BOOLEAN NOT NULL DEFAULT false,
    snapshot_id    TEXT,
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    -- The read-only lock's own expiry. Any request finding an expired one
    -- clears it, so a helper that dies between phases cannot leave the panel
    -- read-only forever.
    expires_at     TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 minutes'
);

CREATE UNIQUE INDEX idx_panel_upgrades_active ON panel_upgrades((finished_at IS NULL)) WHERE finished_at IS NULL;
CREATE INDEX idx_panel_upgrades_started ON panel_upgrades(started_at DESC);

CREATE TABLE panel_snapshots (
    id          TEXT PRIMARY KEY,
    version     TEXT NOT NULL,
    path        TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- NULL means keep forever. The retention is the ONE decision the operator
    -- makes about this feature, and it must not be made for them: the snapshot
    -- is the only thing standing between a bad release and a lost panel.
    expires_at  TIMESTAMPTZ,
    pinned      BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_panel_snapshots_created ON panel_snapshots(created_at DESC);

-- +goose Down
DROP TABLE panel_snapshots;
DROP TABLE panel_upgrades;

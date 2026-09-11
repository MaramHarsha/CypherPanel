-- Public per-project status pages (status-pages.md §5).
--
-- One page per project, enforced by UNIQUE(project_id): "which of our four
-- status pages is the real one" is not a question this product can be asked.
--
-- status_intervals is the whole time series and it is written PER CHANGE, not
-- per tick: a healthy component has exactly one open row, forever. The 30 daily
-- bars, the uptime percentage and the incident list are all queries over the
-- same rows, so there is no samples table and nothing whose cost scales with
-- time rather than with events.

-- +goose Up
CREATE TABLE status_pages (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    slug            TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT false,
    domain          TEXT NOT NULL DEFAULT '',
    https           BOOLEAN NOT NULL DEFAULT true,
    route_server_id TEXT REFERENCES servers(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- resource_id carries no foreign key on purpose: the target is an Application,
-- a Compose Stack or a Managed Database, and a polymorphic owner cannot have
-- one. A component whose resource no longer exists is not rendered (the read is
-- a join) and is swept.
CREATE TABLE status_page_components (
    id             TEXT PRIMARY KEY,
    status_page_id TEXT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
    resource_kind  TEXT NOT NULL,
    resource_id    TEXT NOT NULL,
    label          TEXT NOT NULL,
    position       INTEGER NOT NULL DEFAULT 0,
    tracking_since TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (status_page_id, resource_kind, resource_id)
);

CREATE INDEX idx_status_page_components_page ON status_page_components(status_page_id, position);

-- Exactly one row, ever: the evaluator's last tick. On boot a gap wider than
-- two ticks is drawn as grey on every tracked component, so time the plane
-- could not observe is excluded from the uptime denominator rather than
-- silently counted as up (§6.3).
CREATE TABLE status_evaluations (
    id           TEXT PRIMARY KEY,
    evaluated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE status_intervals (
    id           TEXT PRIMARY KEY,
    component_id TEXT NOT NULL REFERENCES status_page_components(id) ON DELETE CASCADE,
    state        TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL,
    ended_at     TIMESTAMPTZ,
    message      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_status_intervals_component ON status_intervals(component_id, started_at DESC);
-- The open interval per component is read on every evaluator tick.
CREATE UNIQUE INDEX idx_status_intervals_open ON status_intervals(component_id) WHERE ended_at IS NULL;

-- +goose Down
DROP TABLE status_intervals;
DROP TABLE status_evaluations;
DROP TABLE status_page_components;
DROP TABLE status_pages;

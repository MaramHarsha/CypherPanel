-- +goose Up
-- Phase 3, scheduled-tasks.md (ADR-011): a cron task declared on an Application,
-- run by the agent inside the app's own container. command is argv (never a
-- shell string). Run history is the agent's observations, bounded per task.
-- Additive (ENGINEERING rule 16).

CREATE TABLE scheduled_tasks (
    id             TEXT        PRIMARY KEY,
    application_id TEXT        NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    name           TEXT        NOT NULL,
    schedule       TEXT        NOT NULL,          -- standard 5-field cron
    command        TEXT[]      NOT NULL,          -- argv
    enabled        BOOLEAN     NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (application_id, name)
);
CREATE INDEX idx_scheduled_tasks_app ON scheduled_tasks (application_id);

CREATE TABLE scheduled_task_runs (
    id          TEXT        PRIMARY KEY,
    task_id     TEXT        NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
    started_at  TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    exit_code   INTEGER,
    status      TEXT        NOT NULL,             -- running | succeeded | failed
    output_tail TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_task_runs_task ON scheduled_task_runs (task_id, started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS scheduled_task_runs;
DROP TABLE IF EXISTS scheduled_tasks;

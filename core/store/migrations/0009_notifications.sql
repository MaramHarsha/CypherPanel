-- +goose Up
-- Phase 3, notifications.md: a project-scoped Notifier — a declarative
-- "on these events, deliver to this channel" row. Channel config (SMTP creds,
-- webhook URL, bot token) is sealed at rest (secret.Box), never stored or
-- returned in plaintext. Additive (ENGINEERING rule 16).

CREATE TABLE notifiers (
    id           TEXT        PRIMARY KEY,
    project_id   TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    channel      TEXT        NOT NULL,           -- email | discord | slack | telegram
    config_ct    BYTEA       NOT NULL,           -- sealed channel config JSON
    config_nonce BYTEA       NOT NULL,
    events       TEXT[]      NOT NULL,            -- subscribed event keys
    enabled      BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX idx_notifiers_project ON notifiers (project_id);

-- +goose Down
DROP TABLE IF EXISTS notifiers;

-- +goose Up
-- Phase 4, notification-inbox.md §2: the panel's own record of what happened to
-- what you own. A Notifier is opt-in, per project, and carries credentials; the
-- inbox is the one channel that needs no configuration, no webhook and no
-- secret, because it writes rows instead of making a request.
--
-- Two tables, additive, no backfill (ENGINEERING rule 16): the inbox records
-- forward, and `deployments` / `backup_records` remain the historical record.
--
-- Tenancy here is a COLUMN, not a resolver: an item IS a per-user row, so every
-- route filters WHERE user_id = <caller> and no route accepts another user's id
-- (spec §5). Both tables cascade from users(id), so deleting an account takes
-- its inbox with it.

CREATE TABLE inbox_items (
    id           TEXT        PRIMARY KEY,      -- inb_… prefix
    user_id      TEXT        NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    project_id   TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL,         -- an existing event key (§3)
    severity     TEXT        NOT NULL,         -- info | error (= domain.NotifyLevel)
    digest       BOOLEAN     NOT NULL DEFAULT false,
    -- Denormalised on purpose (§2): the item stores the rendered statement
    -- about a moment. Resolving (kind, resource_id) at read time would make
    -- yesterday's failure read like today's success, and would break exactly
    -- when the resource is deleted — the case an operator most wants to read.
    title        TEXT        NOT NULL,         -- immediate: event title; digest: label ("Backups")
    body         TEXT        NOT NULL DEFAULT '',
    -- An in-panel path, validated at write time (§5): a free-text link rendered
    -- inside the authenticated shell is a stored open redirect.
    link         TEXT        NOT NULL DEFAULT '',
    link_label   TEXT        NOT NULL DEFAULT '',
    count_ok     INTEGER     NOT NULL DEFAULT 1,
    count_total  INTEGER     NOT NULL DEFAULT 1,
    -- The focus ids already rolled into this row. The guard that makes a
    -- redelivered observation a no-op instead of an inflated counter
    -- (ENGINEERING rule 12).
    sources      TEXT[]      NOT NULL DEFAULT '{}',
    dedupe_key   TEXT        NOT NULL,         -- <kind>:<focus_id> | digest:<kind>:<project>:<day>
    read_at      TIMESTAMPTZ,                  -- NULL = unread
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Per-user, not global: two members of the same team each get their own row
    -- for one event, and each reads it independently.
    UNIQUE (user_id, dedupe_key)
);
-- The list's seek page, in its exact order (§6).
CREATE INDEX idx_inbox_items_user_created ON inbox_items (user_id, created_at DESC, id DESC);
-- Partial: the bell counts only what is unread, on every panel load.
CREATE INDEX idx_inbox_items_unread ON inbox_items (user_id) WHERE read_at IS NULL;

-- Preferences are stored as MUTES (§2). An absent row and an empty array both
-- mean "everything on", so a kind added later is on by default for everyone; a
-- positive subscription list would silently exclude every future failure kind
-- from every existing account — the wrong direction to fail.
CREATE TABLE inbox_preferences (
    user_id     TEXT        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    muted_kinds TEXT[]      NOT NULL DEFAULT '{}',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS inbox_preferences;
DROP TABLE IF EXISTS inbox_items;

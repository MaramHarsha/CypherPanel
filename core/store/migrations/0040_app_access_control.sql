-- Who may reach an application's front door (app-access-control.md §3).
--
-- Current state, NOT a per-revision snapshot, and that is the whole decision:
-- if access control were snapshotted, a rollback would silently lift a lockout
-- or restore a deleted allowlist entry. A security control that changes because
-- someone re-pointed a revision is not a control. The precedent is already
-- here — restart_token, limits, volumes and ports are read from this row at
-- spec-build time while the route's domain comes from the revision snapshot.
--
-- The passphrase itself is never stored: only its bcrypt hash. The plaintext
-- exists in the operator's clipboard and nowhere else, and is returned exactly
-- once by the call that set it — the contract reset-password already has.
--
-- +goose Up
ALTER TABLE applications
    ADD COLUMN ip_allowlist_enabled     BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN ip_allowlist             JSONB       NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN preview_password_enabled BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN preview_password_hash    TEXT        NOT NULL DEFAULT '',
    ADD COLUMN preview_password_set_at  TIMESTAMPTZ;

-- +goose Down
ALTER TABLE applications
    DROP COLUMN preview_password_set_at,
    DROP COLUMN preview_password_hash,
    DROP COLUMN preview_password_enabled,
    DROP COLUMN ip_allowlist,
    DROP COLUMN ip_allowlist_enabled;

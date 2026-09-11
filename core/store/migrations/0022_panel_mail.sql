-- +goose Up
-- Panel Mail and Email Changes (docs/features/panel-mail.md).
--
-- panel_mail is a singleton, the way plane_ca is: a panel has one outbound
-- identity, and "exactly one row" is a constraint the database can hold rather
-- than a rule the application has to remember. The config is one sealed JSON
-- blob (AES-256-GCM under CYPHERD_MASTER_KEY, the _ct/_nonce convention used by
-- notifiers and backup targets) because it is written and read back whole; a
-- partial update of a credential is exactly the bug that convention prevents.
CREATE TABLE panel_mail (
    id           INTEGER     PRIMARY KEY DEFAULT 1,
    config_ct    BYTEA       NOT NULL,
    config_nonce BYTEA       NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT panel_mail_singleton CHECK (id = 1)
);

-- email_changes copies join_tokens (§5.3 of the threat model) because it is the
-- same object: a short-lived, single-use permission to do one thing once. Only
-- a hash of the secret is stored, so a database read yields nothing usable, and
-- consumed_at makes the single-use guarantee the database's job rather than the
-- application's — an atomic UPDATE is the only race-free way to spend one.
CREATE TABLE email_changes (
    id          TEXT        PRIMARY KEY,
    user_id     TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    new_email   TEXT        NOT NULL,
    token_hash  BYTEA       NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX email_changes_user_idx ON email_changes (user_id);

-- +goose Down
DROP TABLE email_changes;
DROP TABLE panel_mail;

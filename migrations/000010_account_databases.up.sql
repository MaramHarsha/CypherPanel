-- Hosted-account MariaDB databases (plan.md §4B Database Management). This is
-- the account's *user* database inventory — entirely separate from CypherCore's
-- own control-plane Postgres. Names/users are namespaced by the account's system
-- user to prevent cross-account collisions. The DB user password is stored
-- AES-GCM encrypted (password_enc), never in plaintext and never in a task
-- payload (the agent generates it and returns it via result metadata).

CREATE TABLE account_databases (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name         text NOT NULL,                     -- namespaced db name
    db_user      text NOT NULL,                     -- namespaced db user
    db_host      text NOT NULL DEFAULT 'localhost',
    status       text NOT NULL DEFAULT 'creating'
                 CHECK (status IN ('creating', 'active', 'failed', 'deleting')),
    password_enc bytea,                             -- AES-GCM encrypted DB password
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);

CREATE INDEX idx_account_databases_account ON account_databases (account_id);

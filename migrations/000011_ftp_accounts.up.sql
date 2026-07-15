-- Per-account FTP virtual users (plan.md §4B, Pure-FTPd MVP default). Each FTP
-- virtual user maps to the hosting account's system uid/gid and home, so files
-- uploaded over FTP are owned by the account user (isolation). The password is
-- stored AES-GCM encrypted (never plaintext, never in a task payload).

CREATE TABLE ftp_accounts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    username     text NOT NULL,                     -- namespaced ftp login
    home_dir     text NOT NULL,
    status       text NOT NULL DEFAULT 'creating'
                 CHECK (status IN ('creating', 'active', 'failed', 'deleting')),
    password_enc bytea,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (username)
);

CREATE INDEX idx_ftp_accounts_account ON ftp_accounts (account_id);

-- Hosted-account email mailboxes (plan.md §4B/5, Postfix + Dovecot MVP). These
-- are the control-plane records the panel manages; the actual virtual-mailbox
-- auth database (address → hash/maildir/quota that Postfix/Dovecot query) lives
-- in MariaDB on the account's server and is written by the agent. Passwords are
-- never stored here — only the bcrypt hash reaches the agent's mail DB.

CREATE TABLE mail_accounts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    address    text NOT NULL,                       -- full address user@domain
    quota_mb   integer NOT NULL DEFAULT 0,          -- 0 = unlimited
    status     text NOT NULL DEFAULT 'creating'
               CHECK (status IN ('creating', 'active', 'failed', 'deleting')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (address)
);

CREATE INDEX idx_mail_accounts_account ON mail_accounts (account_id);

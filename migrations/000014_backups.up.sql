-- Backup destinations and per-account snapshot records (plan.md §4A/4C).
--
-- A destination is a restic repository plus the credentials to unlock it.
-- Destinations are fleet infrastructure, so they are root-admin managed; any
-- reseller may back their own accounts up into one. Credentials (repo password,
-- S3/SFTP keys) are AES-256-GCM encrypted with the operator-held key
-- (internal/secretcrypt) — the same treatment hosted-account DB passwords get,
-- because a backup repo password is a recoverable secret we must hand to
-- agents, not something we can hash.

CREATE TABLE backup_destinations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    kind       text NOT NULL
               CHECK (kind IN ('local', 's3', 'sftp', 'rest')),
    -- restic repository URL, e.g. s3:s3.amazonaws.com/bucket/prefix, or an
    -- absolute path for 'local'. Not a secret; the password below is.
    repository text NOT NULL,
    -- nonce||ciphertext of the JSON credential blob. Never selected into any
    -- API response — only decrypted server-side when an agent asks for it.
    credentials_encrypted bytea NOT NULL,
    -- Scheduled backups: 'off' | 'daily' | 'weekly'. The scheduler in Core
    -- fans out one backup.run task per active account on each due tick.
    schedule   text NOT NULL DEFAULT 'off'
               CHECK (schedule IN ('off', 'daily', 'weekly')),
    -- restic forget policy. An all-zero policy is rejected at the API layer;
    -- applying it would prune every snapshot.
    retention_daily   integer NOT NULL DEFAULT 7,
    retention_weekly  integer NOT NULL DEFAULT 4,
    retention_monthly integer NOT NULL DEFAULT 6,
    last_run_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (name)
);

-- One row per backup attempt. Kept append-only-ish (status transitions in
-- place) so an operator can see failures, not just successes — a backup
-- history that only records wins hides exactly the thing you need to know.
CREATE TABLE account_backups (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    destination_id uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE CASCADE,
    -- The agent task that performs the work; NULL until dispatched.
    task_id        uuid REFERENCES tasks(id) ON DELETE SET NULL,
    -- restic snapshot id, set from the task result on success.
    snapshot_id    text NOT NULL DEFAULT '',
    kind           text NOT NULL DEFAULT 'manual'
                   CHECK (kind IN ('manual', 'scheduled', 'restore')),
    status         text NOT NULL DEFAULT 'running'
                   CHECK (status IN ('running', 'completed', 'failed')),
    size_bytes     bigint NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    started_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz
);

CREATE INDEX idx_account_backups_account ON account_backups (account_id, started_at DESC);
CREATE INDEX idx_account_backups_task ON account_backups (task_id);
CREATE INDEX idx_account_backups_destination ON account_backups (destination_id);

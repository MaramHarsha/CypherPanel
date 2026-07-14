-- Link provisioning tasks to the account they act on, so agent-reported
-- results can drive account status transitions.

ALTER TABLE tasks
    ADD COLUMN account_id uuid REFERENCES accounts (id) ON DELETE SET NULL;

CREATE INDEX tasks_account_id_idx ON tasks (account_id) WHERE account_id IS NOT NULL;

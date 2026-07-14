-- Per-account SSL certificate state (plan.md §4B Security & SSL). Issuance
-- metadata is stored so the UI and renewal scheduler need not parse PEM.

ALTER TABLE accounts
    ADD COLUMN ssl_status     text NOT NULL DEFAULT 'none'
               CHECK (ssl_status IN ('none', 'issuing', 'active', 'failed')),
    ADD COLUMN ssl_expires_at timestamptz;

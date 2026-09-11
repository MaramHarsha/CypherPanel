-- Email for verified domains, via a provider (managed-email.md §§4, 5).
--
-- The panel is NOT becoming a mail server. It writes DNS, manages mailboxes
-- through a provider's API, and seals one credential; it runs no MTA, stores no
-- message, and holds no DKIM private key — the provider generates that pair and
-- publishes only the public half.

-- +goose Up
CREATE TABLE mail_provider (
    id           INTEGER PRIMARY KEY DEFAULT 1,
    kind         TEXT NOT NULL DEFAULT 'migadu',
    -- Sealed exactly like a notifier's or a registry's: AES-256-GCM under the
    -- master key, never returned by any route, shown back as a hint.
    config_ct    BYTEA NOT NULL,
    config_nonce BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT mail_provider_singleton CHECK (id = 1)
);

-- Which verified domains have mail enabled. The provider owns the domain's
-- mail configuration; this row is the panel's own fact about which of its
-- verified domains the operator asked to route.
CREATE TABLE mail_domains (
    id           TEXT PRIMARY KEY,
    domain       TEXT NOT NULL UNIQUE,
    -- Observed: were the records the provider asked for actually written?
    records_written_at TIMESTAMPTZ,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Mailbox rows are NOT stored as the source of truth: the provider owns them
-- and the panel lists them through the API, caching nothing that would go
-- stale. What is stored is the LINK, because that is a panel fact the provider
-- knows nothing about.
--
-- ON DELETE SET NULL because deleting a panel account must not delete a
-- mailbox: the mail is the person's, and orphaning the link is the correct
-- degradation.
CREATE TABLE mailbox_links (
    id         TEXT PRIMARY KEY,
    domain_id  TEXT NOT NULL REFERENCES mail_domains(id) ON DELETE CASCADE,
    address    TEXT NOT NULL UNIQUE,
    user_id    TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mailbox_links_domain ON mailbox_links(domain_id);

-- +goose Down
DROP TABLE mailbox_links;
DROP TABLE mail_domains;
DROP TABLE mail_provider;

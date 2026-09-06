-- +goose Up
-- Panel TLS settings (docs/features/agent-identity-and-tls.md §4).
--
-- A singleton on panel_mail's and dns_providers' exact shape: a panel has one
-- ACME account, and "exactly one row" is a constraint the database can hold
-- rather than a rule the application has to remember.
--
-- Deliberately NOT sealed, unlike its two neighbours: an ACME account email and
-- a directory URL are public by construction (the email ends up in the account
-- registration Let's Encrypt publishes back, and the CA server is a well-known
-- URL). The secret in ACME is the account key, and that is generated and kept
-- by Traefik on the serving node — never by the plane (ADR-004). Sealing public
-- values would only buy the illusion of protection while making them
-- unreadable to the desired-state builder that has to send them to every agent.
CREATE TABLE panel_tls (
    id             INTEGER     PRIMARY KEY DEFAULT 1,
    -- Non-empty means "configure a certificate resolver on every node".
    acme_email     TEXT        NOT NULL,
    -- Empty means Let's Encrypt production; set to the staging directory while
    -- testing so a misconfigured domain does not burn the production rate limit.
    acme_ca_server TEXT        NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT panel_tls_singleton CHECK (id = 1),
    -- The row exists only when TLS is actually configured; clearing the setting
    -- deletes it. That keeps "is there a resolver?" a single question with a
    -- single answer instead of two (row present AND email non-empty).
    CONSTRAINT panel_tls_email_present CHECK (acme_email <> '')
);

-- +goose Down
DROP TABLE panel_tls;

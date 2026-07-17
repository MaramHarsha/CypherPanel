-- +goose Up
-- Phase 1 schema: the minimum state of record for the control-plane ↔ agent
-- handshake. Everything here is desired/observed state or identity material;
-- no transient coordination data lives in Postgres (that is NATS — ADR-003).

-- The admin/user account(s). Phase 1 bootstraps exactly one owner from config.
CREATE TABLE users (
    id            TEXT        PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL DEFAULT 'owner',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The control-plane certificate authority (asset A2, threat-model §2).
-- cert_pem is public; the private key is stored encrypted (AES-256-GCM) with a
-- master key supplied out-of-band via config, never in this database — so a DB
-- breach alone cannot mint agent identities (threat-model §5.1). Singleton row.
CREATE TABLE plane_ca (
    id            INTEGER     PRIMARY KEY DEFAULT 1,
    cert_pem      BYTEA       NOT NULL,
    encrypted_key BYTEA       NOT NULL,
    key_nonce     BYTEA       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT plane_ca_singleton CHECK (id = 1)
);

-- A managed server. Identity is its agent certificate (CN = this id), never a
-- stored credential (ADR-002). status uses the ui-principles §5 vocabulary;
-- enrolled_at distinguishes "awaiting enrollment" (NULL) from "offline"
-- (enrolled but status unknown). last_seen_at drives the unknown transition.
CREATE TABLE servers (
    id            TEXT        PRIMARY KEY,
    name          TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'unknown',
    driver        TEXT        NOT NULL DEFAULT 'docker',
    agent_version TEXT        NOT NULL DEFAULT '',
    hostname      TEXT        NOT NULL DEFAULT '',
    enrolled_at   TIMESTAMPTZ,
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-use, short-lived enrollment credential (asset A5, threat-model §5.3).
-- The id is the public token identifier; token_hash is SHA-256 of the secret
-- half, so the raw secret is never stored. consumed_at enforces single use.
CREATE TABLE join_tokens (
    id          TEXT        PRIMARY KEY,
    server_id   TEXT        NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    token_hash  BYTEA       NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_join_tokens_server ON join_tokens (server_id);

-- Browser/API sessions. token_hash is SHA-256 of the bearer token; the raw
-- token is returned to the client once and never stored (threat-model §5.8).
CREATE TABLE sessions (
    id         TEXT        PRIMARY KEY,
    user_id    TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA       NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_user ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE join_tokens;
DROP TABLE servers;
DROP TABLE plane_ca;
DROP TABLE users;

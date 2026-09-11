-- +goose Up
-- Scoped API tokens (feature-matrix V1: "API tokens with scoped abilities").
-- A personal access token authenticates as its owning user, inheriting that
-- user's role and team memberships — the scope. Only a sha256 hash is stored,
-- never the token (same discipline as sessions). Additive (rule 16).

CREATE TABLE api_tokens (
    id           TEXT        PRIMARY KEY,
    user_id      TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    token_hash   BYTEA       NOT NULL UNIQUE,
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,          -- NULL = never expires
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);

-- +goose Down
DROP TABLE IF EXISTS api_tokens;

-- +goose Up
-- Two-factor authentication (feature-matrix V1: "TOTP + recovery codes").
-- The encrypted secret already lives on users (totp_secret_enc/nonce, 0001).
-- Enrollment stores the secret first and only flips totp_enabled once a code is
-- verified, so a half-finished enrollment never locks anyone out. Recovery codes
-- are single-use fallbacks, stored hashed (never plaintext, like every other
-- credential). Additive (rule 16).

ALTER TABLE users ADD COLUMN totp_enabled BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE totp_recovery_codes (
    id         TEXT        PRIMARY KEY,
    user_id    TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  BYTEA       NOT NULL UNIQUE,
    used_at    TIMESTAMPTZ,          -- NULL = unused
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_totp_recovery_user ON totp_recovery_codes (user_id);

-- +goose Down
DROP TABLE IF EXISTS totp_recovery_codes;
ALTER TABLE users DROP COLUMN IF EXISTS totp_enabled;

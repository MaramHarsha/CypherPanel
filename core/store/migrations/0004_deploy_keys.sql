-- +goose Up
-- Deploy-key management (docs/features/deploy-key-private-repos.md): SSH
-- deploy keys for private repository access. The private key is sealed at rest
-- with the master key (same secret.Box as env vars and the CA key;
-- threat-model §5.1). Additive — no existing table is altered destructively
-- (ENGINEERING rule 16).

CREATE TABLE deploy_keys (
    id                TEXT        PRIMARY KEY,
    name              TEXT        NOT NULL,
    public_key        TEXT        NOT NULL,
    fingerprint       TEXT        NOT NULL UNIQUE,
    private_key_ct    BYTEA       NOT NULL,
    private_key_nonce BYTEA       NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Wire the existing FK column (applications.source_deploy_key_id was TEXT with
-- no constraint in 0002; add the FK now that the target table exists).
ALTER TABLE applications
    ADD CONSTRAINT fk_applications_deploy_key
    FOREIGN KEY (source_deploy_key_id) REFERENCES deploy_keys(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE applications DROP CONSTRAINT IF EXISTS fk_applications_deploy_key;
DROP TABLE IF EXISTS deploy_keys;

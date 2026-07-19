-- +goose Up
-- Server role for builder split (docs/features/builder-role-and-relay.md):
-- determines which work items a server may process. Default 'both' preserves
-- backward compatibility — single-server deploys are unchanged.
-- Additive (ENGINEERING rule 16).

ALTER TABLE servers ADD COLUMN role TEXT NOT NULL DEFAULT 'both';

-- +goose Down
ALTER TABLE servers DROP COLUMN IF EXISTS role;

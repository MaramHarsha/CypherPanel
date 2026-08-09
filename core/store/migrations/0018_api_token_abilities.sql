-- +goose Up
-- Scoped abilities for personal access tokens (feature-matrix V1: "API tokens
-- with scoped abilities"). Until now a token inherited its owner's full
-- authority; a read-only integration and a deploy webhook had to be handed the
-- same all-powerful credential.
--
-- Existing tokens keep exactly the authority they were issued with — the
-- default is the full set, so this migration changes no live behaviour.
ALTER TABLE api_tokens
    ADD COLUMN abilities TEXT[] NOT NULL DEFAULT ARRAY['read', 'write', 'deploy'];

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN abilities;

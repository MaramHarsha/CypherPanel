-- +goose Up
-- Builder role split + multi-server relay (builder-role-and-relay.md §4).
-- servers.role is the agent's asserted role, recorded from heartbeats;
-- deployments.builder_server_id pins which server built a deployment when it
-- is not the app's own server (NULL = builder = target) — it authorizes that
-- server's build/distribute events and relay pushes, and survives restarts.
ALTER TABLE servers ADD COLUMN role text NOT NULL DEFAULT 'all';
ALTER TABLE deployments ADD COLUMN builder_server_id text;

-- +goose Down
ALTER TABLE deployments DROP COLUMN builder_server_id;
ALTER TABLE servers DROP COLUMN role;

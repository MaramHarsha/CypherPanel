-- +goose Up
-- Raw host-port publishes + non-HTTP health for applications (feature-matrix V1:
-- "TCP/UDP port exposure"). `ports` is a JSONB array of
-- {host_port, container_port, protocol}; `health_kind` selects the rollout gate
-- (http default, tcp, or none). Both additive (ENGINEERING rule 16); the domain
-- route becomes optional at the validation layer, no column change needed.

ALTER TABLE applications ADD COLUMN ports JSONB NOT NULL DEFAULT '[]';
ALTER TABLE applications ADD COLUMN health_kind TEXT NOT NULL DEFAULT 'http';

-- +goose Down
ALTER TABLE applications DROP COLUMN health_kind;
ALTER TABLE applications DROP COLUMN ports;

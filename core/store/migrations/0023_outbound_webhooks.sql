-- +goose Up
-- Phase 4, outbound-webhooks.md §2: project-scoped outbound Webhook Endpoints —
-- the machine-facing twin of a Notifier. Each endpoint holds a SEALED
-- per-endpoint HMAC signing secret (secret.Box AES-256-GCM); the plane must
-- reproduce the signature, so the secret is sealed rather than hashed and never
-- returned in an API response (ENGINEERING rule 20). Deliveries and their
-- per-attempt records are the queryable evidence the operator reads back and
-- replays. Additive (ENGINEERING rule 16).

CREATE TABLE webhook_endpoints (
    id           TEXT        PRIMARY KEY,
    project_id   TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    url          TEXT        NOT NULL,            -- http(s) receiver; the endpoint's identity
    secret_ct    BYTEA       NOT NULL,            -- sealed HMAC signing secret
    secret_nonce BYTEA       NOT NULL,
    events       TEXT[]      NOT NULL,            -- subscribed event keys (§3), non-empty
    enabled      BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Deliberately no name column: the URL names the endpoint, and this
    -- constraint stops the double-add that would double every delivery (§2).
    UNIQUE (project_id, url)
);
CREATE INDEX idx_webhook_endpoints_project ON webhook_endpoints (project_id);

CREATE TABLE webhook_deliveries (
    id              TEXT        PRIMARY KEY,      -- this id IS the X-CypherPanel-Delivery header
    endpoint_id     TEXT        NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_type      TEXT        NOT NULL,         -- deploy.succeeded | … | webhook.ping
    resource_kind   TEXT        NOT NULL,         -- application | database
    -- resource_id is deliberately NOT a foreign key (§2): the log is evidence
    -- and must outlive the resource it describes; resource_name is denormalised
    -- so the feed still reads "backup.failed · atlas-pg" after a deletion.
    resource_id     TEXT        NOT NULL,
    resource_name   TEXT        NOT NULL,
    -- payload is TEXT, not JSONB (§2): the HMAC covers the raw body bytes and a
    -- JSONB round-trip can reorder keys, making a redelivery sign different
    -- bytes than the log shows.
    payload         TEXT        NOT NULL,
    status          TEXT        NOT NULL,         -- pending | succeeded | failed
    attempt         INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,                  -- NULL once terminal; drives the sweeper
    redelivery_of   TEXT,                         -- original delivery id when this is a replay
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, created_at DESC);
-- Partial: the sweeper touches only what is actually due (§2).
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';

CREATE TABLE webhook_delivery_attempts (
    delivery_id     TEXT        NOT NULL REFERENCES webhook_deliveries(id) ON DELETE CASCADE,
    attempt         INTEGER     NOT NULL,         -- 1..4
    response_status INTEGER,                      -- NULL on a transport error
    duration_ms     INTEGER     NOT NULL,
    -- Redacted transport error, never a response body (§6).
    error           TEXT        NOT NULL DEFAULT '',
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Composite PK, no minted id: an attempt insert is idempotent under
    -- redelivery — a duplicate lands as 23505, which the manager treats as
    -- "already recorded" (ENGINEERING rule 12).
    PRIMARY KEY (delivery_id, attempt)
);

-- +goose Down
DROP TABLE IF EXISTS webhook_delivery_attempts;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;

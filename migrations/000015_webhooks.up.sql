-- Outbound webhooks (plan.md §15). Domain events on the `events.>` JetStream
-- tree fan out to operator-registered HTTP endpoints, signed HMAC-SHA256.
--
-- Delivery is modelled as durable rows rather than as JetStream redelivery:
-- one event fans out to many webhooks, and NAKing the event message would
-- re-deliver to endpoints that already succeeded. Instead the stream consumer
-- records one row per (webhook, event) and acks; a delivery worker owns retry,
-- dead-lettering, and manual redelivery from these rows.

CREATE TABLE webhooks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    url        text NOT NULL,
    -- HMAC signing key, AES-256-GCM encrypted (internal/secretcrypt). The
    -- receiver needs the same key to verify, so it must be recoverable —
    -- hashing it is not an option. Shown once at creation, never again.
    secret_encrypted bytea NOT NULL,
    -- Exact event subjects this endpoint wants, e.g. {events.account.created}.
    -- An empty array means "every event" — the deliberate default for the
    -- common case of a billing/CRM integration that wants the whole feed.
    events     text[] NOT NULL DEFAULT '{}',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (name)
);

CREATE TABLE webhook_deliveries (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    -- The domain event's own id (events.Event.ID), not a row id.
    event_id   text NOT NULL,
    subject    text NOT NULL,
    -- The exact bytes that were signed and sent, retained so a manual
    -- redelivery replays the original event rather than a reconstruction.
    payload    jsonb NOT NULL,
    status     text NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'delivered', 'failed', 'dead')),
    attempts   integer NOT NULL DEFAULT 0,
    response_status integer NOT NULL DEFAULT 0,
    error      text NOT NULL DEFAULT '',
    -- Backoff schedule: the worker only picks up rows that are due.
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    -- Idempotency against JetStream redelivery: the same event can only ever
    -- produce one delivery row per endpoint.
    UNIQUE (webhook_id, event_id)
);

CREATE INDEX idx_webhook_deliveries_due
    ON webhook_deliveries (next_attempt_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX idx_webhook_deliveries_webhook
    ON webhook_deliveries (webhook_id, created_at DESC);

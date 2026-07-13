-- Agent task queue state. The queue itself is NATS JetStream; this table is
-- the durable record (status, errors, audit of what ran where).

CREATE TABLE tasks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id  uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    type       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    status     text NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'succeeded', 'failed')),
    error      text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tasks_server_id_idx ON tasks (server_id);
CREATE INDEX tasks_status_idx ON tasks (status) WHERE status = 'pending';

-- CypherPanel initial schema: identity, servers, packages, accounts, audit.
-- Roles per plan.md Section 6; audit trail is Phase 1 scope, not an add-on.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL UNIQUE,
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('root_admin', 'reseller', 'end_user')),
    -- For end users created by a reseller: which reseller's pool they belong to.
    reseller_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    suspended_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX users_reseller_id_idx ON users (reseller_id) WHERE reseller_id IS NOT NULL;

CREATE TABLE servers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    hostname     text NOT NULL,
    ip_address   inet NOT NULL,
    agent_status text NOT NULL DEFAULT 'unregistered'
                 CHECK (agent_status IN ('unregistered', 'online', 'offline', 'error')),
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE packages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    -- Owner is a root admin or a reseller carving up their pool.
    owner_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Resource limits (disk_mb, bandwidth_mb, domains, databases,
    -- email_accounts, cpu_quota_pct, memory_max_mb, ...) as a validated
    -- document: limit kinds will grow and are read as a unit at enforcement.
    limits     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, name)
);

CREATE TABLE accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    server_id       uuid NOT NULL REFERENCES servers (id) ON DELETE RESTRICT,
    package_id      uuid NOT NULL REFERENCES packages (id) ON DELETE RESTRICT,
    -- The Linux system user on the target server (e.g. cyph_a1b2c3).
    system_username text NOT NULL,
    primary_domain  text NOT NULL UNIQUE,
    status          text NOT NULL DEFAULT 'provisioning'
                    CHECK (status IN ('provisioning', 'active', 'suspended', 'terminating', 'failed')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, system_username)
);

CREATE INDEX accounts_user_id_idx ON accounts (user_id);
CREATE INDEX accounts_server_id_idx ON accounts (server_id);

CREATE TABLE audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id    uuid,
    actor_role  text,
    action      text NOT NULL,
    target_type text NOT NULL,
    target_id   text NOT NULL DEFAULT '',
    detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip_address  inet,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_created_at_idx ON audit_log (created_at);
CREATE INDEX audit_log_actor_id_idx ON audit_log (actor_id);

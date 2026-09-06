-- +goose Up
-- V1.x, audit-log.md §2: the audit log — one immutable row per sensitive
-- action, queryable per team. It is the record the threat model leans on
-- (§5.1 "full audit log of every desired-state mutation, so a compromise is
-- reconstructable"), the evidence behind the destructive-confirm dialog's
-- promise that an action is "audit-logged with your name", and the answer to
-- "who deleted this?" after the resource is gone.
--
-- Two deliberate schema decisions, both load-bearing:
--
--   NO FOREIGN KEYS. An audit row must outlive everything it describes. A
--   CASCADE would delete exactly the evidence of a deletion, and a SET NULL
--   would erase the team scope that authorizes the read — so a member who
--   deleted a project would make their own action invisible. Every id column
--   here is therefore a plain snapshot of an identifier that was real when the
--   action happened; a dangling one means the resource is gone, which is
--   information, not corruption.
--
--   SNAPSHOT LABELS. actor_label and resource_name are copied at write time and
--   never updated. Renaming an application must not rewrite history, and an
--   entry stays attributable by name after the account or resource is deleted
--   (canvas 14k: "audit entries stay").
--
-- Additive and reversible (ENGINEERING rule 16): one new table, nothing else
-- touched.

CREATE TABLE audit_events (
    id TEXT NOT NULL PRIMARY KEY,                    -- aud_… prefix
    at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The dotted verb, from the closed vocabulary in core/audit (audit-log.md
    -- §3). Stored as text rather than an enum so a new verb is a code change,
    -- not a migration — the vocabulary is documented and validated in Go.
    action TEXT NOT NULL,

    -- outcome is 'success' or 'failure'. A refused action is as much a
    -- first-class row as a completed one (canvas 13t: "every failure is in the
    -- audit log"), which is why failure is an OUTCOME rather than a separate
    -- verb: "show me everything that failed" stays one predicate.
    outcome TEXT NOT NULL DEFAULT 'success',

    -- Actor. kind ∈ user | token | agent | system | anonymous.
    -- actor_user_id/actor_token_id are snapshots, never foreign keys (above);
    -- actor_label is the email (or the agent/system description) as it read at
    -- the time.
    actor_kind     TEXT NOT NULL,
    actor_user_id  TEXT,
    actor_token_id TEXT,
    actor_label    TEXT NOT NULL DEFAULT '',

    -- Resource. kind is the glossary noun ('application', 'server', …); id and
    -- name are snapshots of the thing acted on.
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL DEFAULT '',
    resource_name TEXT NOT NULL DEFAULT '',

    -- Scope, resolved at INSERT time from whichever link the caller knew
    -- (audit.sql). NULL team_id means the action was panel-level — a server, a
    -- user, the mail or DNS settings — and only panel admins may read it.
    team_id        TEXT,
    project_id     TEXT,
    environment_id TEXT,

    -- Structured extras. Keys and identifiers only, never a secret VALUE
    -- (audit-log.md §6); core/audit strips a small denylist of key names as a
    -- second line of defence and bounds the encoded size.
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Where the action came from: the response's X-Request-Id (so a support
    -- conversation about one trace id finds the action it performed) and the
    -- client address the panel attributed the request to.
    trace_id  TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT ''
);

-- The page is always newest-first on (at, id), and the retention purge walks
-- the same order from the other end, so one index serves both.
CREATE INDEX idx_audit_events_at ON audit_events (at DESC, id DESC);
-- The three filters that narrow before they sort.
CREATE INDEX idx_audit_events_team ON audit_events (team_id, at DESC);
CREATE INDEX idx_audit_events_project ON audit_events (project_id, at DESC);
CREATE INDEX idx_audit_events_actor ON audit_events (actor_user_id, at DESC);
-- "Everything that ever happened to this resource" — the resource timeline.
CREATE INDEX idx_audit_events_resource ON audit_events (resource_id, at DESC);
-- action is filtered exactly ('deploy.started') or by family prefix
-- ('deploy.%'), which a plain btree on text serves with the default collation
-- only for equality; text_pattern_ops makes the prefix form an index scan too.
CREATE INDEX idx_audit_events_action ON audit_events (action text_pattern_ops, at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_events;

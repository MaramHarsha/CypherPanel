-- Threshold alerts (threshold-alerts.md §2).
--
-- NO NAME COLUMN. The sentence names the rule, rendered from the row, so the
-- list, the modal, the Discord message and the API say the same words and no
-- label typed in March drifts from what the rule now does. The unique
-- constraint does the second job: it stops the double-add that would silently
-- deliver every alert twice. Two rules differing only in notifier are legal and
-- useful — page on-call, post to a status channel — which is why notifier_id
-- sits inside it.

-- +goose Up
CREATE TABLE alert_rules (
    id             TEXT PRIMARY KEY,
    -- Polymorphic, so no foreign key to point at. Unlike an audit event, a rule
    -- is CONFIGURATION rather than evidence: configuration for a deleted
    -- application is noise, so the deletion path drops its rules and the audit
    -- log keeps the record that it happened.
    target_kind    TEXT NOT NULL,
    target_id      TEXT NOT NULL,
    signal         TEXT NOT NULL,
    threshold      DOUBLE PRECISION NOT NULL,
    -- Stored, not derived: 90 is percent for a server's memory and bytes for an
    -- application's disk, and a rule must never be reinterpreted in a unit it
    -- was not written in because its target's config changed later.
    threshold_unit TEXT NOT NULL,
    window_seconds INTEGER NOT NULL,
    -- RESTRICT, so a notifier cannot vanish and leave a rule silently
    -- undeliverable — a smoke detector whose battery someone removed in another
    -- room. The 409 names the rules.
    notifier_id    TEXT NOT NULL REFERENCES notifiers(id) ON DELETE RESTRICT,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    -- All four states are visible, including the two that deliver nothing: a
    -- rule quiet for a bad reason must not look like one quiet for a good
    -- reason (ui-principles §10).
    state          TEXT NOT NULL DEFAULT 'no_data',
    state_since    TIMESTAMPTZ NOT NULL DEFAULT now(),
    rearm_until    TIMESTAMPTZ,
    -- When the no_data / flapping inbox item was last written, so it is written
    -- once on the transition rather than every tick.
    quiet_notified_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (target_kind, target_id, signal, threshold, window_seconds, notifier_id)
);

CREATE INDEX idx_alert_rules_target ON alert_rules(target_kind, target_id);

-- One row per EPISODE, not per evaluation. CASCADE because it is the rule's own
-- history rather than a record of what a principal did.
CREATE TABLE alert_events (
    id          TEXT PRIMARY KEY,
    rule_id     TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    peak_value  DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- False when the flap guard held it: the episode still happened and is
    -- still counted, it just was not delivered.
    delivered   BOOLEAN NOT NULL DEFAULT true
);

CREATE INDEX idx_alert_events_rule ON alert_events(rule_id, started_at DESC);

-- +goose Down
DROP TABLE alert_events;
DROP TABLE alert_rules;

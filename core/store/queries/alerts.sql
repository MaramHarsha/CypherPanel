-- name: CreateAlertRule :one
INSERT INTO alert_rules (id, target_kind, target_id, signal, threshold, threshold_unit, window_seconds, notifier_id, enabled, state_since)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
RETURNING *;

-- name: GetAlertRule :one
SELECT * FROM alert_rules WHERE id = $1;

-- name: ListAlertRules :many
SELECT * FROM alert_rules ORDER BY created_at DESC;

-- name: ListAlertRulesForTarget :many
SELECT * FROM alert_rules WHERE target_kind = $1 AND target_id = $2 ORDER BY created_at DESC;

-- name: ListEnabledAlertRules :many
SELECT * FROM alert_rules WHERE enabled = true;

-- name: SetAlertRuleEnabled :one
UPDATE alert_rules SET enabled = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetAlertRuleState :exec
UPDATE alert_rules
SET state = $2, state_since = $3, rearm_until = $4, quiet_notified_at = $5, updated_at = now()
WHERE id = $1;

-- name: DeleteAlertRule :exec
DELETE FROM alert_rules WHERE id = $1;

-- Rules for a resource being deleted go with it: configuration for a resource
-- that no longer exists is noise, and the audit log keeps the record.
-- name: DeleteAlertRulesForTarget :exec
DELETE FROM alert_rules WHERE target_kind = $1 AND target_id = $2;

-- name: CountAlertRulesByNotifier :one
SELECT COUNT(*)::BIGINT FROM alert_rules WHERE notifier_id = $1;

-- name: OpenAlertEvent :one
INSERT INTO alert_events (id, rule_id, started_at, peak_value, delivered)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ResolveAlertEvent :exec
UPDATE alert_events SET resolved_at = $2 WHERE id = $1 AND resolved_at IS NULL;

-- name: UpdateAlertEventPeak :exec
UPDATE alert_events SET peak_value = GREATEST(peak_value, $2) WHERE id = $1 AND resolved_at IS NULL;

-- name: GetOpenAlertEvent :one
SELECT * FROM alert_events WHERE rule_id = $1 AND resolved_at IS NULL ORDER BY started_at DESC LIMIT 1;

-- name: ListAlertEvents :many
SELECT * FROM alert_events WHERE rule_id = $1 ORDER BY started_at DESC LIMIT $2;

-- The flap guard counts EPISODES in a rolling window, delivered or not.
-- name: CountAlertEpisodesSince :one
SELECT COUNT(*)::BIGINT FROM alert_events WHERE rule_id = $1 AND started_at >= $2;

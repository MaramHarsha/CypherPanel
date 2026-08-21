-- Outbound webhooks (outbound-webhooks.md §2, §4): endpoints, deliveries, and
-- the per-attempt record.

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (
    id, project_id, url, secret_ct, secret_nonce, events, enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: GetWebhookEndpoint :one
SELECT * FROM webhook_endpoints WHERE id = $1;

-- name: ListWebhookEndpointsByProject :many
SELECT * FROM webhook_endpoints WHERE project_id = $1 ORDER BY created_at DESC;

-- name: ListEnabledWebhookEndpointsForEvent :many
SELECT * FROM webhook_endpoints
WHERE project_id = @project_id AND enabled = true AND @event_type::text = ANY(events)
ORDER BY created_at;

-- name: UpdateWebhookEndpoint :one
UPDATE webhook_endpoints
SET url = $2, events = $3, enabled = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RotateWebhookEndpointSecret :one
UPDATE webhook_endpoints
SET secret_ct = $2, secret_nonce = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteWebhookEndpoint :exec
DELETE FROM webhook_endpoints WHERE id = $1;

-- name: CreateWebhookDelivery :one
INSERT INTO webhook_deliveries (
    id, endpoint_id, event_type, resource_kind, resource_id, resource_name,
    payload, status, attempt, next_attempt_at, redelivery_of
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;

-- name: UpdateWebhookDeliveryProgress :one
UPDATE webhook_deliveries
SET status = $2, attempt = $3, next_attempt_at = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListWebhookDeliveriesByEndpoint :many
SELECT d.* FROM webhook_deliveries d
WHERE d.endpoint_id = $1
ORDER BY d.created_at DESC, d.id DESC
LIMIT $2;

-- ListWebhookDeliveriesBefore is the seek page (outbound-webhooks.md §7):
-- everything strictly older than the cursor row on (created_at, id) DESC. An
-- unknown or already-pruned cursor makes the row comparison NULL, so the page
-- comes back empty rather than restarting at the newest row.
-- name: ListWebhookDeliveriesBefore :many
SELECT d.* FROM webhook_deliveries d
WHERE d.endpoint_id = $1
  AND (d.created_at, d.id) < (
      SELECT c.created_at, c.id FROM webhook_deliveries c WHERE c.id = $2
  )
ORDER BY d.created_at DESC, d.id DESC
LIMIT $3;

-- name: ListDueWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE status = 'pending' AND next_attempt_at IS NOT NULL AND next_attempt_at <= $1
ORDER BY next_attempt_at
LIMIT $2;

-- ListRecentTerminalWebhookDeliveryStatuses feeds the derived Endpoint Health
-- (outbound-webhooks.md §4) — newest first, pending rows excluded.
-- name: ListRecentTerminalWebhookDeliveryStatuses :many
SELECT status FROM webhook_deliveries
WHERE endpoint_id = $1 AND status <> 'pending'
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: LastWebhookDeliveryAt :one
SELECT created_at FROM webhook_deliveries
WHERE endpoint_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- DeleteOldWebhookDeliveries prunes the delivery log to the most recent keep
-- rows per endpoint, attempts cascading (outbound-webhooks.md §6).
-- name: DeleteOldWebhookDeliveries :exec
DELETE FROM webhook_deliveries d
WHERE d.endpoint_id = $1
  AND d.id NOT IN (
      SELECT keep.id FROM webhook_deliveries keep
      WHERE keep.endpoint_id = $1
      ORDER BY keep.created_at DESC, keep.id DESC
      LIMIT $2
  );

-- name: CreateWebhookDeliveryAttempt :one
INSERT INTO webhook_delivery_attempts (
    delivery_id, attempt, response_status, duration_ms, error
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: ListWebhookDeliveryAttempts :many
SELECT * FROM webhook_delivery_attempts WHERE delivery_id = $1 ORDER BY attempt;

-- ListLatestWebhookDeliveryAttempts returns the most recent attempt for each of
-- the given deliveries — what the feed reads its "200 - 84ms" from
-- (outbound-webhooks.md section 7). It is a second query rather than a join on
-- the paging queries so every column keeps the nullability its own table
-- declares: a delivery with no attempt yet is simply absent from the result.
-- name: ListLatestWebhookDeliveryAttempts :many
SELECT DISTINCT ON (delivery_id) *
FROM webhook_delivery_attempts
WHERE delivery_id = ANY(@delivery_ids::text[])
ORDER BY delivery_id, attempt DESC;

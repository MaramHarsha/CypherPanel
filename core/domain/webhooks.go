package domain

import "time"

// Outbound webhooks (outbound-webhooks.md): the machine-facing twin of a
// Notifier. `core/notify` talks to people; `core/webhooks` talks to machines.
// Every constant below is a stable wire/DB value.

// EventWebhookPing is delivery-only (outbound-webhooks.md §3): the ping route
// sends a real, signed, logged delivery carrying it, but it is not a member of
// EventTypes() and is rejected inside an endpoint's `events` array. An endpoint
// always receives its own pings regardless of subscription — that is what makes
// ping usable as the setup check.
const EventWebhookPing = "webhook.ping"

// Resource kinds a delivery can describe (outbound-webhooks.md §2).
const (
	WebhookResourceApplication = "application"
	WebhookResourceDatabase    = "database"
)

// Delivery lifecycle (outbound-webhooks.md §2). A delivery is pending while
// attempts remain, then terminal either way.
const (
	DeliveryPending   = "pending"
	DeliverySucceeded = "succeeded"
	DeliveryFailed    = "failed"
)

// Endpoint Health is DERIVED from recent terminal deliveries, never stored
// (outbound-webhooks.md §4). It is a deliberately tiny vocabulary distinct from
// ui-principles §5's workload statuses — an endpoint is not a workload, so
// calling a URL "running" would be worse copy, not better consistency.
const (
	// EndpointHealthy — all of the ten most recent terminal deliveries succeeded.
	EndpointHealthy = "healthy"
	// EndpointDegraded — the most recent succeeded, but at least one of the ten failed.
	EndpointDegraded = "degraded"
	// EndpointFailing — the most recent delivery exhausted its attempts.
	EndpointFailing = "failing"
	// EndpointHealthUnknown — disabled, or no terminal delivery yet. Never fake
	// certainty (ui-principles §5).
	EndpointHealthUnknown = "unknown"
)

// WebhookEndpoint is a declarative "on these events, POST signed JSON to this
// URL" row, scoped to a Project exactly like a Notifier. There is deliberately
// no name: the URL is the identity the operator debugs with, and
// UNIQUE (project_id, url) stops the double-add that would double every
// delivery (outbound-webhooks.md §2).
//
// The signing secret is SEALED, not hashed — the plane must reproduce the HMAC,
// so it is unsealed only at signing time and never returned in a response
// (ENGINEERING rule 20).
type WebhookEndpoint struct {
	ID          string
	ProjectID   string
	URL         string
	SecretCT    []byte
	SecretNonce []byte
	Events      []string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WebhookDelivery is one event aimed at one endpoint. Its ID is the value of
// the X-CypherPanel-Delivery header, stable across the retries of one delivery
// and fresh on a redelivery (outbound-webhooks.md §4).
//
// ResourceID is deliberately NOT a foreign key: the log is evidence and must
// outlive the resource it describes. ResourceName is denormalised so the feed
// still reads `backup.failed · atlas-pg` after the database is gone. Payload is
// the exact signed body bytes — TEXT, not JSONB, because the HMAC covers raw
// bytes and a JSONB round-trip can reorder keys (§2).
type WebhookDelivery struct {
	ID           string
	EndpointID   string
	EventType    string
	ResourceKind string
	ResourceID   string
	ResourceName string
	Payload      string
	Status       string
	// Attempt counts attempts made so far — the board's "retrying ×3".
	Attempt int
	// NextAttemptAt drives the retry sweeper; NULL once terminal.
	NextAttemptAt *time.Time
	// RedeliveryOf names the original delivery when this row is a replay.
	RedeliveryOf *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebhookDeliveryAttempt is one HTTP request made for a delivery. It has a
// composite primary key (delivery_id, attempt) and no minted id, which makes an
// attempt insert idempotent under redelivery: a duplicate lands as a conflict
// the manager treats as "already recorded" (ENGINEERING rule 12).
//
// A response body is never stored (outbound-webhooks.md §6) — a receiver's
// error page can carry its own secrets and has no size bound we control.
type WebhookDeliveryAttempt struct {
	DeliveryID string
	Attempt    int
	// ResponseStatus is nil on a transport error (nothing answered).
	ResponseStatus *int
	DurationMS     int
	// Error is a redacted transport error, never a response body.
	Error string
	At    time.Time
}

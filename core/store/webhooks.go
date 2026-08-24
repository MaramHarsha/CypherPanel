package store

// Outbound webhook persistence (outbound-webhooks.md §2). Domain types in,
// domain types out; pgx/pgtype stays inside this package.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── Endpoints ──────────────────────────────────────────────────────────────

// CreateWebhookEndpoint inserts an endpoint row. A second endpoint on the same
// URL in the same project is a conflict, not a silent doubling of every
// delivery (spec §2).
func (s *Store) CreateWebhookEndpoint(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	row, err := s.q.CreateWebhookEndpoint(ctx, db.CreateWebhookEndpointParams{
		ID:          e.ID,
		ProjectID:   e.ProjectID,
		Url:         e.URL,
		SecretCt:    e.SecretCT,
		SecretNonce: e.SecretNonce,
		Events:      e.Events,
		Enabled:     e.Enabled,
	})
	if err != nil {
		return domain.WebhookEndpoint{}, wrapCreate("creating webhook endpoint", err)
	}
	return webhookEndpointFromRow(row), nil
}

func (s *Store) GetWebhookEndpoint(ctx context.Context, id string) (domain.WebhookEndpoint, error) {
	row, err := s.q.GetWebhookEndpoint(ctx, id)
	if err != nil {
		return domain.WebhookEndpoint{}, wrap("getting webhook endpoint", err)
	}
	return webhookEndpointFromRow(row), nil
}

func (s *Store) ListWebhookEndpointsByProject(ctx context.Context, projectID string) ([]domain.WebhookEndpoint, error) {
	rows, err := s.q.ListWebhookEndpointsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook endpoints: %w", err)
	}
	return webhookEndpointsFromRows(rows), nil
}

// ListEnabledWebhookEndpointsForEvent returns the enabled endpoints in a
// project subscribed to eventType — the fan-out's input (spec §4).
func (s *Store) ListEnabledWebhookEndpointsForEvent(ctx context.Context, projectID, eventType string) ([]domain.WebhookEndpoint, error) {
	rows, err := s.q.ListEnabledWebhookEndpointsForEvent(ctx, db.ListEnabledWebhookEndpointsForEventParams{
		ProjectID: projectID,
		EventType: eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook endpoints for event: %w", err)
	}
	return webhookEndpointsFromRows(rows), nil
}

func (s *Store) UpdateWebhookEndpoint(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	row, err := s.q.UpdateWebhookEndpoint(ctx, db.UpdateWebhookEndpointParams{
		ID:      e.ID,
		Url:     e.URL,
		Events:  e.Events,
		Enabled: e.Enabled,
	})
	if err != nil {
		return domain.WebhookEndpoint{}, wrapUpdate("updating webhook endpoint", err)
	}
	return webhookEndpointFromRow(row), nil
}

// RotateWebhookEndpointSecret replaces the sealed signing secret in place. There
// is no overlap window: the previous secret stops verifying immediately (§7).
func (s *Store) RotateWebhookEndpointSecret(ctx context.Context, id string, ct, nonce []byte) (domain.WebhookEndpoint, error) {
	row, err := s.q.RotateWebhookEndpointSecret(ctx, db.RotateWebhookEndpointSecretParams{
		ID:          id,
		SecretCt:    ct,
		SecretNonce: nonce,
	})
	if err != nil {
		return domain.WebhookEndpoint{}, wrapUpdate("rotating webhook endpoint secret", err)
	}
	return webhookEndpointFromRow(row), nil
}

func (s *Store) DeleteWebhookEndpoint(ctx context.Context, id string) error {
	if err := s.q.DeleteWebhookEndpoint(ctx, id); err != nil {
		return wrapDelete("deleting webhook endpoint", err)
	}
	return nil
}

// ─── Deliveries ─────────────────────────────────────────────────────────────

// CreateWebhookDelivery records a delivery before its first attempt runs, so a
// plane restart mid-backoff loses nothing (ENGINEERING rule 15).
func (s *Store) CreateWebhookDelivery(ctx context.Context, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	row, err := s.q.CreateWebhookDelivery(ctx, db.CreateWebhookDeliveryParams{
		ID:            d.ID,
		EndpointID:    d.EndpointID,
		EventType:     d.EventType,
		ResourceKind:  d.ResourceKind,
		ResourceID:    d.ResourceID,
		ResourceName:  d.ResourceName,
		Payload:       d.Payload,
		Status:        d.Status,
		Attempt:       int32(d.Attempt),
		NextAttemptAt: tsFromPtr(d.NextAttemptAt),
		RedeliveryOf:  textFromPtr(d.RedeliveryOf),
	})
	if err != nil {
		return domain.WebhookDelivery{}, wrapCreate("creating webhook delivery", err)
	}
	return webhookDeliveryFromRow(row), nil
}

func (s *Store) GetWebhookDelivery(ctx context.Context, id string) (domain.WebhookDelivery, error) {
	row, err := s.q.GetWebhookDelivery(ctx, id)
	if err != nil {
		return domain.WebhookDelivery{}, wrap("getting webhook delivery", err)
	}
	return webhookDeliveryFromRow(row), nil
}

// UpdateWebhookDeliveryProgress advances a delivery's status, attempt count and
// next-attempt time. nextAttemptAt is nil once the delivery is terminal.
//
// fromAttempt is the attempt count the caller read before it started work; the
// write only lands if the row still holds it. A caller that loses the race gets
// ErrNotFound, which means "another worker already advanced this delivery",
// never "no such delivery".
func (s *Store) UpdateWebhookDeliveryProgress(ctx context.Context, id, status string, fromAttempt, attempt int, nextAttemptAt *time.Time) (domain.WebhookDelivery, error) {
	row, err := s.q.UpdateWebhookDeliveryProgress(ctx, db.UpdateWebhookDeliveryProgressParams{
		ID:            id,
		Status:        status,
		Attempt:       int32(attempt),
		NextAttemptAt: tsFromPtr(nextAttemptAt),
		FromAttempt:   int32(fromAttempt),
	})
	if err != nil {
		return domain.WebhookDelivery{}, wrapUpdate("updating webhook delivery", err)
	}
	return webhookDeliveryFromRow(row), nil
}

// ListWebhookDeliveriesByEndpoint returns the newest limit deliveries — the
// first seek page (spec §7).
func (s *Store) ListWebhookDeliveriesByEndpoint(ctx context.Context, endpointID string, limit int32) ([]domain.WebhookDelivery, error) {
	rows, err := s.q.ListWebhookDeliveriesByEndpoint(ctx, db.ListWebhookDeliveriesByEndpointParams{
		EndpointID: endpointID,
		Limit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook deliveries: %w", err)
	}
	return webhookDeliveriesFromRows(rows), nil
}

// ListWebhookDeliveriesBefore returns the page strictly older than the cursor
// delivery on (created_at, id) DESC. A cursor that no longer exists (pruned)
// yields an empty page rather than restarting at the newest row.
func (s *Store) ListWebhookDeliveriesBefore(ctx context.Context, endpointID, before string, limit int32) ([]domain.WebhookDelivery, error) {
	rows, err := s.q.ListWebhookDeliveriesBefore(ctx, db.ListWebhookDeliveriesBeforeParams{
		EndpointID: endpointID,
		ID:         before,
		Limit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook deliveries before cursor: %w", err)
	}
	return webhookDeliveriesFromRows(rows), nil
}

// ListDueWebhookDeliveries returns pending deliveries whose backoff has
// elapsed — the retry sweeper's input (spec §4).
func (s *Store) ListDueWebhookDeliveries(ctx context.Context, now time.Time, limit int32) ([]domain.WebhookDelivery, error) {
	rows, err := s.q.ListDueWebhookDeliveries(ctx, db.ListDueWebhookDeliveriesParams{
		NextAttemptAt: tsFromTime(now),
		Limit:         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing due webhook deliveries: %w", err)
	}
	return webhookDeliveriesFromRows(rows), nil
}

// ListRecentTerminalWebhookDeliveryStatuses returns the newest terminal
// statuses for an endpoint, newest first — the input to derived Endpoint
// Health (spec §4).
func (s *Store) ListRecentTerminalWebhookDeliveryStatuses(ctx context.Context, endpointID string, limit int32) ([]string, error) {
	out, err := s.q.ListRecentTerminalWebhookDeliveryStatuses(ctx, db.ListRecentTerminalWebhookDeliveryStatusesParams{
		EndpointID: endpointID,
		Limit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing recent webhook delivery statuses: %w", err)
	}
	return out, nil
}

// LastWebhookDeliveryAt returns when the endpoint last had a delivery created,
// or nil when it has never had one.
func (s *Store) LastWebhookDeliveryAt(ctx context.Context, endpointID string) (*time.Time, error) {
	ts, err := s.q.LastWebhookDeliveryAt(ctx, endpointID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: getting last webhook delivery time: %w", err)
	}
	return ptrTime(ts), nil
}

// DeleteOldWebhookDeliveries prunes an endpoint's delivery log to the most
// recent keep rows; attempts cascade (spec §6).
func (s *Store) DeleteOldWebhookDeliveries(ctx context.Context, endpointID string, keep int32) error {
	if err := s.q.DeleteOldWebhookDeliveries(ctx, db.DeleteOldWebhookDeliveriesParams{
		EndpointID: endpointID,
		Limit:      keep,
	}); err != nil {
		return wrapDelete("pruning webhook deliveries", err)
	}
	return nil
}

// ─── Attempts ───────────────────────────────────────────────────────────────

// CreateWebhookDeliveryAttempt records one HTTP request. The composite key
// makes it idempotent: a redelivered attempt number comes back as ErrConflict,
// which the caller reads as "already recorded" (ENGINEERING rule 12).
func (s *Store) CreateWebhookDeliveryAttempt(ctx context.Context, a domain.WebhookDeliveryAttempt) (domain.WebhookDeliveryAttempt, error) {
	row, err := s.q.CreateWebhookDeliveryAttempt(ctx, db.CreateWebhookDeliveryAttemptParams{
		DeliveryID:     a.DeliveryID,
		Attempt:        int32(a.Attempt),
		ResponseStatus: int4FromPtr(a.ResponseStatus),
		DurationMs:     int32(a.DurationMS),
		Error:          a.Error,
	})
	if err != nil {
		return domain.WebhookDeliveryAttempt{}, wrapCreate("creating webhook delivery attempt", err)
	}
	return webhookAttemptFromRow(row), nil
}

func (s *Store) ListWebhookDeliveryAttempts(ctx context.Context, deliveryID string) ([]domain.WebhookDeliveryAttempt, error) {
	rows, err := s.q.ListWebhookDeliveryAttempts(ctx, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook delivery attempts: %w", err)
	}
	return webhookAttemptsFromRows(rows), nil
}

// ListLatestWebhookDeliveryAttempts returns the most recent attempt for each of
// deliveryIDs — the feed's "200 · 84ms". Deliveries with no attempt yet are
// simply absent from the result.
func (s *Store) ListLatestWebhookDeliveryAttempts(ctx context.Context, deliveryIDs []string) ([]domain.WebhookDeliveryAttempt, error) {
	if len(deliveryIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListLatestWebhookDeliveryAttempts(ctx, deliveryIDs)
	if err != nil {
		return nil, fmt.Errorf("store: listing latest webhook delivery attempts: %w", err)
	}
	return webhookAttemptsFromRows(rows), nil
}

// ─── row mappers ────────────────────────────────────────────────────────────

func webhookEndpointsFromRows(rows []db.WebhookEndpoint) []domain.WebhookEndpoint {
	out := make([]domain.WebhookEndpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhookEndpointFromRow(r))
	}
	return out
}

func webhookEndpointFromRow(r db.WebhookEndpoint) domain.WebhookEndpoint {
	return domain.WebhookEndpoint{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		URL:         r.Url,
		SecretCT:    r.SecretCt,
		SecretNonce: r.SecretNonce,
		Events:      r.Events,
		Enabled:     r.Enabled,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}

func webhookDeliveriesFromRows(rows []db.WebhookDelivery) []domain.WebhookDelivery {
	out := make([]domain.WebhookDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhookDeliveryFromRow(r))
	}
	return out
}

func webhookDeliveryFromRow(r db.WebhookDelivery) domain.WebhookDelivery {
	return domain.WebhookDelivery{
		ID:            r.ID,
		EndpointID:    r.EndpointID,
		EventType:     r.EventType,
		ResourceKind:  r.ResourceKind,
		ResourceID:    r.ResourceID,
		ResourceName:  r.ResourceName,
		Payload:       r.Payload,
		Status:        r.Status,
		Attempt:       int(r.Attempt),
		NextAttemptAt: ptrTime(r.NextAttemptAt),
		RedeliveryOf:  ptrFromText(r.RedeliveryOf),
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

func webhookAttemptsFromRows(rows []db.WebhookDeliveryAttempt) []domain.WebhookDeliveryAttempt {
	out := make([]domain.WebhookDeliveryAttempt, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhookAttemptFromRow(r))
	}
	return out
}

func webhookAttemptFromRow(r db.WebhookDeliveryAttempt) domain.WebhookDeliveryAttempt {
	return domain.WebhookDeliveryAttempt{
		DeliveryID:     r.DeliveryID,
		Attempt:        int(r.Attempt),
		ResponseStatus: ptrFromInt4(r.ResponseStatus),
		DurationMS:     int(r.DurationMs),
		Error:          r.Error,
		At:             r.At.Time,
	}
}

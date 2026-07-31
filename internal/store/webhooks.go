package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Webhook is an operator-registered HTTP endpoint that receives domain events.
// SecretEncrypted is the AES-GCM HMAC key; it is never serialised to an API
// response after creation.
type Webhook struct {
	ID              string
	Name            string
	URL             string
	SecretEncrypted []byte
	Events          []string
	Active          bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// WebhookDelivery is one attempt-tracked delivery of one event to one endpoint.
type WebhookDelivery struct {
	ID             string
	WebhookID      string
	WebhookName    string // joined for the delivery log
	EventID        string
	Subject        string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	ResponseStatus int
	Error          string
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	DeliveredAt    *time.Time
}

type Webhooks struct{ pool *pgxpool.Pool }

func NewWebhooks(pool *pgxpool.Pool) *Webhooks { return &Webhooks{pool: pool} }

const webhookColumns = `id, name, url, secret_encrypted, events, active, created_at, updated_at`

func scanWebhook(row pgx.Row) (*Webhook, error) {
	var w Webhook
	err := row.Scan(&w.ID, &w.Name, &w.URL, &w.SecretEncrypted, &w.Events, &w.Active, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning webhook: %w", err)
	}
	return &w, nil
}

func (s *Webhooks) Create(ctx context.Context, name, url string, secret []byte, evts []string) (*Webhook, error) {
	if evts == nil {
		evts = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO webhooks (name, url, secret_encrypted, events)
		VALUES ($1, $2, $3, $4)
		RETURNING `+webhookColumns, name, url, secret, evts)
	return scanWebhook(row)
}

func (s *Webhooks) List(ctx context.Context) ([]Webhook, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookColumns+` FROM webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: listing webhooks: %w", err)
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ListActive returns only enabled endpoints — the set an incoming event fans
// out to.
func (s *Webhooks) ListActive(ctx context.Context) ([]Webhook, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE active ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: listing active webhooks: %w", err)
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Webhooks) GetByID(ctx context.Context, id string) (*Webhook, error) {
	return scanWebhook(s.pool.QueryRow(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE id = $1`, id))
}

func (s *Webhooks) SetActive(ctx context.Context, id string, active bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE webhooks SET active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("store: setting webhook active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Webhooks) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhooks WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: deleting webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnqueueDelivery records an intent to deliver one event to one endpoint.
//
// ON CONFLICT DO NOTHING makes this idempotent against JetStream redelivery:
// the (webhook_id, event_id) unique constraint means a replayed event can
// never produce a second delivery.
func (s *Webhooks) EnqueueDelivery(ctx context.Context, webhookID, eventID, subject string, payload []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (webhook_id, event_id, subject, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (webhook_id, event_id) DO NOTHING`,
		webhookID, eventID, subject, payload)
	if err != nil {
		return fmt.Errorf("store: enqueuing webhook delivery: %w", err)
	}
	return nil
}

const deliveryColumns = `d.id, d.webhook_id, w.name, d.event_id, d.subject, d.payload,
	d.status, d.attempts, d.response_status, d.error, d.next_attempt_at, d.created_at, d.delivered_at`

func scanDelivery(row pgx.Row) (*WebhookDelivery, error) {
	var d WebhookDelivery
	err := row.Scan(&d.ID, &d.WebhookID, &d.WebhookName, &d.EventID, &d.Subject, &d.Payload,
		&d.Status, &d.Attempts, &d.ResponseStatus, &d.Error, &d.NextAttemptAt, &d.CreatedAt, &d.DeliveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning webhook delivery: %w", err)
	}
	return &d, nil
}

// ClaimDueDeliveries atomically marks up to limit due deliveries as in-flight
// and returns them.
//
// FOR UPDATE SKIP LOCKED plus the attempt bump in the same statement is what
// makes this safe with several Core instances running: two workers can never
// claim the same row, so an endpoint is not hit twice for one event.
func (s *Webhooks) ClaimDueDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM webhook_deliveries
			WHERE status IN ('pending', 'failed') AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE webhook_deliveries d
		SET attempts = d.attempts + 1, next_attempt_at = now() + interval '1 hour'
		FROM due, webhooks w
		WHERE d.id = due.id AND w.id = d.webhook_id
		RETURNING `+deliveryColumns, limit)
	if err != nil {
		return nil, fmt.Errorf("store: claiming webhook deliveries: %w", err)
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// MarkDelivered records a successful delivery.
func (s *Webhooks) MarkDelivered(ctx context.Context, id string, responseStatus int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = 'delivered', response_status = $2, error = '', delivered_at = now()
		WHERE id = $1`, id, responseStatus)
	if err != nil {
		return fmt.Errorf("store: marking webhook delivered: %w", err)
	}
	return nil
}

// MarkFailed schedules a retry, or dead-letters the delivery once it has
// exhausted its attempts.
func (s *Webhooks) MarkFailed(ctx context.Context, id string, responseStatus int, errMsg string, retryIn time.Duration, dead bool) error {
	status := "failed"
	if dead {
		status = "dead"
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = $2, response_status = $3, error = $4, next_attempt_at = now() + $5::interval
		WHERE id = $1`, id, status, responseStatus, errMsg, retryIn.String())
	if err != nil {
		return fmt.Errorf("store: marking webhook failed: %w", err)
	}
	return nil
}

// Redeliver resets a delivery so the worker picks it up on the next sweep.
// Used for the manual "redeliver" action on a dead or failed delivery.
func (s *Webhooks) Redeliver(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = 'pending', attempts = 0, error = '', next_attempt_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: redelivering webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDeliveries returns the delivery log, newest first, optionally filtered
// to one endpoint.
func (s *Webhooks) ListDeliveries(ctx context.Context, webhookID string, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryColumns+`
		FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id
		WHERE ($1 = '' OR d.webhook_id = NULLIF($1, '')::uuid)
		ORDER BY d.created_at DESC
		LIMIT $2`, webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing webhook deliveries: %w", err)
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Webhooks) GetDelivery(ctx context.Context, id string) (*WebhookDelivery, error) {
	return scanDelivery(s.pool.QueryRow(ctx, `
		SELECT `+deliveryColumns+`
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.id = $1`, id))
}

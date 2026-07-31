package webhooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// Store is the slice of the webhooks store the dispatcher needs, kept as an
// interface so the delivery loop is unit-testable without a database.
type Store interface {
	ListActive(ctx context.Context) ([]store.Webhook, error)
	EnqueueDelivery(ctx context.Context, webhookID, eventID, subject string, payload []byte) error
	ClaimDueDeliveries(ctx context.Context, limit int) ([]store.WebhookDelivery, error)
	MarkDelivered(ctx context.Context, id string, responseStatus int) error
	MarkFailed(ctx context.Context, id string, responseStatus int, errMsg string, retryIn time.Duration, dead bool) error
}

// Decryptor recovers a webhook's HMAC signing key from its stored ciphertext.
type Decryptor interface {
	Decrypt([]byte) ([]byte, error)
}

// Dispatcher fans domain events out to registered endpoints and owns the
// delivery retry loop.
type Dispatcher struct {
	Store  Store
	Crypt  Decryptor
	Client *http.Client
	// PollInterval is how often the worker sweeps for due deliveries.
	PollInterval time.Duration
	// Batch is how many deliveries one sweep claims.
	Batch int
	Log   *slog.Logger
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *Dispatcher) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	// A slow endpoint must not pin a worker: bound the whole request.
	return &http.Client{Timeout: 15 * time.Second}
}

// Consume runs the durable JetStream consumer that turns events into delivery
// rows. It acks as soon as the rows are recorded — the rows, not the stream,
// are the retry unit (one event fans out to many endpoints, and NAKing would
// re-deliver to endpoints that already succeeded).
func (d *Dispatcher) Consume(ctx context.Context, nc *nats.Conn) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("webhooks: jetstream context: %w", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, events.StreamName, jetstream.ConsumerConfig{
		Durable:       "webhook_dispatcher",
		FilterSubject: events.SubjectWildcard,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       time.Minute,
		MaxDeliver:    5,
	})
	if err != nil {
		return fmt.Errorf("webhooks: ensuring consumer: %w", err)
	}

	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("webhooks: opening message iterator: %w", err)
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	d.log().Info("webhook dispatcher consuming domain events")
	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("webhooks: message iterator: %w", err)
		}
		if err := d.fanOut(ctx, msg.Data()); err != nil {
			// Enqueueing failed (e.g. database blip): NAK so the event is
			// replayed rather than silently dropped.
			d.log().Error("webhooks: fanning out event", "error", err)
			_ = msg.Nak()
			continue
		}
		_ = msg.Ack()
	}
}

// fanOut records one delivery row per interested, active endpoint.
func (d *Dispatcher) fanOut(ctx context.Context, data []byte) error {
	evt, err := events.Decode(data)
	if err != nil {
		// A malformed event can never be delivered; drop it rather than
		// replay it forever.
		d.log().Error("webhooks: undecodable event", "error", err)
		return nil
	}
	hooks, err := d.Store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, h := range hooks {
		if !Wants(h.Events, evt.Subject) {
			continue
		}
		if err := d.Store.EnqueueDelivery(ctx, h.ID, evt.ID, evt.Subject, data); err != nil {
			return err
		}
	}
	return nil
}

// Run sweeps for due deliveries until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.PollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	d.log().Info("webhook delivery worker started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.log().Info("webhook delivery worker stopping")
			return
		case <-ticker.C:
			d.RunOnce(ctx)
		}
	}
}

// RunOnce claims and attempts one batch of due deliveries, returning how many
// succeeded.
func (d *Dispatcher) RunOnce(ctx context.Context) (delivered int) {
	batch := d.Batch
	if batch <= 0 {
		batch = 20
	}
	due, err := d.Store.ClaimDueDeliveries(ctx, batch)
	if err != nil {
		d.log().Error("webhooks: claiming deliveries", "error", err)
		return 0
	}
	for _, dl := range due {
		if d.attempt(ctx, dl) {
			delivered++
		}
	}
	return delivered
}

// attempt performs one HTTP delivery and records the outcome.
func (d *Dispatcher) attempt(ctx context.Context, dl store.WebhookDelivery) bool {
	hook, err := d.lookupSecret(ctx, dl.WebhookID)
	if err != nil {
		// Without the signing key the delivery can never be made correctly;
		// dead-letter it rather than hammer the endpoint with bad signatures.
		_ = d.Store.MarkFailed(ctx, dl.ID, 0, "signing key unavailable: "+err.Error(), time.Hour, true)
		return false
	}

	ts := time.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(dl.Payload))
	if err != nil {
		_ = d.Store.MarkFailed(ctx, dl.ID, 0, "building request: "+err.Error(), Backoff(dl.Attempts), true)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CypherPanel-Webhook/1")
	req.Header.Set(HeaderEvent, dl.Subject)
	req.Header.Set(HeaderDelivery, dl.ID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set(HeaderSignature, Sign(hook.Secret, dl.Payload, ts))

	resp, err := d.client().Do(req)
	if err != nil {
		d.recordFailure(ctx, dl, 0, err.Error())
		return false
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused, and keep any
	// error body for the operator without letting a hostile endpoint stream
	// unbounded data into the delivery log.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := d.Store.MarkDelivered(ctx, dl.ID, resp.StatusCode); err != nil {
			d.log().Error("webhooks: marking delivered", "delivery_id", dl.ID, "error", err)
		}
		return true
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	d.recordFailure(ctx, dl, resp.StatusCode, msg)
	return false
}

func (d *Dispatcher) recordFailure(ctx context.Context, dl store.WebhookDelivery, status int, msg string) {
	// dl.Attempts was already incremented by the claim, so it is this attempt's
	// number. Past MaxAttempts the endpoint is treated as down, not flaky.
	dead := dl.Attempts >= MaxAttempts
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := d.Store.MarkFailed(ctx, dl.ID, status, msg, Backoff(dl.Attempts), dead); err != nil {
		d.log().Error("webhooks: marking failed", "delivery_id", dl.ID, "error", err)
	}
	d.log().Warn("webhooks: delivery failed",
		"delivery_id", dl.ID, "webhook", dl.WebhookName, "attempt", dl.Attempts,
		"status", status, "dead", dead)
}

// resolvedHook is an endpoint with its signing key decrypted for one attempt.
type resolvedHook struct {
	URL    string
	Secret []byte
}

// lookupSecret finds the endpoint and decrypts its signing key. Secrets are
// resolved per attempt rather than cached so a rotated key takes effect
// immediately and plaintext keys are not held in memory between deliveries.
func (d *Dispatcher) lookupSecret(ctx context.Context, webhookID string) (resolvedHook, error) {
	hooks, err := d.Store.ListActive(ctx)
	if err != nil {
		return resolvedHook{}, err
	}
	for _, h := range hooks {
		if h.ID != webhookID {
			continue
		}
		secret, err := d.Crypt.Decrypt(h.SecretEncrypted)
		if err != nil {
			return resolvedHook{}, err
		}
		return resolvedHook{URL: h.URL, Secret: secret}, nil
	}
	return resolvedHook{}, fmt.Errorf("webhooks: endpoint %s is no longer active", webhookID)
}

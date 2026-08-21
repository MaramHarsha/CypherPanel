// Package webhooks turns the control plane's observed terminal state
// transitions (deploy succeeded/failed, backup succeeded/failed) into signed
// JSON POSTed to an operator's own systems — the machine-facing twin of
// core/notify (outbound-webhooks.md). It is a SIBLING of that package, not a
// fork: it reuses the event vocabulary (domain.EventTypes) and the scheduler's
// fan-out seam, and adds what machines want and people do not — a stable
// envelope, a per-endpoint HMAC, a delivery id, bounded retry, and a queryable
// record of every attempt (spec §1).
//
// Delivery is best-effort and detached: it MUST NEVER block or fail a deploy
// (spec §5).
package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Attempt policy (outbound-webhooks.md §4). Four attempts over a ~31-minute
// horizon: immediately, then +1 min, +5 min, +25 min (×5 backoff, ±20 %
// jitter). Three retries is the design board's "retrying ×3"; the jitter keeps
// many endpoints pointed at one recovering receiver from re-synchronising.
const (
	maxAttempts     = 4
	deliveryTimeout = 10 * time.Second
	// resolveTimeout bounds the project/environment/endpoint lookups that
	// precede a fan-out.
	resolveTimeout = 10 * time.Second
	// responseReadLimit is how much of a receiver's body we drain so the
	// connection can be reused. It is discarded, never stored (spec §6).
	responseReadLimit = 4 << 10
	// deliveryRetention is how many deliveries an endpoint keeps; older rows are
	// pruned on insert and their attempts cascade (spec §6).
	deliveryRetention = 200
	// sweepBatch bounds one retry sweep so a large backlog cannot monopolise a
	// tick.
	sweepBatch = 100
	// healthWindow is how many recent terminal deliveries Endpoint Health looks
	// at (spec §4).
	healthWindow = 10
	// jitterFraction is the ±spread applied to each backoff delay.
	jitterFraction = 0.2
)

// backoff is the delay before attempt N+1 after attempt N failed, indexed by
// the attempt that just failed minus one.
var backoff = [maxAttempts - 1]time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	25 * time.Minute,
}

// Store is the persistence the manager needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	ListEnabledWebhookEndpointsForEvent(ctx context.Context, projectID, eventType string) ([]domain.WebhookEndpoint, error)
	GetWebhookEndpoint(ctx context.Context, id string) (domain.WebhookEndpoint, error)
	CreateWebhookDelivery(ctx context.Context, d domain.WebhookDelivery) (domain.WebhookDelivery, error)
	UpdateWebhookDeliveryProgress(ctx context.Context, id, status string, fromAttempt, attempt int, nextAttemptAt *time.Time) (domain.WebhookDelivery, error)
	CreateWebhookDeliveryAttempt(ctx context.Context, a domain.WebhookDeliveryAttempt) (domain.WebhookDeliveryAttempt, error)
	ListDueWebhookDeliveries(ctx context.Context, now time.Time, limit int32) ([]domain.WebhookDelivery, error)
	DeleteOldWebhookDeliveries(ctx context.Context, endpointID string, keep int32) error
}

// Opener unseals an endpoint's signing secret (consumer-defined; *secret.Box
// satisfies it). The secret is unsealed only to sign, never logged (spec §6).
type Opener interface {
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// Manager persists deliveries, signs and attempts them, and drives the bounded
// retry. Construct with New.
type Manager struct {
	store  Store
	opener Opener
	http   *http.Client
	log    *slog.Logger

	// now and jitter are injected so backoff scheduling is deterministic in
	// tests (ENGINEERING rule 9).
	now    func() time.Time
	jitter func() float64
}

// New wires the manager. Redirects are deliberately not followed: a receiver
// must not be able to bounce a signed body somewhere else (spec §6).
func New(st Store, opener Opener, log *slog.Logger) *Manager {
	return &Manager{
		store:  st,
		opener: opener,
		http: &http.Client{
			Timeout: deliveryTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		log:    log,
		now:    time.Now,
		jitter: rand.Float64,
	}
}

// ─── The envelope (outbound-webhooks.md §4) ─────────────────────────────────
//
// One shape for every event. This is a PUBLISHED CONTRACT: fields are only ever
// added, never removed or retyped (ENGINEERING rule 17 binds what we emit, not
// only what we accept). It carries no sealed material — only deploy/backup
// metadata already surfaced through the API (spec §6).

type namedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type resourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type eventData struct {
	DeploymentID   string `json:"deployment_id,omitempty"`
	RevisionID     string `json:"revision_id,omitempty"`
	BackupRecordID string `json:"backup_record_id,omitempty"`
	BackupID       string `json:"backup_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Trigger        string `json:"trigger,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

type envelope struct {
	Event       string      `json:"event"`
	DeliveryID  string      `json:"delivery_id"`
	OccurredAt  string      `json:"occurred_at"`
	Project     namedRef    `json:"project"`
	Environment namedRef    `json:"environment"`
	Resource    resourceRef `json:"resource"`
	Data        eventData   `json:"data"`
}

// event is the plane-domain value one delivery describes, before it is bound to
// a project/environment and an id.
type event struct {
	Type     string
	Resource resourceRef
	Data     eventData
}

// ─── The fan-out seam (outbound-webhooks.md §5) ─────────────────────────────

// NotifyDeploy delivers a deploy's terminal outcome to every subscribed
// endpoint in the owning project. It satisfies scheduler.EventSink alongside
// notify.Manager, and returns immediately — everything runs detached so a
// dead receiver can never slow or fail a deploy (spec §5).
func (m *Manager) NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment) {
	ev := event{
		Type: domain.EventDeploySucceeded,
		Resource: resourceRef{
			Kind: domain.WebhookResourceApplication,
			ID:   app.ID,
			Name: app.Name,
		},
		Data: eventData{
			DeploymentID: dep.ID,
			RevisionID:   dep.RevisionID,
			Status:       string(dep.Status),
			Trigger:      dep.Trigger,
			Detail:       dep.Detail,
		},
	}
	if dep.Status == domain.DeployFailed {
		ev.Type = domain.EventDeployFailed
	}
	m.dispatch(ctx, app.EnvironmentID, ev)
}

// NotifyBackup delivers a database backup's terminal outcome.
func (m *Manager) NotifyBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord) {
	ev := event{
		Type: domain.EventBackupSucceeded,
		Resource: resourceRef{
			Kind: domain.WebhookResourceDatabase,
			ID:   db.ID,
			Name: db.Name,
		},
		Data: eventData{
			BackupRecordID: rec.ID,
			BackupID:       rec.DatabaseBackupID,
			Status:         rec.Status,
			Detail:         rec.Detail,
		},
	}
	if rec.Status != domain.BackupSucceeded {
		ev.Type = domain.EventBackupFailed
	}
	m.dispatch(ctx, db.EnvironmentID, ev)
}

// dispatch resolves the environment's project, loads the subscribed endpoints,
// and enqueues plus first-attempts one delivery each. It detaches from the
// caller's context (context.WithoutCancel) so a finished request cannot cancel
// an in-flight send — the same shape as notify.dispatch.
//
// Goroutine ownership (ENGINEERING rule 7): every context derived here carries
// a timeout, the outer goroutine waits on its children, and every failure is
// logged with the ids it concerns.
func (m *Manager) dispatch(ctx context.Context, envID string, ev event) {
	base := context.WithoutCancel(ctx)
	go func() {
		endpoints, project, environment, ok := m.resolve(base, envID, ev.Type)
		if !ok {
			return
		}
		var wg sync.WaitGroup
		for _, e := range endpoints {
			wg.Add(1)
			go func(e domain.WebhookEndpoint) {
				defer wg.Done()
				d, err := m.enqueue(base, e, ev, project, environment, nil)
				if err != nil {
					// The deploy is already recorded and its status is
					// authoritative; a failure to log a delivery is dropped
					// (spec §5).
					m.log.Error("webhooks: persisting delivery", "endpoint_id", e.ID, "event", ev.Type, "resource_id", ev.Resource.ID, "error", err)
					return
				}
				if _, err := m.Attempt(base, e, d); err != nil {
					m.log.Error("webhooks: first attempt", "endpoint_id", e.ID, "delivery_id", d.ID, "error", err)
				}
			}(e)
		}
		wg.Wait()
	}()
}

// resolve loads the endpoints subscribed to eventType in the project owning
// envID, with the project and environment names the envelope carries. Unlike
// notify.dispatch — which assigns the environment name to the event's project
// field — both are resolved here so both are accurate (spec §4).
func (m *Manager) resolve(base context.Context, envID, eventType string) ([]domain.WebhookEndpoint, namedRef, namedRef, bool) {
	c, cancel := context.WithTimeout(base, resolveTimeout)
	defer cancel()

	env, err := m.store.GetEnvironment(c, envID)
	if err != nil {
		m.log.Error("webhooks: resolving environment", "env_id", envID, "error", err)
		return nil, namedRef{}, namedRef{}, false
	}
	proj, err := m.store.GetProject(c, env.ProjectID)
	if err != nil {
		m.log.Error("webhooks: resolving project", "project_id", env.ProjectID, "error", err)
		return nil, namedRef{}, namedRef{}, false
	}
	endpoints, err := m.store.ListEnabledWebhookEndpointsForEvent(c, env.ProjectID, eventType)
	if err != nil {
		m.log.Error("webhooks: listing endpoints", "project_id", env.ProjectID, "event", eventType, "error", err)
		return nil, namedRef{}, namedRef{}, false
	}
	return endpoints, namedRef{ID: proj.ID, Name: proj.Name}, namedRef{ID: env.ID, Name: env.Name}, true
}

// ─── Enqueue, attempt, retry (outbound-webhooks.md §4) ──────────────────────

// enqueue mints the delivery id, renders the exact body bytes that will be
// signed, and writes the row BEFORE any attempt runs, so a plane restart
// mid-backoff loses nothing (ENGINEERING rule 15). Retention is pruned on
// insert (spec §6).
func (m *Manager) enqueue(ctx context.Context, e domain.WebhookEndpoint, ev event, project, environment namedRef, redeliveryOf *string) (domain.WebhookDelivery, error) {
	c, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	now := m.now()
	// The first attempt runs detached, immediately after this row lands. Writing
	// next_attempt_at = now would make the delivery due the instant it exists,
	// so a sweeper tick landing in the middle of that attempt would pick it up
	// and send a second, concurrent POST of the same signed body. Leasing it for
	// one attempt window hands ownership to that first attempt; if the process
	// dies before the attempt lands, the row is still persisted and the sweeper
	// collects it once the lease elapses, which is the restart property spec §4
	// and ENGINEERING rule 15 actually ask for.
	leaseUntil := now.Add(deliveryTimeout)
	id := ids.New(ids.PrefixWebhookDelivery)
	body, err := json.Marshal(envelope{
		Event:       ev.Type,
		DeliveryID:  id,
		OccurredAt:  now.UTC().Format(time.RFC3339),
		Project:     project,
		Environment: environment,
		Resource:    ev.Resource,
		Data:        ev.Data,
	})
	if err != nil {
		return domain.WebhookDelivery{}, fmt.Errorf("webhooks: rendering payload: %w", err)
	}
	return m.persist(c, e, domain.WebhookDelivery{
		ID:            id,
		EndpointID:    e.ID,
		EventType:     ev.Type,
		ResourceKind:  ev.Resource.Kind,
		ResourceID:    ev.Resource.ID,
		ResourceName:  ev.Resource.Name,
		Payload:       string(body),
		Status:        domain.DeliveryPending,
		Attempt:       0,
		NextAttemptAt: &leaseUntil,
		RedeliveryOf:  redeliveryOf,
	})
}

// persist writes a delivery row and prunes the endpoint's log to its retention
// window. A prune failure is logged, never surfaced: the delivery is recorded
// and that is what the caller asked for.
func (m *Manager) persist(ctx context.Context, e domain.WebhookEndpoint, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	saved, err := m.store.CreateWebhookDelivery(ctx, d)
	if err != nil {
		return domain.WebhookDelivery{}, fmt.Errorf("webhooks: creating delivery for endpoint %s: %w", e.ID, err)
	}
	if err := m.store.DeleteOldWebhookDeliveries(ctx, e.ID, deliveryRetention); err != nil {
		m.log.Warn("webhooks: pruning delivery log", "endpoint_id", e.ID, "error", err)
	}
	return saved, nil
}

// Attempt makes one HTTP attempt for a delivery, records it, and advances the
// delivery — succeeded, failed, or pending with the next backoff. It is the
// synchronous seam: event fan-out reaches it through the detached path in
// dispatch, the retry sweeper calls it directly.
//
// The returned error means the attempt could not be RECORDED (a store failure);
// a receiver answering 500 is a normal outcome, not an error.
func (m *Manager) Attempt(ctx context.Context, e domain.WebhookEndpoint, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	n := d.Attempt + 1
	secret, err := m.opener.Open(e.SecretCT, e.SecretNonce)
	if err != nil {
		// Without the secret nothing can be signed, and no later attempt will
		// fare better — terminate rather than burn the retry budget.
		m.log.Error("webhooks: unsealing signing secret", "endpoint_id", e.ID, "delivery_id", d.ID, "error", err)
		return m.advance(ctx, d, domain.DeliveryFailed, d.Attempt, nil)
	}

	status, dur, sendErr := m.post(ctx, e.URL, d, secret)

	rec := domain.WebhookDeliveryAttempt{
		DeliveryID: d.ID,
		Attempt:    n,
		DurationMS: int(dur.Milliseconds()),
	}
	if sendErr != nil {
		rec.Error = sendErr.Error()
	} else {
		s := status
		rec.ResponseStatus = &s
	}
	if _, err := m.store.CreateWebhookDeliveryAttempt(ctx, rec); err != nil && !errors.Is(err, store.ErrConflict) {
		// ErrConflict means this attempt number is already on record — a
		// redelivery of the same work, not a failure (ENGINEERING rule 12).
		return domain.WebhookDelivery{}, fmt.Errorf("webhooks: recording attempt %d of %s: %w", n, d.ID, err)
	}

	switch {
	case sendErr == nil && status/100 == 2:
		return m.advance(ctx, d, domain.DeliverySucceeded, n, nil)
	case retryable(status, sendErr) && n < maxAttempts:
		next := m.now().Add(m.backoffFor(n))
		return m.advance(ctx, d, domain.DeliveryPending, n, &next)
	default:
		return m.advance(ctx, d, domain.DeliveryFailed, n, nil)
	}
}

// advance writes a delivery's new state, but only if the row still holds the
// attempt count this worker started from.
//
// Losing that compare-and-set is not an error. It means another worker — the
// detached first attempt, or a sweeper tick that found the row due — already
// moved this delivery on, and the correct thing to do with our own result is
// drop it: the winner's outcome is the one on record, and re-writing ours could
// resurrect a delivery that already succeeded. The delivery is returned as we
// last read it so callers keep a coherent value.
func (m *Manager) advance(ctx context.Context, d domain.WebhookDelivery, status string, attempt int, next *time.Time) (domain.WebhookDelivery, error) {
	saved, err := m.store.UpdateWebhookDeliveryProgress(ctx, d.ID, status, d.Attempt, attempt, next)
	if errors.Is(err, store.ErrNotFound) {
		m.log.Info("webhooks: delivery already advanced by another worker",
			"delivery_id", d.ID, "from_attempt", d.Attempt, "dropped_status", status)
		return d, nil
	}
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	return saved, nil
}

// retryable decides whether another attempt could plausibly succeed
// (outbound-webhooks.md §4). Retryable: a transport error, 429, any 5xx.
// Terminal: any other 4xx, or a 3xx — the receiver answered, and a 401 or a 404
// will answer the same in five minutes.
func retryable(status int, sendErr error) bool {
	if sendErr != nil {
		return true
	}
	return status == http.StatusTooManyRequests || status/100 == 5
}

// backoffFor returns the delay before attempt n+1, with ±jitterFraction spread.
func (m *Manager) backoffFor(n int) time.Duration {
	i := n - 1
	if i < 0 {
		i = 0
	}
	if i >= len(backoff) {
		i = len(backoff) - 1
	}
	base := float64(backoff[i])
	// jitter() ∈ [0,1) → a multiplier in [1-f, 1+f).
	return time.Duration(base * (1 - jitterFraction + 2*jitterFraction*m.jitter()))
}

// post makes the signed request and returns the response status, how long it
// took, and a redacted transport error. The response body is drained and
// discarded, never stored or logged (spec §6).
func (m *Manager) post(ctx context.Context, target string, d domain.WebhookDelivery, secret []byte) (int, time.Duration, error) {
	body := []byte(d.Payload)
	start := m.now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, m.now().Sub(start), redactTransportError(err)
	}
	applySignatureHeaders(req, d.EventType, d.ID, start, secret, body)

	resp, err := m.http.Do(req)
	if err != nil {
		return 0, m.now().Sub(start), redactTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseReadLimit))
	return resp.StatusCode, m.now().Sub(start), nil
}

// redactTransportError strips the request URL out of a transport error before
// it is stored in the attempt record and logged. An endpoint URL is stored in
// the clear, but an operator is free to put a token in its query string, so the
// same defense notify.redactURL applies to Slack/Telegram URLs applies here
// (spec §6). It is duplicated rather than imported: core/notify is a sibling,
// not a dependency, and a six-line defense is cheaper than the coupling.
func redactTransportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ─── Ping and redeliver (outbound-webhooks.md §7) ───────────────────────────

// Ping delivers a synthetic webhook.ping so an operator can prove the wiring at
// setup time, before any real deploy — the same affordance as
// POST /notifiers/{id}/test. It also gives a new endpoint its first terminal
// delivery, so health stops being "unknown" on day one rather than at the first
// failure. webhook.ping is delivery-only: it is not subscribable (spec §3), so
// an endpoint always receives its own pings.
func (m *Manager) Ping(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookDelivery, error) {
	if !e.Enabled {
		return domain.WebhookDelivery{}, ErrEndpointDisabled
	}
	proj, err := m.store.GetProject(ctx, e.ProjectID)
	if err != nil {
		return domain.WebhookDelivery{}, fmt.Errorf("webhooks: resolving project %s: %w", e.ProjectID, err)
	}
	d, err := m.enqueue(ctx, e, event{
		Type: domain.EventWebhookPing,
		Resource: resourceRef{
			Kind: domain.WebhookResourceApplication,
			ID:   e.ID,
			Name: e.URL,
		},
		Data: eventData{Status: "ok", Detail: "Ping from CypherPanel."},
	}, namedRef{ID: proj.ID, Name: proj.Name}, namedRef{}, nil)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	m.attemptDetached(ctx, e, d)
	return d, nil
}

// Redeliver mints a NEW delivery replaying the original's stored payload bytes
// verbatim, with redelivery_of set. The original row is never mutated — the log
// is evidence. The body is byte-identical (so the signature covers the same
// event), while X-CypherPanel-Delivery is new, which is what receivers dedupe
// on (spec §7).
func (m *Manager) Redeliver(ctx context.Context, e domain.WebhookEndpoint, orig domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	if !e.Enabled {
		return domain.WebhookDelivery{}, ErrEndpointDisabled
	}
	now := m.now()
	origID := orig.ID
	d, err := m.persist(ctx, e, domain.WebhookDelivery{
		ID:            ids.New(ids.PrefixWebhookDelivery),
		EndpointID:    e.ID,
		EventType:     orig.EventType,
		ResourceKind:  orig.ResourceKind,
		ResourceID:    orig.ResourceID,
		ResourceName:  orig.ResourceName,
		Payload:       orig.Payload,
		Status:        domain.DeliveryPending,
		Attempt:       0,
		NextAttemptAt: &now,
		RedeliveryOf:  &origID,
	})
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	m.attemptDetached(ctx, e, d)
	return d, nil
}

// attemptDetached runs the first attempt off the caller's context so an HTTP
// handler returns immediately (202) and a finished request cannot cancel an
// in-flight send. If the process dies before it lands, next_attempt_at is
// already persisted and the sweeper picks the delivery up.
func (m *Manager) attemptDetached(ctx context.Context, e domain.WebhookEndpoint, d domain.WebhookDelivery) {
	base := context.WithoutCancel(ctx)
	go func() {
		c, cancel := context.WithTimeout(base, deliveryTimeout+resolveTimeout)
		defer cancel()
		if _, err := m.Attempt(c, e, d); err != nil {
			m.log.Error("webhooks: attempt", "endpoint_id", e.ID, "delivery_id", d.ID, "error", err)
		}
	}()
}

// ─── The retry sweeper (outbound-webhooks.md §4) ────────────────────────────

// RunRetrySweeper attempts every due delivery on each tick until ctx is
// cancelled. It owns its ticker's lifecycle (ENGINEERING rule 7) and is wired
// beside previews.RunSweeper and scheduler.RunBackupSweeper in main.go.
func (m *Manager) RunRetrySweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.SweepDue(ctx)
		}
	}
}

// SweepDue attempts every pending delivery whose backoff has elapsed. Because
// next_attempt_at lives in Postgres and the row is written before the first
// attempt, a plane restart mid-backoff loses nothing (ENGINEERING rule 15).
func (m *Manager) SweepDue(ctx context.Context) {
	due, err := m.store.ListDueWebhookDeliveries(ctx, m.now(), sweepBatch)
	if err != nil {
		m.log.Error("webhooks: listing due deliveries", "error", err)
		return
	}
	for _, d := range due {
		e, err := m.store.GetWebhookEndpoint(ctx, d.EndpointID)
		if err != nil {
			m.log.Error("webhooks: resolving endpoint for retry", "endpoint_id", d.EndpointID, "delivery_id", d.ID, "error", err)
			continue
		}
		if !e.Enabled {
			// The endpoint was switched off mid-backoff. No request is made, so
			// no attempt row is written; the delivery just stops being pending
			// (a disabled endpoint reports health "unknown" regardless).
			if _, err := m.advance(ctx, d, domain.DeliveryFailed, d.Attempt, nil); err != nil {
				m.log.Error("webhooks: abandoning delivery for disabled endpoint", "endpoint_id", e.ID, "delivery_id", d.ID, "error", err)
			}
			continue
		}
		if _, err := m.Attempt(ctx, e, d); err != nil {
			m.log.Error("webhooks: retry attempt", "endpoint_id", e.ID, "delivery_id", d.ID, "error", err)
		}
	}
}

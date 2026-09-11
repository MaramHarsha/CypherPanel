package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrProjectNotFound is returned when the addressed project does not exist —
// distinct from store.ErrNotFound, which from this service means the endpoint
// or delivery is missing.
var ErrProjectNotFound = errors.New("webhooks: project not found")

// ErrEndpointDisabled is returned when an action needs a live endpoint: ping
// and redeliver both refuse a disabled one rather than silently queueing work
// nothing will send (outbound-webhooks.md §7).
var ErrEndpointDisabled = errors.New("webhooks: endpoint is disabled")

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Paging bounds for the delivery log (outbound-webhooks.md §7). Seek paging on
// (created_at, id) DESC, because offset paging over a continuously-prepended
// table re-shows and skips rows as deliveries land mid-scroll.
const (
	defaultDeliveryLimit = 50
	maxDeliveryLimit     = 100
)

// Sealer seals an endpoint's signing secret at rest (consumer-defined;
// *secret.Box satisfies it). The secret is SEALED, not hashed: API tokens are
// hashed because the plane only ever compares them, but a signing secret must
// be USED, so the plane has to be able to reproduce it (spec §2).
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// EndpointStore is the persistence the CRUD service needs (consumer-defined;
// *store.Store satisfies it).
type EndpointStore interface {
	GetProject(ctx context.Context, id string) (domain.Project, error)

	CreateWebhookEndpoint(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error)
	GetWebhookEndpoint(ctx context.Context, id string) (domain.WebhookEndpoint, error)
	ListWebhookEndpointsByProject(ctx context.Context, projectID string) ([]domain.WebhookEndpoint, error)
	UpdateWebhookEndpoint(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error)
	RotateWebhookEndpointSecret(ctx context.Context, id string, ct, nonce []byte) (domain.WebhookEndpoint, error)
	DeleteWebhookEndpoint(ctx context.Context, id string) error

	GetWebhookDelivery(ctx context.Context, id string) (domain.WebhookDelivery, error)
	ListWebhookDeliveriesByEndpoint(ctx context.Context, endpointID string, limit int32) ([]domain.WebhookDelivery, error)
	ListWebhookDeliveriesBefore(ctx context.Context, endpointID, before string, limit int32) ([]domain.WebhookDelivery, error)
	ListLatestWebhookDeliveryAttempts(ctx context.Context, deliveryIDs []string) ([]domain.WebhookDeliveryAttempt, error)
	ListRecentTerminalWebhookDeliveryStatuses(ctx context.Context, endpointID string, limit int32) ([]string, error)
	LastWebhookDeliveryAt(ctx context.Context, endpointID string) (*time.Time, error)
}

// Dispatcher sends deliveries (consumer-defined; *Manager satisfies it). The
// CRUD service resolves ids to rows; the manager owns the envelope, the
// signature and the retry.
type Dispatcher interface {
	Ping(ctx context.Context, e domain.WebhookEndpoint) (domain.WebhookDelivery, error)
	Redeliver(ctx context.Context, e domain.WebhookEndpoint, orig domain.WebhookDelivery) (domain.WebhookDelivery, error)
}

// Service is the webhook endpoint CRUD and read surface: it validates, seals
// the signing secret, derives Endpoint Health, and pages the delivery log. The
// secret is structurally absent from everything it returns except the create
// and rotate results, where it is shown exactly once (ENGINEERING rule 20).
type Service struct {
	store    EndpointStore
	sealer   Sealer
	dispatch Dispatcher
}

// NewService wires the CRUD service. dispatch is the delivery manager; ping and
// redeliver route through it.
func NewService(st EndpointStore, sealer Sealer, dispatch Dispatcher) *Service {
	return &Service{store: st, sealer: sealer, dispatch: dispatch}
}

// EndpointView is an endpoint plus the two fields the API surfaces that are
// DERIVED, never stored: health (spec §4) and the time of its last delivery.
type EndpointView struct {
	Endpoint       domain.WebhookEndpoint
	Health         string
	LastDeliveryAt *time.Time
}

// Created is a create result. Secret is the raw signing secret, returned
// exactly once — it is sealed at rest and never retrievable again (spec §2).
type Created struct {
	Endpoint domain.WebhookEndpoint
	Secret   string
}

// CreateInput is an endpoint create request.
type CreateInput struct {
	URL     string
	Events  []string
	Enabled bool
}

// Create validates, mints and seals a signing secret, and stores the endpoint
// under a project.
func (s *Service) Create(ctx context.Context, projectID string, in CreateInput) (Created, error) {
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Created{}, ErrProjectNotFound
		}
		return Created{}, err
	}
	target, events, err := validate(in.URL, in.Events)
	if err != nil {
		return Created{}, err
	}
	secret := ids.Secret()
	ct, nonce, err := s.sealer.Seal([]byte(secret))
	if err != nil {
		return Created{}, fmt.Errorf("webhooks: sealing signing secret: %w", err)
	}
	e, err := s.store.CreateWebhookEndpoint(ctx, domain.WebhookEndpoint{
		ID:          ids.New(ids.PrefixWebhookEndpoint),
		ProjectID:   projectID,
		URL:         target,
		SecretCT:    ct,
		SecretNonce: nonce,
		Events:      events,
		Enabled:     in.Enabled,
	})
	if err != nil {
		return Created{}, err
	}
	return Created{Endpoint: e, Secret: secret}, nil
}

// UpdateInput carries the mutable fields (spec §7). The signing secret is not
// among them — it changes only through RotateSecret, which returns it.
type UpdateInput struct {
	URL     string
	Events  []string
	Enabled bool
}

// Update mutates an endpoint's url, events and enabled flag.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (EndpointView, error) {
	cur, err := s.store.GetWebhookEndpoint(ctx, id)
	if err != nil {
		return EndpointView{}, err
	}
	target, events, err := validate(in.URL, in.Events)
	if err != nil {
		return EndpointView{}, err
	}
	cur.URL, cur.Events, cur.Enabled = target, events, in.Enabled
	updated, err := s.store.UpdateWebhookEndpoint(ctx, cur)
	if err != nil {
		return EndpointView{}, err
	}
	return s.view(ctx, updated)
}

// RotateSecret re-seals a fresh signing secret and returns it once. There is no
// overlap window: the previous secret stops verifying immediately, and `ping`
// confirms the new one in a click (spec §7).
func (s *Service) RotateSecret(ctx context.Context, id string) (string, error) {
	secret := ids.Secret()
	ct, nonce, err := s.sealer.Seal([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("webhooks: sealing signing secret: %w", err)
	}
	if _, err := s.store.RotateWebhookEndpointSecret(ctx, id, ct, nonce); err != nil {
		return "", err
	}
	return secret, nil
}

// Get returns one endpoint. It is also the authorization resolver's entry
// point, so it must stay a plain lookup.
func (s *Service) Get(ctx context.Context, id string) (domain.WebhookEndpoint, error) {
	return s.store.GetWebhookEndpoint(ctx, id)
}

// View returns one endpoint with its derived health and last delivery time.
func (s *Service) View(ctx context.Context, id string) (EndpointView, error) {
	e, err := s.store.GetWebhookEndpoint(ctx, id)
	if err != nil {
		return EndpointView{}, err
	}
	return s.view(ctx, e)
}

// ListViews returns a project's endpoints, each with its derived fields.
func (s *Service) ListViews(ctx context.Context, projectID string) ([]EndpointView, error) {
	list, err := s.store.ListWebhookEndpointsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]EndpointView, 0, len(list))
	for _, e := range list {
		v, err := s.view(ctx, e)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Delete removes an endpoint; its deliveries and their attempts cascade.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteWebhookEndpoint(ctx, id)
}

// Ping sends a synthetic webhook.ping through one endpoint (spec §7).
func (s *Service) Ping(ctx context.Context, id string) (domain.WebhookDelivery, error) {
	e, err := s.store.GetWebhookEndpoint(ctx, id)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	return s.dispatch.Ping(ctx, e)
}

// GetDelivery returns one delivery. It is the authorization resolver's entry
// point for /webhook-deliveries/{id}.
func (s *Service) GetDelivery(ctx context.Context, id string) (domain.WebhookDelivery, error) {
	return s.store.GetWebhookDelivery(ctx, id)
}

// Redeliver replays a delivery's stored payload as a new delivery (spec §7).
func (s *Service) Redeliver(ctx context.Context, deliveryID string) (domain.WebhookDelivery, error) {
	orig, err := s.store.GetWebhookDelivery(ctx, deliveryID)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	e, err := s.store.GetWebhookEndpoint(ctx, orig.EndpointID)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	return s.dispatch.Redeliver(ctx, e, orig)
}

// DeliveryView is a delivery plus the outcome of its most recent attempt — the
// two facts the feed shows ("… · 200 · 84ms") that the delivery row itself does
// not store. Both are nil until the first attempt lands, and ResponseStatus
// stays nil when the attempt ended in a transport error (nothing answered).
type DeliveryView struct {
	Delivery       domain.WebhookDelivery
	ResponseStatus *int
	DurationMS     *int
}

// Page is one seek page of the delivery log. NextBefore is the cursor for the
// following page, empty when this is the last one (spec §7).
type Page struct {
	Deliveries []DeliveryView
	NextBefore string
}

// Deliveries returns the newest page of an endpoint's delivery log, or the page
// strictly older than the before cursor. limit defaults to 50 and caps at 100.
func (s *Service) Deliveries(ctx context.Context, endpointID string, limit int, before string) (Page, error) {
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}
	if limit > maxDeliveryLimit {
		limit = maxDeliveryLimit
	}
	// Fetch one extra row to learn whether a further page exists without a
	// second query or a count.
	fetch := int32(limit) + 1

	var (
		rows []domain.WebhookDelivery
		err  error
	)
	if before == "" {
		rows, err = s.store.ListWebhookDeliveriesByEndpoint(ctx, endpointID, fetch)
	} else {
		rows, err = s.store.ListWebhookDeliveriesBefore(ctx, endpointID, before, fetch)
	}
	if err != nil {
		return Page{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	views, err := s.withLastAttempt(ctx, rows)
	if err != nil {
		return Page{}, err
	}
	return Page{Deliveries: views, NextBefore: next}, nil
}

// withLastAttempt attaches each delivery's most recent attempt outcome in one
// batch lookup, keeping the page to two queries rather than one per row.
func (s *Service) withLastAttempt(ctx context.Context, rows []domain.WebhookDelivery) ([]DeliveryView, error) {
	out := make([]DeliveryView, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(rows))
	for _, d := range rows {
		ids = append(ids, d.ID)
	}
	attempts, err := s.store.ListLatestWebhookDeliveryAttempts(ctx, ids)
	if err != nil {
		return nil, err
	}
	byDelivery := make(map[string]domain.WebhookDeliveryAttempt, len(attempts))
	for _, a := range attempts {
		byDelivery[a.DeliveryID] = a
	}
	for _, d := range rows {
		v := DeliveryView{Delivery: d}
		if a, ok := byDelivery[d.ID]; ok {
			ms := a.DurationMS
			v.DurationMS, v.ResponseStatus = &ms, a.ResponseStatus
		}
		out = append(out, v)
	}
	return out, nil
}

// view attaches the derived read-model fields to an endpoint.
func (s *Service) view(ctx context.Context, e domain.WebhookEndpoint) (EndpointView, error) {
	statuses, err := s.store.ListRecentTerminalWebhookDeliveryStatuses(ctx, e.ID, healthWindow)
	if err != nil {
		return EndpointView{}, err
	}
	last, err := s.store.LastWebhookDeliveryAt(ctx, e.ID)
	if err != nil {
		return EndpointView{}, err
	}
	return EndpointView{Endpoint: e, Health: Health(e.Enabled, statuses), LastDeliveryAt: last}, nil
}

// Health derives an endpoint's health from its most recent TERMINAL delivery
// statuses, newest first (outbound-webhooks.md §4):
//
//   - unknown  — disabled, or no terminal delivery yet. A disabled endpoint is
//     "unknown" rather than a fifth word: nothing is being attempted, so we
//     cannot claim health (ui-principles §5 forbids faking certainty), and the
//     UI writes the plain word "Disabled" beside the URL.
//   - failing  — the most recent delivery exhausted its attempts.
//   - degraded — the most recent succeeded, but at least one in the window failed.
//   - healthy  — every delivery in the window succeeded.
func Health(enabled bool, recentTerminal []string) string {
	if !enabled || len(recentTerminal) == 0 {
		return domain.EndpointHealthUnknown
	}
	if recentTerminal[0] == domain.DeliveryFailed {
		return domain.EndpointFailing
	}
	for _, st := range recentTerminal {
		if st == domain.DeliveryFailed {
			return domain.EndpointDegraded
		}
	}
	return domain.EndpointHealthy
}

// validate checks the receiver URL and the event selection, returning the
// cleaned URL and the deduplicated events.
func validate(rawURL string, events []string) (string, []string, error) {
	target := strings.TrimSpace(rawURL)
	if err := validateHTTPURL(target); err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(events))
	for _, e := range events {
		if e == domain.EventWebhookPing {
			// Delivery-only (spec §3): an endpoint always receives its own
			// pings, so naming it here is a mistake, not a subscription.
			return "", nil, invalid("webhook.ping is delivered by the ping action, not subscribed to")
		}
		if !domain.ValidEventType(e) {
			return "", nil, invalid("unknown event: " + e)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return "", nil, invalid("at least one event is required")
	}
	return target, out, nil
}

// validateHTTPURL requires an http(s) receiver (spec §6): no other scheme
// (file://, gopher://) is accepted. Same posture as notifiers — the operator is
// the trust root — plus no redirect following and a 10s per-attempt timeout in
// the manager.
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("url must be an http(s) URL")
	}
	return nil
}

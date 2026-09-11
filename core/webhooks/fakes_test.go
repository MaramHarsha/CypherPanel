package webhooks

// Hand-rolled fakes for the service and manager unit tests, matching the
// convention in core/notify: an identity sealer/opener that makes sealing
// observable and reversible, and an in-memory store with the ordering the real
// queries guarantee.

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const sealedPrefix = "sealed:"

// identitySealer makes sealing observable and reversible in tests: the
// "ciphertext" is the plaintext prefixed, and Open strips the prefix.
type identitySealer struct{}

func (identitySealer) Seal(pt []byte) (ct, nonce []byte, err error) {
	return append([]byte(sealedPrefix), pt...), []byte("n"), nil
}

func (identitySealer) Open(ct, _ []byte) ([]byte, error) {
	return []byte(strings.TrimPrefix(string(ct), sealedPrefix)), nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore satisfies both EndpointStore (the CRUD service) and Store (the
// manager). It is mutex-guarded because dispatch fans out across goroutines.
type fakeStore struct {
	mu sync.Mutex

	projects     map[string]domain.Project
	environments map[string]domain.Environment
	endpoints    map[string]domain.WebhookEndpoint
	deliveries   map[string]domain.WebhookDelivery
	attempts     map[string]domain.WebhookDeliveryAttempt // keyed "<delivery>:<n>"

	now func() time.Time

	// pruned records the (endpointID, keep) pairs the manager asked for, so a
	// test can assert retention runs on insert.
	pruned []pruneCall
}

type pruneCall struct {
	EndpointID string
	Keep       int32
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		projects: map[string]domain.Project{
			"prj_1": {ID: "prj_1", Name: "atlas-crm"},
			"prj_2": {ID: "prj_2", Name: "other"},
		},
		environments: map[string]domain.Environment{
			"env_1": {ID: "env_1", ProjectID: "prj_1", Name: "production"},
			"env_2": {ID: "env_2", ProjectID: "prj_2", Name: "production"},
		},
		endpoints:  map[string]domain.WebhookEndpoint{},
		deliveries: map[string]domain.WebhookDelivery{},
		attempts:   map[string]domain.WebhookDeliveryAttempt{},
		now:        now,
	}
}

func (s *fakeStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (s *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.environments[id]
	if !ok {
		return domain.Environment{}, store.ErrNotFound
	}
	return e, nil
}

func (s *fakeStore) CreateWebhookEndpoint(_ context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.endpoints {
		if ex.ProjectID == e.ProjectID && ex.URL == e.URL {
			return domain.WebhookEndpoint{}, store.ErrConflict
		}
	}
	e.CreatedAt, e.UpdatedAt = s.now(), s.now()
	s.endpoints[e.ID] = e
	return e, nil
}

func (s *fakeStore) GetWebhookEndpoint(_ context.Context, id string) (domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.endpoints[id]
	if !ok {
		return domain.WebhookEndpoint{}, store.ErrNotFound
	}
	return e, nil
}

func (s *fakeStore) ListWebhookEndpointsByProject(_ context.Context, projectID string) ([]domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.WebhookEndpoint{}
	for _, e := range s.endpoints {
		if e.ProjectID == projectID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) ListEnabledWebhookEndpointsForEvent(_ context.Context, projectID, eventType string) ([]domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.WebhookEndpoint{}
	for _, e := range s.endpoints {
		if e.ProjectID != projectID || !e.Enabled {
			continue
		}
		for _, sub := range e.Events {
			if sub == eventType {
				out = append(out, e)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) UpdateWebhookEndpoint(_ context.Context, e domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.endpoints[e.ID]
	if !ok {
		return domain.WebhookEndpoint{}, store.ErrNotFound
	}
	for id, ex := range s.endpoints {
		if id != e.ID && ex.ProjectID == e.ProjectID && ex.URL == e.URL {
			return domain.WebhookEndpoint{}, store.ErrConflict
		}
	}
	cur.URL, cur.Events, cur.Enabled = e.URL, e.Events, e.Enabled
	cur.UpdatedAt = s.now()
	s.endpoints[e.ID] = cur
	return cur, nil
}

func (s *fakeStore) RotateWebhookEndpointSecret(_ context.Context, id string, ct, nonce []byte) (domain.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.endpoints[id]
	if !ok {
		return domain.WebhookEndpoint{}, store.ErrNotFound
	}
	e.SecretCT, e.SecretNonce, e.UpdatedAt = ct, nonce, s.now()
	s.endpoints[id] = e
	return e, nil
}

func (s *fakeStore) DeleteWebhookEndpoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.endpoints, id)
	return nil
}

func (s *fakeStore) CreateWebhookDelivery(_ context.Context, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, clash := s.deliveries[d.ID]; clash {
		return domain.WebhookDelivery{}, store.ErrConflict
	}
	d.CreatedAt, d.UpdatedAt = s.now(), s.now()
	s.deliveries[d.ID] = d
	return d, nil
}

func (s *fakeStore) GetWebhookDelivery(_ context.Context, id string) (domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deliveries[id]
	if !ok {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	return d, nil
}

func (s *fakeStore) UpdateWebhookDeliveryProgress(_ context.Context, id, status string, fromAttempt, attempt int, next *time.Time) (domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deliveries[id]
	if !ok {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	// The compare-and-set the real query does in SQL: a caller whose starting
	// attempt no longer matches lost the row to another worker and its write is
	// refused, exactly as `WHERE id = $1 AND attempt = @from_attempt` refuses it.
	if d.Attempt != fromAttempt {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	d.Status, d.Attempt, d.NextAttemptAt, d.UpdatedAt = status, attempt, next, s.now()
	s.deliveries[id] = d
	return d, nil
}

// sortedDeliveries returns an endpoint's deliveries newest-first on
// (created_at, id) DESC — the ordering the real index guarantees.
func (s *fakeStore) sortedDeliveries(endpointID string) []domain.WebhookDelivery {
	out := []domain.WebhookDelivery{}
	for _, d := range s.deliveries {
		if d.EndpointID == endpointID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *fakeStore) ListWebhookDeliveriesByEndpoint(_ context.Context, endpointID string, limit int32) ([]domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.sortedDeliveries(endpointID)
	if int32(len(rows)) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *fakeStore) ListWebhookDeliveriesBefore(_ context.Context, endpointID, before string, limit int32) ([]domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, ok := s.deliveries[before]
	if !ok {
		return []domain.WebhookDelivery{}, nil
	}
	out := []domain.WebhookDelivery{}
	for _, d := range s.sortedDeliveries(endpointID) {
		older := d.CreatedAt.Before(cursor.CreatedAt) ||
			(d.CreatedAt.Equal(cursor.CreatedAt) && d.ID < cursor.ID)
		if older {
			out = append(out, d)
		}
	}
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) ListDueWebhookDeliveries(_ context.Context, now time.Time, limit int32) ([]domain.WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.WebhookDelivery{}
	for _, d := range s.deliveries {
		if d.Status == domain.DeliveryPending && d.NextAttemptAt != nil && !d.NextAttemptAt.After(now) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextAttemptAt.Before(*out[j].NextAttemptAt) })
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) ListRecentTerminalWebhookDeliveryStatuses(_ context.Context, endpointID string, limit int32) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, d := range s.sortedDeliveries(endpointID) {
		if d.Status == domain.DeliveryPending {
			continue
		}
		out = append(out, d.Status)
		if int32(len(out)) == limit {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) LastWebhookDeliveryAt(_ context.Context, endpointID string) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.sortedDeliveries(endpointID)
	if len(rows) == 0 {
		return nil, nil
	}
	t := rows[0].CreatedAt
	return &t, nil
}

func (s *fakeStore) DeleteOldWebhookDeliveries(_ context.Context, endpointID string, keep int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned = append(s.pruned, pruneCall{EndpointID: endpointID, Keep: keep})
	rows := s.sortedDeliveries(endpointID)
	for i, d := range rows {
		if int32(i) >= keep {
			delete(s.deliveries, d.ID)
		}
	}
	return nil
}

func (s *fakeStore) CreateWebhookDeliveryAttempt(_ context.Context, a domain.WebhookDeliveryAttempt) (domain.WebhookDeliveryAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := a.DeliveryID + ":" + strconv.Itoa(a.Attempt)
	if _, dup := s.attempts[key]; dup {
		return domain.WebhookDeliveryAttempt{}, store.ErrConflict
	}
	a.At = s.now()
	s.attempts[key] = a
	return a, nil
}

// ListLatestWebhookDeliveryAttempts returns the newest attempt for each id;
// deliveries with no attempt yet are absent, as in the real query.
func (s *fakeStore) ListLatestWebhookDeliveryAttempts(_ context.Context, deliveryIDs []string) ([]domain.WebhookDeliveryAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range deliveryIDs {
		want[id] = true
	}
	latest := map[string]domain.WebhookDeliveryAttempt{}
	for _, a := range s.attempts {
		if !want[a.DeliveryID] {
			continue
		}
		if cur, ok := latest[a.DeliveryID]; !ok || a.Attempt > cur.Attempt {
			latest[a.DeliveryID] = a
		}
	}
	out := []domain.WebhookDeliveryAttempt{}
	for _, a := range latest {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeliveryID < out[j].DeliveryID })
	return out, nil
}

// attemptsFor returns one delivery's attempts in order.
func (s *fakeStore) attemptsFor(deliveryID string) []domain.WebhookDeliveryAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.WebhookDeliveryAttempt{}
	for _, a := range s.attempts {
		if a.DeliveryID == deliveryID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Attempt < out[j].Attempt })
	return out
}

// seedEndpoint stores a ready endpoint whose sealed secret opens to secret.
func (s *fakeStore) seedEndpoint(id, projectID, target, secret string, events []string, enabled bool) domain.WebhookEndpoint {
	ct, nonce, _ := identitySealer{}.Seal([]byte(secret))
	e := domain.WebhookEndpoint{
		ID:          id,
		ProjectID:   projectID,
		URL:         target,
		SecretCT:    ct,
		SecretNonce: nonce,
		Events:      events,
		Enabled:     enabled,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.CreatedAt, e.UpdatedAt = s.now(), s.now()
	s.endpoints[id] = e
	return e
}

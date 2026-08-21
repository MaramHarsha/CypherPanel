package webhooks

// Service unit tests (outbound-webhooks.md §2, §4, §7).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

var fixedNow = time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC)

func testClock() func() time.Time { return func() time.Time { return fixedNow } }

// nopDispatcher stands in where a test exercises CRUD only.
type nopDispatcher struct {
	pings      int
	redelivers int
}

func (d *nopDispatcher) Ping(_ context.Context, e domain.WebhookEndpoint) (domain.WebhookDelivery, error) {
	if !e.Enabled {
		return domain.WebhookDelivery{}, ErrEndpointDisabled
	}
	d.pings++
	return domain.WebhookDelivery{ID: "whd_ping", EndpointID: e.ID, EventType: domain.EventWebhookPing}, nil
}

func (d *nopDispatcher) Redeliver(_ context.Context, e domain.WebhookEndpoint, orig domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	if !e.Enabled {
		return domain.WebhookDelivery{}, ErrEndpointDisabled
	}
	d.redelivers++
	return domain.WebhookDelivery{ID: "whd_replay", EndpointID: e.ID, RedeliveryOf: &orig.ID}, nil
}

func newTestService(t *testing.T) (*Service, *fakeStore, *nopDispatcher) {
	t.Helper()
	st := newFakeStore(testClock())
	d := &nopDispatcher{}
	return NewService(st, identitySealer{}, d), st, d
}

func validInput() CreateInput {
	return CreateInput{
		URL:     "https://ops.meridian.dev/hooks/cypher",
		Events:  []string{domain.EventDeploySucceeded, domain.EventDeployFailed},
		Enabled: true,
	}
}

func TestCreateSealsSecretAndReturnsItOnce(t *testing.T) {
	svc, st, _ := newTestService(t)
	got, err := svc.Create(context.Background(), "prj_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Secret == "" {
		t.Fatal("create returned no signing secret; it must be shown exactly once")
	}
	// The stored row carries only the sealed pair — never the plaintext.
	stored, err := st.GetWebhookEndpoint(context.Background(), got.Endpoint.ID)
	if err != nil {
		t.Fatalf("GetWebhookEndpoint: %v", err)
	}
	if len(stored.SecretCT) == 0 || len(stored.SecretNonce) == 0 {
		t.Fatal("endpoint stored without a sealed secret")
	}
	if !strings.HasPrefix(string(stored.SecretCT), sealedPrefix) {
		t.Fatalf("secret was not sealed: %q", stored.SecretCT)
	}
	// A later read never carries the secret back.
	v, err := svc.View(context.Background(), got.Endpoint.ID)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Health != domain.EndpointHealthUnknown {
		t.Fatalf("fresh endpoint health = %q, want unknown", v.Health)
	}
	if v.LastDeliveryAt != nil {
		t.Fatalf("fresh endpoint last_delivery_at = %v, want nil", v.LastDeliveryAt)
	}
	if !strings.HasPrefix(got.Endpoint.ID, "whe_") {
		t.Fatalf("endpoint id = %q, want a whe_ prefix", got.Endpoint.ID)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]struct {
		mutate func(*CreateInput)
		want   string
	}{
		"empty url":         {func(in *CreateInput) { in.URL = "" }, "url must be an http(s) URL"},
		"non-http scheme":   {func(in *CreateInput) { in.URL = "file:///etc/passwd" }, "url must be an http(s) URL"},
		"no host":           {func(in *CreateInput) { in.URL = "https://" }, "url must be an http(s) URL"},
		"no events":         {func(in *CreateInput) { in.Events = nil }, "at least one event is required"},
		"unknown event":     {func(in *CreateInput) { in.Events = []string{"scale.changed"} }, "unknown event: scale.changed"},
		"ping not settable": {func(in *CreateInput) { in.Events = []string{domain.EventWebhookPing} }, "webhook.ping is delivered by the ping action, not subscribed to"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			in := validInput()
			tc.mutate(&in)
			_, err := svc.Create(context.Background(), "prj_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if ve.Msg != tc.want {
				t.Fatalf("message = %q, want %q", ve.Msg, tc.want)
			}
		})
	}
}

func TestCreateDeduplicatesEvents(t *testing.T) {
	svc, _, _ := newTestService(t)
	in := validInput()
	in.Events = []string{domain.EventDeployFailed, domain.EventDeployFailed, domain.EventBackupFailed}
	got, err := svc.Create(context.Background(), "prj_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(got.Endpoint.Events) != 2 {
		t.Fatalf("events = %v, want the duplicate collapsed", got.Endpoint.Events)
	}
}

func TestCreateUnknownProject(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.Create(context.Background(), "prj_missing", validInput()); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

// The URL is the endpoint's identity: a second endpoint on the same URL would
// silently double every delivery (spec §2).
func TestCreateRejectsDuplicateURL(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.Create(context.Background(), "prj_1", validInput()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), "prj_1", validInput()); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second create on the same URL = %v, want ErrConflict", err)
	}
}

func TestUpdateKeepsSecretAndRotateReplacesIt(t *testing.T) {
	svc, st, _ := newTestService(t)
	created, err := svc.Create(context.Background(), "prj_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.Endpoint.ID

	if _, err := svc.Update(context.Background(), id, UpdateInput{
		URL:     "https://ops.meridian.dev/hooks/v2",
		Events:  []string{domain.EventBackupFailed},
		Enabled: false,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, _ := st.GetWebhookEndpoint(context.Background(), id)
	if string(after.SecretCT) != string(created.Endpoint.SecretCT) {
		t.Fatal("update changed the signing secret; only rotate-secret may")
	}
	if after.Enabled {
		t.Fatal("update did not disable the endpoint")
	}

	rotated, err := svc.RotateSecret(context.Background(), id)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if rotated == created.Secret {
		t.Fatal("rotate returned the same secret")
	}
	reread, _ := st.GetWebhookEndpoint(context.Background(), id)
	if string(reread.SecretCT) != sealedPrefix+rotated {
		t.Fatalf("stored secret %q does not match the returned one", reread.SecretCT)
	}
}

// A disabled endpoint reports health "unknown" and refuses ping: nothing is
// being attempted, so we do not claim health (spec §4, §8 case 5).
func TestDisabledEndpointIsUnknownAndRefusesPing(t *testing.T) {
	svc, st, _ := newTestService(t)
	e := st.seedEndpoint("whe_off", "prj_1", "https://r.example/h", "s3cret", []string{domain.EventDeployFailed}, false)

	v, err := svc.View(context.Background(), e.ID)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Health != domain.EndpointHealthUnknown {
		t.Fatalf("disabled health = %q, want unknown", v.Health)
	}
	if _, err := svc.Ping(context.Background(), e.ID); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("ping on a disabled endpoint = %v, want ErrEndpointDisabled", err)
	}
}

func TestHealthDerivation(t *testing.T) {
	f, s := domain.DeliveryFailed, domain.DeliverySucceeded
	cases := map[string]struct {
		enabled  bool
		statuses []string
		want     string
	}{
		"disabled":            {false, []string{s, s}, domain.EndpointHealthUnknown},
		"nothing delivered":   {true, nil, domain.EndpointHealthUnknown},
		"all succeeded":       {true, []string{s, s, s}, domain.EndpointHealthy},
		"newest failed":       {true, []string{f, s, s}, domain.EndpointFailing},
		"older failure only":  {true, []string{s, f, s}, domain.EndpointDegraded},
		"every one failed":    {true, []string{f, f}, domain.EndpointFailing},
		"single success only": {true, []string{s}, domain.EndpointHealthy},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Health(tc.enabled, tc.statuses); got != tc.want {
				t.Fatalf("Health(%v, %v) = %q, want %q", tc.enabled, tc.statuses, got, tc.want)
			}
		})
	}
}

// Seek paging: the newest page comes back with a cursor, following it twice
// yields the rest with no repeats and no gaps (spec §7, §8 case 6).
func TestDeliveriesSeekPaging(t *testing.T) {
	svc, st, _ := newTestService(t)
	e := st.seedEndpoint("whe_p", "prj_1", "https://r.example/h", "s", []string{domain.EventDeployFailed}, true)

	const total = 120
	for i := 0; i < total; i++ {
		if _, err := st.CreateWebhookDelivery(context.Background(), domain.WebhookDelivery{
			// Ids sort lexicographically, so a zero-padded suffix makes the
			// (created_at, id) DESC tie-break deterministic under a fixed clock.
			ID:         "whd_" + pad(i),
			EndpointID: e.ID,
			EventType:  domain.EventDeployFailed,
			Status:     domain.DeliverySucceeded,
		}); err != nil {
			t.Fatalf("CreateWebhookDelivery: %v", err)
		}
	}

	seen := map[string]bool{}
	before := ""
	pages := 0
	for {
		page, err := svc.Deliveries(context.Background(), e.ID, 50, before)
		if err != nil {
			t.Fatalf("Deliveries: %v", err)
		}
		pages++
		for _, v := range page.Deliveries {
			if seen[v.Delivery.ID] {
				t.Fatalf("delivery %s repeated across pages", v.Delivery.ID)
			}
			seen[v.Delivery.ID] = true
		}
		if pages == 1 && len(page.Deliveries) != 50 {
			t.Fatalf("first page = %d rows, want 50", len(page.Deliveries))
		}
		if page.NextBefore == "" {
			break
		}
		before = page.NextBefore
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("saw %d deliveries across %d pages, want %d with no gaps", len(seen), pages, total)
	}
	if pages != 3 {
		t.Fatalf("paged in %d requests, want 3 (50 + 50 + 20)", pages)
	}
}

func TestDeliveriesLimitCapsAtHundred(t *testing.T) {
	svc, st, _ := newTestService(t)
	e := st.seedEndpoint("whe_c", "prj_1", "https://r.example/h", "s", []string{domain.EventDeployFailed}, true)
	for i := 0; i < 150; i++ {
		if _, err := st.CreateWebhookDelivery(context.Background(), domain.WebhookDelivery{
			ID: "whd_" + pad(i), EndpointID: e.ID, EventType: domain.EventDeployFailed, Status: domain.DeliverySucceeded,
		}); err != nil {
			t.Fatalf("CreateWebhookDelivery: %v", err)
		}
	}
	page, err := svc.Deliveries(context.Background(), e.ID, 5000, "")
	if err != nil {
		t.Fatalf("Deliveries: %v", err)
	}
	if len(page.Deliveries) != maxDeliveryLimit {
		t.Fatalf("limit=5000 returned %d rows, want the %d cap", len(page.Deliveries), maxDeliveryLimit)
	}
}

// The feed line reads "deploy.succeeded · web · 200 · 84ms", so each page row
// carries the outcome of its most recent attempt — nil until one lands, and nil
// for response status when the attempt ended in a transport error (spec §7).
func TestDeliveryPageCarriesTheLastAttemptOutcome(t *testing.T) {
	svc, st, _ := newTestService(t)
	e := st.seedEndpoint("whe_v", "prj_1", "https://r.example/h", "s", []string{domain.EventDeployFailed}, true)
	ctx := context.Background()

	for _, id := range []string{"whd_001", "whd_002", "whd_003"} {
		if _, err := st.CreateWebhookDelivery(ctx, domain.WebhookDelivery{
			ID: id, EndpointID: e.ID, EventType: domain.EventDeployFailed, Status: domain.DeliverySucceeded,
		}); err != nil {
			t.Fatalf("CreateWebhookDelivery: %v", err)
		}
	}
	ok := 200
	// whd_003 got two attempts — the newest one wins.
	mustAttempt(t, st, domain.WebhookDeliveryAttempt{DeliveryID: "whd_003", Attempt: 1, DurationMS: 900, Error: "dial tcp: connection refused"})
	mustAttempt(t, st, domain.WebhookDeliveryAttempt{DeliveryID: "whd_003", Attempt: 2, ResponseStatus: &ok, DurationMS: 84})
	// whd_002 only ever hit a transport error, so it has a duration but no status.
	mustAttempt(t, st, domain.WebhookDeliveryAttempt{DeliveryID: "whd_002", Attempt: 1, DurationMS: 10001, Error: "context deadline exceeded"})
	// whd_001 has never been attempted.

	page, err := svc.Deliveries(ctx, e.ID, 50, "")
	if err != nil {
		t.Fatalf("Deliveries: %v", err)
	}
	got := map[string]DeliveryView{}
	for _, v := range page.Deliveries {
		got[v.Delivery.ID] = v
	}
	if v := got["whd_003"]; v.ResponseStatus == nil || *v.ResponseStatus != 200 || v.DurationMS == nil || *v.DurationMS != 84 {
		t.Fatalf("whd_003 = %v/%v, want the newest attempt's 200/84ms", v.ResponseStatus, v.DurationMS)
	}
	if v := got["whd_002"]; v.ResponseStatus != nil || v.DurationMS == nil || *v.DurationMS != 10001 {
		t.Fatalf("whd_002 = %v/%v, want no status and a duration", v.ResponseStatus, v.DurationMS)
	}
	if v := got["whd_001"]; v.ResponseStatus != nil || v.DurationMS != nil {
		t.Fatalf("whd_001 = %v/%v, want both nil before the first attempt", v.ResponseStatus, v.DurationMS)
	}
}

func mustAttempt(t *testing.T, st *fakeStore, a domain.WebhookDeliveryAttempt) {
	t.Helper()
	if _, err := st.CreateWebhookDeliveryAttempt(context.Background(), a); err != nil {
		t.Fatalf("CreateWebhookDeliveryAttempt: %v", err)
	}
}

func TestRedeliverResolvesEndpointAndRefusesDisabled(t *testing.T) {
	svc, st, disp := newTestService(t)
	e := st.seedEndpoint("whe_r", "prj_1", "https://r.example/h", "s", []string{domain.EventDeployFailed}, true)
	orig, err := st.CreateWebhookDelivery(context.Background(), domain.WebhookDelivery{
		ID: "whd_orig", EndpointID: e.ID, EventType: domain.EventDeployFailed, Status: domain.DeliveryFailed,
	})
	if err != nil {
		t.Fatalf("CreateWebhookDelivery: %v", err)
	}

	replay, err := svc.Redeliver(context.Background(), orig.ID)
	if err != nil {
		t.Fatalf("Redeliver: %v", err)
	}
	if replay.RedeliveryOf == nil || *replay.RedeliveryOf != orig.ID {
		t.Fatalf("redelivery_of = %v, want %s", replay.RedeliveryOf, orig.ID)
	}
	if disp.redelivers != 1 {
		t.Fatalf("dispatcher redeliveries = %d, want 1", disp.redelivers)
	}

	// Disabling the endpoint refuses the replay rather than queueing work
	// nothing will send.
	e.Enabled = false
	if _, err := st.UpdateWebhookEndpoint(context.Background(), e); err != nil {
		t.Fatalf("UpdateWebhookEndpoint: %v", err)
	}
	if _, err := svc.Redeliver(context.Background(), orig.ID); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("redeliver on a disabled endpoint = %v, want ErrEndpointDisabled", err)
	}
}

func TestGetMissingEndpoint(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.Get(context.Background(), "whe_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

// pad renders a zero-padded three-digit suffix so generated ids sort in
// insertion order, making the (created_at, id) DESC tie-break deterministic
// under a fixed clock.
func pad(i int) string {
	return fmt.Sprintf("%03d", i)
}

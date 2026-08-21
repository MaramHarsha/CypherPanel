package rest

// HTTP + authorization tests for the outbound webhook routes
// (outbound-webhooks.md §6, §7). Every route is project-scoped at RoleMember —
// the same rank as notifiers, deliberately, because an endpoint has the
// identical risk shape. A non-member gets 404 (no tenancy probing); a token
// without the `write` ability gets 403 on every mutation.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/webhooks"
)

// fakeWebhookService implements WebhookEndpointService with just enough state
// to exercise routing, authorization and DTO shape.
type fakeWebhookService struct {
	endpoints  map[string]domain.WebhookEndpoint
	deliveries map[string]domain.WebhookDelivery
	rotations  int
	pings      int
	lastLimit  int
	lastBefore string
}

func newFakeWebhookService() *fakeWebhookService {
	return &fakeWebhookService{
		endpoints: map[string]domain.WebhookEndpoint{
			"whe_test": {
				ID: "whe_test", ProjectID: "prj_test",
				URL:         "https://ops.meridian.dev/hooks/cypher",
				SecretCT:    []byte("sealed"),
				SecretNonce: []byte("n"),
				Events:      []string{domain.EventDeploySucceeded},
				Enabled:     true,
			},
			"whe_off": {
				ID: "whe_off", ProjectID: "prj_test",
				URL:         "https://ops.meridian.dev/hooks/off",
				SecretCT:    []byte("sealed"),
				SecretNonce: []byte("n"),
				Events:      []string{domain.EventDeployFailed},
				Enabled:     false,
			},
		},
		deliveries: map[string]domain.WebhookDelivery{
			"whd_test": {
				ID: "whd_test", EndpointID: "whe_test",
				EventType: domain.EventDeployFailed, ResourceKind: domain.WebhookResourceApplication,
				ResourceID: "app_x", ResourceName: "web", Status: domain.DeliveryFailed, Attempt: 4,
			},
			"whd_off": {
				ID: "whd_off", EndpointID: "whe_off",
				EventType: domain.EventDeployFailed, ResourceKind: domain.WebhookResourceApplication,
				ResourceID: "app_x", ResourceName: "web", Status: domain.DeliveryFailed, Attempt: 4,
			},
		},
	}
}

func (f *fakeWebhookService) Create(_ context.Context, projectID string, in webhooks.CreateInput) (webhooks.Created, error) {
	if projectID != "prj_test" {
		return webhooks.Created{}, webhooks.ErrProjectNotFound
	}
	e := domain.WebhookEndpoint{
		ID: "whe_new", ProjectID: projectID, URL: in.URL, Events: in.Events, Enabled: in.Enabled,
		SecretCT: []byte("sealed"), SecretNonce: []byte("n"),
	}
	f.endpoints[e.ID] = e
	return webhooks.Created{Endpoint: e, Secret: "RAWSIGNINGSECRET"}, nil
}

func (f *fakeWebhookService) Update(_ context.Context, id string, in webhooks.UpdateInput) (webhooks.EndpointView, error) {
	e, ok := f.endpoints[id]
	if !ok {
		return webhooks.EndpointView{}, store.ErrNotFound
	}
	e.URL, e.Events, e.Enabled = in.URL, in.Events, in.Enabled
	f.endpoints[id] = e
	return webhooks.EndpointView{Endpoint: e, Health: domain.EndpointHealthUnknown}, nil
}

func (f *fakeWebhookService) Get(_ context.Context, id string) (domain.WebhookEndpoint, error) {
	e, ok := f.endpoints[id]
	if !ok {
		return domain.WebhookEndpoint{}, store.ErrNotFound
	}
	return e, nil
}

func (f *fakeWebhookService) View(_ context.Context, id string) (webhooks.EndpointView, error) {
	e, ok := f.endpoints[id]
	if !ok {
		return webhooks.EndpointView{}, store.ErrNotFound
	}
	return webhooks.EndpointView{Endpoint: e, Health: domain.EndpointHealthy}, nil
}

func (f *fakeWebhookService) ListViews(_ context.Context, projectID string) ([]webhooks.EndpointView, error) {
	out := []webhooks.EndpointView{}
	for _, e := range f.endpoints {
		if e.ProjectID == projectID {
			out = append(out, webhooks.EndpointView{Endpoint: e, Health: domain.EndpointHealthy})
		}
	}
	return out, nil
}

func (f *fakeWebhookService) Delete(_ context.Context, id string) error {
	if _, ok := f.endpoints[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.endpoints, id)
	return nil
}

func (f *fakeWebhookService) RotateSecret(_ context.Context, id string) (string, error) {
	if _, ok := f.endpoints[id]; !ok {
		return "", store.ErrNotFound
	}
	f.rotations++
	return "ROTATEDSIGNINGSECRET", nil
}

func (f *fakeWebhookService) Ping(_ context.Context, id string) (domain.WebhookDelivery, error) {
	e, ok := f.endpoints[id]
	if !ok {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	if !e.Enabled {
		return domain.WebhookDelivery{}, webhooks.ErrEndpointDisabled
	}
	f.pings++
	return domain.WebhookDelivery{ID: "whd_ping", EndpointID: id, EventType: domain.EventWebhookPing, Status: domain.DeliveryPending}, nil
}

func (f *fakeWebhookService) Deliveries(_ context.Context, endpointID string, limit int, before string) (webhooks.Page, error) {
	if _, ok := f.endpoints[endpointID]; !ok {
		return webhooks.Page{}, store.ErrNotFound
	}
	f.lastLimit, f.lastBefore = limit, before
	status, ms := 500, 84
	return webhooks.Page{
		Deliveries: []webhooks.DeliveryView{{
			Delivery: f.deliveries["whd_test"], ResponseStatus: &status, DurationMS: &ms,
		}},
		NextBefore: "whd_older",
	}, nil
}

func (f *fakeWebhookService) GetDelivery(_ context.Context, id string) (domain.WebhookDelivery, error) {
	d, ok := f.deliveries[id]
	if !ok {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeWebhookService) Redeliver(_ context.Context, deliveryID string) (domain.WebhookDelivery, error) {
	orig, ok := f.deliveries[deliveryID]
	if !ok {
		return domain.WebhookDelivery{}, store.ErrNotFound
	}
	if e := f.endpoints[orig.EndpointID]; !e.Enabled {
		return domain.WebhookDelivery{}, webhooks.ErrEndpointDisabled
	}
	return domain.WebhookDelivery{ID: "whd_replay", EndpointID: orig.EndpointID, EventType: orig.EventType, RedeliveryOf: &orig.ID}, nil
}

// newWebhookServer wires a server whose user holds panelRole on the panel and
// projectRole in prj_test's team (empty = not a member at all).
func newWebhookServer(t *testing.T, panelRole, projectRole string) (*httptest.Server, *fakeWebhookService) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: panelRole},
		sessions: map[string]domain.User{},
	}
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ft := newFakeTeams()
	if projectRole != "" {
		ft.projectRoles["usr_test"] = map[string]string{"prj_test": projectRole}
	}
	ft.teams = nil

	svc := newFakeWebhookService()
	api := New(Deps{
		Auth:             auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:         projects.NewService(newFakeProjectsStore()),
		Applications:     applications.NewService(newFakeAppsStore(), box),
		WebhookEndpoints: svc,
		Teams:            ft,
		Opener:           box,
		Pinger:           okPinger{},
		CACertPEM:        []byte("x"),
		Log:              log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, svc
}

// webhookRoutes is every route this feature adds, with a body where one is
// needed. Used to assert the tenancy rule uniformly. Deletion sits last so a
// table walked in order does not remove the row the later rows address.
var webhookRoutes = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"create endpoint", "POST", "/api/v1/projects/prj_test/webhook-endpoints", `{"url":"https://r.example/h","events":["deploy.failed"]}`},
	{"list endpoints", "GET", "/api/v1/projects/prj_test/webhook-endpoints", ""},
	{"get endpoint", "GET", "/api/v1/webhook-endpoints/whe_test", ""},
	{"patch endpoint", "PATCH", "/api/v1/webhook-endpoints/whe_test", `{"url":"https://r.example/h","events":["deploy.failed"]}`},
	{"rotate secret", "POST", "/api/v1/webhook-endpoints/whe_test/rotate-secret", ""},
	{"ping endpoint", "POST", "/api/v1/webhook-endpoints/whe_test/ping", ""},
	{"list deliveries", "GET", "/api/v1/webhook-endpoints/whe_test/deliveries", ""},
	{"redeliver", "POST", "/api/v1/webhook-deliveries/whd_test/redeliver", ""},
	{"delete endpoint", "DELETE", "/api/v1/webhook-endpoints/whe_test", ""},
}

// A project you cannot see does not exist: every route answers 404, never 403,
// so membership is not probeable.
func TestNonMemberGets404OnEveryWebhookRoute(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleMember, "") // panel member, no project role
	token := login(t, ts)
	for _, r := range webhookRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status != http.StatusNotFound {
			t.Errorf("%s as a non-member = %d, want 404 (body %s)", r.name, status, body)
		}
	}
}

// A project member holds the minimum rank every webhook route needs — the same
// rank as notifiers (spec §6).
func TestProjectMemberReachesEveryWebhookRoute(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleMember, domain.RoleMember)
	token := login(t, ts)
	for _, r := range webhookRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status == http.StatusNotFound || status == http.StatusForbidden {
			t.Errorf("%s as a member = %d, want it authorized (body %s)", r.name, status, body)
		}
	}
}

// The rank gate is real: a caller who is not in the project's team is refused
// even when they hold a high panel role that is not owner.
func TestPanelAdminWithoutMembershipIsRefused(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleAdmin, "")
	token := login(t, ts)
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/webhook-endpoints/whe_test", token, ""); status != http.StatusNotFound {
		t.Fatalf("panel admin, non-member GET = %d, want 404 (body %s)", status, body)
	}
}

// A token narrower than its owner is refused with 403 — the "insufficient
// privilege" answer on these routes, since their role floor is already the
// minimum rank (spec §6).
func TestWebhookMutationsRequireTheWriteAbility(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	session := login(t, ts)
	readOnly := createToken(t, ts, session, "readonly", `["read"]`)

	for _, r := range webhookRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, readOnly, r.body)
		if r.method == http.MethodGet {
			if status == http.StatusForbidden {
				t.Errorf("%s with a read token = 403, want it allowed (body %s)", r.name, body)
			}
			continue
		}
		if status != http.StatusForbidden {
			t.Errorf("%s with a read-only token = %d, want 403 (body %s)", r.name, status, body)
		}
	}
}

// The signing secret is returned exactly once, at create, and is structurally
// absent from every other response (ENGINEERING rule 20, spec §8 case 1).
func TestSigningSecretIsShownOnceAndNeverAgain(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/webhook-endpoints", token,
		`{"url":"https://ops.meridian.dev/hooks/new","events":["deploy.failed"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, body %s", status, body)
	}
	var created struct {
		Endpoint map[string]any `json:"endpoint"`
		Secret   string         `json:"secret"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response %s: %v", body, err)
	}
	if created.Secret != "RAWSIGNINGSECRET" {
		t.Fatalf("create did not return the signing secret: %s", body)
	}
	if _, leaked := created.Endpoint["secret"]; leaked {
		t.Fatalf("the endpoint object carries a secret field: %s", body)
	}
	for _, key := range []string{"secret_ct", "secret_nonce"} {
		if _, leaked := created.Endpoint[key]; leaked {
			t.Fatalf("the endpoint object carries %s: %s", key, body)
		}
	}
	if created.Endpoint["health"] != domain.EndpointHealthUnknown {
		t.Fatalf("a fresh endpoint reports health %v, want unknown", created.Endpoint["health"])
	}

	// No later read carries it back.
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/webhook-endpoints/whe_new", ""},
		{"GET", "/api/v1/projects/prj_test/webhook-endpoints", ""},
		{"PATCH", "/api/v1/webhook-endpoints/whe_new", `{"enabled":false}`},
	} {
		_, _, out := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if strings.Contains(string(out), "RAWSIGNINGSECRET") || strings.Contains(string(out), "secret") {
			t.Fatalf("%s %s leaked secret material: %s", r.method, r.path, out)
		}
	}
}

// Rotate returns the new secret once, and nothing else.
func TestRotateSecretReturnsTheSecretOnly(t *testing.T) {
	ts, svc := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/webhook-endpoints/whe_test/rotate-secret", token, "")
	if status != http.StatusOK {
		t.Fatalf("rotate = %d, body %s", status, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("rotate response %s: %v", body, err)
	}
	if got["secret"] != "ROTATEDSIGNINGSECRET" || len(got) != 1 {
		t.Fatalf("rotate body = %s, want exactly the new secret", body)
	}
	if svc.rotations != 1 {
		t.Fatalf("rotations = %d, want 1", svc.rotations)
	}
}

// Ping is refused on a disabled endpoint rather than queueing work nothing will
// send (spec §7, §8 case 5); so is a redelivery through it.
func TestDisabledEndpointRefusesPingAndRedeliver(t *testing.T) {
	ts, _ := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/webhook-endpoints/whe_off/ping", token, ""); status != http.StatusConflict {
		t.Fatalf("ping on a disabled endpoint = %d, want 409 (body %s)", status, body)
	}
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/webhook-deliveries/whd_off/redeliver", token, ""); status != http.StatusConflict {
		t.Fatalf("redeliver through a disabled endpoint = %d, want 409 (body %s)", status, body)
	}
}

func TestPingAndRedeliverAreAccepted(t *testing.T) {
	ts, svc := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/webhook-endpoints/whe_test/ping", token, "")
	if status != http.StatusAccepted {
		t.Fatalf("ping = %d, body %s", status, body)
	}
	var ping webhookDeliveryDTO
	if err := json.Unmarshal(body, &ping); err != nil {
		t.Fatalf("ping response %s: %v", body, err)
	}
	if ping.EventType != domain.EventWebhookPing {
		t.Fatalf("ping event_type = %q, want webhook.ping", ping.EventType)
	}
	if svc.pings != 1 {
		t.Fatalf("pings = %d, want 1", svc.pings)
	}

	status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/webhook-deliveries/whd_test/redeliver", token, "")
	if status != http.StatusAccepted {
		t.Fatalf("redeliver = %d, body %s", status, body)
	}
	var replay webhookDeliveryDTO
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatalf("redeliver response %s: %v", body, err)
	}
	if replay.RedeliveryOf == nil || *replay.RedeliveryOf != "whd_test" {
		t.Fatalf("redelivery_of = %v, want whd_test", replay.RedeliveryOf)
	}
}

// The delivery log pages with ?limit= and ?before=, and rejects a limit that is
// not a positive integer.
func TestDeliveryLogPagingParameters(t *testing.T) {
	ts, svc := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/webhook-endpoints/whe_test/deliveries?limit=25&before=whd_cursor", token, "")
	if status != http.StatusOK {
		t.Fatalf("deliveries = %d, body %s", status, body)
	}
	if svc.lastLimit != 25 || svc.lastBefore != "whd_cursor" {
		t.Fatalf("service saw limit=%d before=%q, want 25/whd_cursor", svc.lastLimit, svc.lastBefore)
	}
	var page deliveryPageDTO
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("page response %s: %v", body, err)
	}
	if len(page.Deliveries) != 1 || page.NextBefore != "whd_older" {
		t.Fatalf("page = %+v, want one delivery plus a cursor", page)
	}
	// The feed line is "deploy.failed - web - 500 - 84ms": the last attempt's
	// outcome rides along with the delivery (spec section 7).
	row := page.Deliveries[0]
	if row.ResponseStatus == nil || *row.ResponseStatus != 500 || row.DurationMS == nil || *row.DurationMS != 84 {
		t.Fatalf("feed row = %v/%v, want 500 and 84ms", row.ResponseStatus, row.DurationMS)
	}
	// The body is not part of the feed: it is only ever needed by the receiver.
	if strings.Contains(string(body), "payload") {
		t.Fatalf("delivery DTO carries a payload: %s", body)
	}

	// Omitting limit leaves the default to the service (0 = default).
	if _, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/webhook-endpoints/whe_test/deliveries", token, ""); svc.lastLimit != 0 {
		t.Fatalf("omitted limit forwarded %d, want 0 so the service applies its default", svc.lastLimit)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/webhook-endpoints/whe_test/deliveries?limit=0", token, ""); status != http.StatusBadRequest {
		t.Fatalf("limit=0 = %d, want 400", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/webhook-endpoints/whe_test/deliveries?limit=x", token, ""); status != http.StatusBadRequest {
		t.Fatalf("limit=x = %d, want 400", status)
	}
}

// PATCH keeps what the request omits (the repo's uniform semantics).
func TestPatchWebhookEndpointKeepsOmittedFields(t *testing.T) {
	ts, svc := newWebhookServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	before := svc.endpoints["whe_test"]

	status, _, body := doJSON(t, "PATCH", ts.URL+"/api/v1/webhook-endpoints/whe_test", token, `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("patch = %d, body %s", status, body)
	}
	after := svc.endpoints["whe_test"]
	if after.URL != before.URL {
		t.Fatalf("url changed to %q on a patch that omitted it", after.URL)
	}
	if len(after.Events) != len(before.Events) || after.Events[0] != before.Events[0] {
		t.Fatalf("events changed to %v on a patch that omitted them", after.Events)
	}
	if after.Enabled {
		t.Fatal("patch did not apply enabled=false")
	}
}

// Without the service wired the routes must degrade, never nil-panic: list
// answers an empty array, item routes answer 404.
func TestWebhookRoutesDegradeWhenNotEnabled(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t) // Deps.WebhookEndpoints is nil here
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_test/webhook-endpoints", token, "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("list without the service = %d %s, want 200 []", status, body)
	}
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/webhook-endpoints", token, `{"url":"https://r.example/h","events":["deploy.failed"]}`); status != http.StatusNotImplemented {
		t.Fatalf("create without the service = %d, want 501", status)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/webhook-endpoints/whe_test"},
		{"DELETE", "/api/v1/webhook-endpoints/whe_test"},
		{"POST", "/api/v1/webhook-endpoints/whe_test/ping"},
		{"GET", "/api/v1/webhook-endpoints/whe_test/deliveries"},
		{"POST", "/api/v1/webhook-deliveries/whd_test/redeliver"},
	} {
		if status, _, _ := doJSON(t, r.method, ts.URL+r.path, token, ""); status != http.StatusNotFound {
			t.Fatalf("%s %s without the service = %d, want 404", r.method, r.path, status)
		}
	}
}

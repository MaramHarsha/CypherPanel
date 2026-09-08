package rest

// HTTP + authorization tests for the panel TLS routes and the derived
// Application.tls_state (agent-identity-and-tls.md §4–5). The account is
// owner-only: it decides how every routed application on every server is served
// to the public internet.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/paneltls"
	"github.com/MaramHarsha/cypherpanel/core/projects"
)

// fakePanelTLS is the service with just enough state to exercise routing,
// authorization and DTO shape, plus the validation the handler maps onto 400.
type fakePanelTLS struct {
	mu       sync.Mutex
	settings domain.PanelTLS
	getErr   error
}

func (f *fakePanelTLS) Get(context.Context) (domain.PanelTLS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return domain.PanelTLS{}, f.getErr
	}
	return f.settings, nil
}

func (f *fakePanelTLS) Set(_ context.Context, in paneltls.Input) (domain.PanelTLS, error) {
	if in.ACMEEmail != "" && !strings.Contains(in.ACMEEmail, "@") {
		return domain.PanelTLS{}, paneltls.ErrInvalidEmail
	}
	if in.ACMECAServer != "" && !strings.HasPrefix(in.ACMECAServer, "http") {
		return domain.PanelTLS{}, paneltls.ErrInvalidCAServer
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ACMEEmail == "" {
		f.settings = domain.PanelTLS{}
		return f.settings, nil
	}
	f.settings = domain.PanelTLS{
		ACMEEmail:    in.ACMEEmail,
		ACMECAServer: in.ACMECAServer,
		UpdatedAt:    time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
	}
	return f.settings, nil
}

func (f *fakePanelTLS) Configured(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return false, f.getErr
	}
	return f.settings.Configured(), nil
}

// newPanelTLSServer wires a server whose user holds panelRole. tls may be nil,
// which models a panel where the feature is not wired at all.
func newPanelTLSServer(t *testing.T, panelRole string, tls PanelTLSService) *httptest.Server {
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
	api := New(Deps{
		Auth:         auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), box),
		Teams:        newFakeTeams(),
		PanelTLS:     tls,
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestPanelTLSRoundTrip(t *testing.T) {
	ts := newPanelTLSServer(t, domain.RoleOwner, &fakePanelTLS{})
	token := login(t, ts)

	// A fresh panel: unconfigured is a normal state, not an error.
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/tls", token, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d body %s", status, body)
	}
	var got struct {
		Configured   bool   `json:"configured"`
		ACMEEmail    string `json:"acme_email"`
		ACMECAServer string `json:"acme_ca_server"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Configured || got.ACMEEmail != "" {
		t.Fatalf("fresh panel reports %+v", got)
	}

	// Set it.
	status, _, body = doJSON(t, "PUT", ts.URL+"/api/v1/panel/tls", token,
		`{"acme_email":"ops@example.com","acme_ca_server":"https://acme-staging-v02.api.letsencrypt.org/directory"}`)
	if status != http.StatusOK {
		t.Fatalf("put: status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Configured || got.ACMEEmail != "ops@example.com" {
		t.Fatalf("put response = %+v", got)
	}

	// Read back.
	_, _, body = doJSON(t, "GET", ts.URL+"/api/v1/panel/tls", token, "")
	if !strings.Contains(string(body), "ops@example.com") {
		t.Fatalf("get after put = %s", body)
	}

	// Empty email clears: "no email" and "no ACME" are the same statement.
	status, _, body = doJSON(t, "PUT", ts.URL+"/api/v1/panel/tls", token, `{"acme_email":""}`)
	if status != http.StatusOK {
		t.Fatalf("clear: status %d body %s", status, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Configured {
		t.Fatalf("clear left the account configured: %+v", got)
	}
}

func TestPanelTLSValidationFailures(t *testing.T) {
	ts := newPanelTLSServer(t, domain.RoleOwner, &fakePanelTLS{})
	token := login(t, ts)

	for _, tc := range []struct{ name, body string }{
		{"not an address", `{"acme_email":"ops-at-example.com"}`},
		{"relative ca server", `{"acme_email":"ops@example.com","acme_ca_server":"/directory"}`},
		{"unknown field", `{"acme_email":"ops@example.com","nope":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/panel/tls", token, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status %d body %s, want 400", status, body)
			}
		})
	}
}

// Owner-only: an admin can add servers and deploy keys but cannot decide how the
// whole fleet terminates TLS.
func TestPanelTLSRequiresOwner(t *testing.T) {
	for _, role := range []string{domain.RoleMember, domain.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			ts := newPanelTLSServer(t, role, &fakePanelTLS{})
			token := login(t, ts)
			for _, r := range []struct{ method, body string }{
				{"GET", ""},
				{"PUT", `{"acme_email":"ops@example.com"}`},
			} {
				status, _, body := doJSON(t, r.method, ts.URL+"/api/v1/panel/tls", token, r.body)
				if status != http.StatusForbidden {
					t.Fatalf("%s as %s: status %d body %s, want 403", r.method, role, status, body)
				}
			}
		})
	}
}

func TestPanelTLSRequiresAuthentication(t *testing.T) {
	ts := newPanelTLSServer(t, domain.RoleOwner, &fakePanelTLS{})
	for _, r := range []struct{ method, body string }{
		{"GET", ""},
		{"PUT", `{"acme_email":"ops@example.com"}`},
	} {
		status, _, _ := doJSON(t, r.method, ts.URL+"/api/v1/panel/tls", "", r.body)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: status %d, want 401", r.method, status)
		}
	}
}

// ─── the derived route state (§5) ───────────────────────────────────────────

func createRoutedApp(t *testing.T, ts *httptest.Server, token, name, domainName string, https bool) map[string]any {
	t.Helper()
	body := `{"name":"` + name + `","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},` +
		`"route":{"domain":"` + domainName + `","https":` + boolText(https) + `,"path_prefix":""}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create %s: status %d body %s", name, status, resp)
	}
	var created struct {
		Application map[string]any `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return created.Application
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// The honesty property: an https route on a panel with no ACME account reports
// http_only_no_resolver, because that is what the node is actually serving.
func TestApplicationTLSStateFollowsThePanelAccount(t *testing.T) {
	tls := &fakePanelTLS{}
	ts := newPanelTLSServer(t, domain.RoleOwner, tls)
	token := login(t, ts)

	app := createRoutedApp(t, ts, token, "web", "app.example.com", true)
	if got := app["tls_state"]; got != domain.TLSStateHTTPOnlyNoResolver {
		t.Fatalf("tls_state on create = %v, want %s", got, domain.TLSStateHTTPOnlyNoResolver)
	}
	appID, _ := app["id"].(string)

	// Configure TLS: the same application now reports https.
	if status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/panel/tls", token, `{"acme_email":"ops@example.com"}`); status != http.StatusOK {
		t.Fatalf("put tls: %d %s", status, body)
	}
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/applications/"+appID, token, "")
	if status != http.StatusOK {
		t.Fatalf("get app: %d %s", status, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["tls_state"] != domain.TLSStateHTTPS {
		t.Fatalf("tls_state = %v after configuring TLS, want %s", got["tls_state"], domain.TLSStateHTTPS)
	}
}

// http_only (asked for) and omitted (no route at all) are distinct from
// http_only_no_resolver (asked for HTTPS, cannot have it).
func TestApplicationTLSStateDistinguishesDeliberateHTTP(t *testing.T) {
	ts := newPanelTLSServer(t, domain.RoleOwner, &fakePanelTLS{})
	token := login(t, ts)

	plain := createRoutedApp(t, ts, token, "plain", "plain.example.com", false)
	if plain["tls_state"] != domain.TLSStateHTTPOnly {
		t.Fatalf("tls_state for an http-by-choice route = %v, want %s", plain["tls_state"], domain.TLSStateHTTPOnly)
	}

	unrouted := createRoutedApp(t, ts, token, "internal", "", false)
	if v, present := unrouted["tls_state"]; present {
		t.Fatalf("tls_state = %v on an application with no domain; it should be omitted", v)
	}
}

// A panel where the feature is unwired must answer "no resolver" rather than
// assume one: an assumed certificate is the false certainty this removes.
func TestApplicationTLSStateWithoutTheServiceIsHonest(t *testing.T) {
	ts := newPanelTLSServer(t, domain.RoleOwner, nil)
	token := login(t, ts)

	app := createRoutedApp(t, ts, token, "web", "app.example.com", true)
	if app["tls_state"] != domain.TLSStateHTTPOnlyNoResolver {
		t.Fatalf("tls_state = %v with no TLS service, want %s", app["tls_state"], domain.TLSStateHTTPOnlyNoResolver)
	}
	// And the routes themselves report "not available" rather than pretending.
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/panel/tls", token, ""); status != http.StatusOK {
		t.Fatalf("get with no service = %d, want 200 with configured:false", status)
	}
	if status, _, _ := doJSON(t, "PUT", ts.URL+"/api/v1/panel/tls", token, `{"acme_email":"ops@example.com"}`); status != http.StatusNotFound {
		t.Fatalf("put with no service = %d, want 404", status)
	}
}

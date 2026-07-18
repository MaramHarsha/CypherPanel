package rest

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

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

type fakeAuthStore struct {
	user     domain.User
	sessions map[string]domain.User // key: string(tokenHash)
}

func (f *fakeAuthStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	if email != f.user.Email {
		return domain.User{}, store.ErrNotFound
	}
	return f.user, nil
}

func (f *fakeAuthStore) CreateSession(_ context.Context, _, _ string, tokenHash []byte, _ time.Time) error {
	f.sessions[string(tokenHash)] = f.user
	return nil
}

func (f *fakeAuthStore) UserForSessionToken(_ context.Context, tokenHash []byte) (domain.User, error) {
	u, ok := f.sessions[string(tokenHash)]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeAuthStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	delete(f.sessions, string(tokenHash))
	return nil
}

type fakeServersStore struct {
	list []domain.Server
}

func (f *fakeServersStore) CreateServerWithToken(_ context.Context, serverID, name, _ string, _ []byte, _ time.Time) (domain.Server, error) {
	s := domain.Server{ID: serverID, Name: name, Status: domain.StatusUnknown, CreatedAt: time.Now()}
	f.list = append(f.list, s)
	return s, nil
}

func (f *fakeServersStore) ListServers(context.Context) ([]domain.Server, error) { return f.list, nil }

func (f *fakeServersStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	for _, s := range f.list {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Server{}, store.ErrNotFound
}

func (f *fakeServersStore) DeleteServer(context.Context, string) error { return nil }

type noopDisconnector struct{}

func (noopDisconnector) DisconnectAgent(string) error { return nil }

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// ─── harness ────────────────────────────────────────────────────────────────

const (
	testEmail    = "owner@example.com"
	testPassword = "correct-horse-battery"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: "owner"},
		sessions: map[string]domain.User{},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(Deps{
		Auth:       auth.NewAuthenticator(authStore, auth.NewLimiter(100, time.Minute), time.Hour),
		Servers:    servers.NewService(&fakeServersStore{}, noopDisconnector{}, 15*time.Minute, log),
		Pinger:     okPinger{},
		CACertPEM:  []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		EnrollAddr: "localhost:8443",
		NATSURL:    "tls://localhost:4222",
		Log:        log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, method, url, token, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

func login(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`)
	if status != http.StatusOK {
		t.Fatalf("login: status %d body %s", status, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &lr); err != nil || lr.Token == "" {
		t.Fatalf("login response %s: %v", body, err)
	}
	return lr.Token
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestProtectedRoutesRequireAuth(t *testing.T) {
	ts := newTestServer(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/servers"},
		{"POST", "/api/v1/servers"},
		{"GET", "/api/v1/servers/srv_x"},
		{"DELETE", "/api/v1/servers/srv_x"},
		{"GET", "/api/v1/auth/me"},
		{"POST", "/api/v1/auth/logout"},
	} {
		status, _, _ := doJSON(t, route.method, ts.URL+route.path, "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s without token: status %d, want 401", route.method, route.path, status)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ts := newTestServer(t)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"wrong"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", status)
	}
	// The message must not reveal whether the email exists.
	if !strings.Contains(string(body), "invalid email or password") {
		t.Errorf("unexpected error body: %s", body)
	}
}

func TestServerLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create: join instructions appear exactly once, with the raw token.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"web-1"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, body)
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
		Join struct {
			Token   string `json:"token"`
			Command string `json:"command"`
		} `json:"join"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.Join.Token == "" || !strings.Contains(created.Join.Command, created.Join.Token) {
		t.Errorf("join instructions incomplete: %+v", created.Join)
	}

	// List includes it; get returns it; unknown id is 404.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/servers", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), created.Server.ID) {
		t.Errorf("list: status %d body %s", status, body)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusOK {
		t.Errorf("get: status %d", status)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/servers/srv_missing", token, "")
	if status != http.StatusNotFound {
		t.Errorf("get missing: status %d, want 404", status)
	}

	// Invalid name is a 400, not a 500.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":""}`)
	if status != http.StatusBadRequest {
		t.Errorf("create empty name: status %d, want 400", status)
	}

	// Delete is a 204.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/logout", token, ""); status != http.StatusNoContent {
		t.Fatalf("logout: status %d", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/auth/me", token, ""); status != http.StatusUnauthorized {
		t.Errorf("me after logout: status %d, want 401", status)
	}
}

// TestOpenAPISpecServedAndCoversRoutes: the spec ships with the binary
// (ENGINEERING rule 19) and must mention every route the mux actually serves.
func TestOpenAPISpecServedAndCoversRoutes(t *testing.T) {
	ts := newTestServer(t)
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/openapi.yaml", "", "")
	if status != http.StatusOK {
		t.Fatalf("openapi: status %d", status)
	}
	spec := string(body)
	if !strings.Contains(spec, "openapi: 3.1.0") {
		t.Error("spec is not OpenAPI 3.1")
	}
	for _, path := range []string{
		"/healthz", "/readyz", "/api/v1/auth/login", "/api/v1/auth/logout",
		"/api/v1/auth/me", "/api/v1/ca.pem", "/api/v1/openapi.yaml",
		"/api/v1/servers", "/api/v1/servers/{id}",
	} {
		if !strings.Contains(spec, path+":") {
			t.Errorf("spec does not document %s", path)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	_, headers, _ := doJSON(t, "GET", ts.URL+"/healthz", "", "")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := headers.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

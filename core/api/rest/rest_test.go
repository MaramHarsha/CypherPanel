package rest

import (
	"context"
	"crypto/rand"
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
	"github.com/MaramHarsha/cypherpanel/core/secret"
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
	list  []domain.Server
	inUse map[string]bool // servers that still run applications (RESTRICT)
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

func (f *fakeServersStore) DeleteServer(_ context.Context, id string) error {
	if f.inUse[id] {
		return store.ErrInUse
	}
	return nil
}

type noopDisconnector struct{}

func (noopDisconnector) DisconnectAgent(string) error { return nil }

type fakeProjectsStore struct {
	projects map[string]domain.Project
	envs     map[string][]domain.Environment
}

func newFakeProjectsStore() *fakeProjectsStore {
	return &fakeProjectsStore{projects: map[string]domain.Project{}, envs: map[string][]domain.Environment{}}
}

func (f *fakeProjectsStore) CreateProjectWithEnvironment(_ context.Context, pid, name, eid, ename string) (domain.Project, domain.Environment, error) {
	p := domain.Project{ID: pid, Name: name, CreatedAt: time.Now()}
	e := domain.Environment{ID: eid, ProjectID: pid, Name: ename, CreatedAt: time.Now()}
	f.projects[pid] = p
	f.envs[pid] = append(f.envs[pid], e)
	return p, e, nil
}

func (f *fakeProjectsStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeProjectsStore) ListProjects(context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProjectsStore) DeleteProject(_ context.Context, id string) error {
	delete(f.projects, id)
	delete(f.envs, id)
	return nil
}

func (f *fakeProjectsStore) CreateEnvironment(_ context.Context, id, pid, name string) (domain.Environment, error) {
	for _, e := range f.envs[pid] {
		if e.Name == name {
			return domain.Environment{}, store.ErrConflict
		}
	}
	e := domain.Environment{ID: id, ProjectID: pid, Name: name, CreatedAt: time.Now()}
	f.envs[pid] = append(f.envs[pid], e)
	return e, nil
}

func (f *fakeProjectsStore) ListEnvironmentsByProject(_ context.Context, pid string) ([]domain.Environment, error) {
	return f.envs[pid], nil
}

type fakeAppsStore struct {
	envs    map[string]bool
	servers map[string]bool
	apps    map[string]domain.Application
	env     map[string][]domain.EnvVar
}

func newFakeAppsStore() *fakeAppsStore {
	return &fakeAppsStore{
		envs:    map[string]bool{"env_test": true},
		servers: map[string]bool{"srv_test": true},
		apps:    map[string]domain.Application{},
		env:     map[string][]domain.EnvVar{},
	}
}

func (f *fakeAppsStore) CreateApplicationWithEnv(_ context.Context, a domain.Application, vars []domain.EnvVar) (domain.Application, error) {
	for _, other := range f.apps {
		if other.EnvironmentID == a.EnvironmentID && other.Name == a.Name {
			return domain.Application{}, store.ErrConflict
		}
	}
	f.apps[a.ID] = a
	f.env[a.ID] = vars
	return a, nil
}

func (f *fakeAppsStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeAppsStore) ListApplicationsByEnvironment(_ context.Context, envID string) ([]domain.Application, error) {
	var out []domain.Application
	for _, a := range f.apps {
		if a.EnvironmentID == envID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeAppsStore) DeleteApplication(_ context.Context, id string) error {
	delete(f.apps, id)
	return nil
}

func (f *fakeAppsStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if !f.envs[id] {
		return domain.Environment{}, store.ErrNotFound
	}
	return domain.Environment{ID: id}, nil
}

func (f *fakeAppsStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	if !f.servers[id] {
		return domain.Server{}, store.ErrNotFound
	}
	return domain.Server{ID: id}, nil
}

func (f *fakeAppsStore) UpsertEnvVar(_ context.Context, appID string, v domain.EnvVar) error {
	f.env[appID] = append(f.env[appID], v)
	return nil
}

func (f *fakeAppsStore) ListEnvVars(_ context.Context, appID string) ([]domain.EnvVar, error) {
	return f.env[appID], nil
}

func (f *fakeAppsStore) DeleteEnvVar(_ context.Context, appID, key string) error {
	kept := f.env[appID][:0]
	for _, v := range f.env[appID] {
		if v.Key != key {
			kept = append(kept, v)
		}
	}
	f.env[appID] = kept
	return nil
}

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	key := make([]byte, secret.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	box, err := secret.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// ─── harness ────────────────────────────────────────────────────────────────

const (
	testEmail    = "owner@example.com"
	testPassword = "correct-horse-battery"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts, _ := newTestServerWithStores(t)
	return ts
}

func newTestServerWithStores(t *testing.T) (*httptest.Server, *fakeServersStore) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: "owner"},
		sessions: map[string]domain.User{},
	}
	srvStore := &fakeServersStore{inUse: map[string]bool{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(Deps{
		Auth:         auth.NewAuthenticator(authStore, auth.NewLimiter(100, time.Minute), time.Hour),
		Servers:      servers.NewService(srvStore, noopDisconnector{}, 15*time.Minute, log),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), testBox(t)),
		Pinger:       okPinger{},
		CACertPEM:    []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		EnrollAddr:   "localhost:8443",
		NATSURL:      "tls://localhost:4222",
		ConsoleURL:   "http://localhost:8080",
		Log:          log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, srvStore
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
		{"GET", "/api/v1/projects"},
		{"POST", "/api/v1/projects"},
		{"GET", "/api/v1/projects/prj_x"},
		{"DELETE", "/api/v1/projects/prj_x"},
		{"GET", "/api/v1/projects/prj_x/environments"},
		{"POST", "/api/v1/projects/prj_x/environments"},
		{"POST", "/api/v1/environments/env_x/applications"},
		{"GET", "/api/v1/environments/env_x/applications"},
		{"GET", "/api/v1/applications/app_x"},
		{"DELETE", "/api/v1/applications/app_x"},
		{"GET", "/api/v1/applications/app_x/env"},
		{"PUT", "/api/v1/applications/app_x/env/KEY"},
		{"DELETE", "/api/v1/applications/app_x/env/KEY"},
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
			Token          string `json:"token"`
			CAFingerprint  string `json:"ca_fingerprint"`
			Command        string `json:"command"`
			InstallCommand string `json:"install_command"`
		} `json:"join"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.Join.Token == "" || !strings.Contains(created.Join.Command, created.Join.Token) {
		t.Errorf("join instructions incomplete: %+v", created.Join)
	}
	// The curl|sh line must carry everything the installer needs: script URL,
	// token, and the CA fingerprint that guards the CA fetch.
	for _, part := range []string{"/install/agent.sh", created.Join.Token, created.Join.CAFingerprint, "CYPHER_PLANE_HTTP=http://localhost:8080"} {
		if !strings.Contains(created.Join.InstallCommand, part) {
			t.Errorf("install_command missing %q: %s", part, created.Join.InstallCommand)
		}
	}
	if len(created.Join.CAFingerprint) != 64 {
		t.Errorf("ca_fingerprint is not sha256 hex: %q", created.Join.CAFingerprint)
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
		"/api/v1/servers", "/api/v1/servers/{id}", "/install/agent.sh",
		"/api/v1/projects", "/api/v1/projects/{id}", "/api/v1/projects/{id}/environments",
		"/api/v1/environments/{id}/applications", "/api/v1/applications/{id}",
		"/api/v1/applications/{id}/env", "/api/v1/applications/{id}/env/{key}",
	} {
		if !strings.Contains(spec, path+":") {
			t.Errorf("spec does not document %s", path)
		}
	}
}

func TestApplicationLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create under the seeded env_test, targeting the seeded srv_test.
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"},` +
		`"env_vars":{"DATABASE_URL":"postgres://secret"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, resp)
	}
	var created struct {
		Application struct {
			ID     string `json:"id"`
			Source struct {
				Branch string `json:"branch"`
			} `json:"source"`
			Build struct {
				DockerfilePath string `json:"dockerfile_path"`
			} `json:"build"`
		} `json:"application"`
		Webhook struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	// Defaults filled; webhook secret present once; no plaintext env leaked.
	if created.Application.Source.Branch != "main" || created.Application.Build.DockerfilePath != "./Dockerfile" {
		t.Errorf("defaults not applied: %s", resp)
	}
	if created.Webhook.Secret == "" || !strings.Contains(created.Webhook.URL, "/webhooks/github/") {
		t.Errorf("webhook info incomplete: %+v", created.Webhook)
	}
	if strings.Contains(string(resp), "postgres://secret") {
		t.Error("plaintext env var leaked in the create response")
	}

	appID := created.Application.ID

	// Get + list.
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+appID, token, ""); status != http.StatusOK {
		t.Errorf("get: status %d", status)
	}
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/applications", token, "")
	if status != http.StatusOK || !strings.Contains(string(resp), appID) {
		t.Errorf("list: status %d body %s", status, resp)
	}

	// Env vars: value in, keys out (never the value).
	if status, _, _ = doJSON(t, "PUT", ts.URL+"/api/v1/applications/"+appID+"/env/API_KEY", token, `{"value":"supersecret"}`); status != http.StatusNoContent {
		t.Errorf("set env: status %d", status)
	}
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+appID+"/env", token, "")
	if status != http.StatusOK {
		t.Fatalf("list env: status %d", status)
	}
	if !strings.Contains(string(resp), "API_KEY") || strings.Contains(string(resp), "supersecret") {
		t.Errorf("env keys leaked a value or missed a key: %s", resp)
	}

	// Validation: bad port is a 400; unknown environment is a 404; unknown
	// target server is a 400.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token,
		`{"name":"bad","source":{"kind":"github","repo":"x/y"},"runtime":{"server_id":"srv_test","port":0},"route":{"domain":"d"}}`)
	if status != http.StatusBadRequest {
		t.Errorf("bad port: status %d, want 400", status)
	}
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_missing/applications", token, body)
	if status != http.StatusNotFound {
		t.Errorf("unknown env: status %d, want 404", status)
	}
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token,
		`{"name":"nosrv","source":{"kind":"github","repo":"x/y"},"runtime":{"server_id":"srv_missing","port":80},"route":{"domain":"d"}}`)
	if status != http.StatusBadRequest {
		t.Errorf("unknown server: status %d, want 400", status)
	}

	// Delete.
	if status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/applications/"+appID, token, ""); status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

func TestProjectLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create: a project comes with its default production environment.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":"acme"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, body)
	}
	var created struct {
		Project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
		DefaultEnvironment struct {
			Name string `json:"name"`
		} `json:"default_environment"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.Project.ID == "" || created.DefaultEnvironment.Name != "production" {
		t.Fatalf("unexpected create response: %s", body)
	}

	// Get returns the project with its environments.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/projects/"+created.Project.ID, token, "")
	if status != http.StatusOK || !strings.Contains(string(body), "production") {
		t.Fatalf("get: status %d body %s", status, body)
	}

	// Add a second environment.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/projects/"+created.Project.ID+"/environments", token, `{"name":"staging"}`)
	if status != http.StatusCreated {
		t.Fatalf("create environment: status %d", status)
	}
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/projects/"+created.Project.ID+"/environments", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), "staging") {
		t.Fatalf("list environments: status %d body %s", status, body)
	}

	// Invalid name is a 400; unknown project is a 404.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":""}`)
	if status != http.StatusBadRequest {
		t.Errorf("empty name: status %d, want 400", status)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_missing", token, "")
	if status != http.StatusNotFound {
		t.Errorf("missing project: status %d, want 404", status)
	}

	// Delete is a 204.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/projects/"+created.Project.ID, token, "")
	if status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

// TestInstallScriptServed: the join installer is public and secret-free.
func TestInstallScriptServed(t *testing.T) {
	ts := newTestServer(t)
	status, headers, body := doJSON(t, "GET", ts.URL+"/install/agent.sh", "", "")
	if status != http.StatusOK {
		t.Fatalf("install script: status %d", status)
	}
	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Errorf("content type %q", ct)
	}
	if !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Errorf("script does not start with a shebang: %.40s", body)
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

// Duplicate names and referenced servers are client-state conflicts, not
// server faults: the API must answer 409 with a reason, never a generic 500.
func TestConflictAndInUseAre409(t *testing.T) {
	ts, srvStore := newTestServerWithStores(t)
	token := login(t, ts)

	// Duplicate environment name inside one project.
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":"shop"}`)
	if status != http.StatusCreated {
		t.Fatalf("create project: status %d", status)
	}
	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(resp, &proj); err != nil {
		t.Fatalf("project response: %v", err)
	}
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/projects/"+proj.Project.ID+"/environments", token, `{"name":"production"}`)
	if status != http.StatusConflict {
		t.Errorf("duplicate environment: status %d body %s, want 409", status, resp)
	}

	// Duplicate application name inside one environment.
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	if status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body); status != http.StatusCreated {
		t.Fatalf("create application: status %d", status)
	}
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusConflict {
		t.Errorf("duplicate application: status %d body %s, want 409", status, resp)
	}

	// Deleting a server that still runs applications is refused with a reason.
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"busy"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("server response: %v", err)
	}
	srvStore.inUse[created.Server.ID] = true
	status, _, resp = doJSON(t, "DELETE", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusConflict || !strings.Contains(string(resp), "still runs applications") {
		t.Errorf("in-use server delete: status %d body %s, want 409 with reason", status, resp)
	}
}

package rest

// HTTP + authorization tests for the shared-variable routes
// (shared-variables.md §6, §7). Every route is project-scoped at RoleMember —
// the rank an application's own env vars already require, deliberately, because
// they are the same class of secret. A non-member gets 404 (no tenancy
// probing); a token without the `write` ability gets 403 on every mutation.

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
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeSharedVarService implements SharedVariableService with just enough state
// to exercise routing, authorization and DTO shape. It keeps the plaintext it
// was handed so a test can assert the value never appears in any response.
type fakeSharedVarService struct {
	vars   map[string]sharedvars.View
	values map[string]string
	usage  map[string][]domain.SharedVariableUsage
	// refused marks ids whose delete must come back as an in-use conflict.
	refused map[string]bool
}

func newFakeSharedVarService() *fakeSharedVarService {
	envID := "env_test"
	return &fakeSharedVarService{
		vars: map[string]sharedvars.View{
			"sv_project": {
				Variable:    domain.SharedVariable{ID: "sv_project", ProjectID: "prj_test", Key: "SENTRY_DSN"},
				UsedByCount: 3,
			},
			"sv_scoped": {
				Variable:        domain.SharedVariable{ID: "sv_scoped", ProjectID: "prj_test", EnvironmentID: &envID, Key: "SMTP_HOST"},
				EnvironmentName: "production",
				UsedByCount:     2,
			},
		},
		values: map[string]string{},
		usage: map[string][]domain.SharedVariableUsage{
			"sv_project": {
				{ApplicationID: "app_x", ApplicationName: "web", EnvironmentName: "production", RedeployPending: true},
			},
		},
		refused: map[string]bool{},
	}
}

func (f *fakeSharedVarService) Create(_ context.Context, projectID string, in sharedvars.CreateInput) (sharedvars.View, error) {
	if projectID != "prj_test" {
		return sharedvars.View{}, sharedvars.ErrProjectNotFound
	}
	v := sharedvars.View{Variable: domain.SharedVariable{
		ID: "sv_new", ProjectID: projectID, EnvironmentID: in.EnvironmentID, Key: in.Key,
		ValueCT: []byte("sealed:" + in.Value), ValueNonce: []byte("n"),
	}}
	f.vars[v.Variable.ID] = v
	f.values[v.Variable.ID] = in.Value
	return v, nil
}

func (f *fakeSharedVarService) Get(_ context.Context, id string) (domain.SharedVariable, error) {
	v, ok := f.vars[id]
	if !ok {
		return domain.SharedVariable{}, store.ErrNotFound
	}
	return v.Variable, nil
}

func (f *fakeSharedVarService) View(_ context.Context, id string) (sharedvars.View, error) {
	v, ok := f.vars[id]
	if !ok {
		return sharedvars.View{}, store.ErrNotFound
	}
	return v, nil
}

func (f *fakeSharedVarService) ListViews(_ context.Context, projectID string) ([]sharedvars.View, error) {
	out := []sharedvars.View{}
	for _, v := range f.vars {
		if v.Variable.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeSharedVarService) SetValue(_ context.Context, id, value string) (sharedvars.View, error) {
	v, ok := f.vars[id]
	if !ok {
		return sharedvars.View{}, store.ErrNotFound
	}
	v.Variable.ValueCT = []byte("sealed:" + value)
	v.Variable.UpdatedAt = v.Variable.UpdatedAt.Add(time.Second)
	f.vars[id] = v
	f.values[id] = value
	return v, nil
}

func (f *fakeSharedVarService) Delete(_ context.Context, id string) error {
	if _, ok := f.vars[id]; !ok {
		return store.ErrNotFound
	}
	if f.refused[id] {
		return &sharedvars.InUseError{Applications: []string{"web", "worker", "cron"}}
	}
	delete(f.vars, id)
	return nil
}

func (f *fakeSharedVarService) UsedBy(_ context.Context, id string) ([]domain.SharedVariableUsage, error) {
	if _, ok := f.vars[id]; !ok {
		return nil, store.ErrNotFound
	}
	return f.usage[id], nil
}

func (f *fakeSharedVarService) RedeployPending(_ context.Context, appID string) (bool, error) {
	return appID == "app_x", nil
}

func (f *fakeSharedVarService) PendingInEnvironment(_ context.Context, _ string) (map[string]bool, error) {
	return map[string]bool{"app_x": true}, nil
}

// newSharedVarServer wires a server whose user holds panelRole on the panel and
// projectRole in prj_test's team (empty = not a member at all).
func newSharedVarServer(t *testing.T, panelRole, projectRole string) (*httptest.Server, *fakeSharedVarService) {
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

	svc := newFakeSharedVarService()
	api := New(Deps{
		Auth:            auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:        projects.NewService(newFakeProjectsStore()),
		Applications:    applications.NewService(newFakeAppsStore(), box),
		SharedVariables: svc,
		Teams:           ft,
		Opener:          box,
		Pinger:          okPinger{},
		CACertPEM:       []byte("x"),
		Log:             log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, svc
}

// sharedVarRoutes is every route this feature adds, with a body where one is
// needed. Deletion sits last so a table walked in order does not remove the row
// the earlier rows address.
var sharedVarRoutes = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"create variable", "POST", "/api/v1/projects/prj_test/shared-variables", `{"key":"NEW_KEY","value":"v"}`},
	{"list variables", "GET", "/api/v1/projects/prj_test/shared-variables", ""},
	{"get variable", "GET", "/api/v1/shared-variables/sv_project", ""},
	{"patch variable", "PATCH", "/api/v1/shared-variables/sv_project", `{"value":"v2"}`},
	{"used-by", "GET", "/api/v1/shared-variables/sv_project/used-by", ""},
	{"delete variable", "DELETE", "/api/v1/shared-variables/sv_project", ""},
}

// A project you cannot see does not exist: every route answers 404, never 403,
// so membership is not probeable.
func TestNonMemberGets404OnEverySharedVariableRoute(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleMember, "")
	token := login(t, ts)
	for _, r := range sharedVarRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status != http.StatusNotFound {
			t.Errorf("%s as a non-member = %d, want 404 (body %s)", r.name, status, body)
		}
	}
}

// A project member holds the minimum rank every route needs — the same rank
// app env vars already require (§6).
func TestProjectMemberReachesEverySharedVariableRoute(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleMember, domain.RoleMember)
	token := login(t, ts)
	for _, r := range sharedVarRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status == http.StatusNotFound || status == http.StatusForbidden {
			t.Errorf("%s as a member = %d, want it authorized (body %s)", r.name, status, body)
		}
	}
}

// The rank gate is real: a caller outside the project's team is refused even
// holding a high panel role that is not owner.
func TestPanelAdminWithoutMembershipCannotSeeSharedVariables(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleAdmin, "")
	token := login(t, ts)
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/shared-variables/sv_project", token, ""); status != http.StatusNotFound {
		t.Fatalf("panel admin, non-member GET = %d, want 404 (body %s)", status, body)
	}
}

// A token narrower than its owner is refused with 403 on mutations.
func TestSharedVariableMutationsRequireTheWriteAbility(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	session := login(t, ts)
	readOnly := createToken(t, ts, session, "readonly", `["read"]`)

	for _, r := range sharedVarRoutes {
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

// The whole point of §6: values are write-only and carry NO masked hint, so no
// response on any route may contain the value or any fragment of it.
func TestSharedVariableValueNeverAppearsInAResponse(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	const secret = "https://abc123@sentry.io/42"

	status, _, created := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/shared-variables", token,
		`{"key":"SENTRY_DSN_NEW","value":"`+secret+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d (body %s)", status, created)
	}
	for _, probe := range []string{
		string(created),
		mustBody(t, ts, token, "GET", "/api/v1/shared-variables/sv_new"),
		mustBody(t, ts, token, "GET", "/api/v1/projects/prj_test/shared-variables"),
		mustBody(t, ts, token, "GET", "/api/v1/shared-variables/sv_new/used-by"),
	} {
		if strings.Contains(probe, secret) || strings.Contains(probe, "abc123") || strings.Contains(probe, "sealed") {
			t.Fatalf("a shared variable value (or its ciphertext) reached a response: %s", probe)
		}
	}
}

func mustBody(t *testing.T, ts *httptest.Server, token, method, path string) string {
	t.Helper()
	status, _, body := doJSON(t, method, ts.URL+path, token, "")
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d (body %s)", method, path, status, body)
	}
	return string(body)
}

// The list carries scope and the scope-accurate used-by count, which is what
// the tab renders (§7, §8).
func TestSharedVariableListCarriesScopeAndCount(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	body := mustBody(t, ts, token, "GET", "/api/v1/projects/prj_test/shared-variables")

	var list []struct {
		ID              string  `json:"id"`
		EnvironmentID   *string `json:"environment_id"`
		EnvironmentName string  `json:"environment_name"`
		Key             string  `json:"key"`
		UsedByCount     int     `json:"used_by_count"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, body)
	}
	seen := map[string]bool{}
	for _, v := range list {
		seen[v.ID] = true
		switch v.ID {
		case "sv_project":
			if v.EnvironmentID != nil || v.EnvironmentName != "" || v.UsedByCount != 3 {
				t.Errorf("project-scoped row = %+v, want no environment and 3 uses", v)
			}
		case "sv_scoped":
			if v.EnvironmentID == nil || v.EnvironmentName != "production" || v.UsedByCount != 2 {
				t.Errorf("environment-scoped row = %+v, want production and 2 uses", v)
			}
		}
	}
	if !seen["sv_project"] || !seen["sv_scoped"] {
		t.Fatalf("list missed a scope: %s", body)
	}
}

// A guarded delete is a 409 that NAMES the applications — there is no force
// override, so the message has to be actionable (§7).
func TestDeleteRefusedWhileReferencedNamesTheApplications(t *testing.T) {
	ts, svc := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	svc.refused["sv_project"] = true

	status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/shared-variables/sv_project", token, "")
	if status != http.StatusConflict {
		t.Fatalf("delete of a referenced variable = %d, want 409 (body %s)", status, body)
	}
	for _, want := range []string{"web", "worker", "cron"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("409 body %q does not name %q", body, want)
		}
	}

	// Remove the references and the same delete succeeds.
	svc.refused["sv_project"] = false
	if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/shared-variables/sv_project", token, ""); status != http.StatusNoContent {
		t.Fatalf("delete after removing references = %d, want 204 (body %s)", status, body)
	}
}

// used-by is the reach the operator sees before setting a value, and it carries
// the per-application drift marker (§7).
func TestSharedVariableUsedByListing(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	body := mustBody(t, ts, token, "GET", "/api/v1/shared-variables/sv_project/used-by")

	var usage []struct {
		ApplicationID   string `json:"application_id"`
		ApplicationName string `json:"application_name"`
		EnvironmentName string `json:"environment_name"`
		RedeployPending bool   `json:"redeploy_pending"`
	}
	if err := json.Unmarshal([]byte(body), &usage); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, body)
	}
	if len(usage) != 1 || usage[0].ApplicationID != "app_x" || !usage[0].RedeployPending {
		t.Fatalf("usage = %+v, want the one pending application", usage)
	}
}

// The drift marker rides on the Application DTO as an additive boolean, not a
// status word (§5, ui-principles §5).
func TestApplicationDTOCarriesRedeployPending(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	body := mustBody(t, ts, token, "GET", "/api/v1/applications/app_x")

	var app struct {
		Status          string `json:"status"`
		RedeployPending bool   `json:"redeploy_pending"`
	}
	if err := json.Unmarshal([]byte(body), &app); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, body)
	}
	if !app.RedeployPending {
		t.Fatalf("redeploy_pending = false, want true (body %s)", body)
	}
	if app.Status == "redeploy_pending" {
		t.Fatal("the marker leaked into the status vocabulary, which is closed")
	}
}

// The env listing gains shared_refs so the Env vars tab can show the wiring
// without a reveal — and `keys` is unchanged, because the change is additive
// (ENGINEERING rule 17, spec §7).
func TestEnvListingCarriesSharedRefs(t *testing.T) {
	ts, _ := newSharedVarServer(t, domain.RoleOwner, domain.RoleOwner)
	token := login(t, ts)
	body := mustBody(t, ts, token, "GET", "/api/v1/applications/app_x/env")

	var got struct {
		Keys       []string            `json:"keys"`
		SharedRefs map[string][]string `json:"shared_refs"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, body)
	}
	if got.Keys == nil {
		t.Fatalf("keys disappeared from the env listing: %s", body)
	}
	if got.SharedRefs == nil {
		t.Fatalf("shared_refs missing from the env listing: %s", body)
	}
}

// With the service absent (a plane built without it), reads degrade to the
// empty shape and creates say so — never a nil panic.
func TestSharedVariableRoutesWithoutTheService(t *testing.T) {
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	box := testBox(t)
	ft := newFakeTeams()
	ft.projectRoles["usr_test"] = map[string]string{"prj_test": domain.RoleOwner}
	ft.teams = nil
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), box),
		Teams:        ft,
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	token := login(t, ts)

	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_test/shared-variables", token, ""); status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("list without the service = %d %s, want 200 []", status, body)
	}
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/shared-variables", token, `{"key":"K","value":"v"}`); status != http.StatusNotImplemented {
		t.Errorf("create without the service = %d, want 501", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/shared-variables/sv_project", token, ""); status != http.StatusNotFound {
		t.Errorf("get without the service = %d, want 404", status)
	}
	// The application DTO still answers, with the marker simply false.
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/applications/app_x", token, ""); status != http.StatusOK {
		t.Errorf("application GET without the service = %d (body %s)", status, body)
	}
}

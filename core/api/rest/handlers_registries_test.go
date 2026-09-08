package rest

// Registry routes (registries.md §7). What is proved here is what the handler
// layer owns: the team boundary, the rank gates, and that a token never leaves
// the panel — the credential mechanics themselves belong to core/registries.

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

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/registries"
	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeRegistryStore is the persistence core/registries needs.
type fakeRegistryStore struct {
	byID map[string]domain.Registry
	uses map[string][]domain.RegistryUse
}

func newFakeRegistryStore() *fakeRegistryStore {
	return &fakeRegistryStore{byID: map[string]domain.Registry{}, uses: map[string][]domain.RegistryUse{}}
}

func (f *fakeRegistryStore) CreateRegistry(_ context.Context, r domain.Registry) (domain.Registry, error) {
	for _, existing := range f.byID {
		if existing.TeamID == r.TeamID && existing.Name == r.Name {
			return domain.Registry{}, store.ErrConflict
		}
	}
	r.CreatedAt = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	f.byID[r.ID] = r
	return r, nil
}

func (f *fakeRegistryStore) GetRegistry(_ context.Context, id string) (domain.Registry, error) {
	r, ok := f.byID[id]
	if !ok {
		return domain.Registry{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeRegistryStore) ListRegistriesByTeams(_ context.Context, teamIDs []string) ([]domain.Registry, error) {
	want := map[string]bool{}
	for _, t := range teamIDs {
		want[t] = true
	}
	out := []domain.Registry{}
	for _, r := range f.byID {
		if want[r.TeamID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRegistryStore) UpdateRegistry(_ context.Context, id string, u store.UpdateRegistryFields) (domain.Registry, error) {
	r, ok := f.byID[id]
	if !ok {
		return domain.Registry{}, store.ErrNotFound
	}
	if u.Name != nil {
		r.Name = *u.Name
	}
	if u.URL != nil {
		r.URL = *u.URL
	}
	if u.Username != nil {
		r.Username = *u.Username
	}
	if u.TokenCT != nil {
		r.TokenCT, r.TokenNonce = u.TokenCT, u.TokenNonce
	}
	if u.CanPull != nil {
		r.CanPull = *u.CanPull
	}
	if u.CanPush != nil {
		r.CanPush = *u.CanPush
	}
	f.byID[id] = r
	return r, nil
}

func (f *fakeRegistryStore) RecordRegistryTest(_ context.Context, id string, ok bool, detail string) (domain.Registry, error) {
	r := f.byID[id]
	r.LastTestOK, r.LastTestDetail = ok, detail
	f.byID[id] = r
	return r, nil
}

func (f *fakeRegistryStore) DeleteRegistry(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

func (f *fakeRegistryStore) ApplicationsUsingRegistry(_ context.Context, id string) ([]domain.RegistryUse, error) {
	return f.uses[id], nil
}

// newRegistryServer wires an API whose caller is a plain member of tm_default
// and of nothing else, so the team boundary is actually exercised — the
// default harness user is a panel owner, for whom every role check passes.
func newRegistryServer(t *testing.T, role string) (*httptest.Server, *fakeRegistryStore, *memAuditStore, *secret.Box) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
		sessions: map[string]domain.User{},
	}
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ft := newFakeTeams()
	ft.teams = []domain.TeamWithRole{{Team: domain.Team{ID: "tm_default", Name: "default"}, Role: role}}
	ft.teamRoles["usr_test"] = map[string]string{"tm_default": role}

	regStore := newFakeRegistryStore()
	mem := newMemAuditStore()
	api := New(Deps{
		Auth:       auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Teams:      ft,
		Registries: registries.NewService(regStore, box),
		Audit:      audit.NewService(mem, 0, log),
		Log:        log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, regStore, mem, box
}

// seedRegistry puts a registry straight into the store, so a test can start
// from one that exists without going through the create route. The token is
// sealed with the harness's real box, because the routes that unseal it (the
// connection test) must work against what a running panel would hold.
func seedRegistry(t *testing.T, box *secret.Box, st *fakeRegistryStore, id, teamID, name string, canPull, canPush bool) domain.Registry {
	t.Helper()
	ct, nonce, err := box.Seal([]byte("s3cret"))
	if err != nil {
		t.Fatalf("sealing the seeded token: %v", err)
	}
	r := domain.Registry{
		ID: id, TeamID: teamID, Name: name, URL: "ghcr.io", Username: "acme",
		TokenCT: ct, TokenNonce: nonce,
		CanPull: canPull, CanPush: canPush,
		CreatedAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
	}
	st.byID[id] = r
	return r
}

type registryBody struct {
	ID       string `json:"id"`
	TeamID   string `json:"team_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	CanPull  bool   `json:"can_pull"`
	CanPush  bool   `json:"can_push"`
	TokenSet bool   `json:"token_set"`
}

func TestCreateRegistryNeedsAdminAndNeverEchoesTheToken(t *testing.T) {
	ts, _, mem, _ := newRegistryServer(t, domain.RoleAdmin)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries", token,
		`{"name":"ghcr","url":"ghcr.io","username":"acme","token":"ghp_s3cret","can_push":true}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body %s", status, body)
	}
	if strings.Contains(string(body), "ghp_s3cret") {
		t.Fatalf("the token came back in the response: %s", body)
	}
	var got registryBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if !got.TokenSet {
		t.Fatal("token_set = false, want it to report that a credential exists")
	}
	if got.TeamID != "tm_default" {
		t.Fatalf("team_id = %q, want the caller's only team", got.TeamID)
	}
	if !got.CanPull || !got.CanPush {
		t.Fatalf("can_pull/can_push = %v/%v, want pull defaulted on and push as asked", got.CanPull, got.CanPush)
	}

	// The audit row names what changed, and no value of it.
	var found *domain.AuditEvent
	for i := range mem.events {
		if mem.events[i].Action == audit.ActionRegistryCreated {
			found = &mem.events[i]
		}
	}
	if found == nil {
		t.Fatalf("no registry.created row in %d events", len(mem.events))
	}
	if raw, _ := json.Marshal(found.Detail); strings.Contains(string(raw), "ghp_s3cret") {
		t.Fatalf("the token reached the audit detail: %s", raw)
	}
}

// Pull defaults on because a private base image is the common case; push
// defaults off because it is the larger grant.
func TestCreateRegistryDefaultsPullOnAndPushOff(t *testing.T) {
	ts, _, _, _ := newRegistryServer(t, domain.RoleAdmin)
	token := login(t, ts)

	_, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries", token,
		`{"name":"ghcr","url":"ghcr.io","token":"t"}`)
	var got registryBody
	_ = json.Unmarshal(body, &got)
	if !got.CanPull || got.CanPush {
		t.Fatalf("can_pull/can_push = %v/%v, want true/false", got.CanPull, got.CanPush)
	}
}

func TestCreateRegistryRefusedForAMember(t *testing.T) {
	ts, _, _, _ := newRegistryServer(t, domain.RoleMember)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries", token,
		`{"name":"ghcr","url":"ghcr.io","token":"t"}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d body %s, want 403 for a member", status, body)
	}
}

func TestCreateRegistryRejectsAURLWithAScheme(t *testing.T) {
	ts, _, _, _ := newRegistryServer(t, domain.RoleAdmin)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries", token,
		`{"name":"ghcr","url":"https://ghcr.io","token":"t"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", status, body)
	}
}

func TestCreateRegistryConflictsOnADuplicateNameInTheTeam(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleAdmin)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries", token,
		`{"name":"ghcr","url":"ghcr.io","token":"t"}`)
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
}

// Listing filters to the caller's teams rather than refusing, and a listing
// that leaked another team's row would leak the existence of its credential.
func TestListRegistriesShowsOnlyTheCallersTeams(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleMember)
	seedRegistry(t, box, st, "reg_mine", "tm_default", "ours", true, false)
	seedRegistry(t, box, st, "reg_theirs", "tm_other", "theirs", true, false)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/registries", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var got []registryBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(got) != 1 || got[0].ID != "reg_mine" {
		t.Fatalf("list = %+v, want only the caller's team's registry", got)
	}
}

// A registry in another team answers exactly as one that does not exist.
func TestGetRegistryInAnotherTeamIsNotFound(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleAdmin)
	seedRegistry(t, box, st, "reg_theirs", "tm_other", "theirs", true, false)
	token := login(t, ts)

	theirs, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/registries/reg_theirs", token, "")
	missing, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/registries/reg_nope", token, "")
	if theirs != http.StatusNotFound || missing != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want both 404 so membership is not probeable", theirs, missing)
	}
}

func TestGetRegistryIsVisibleToAMemberWithoutTheToken(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleMember)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/registries/reg_1", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	if strings.Contains(string(body), "token_ct") || strings.Contains(string(body), "s3cret") {
		t.Fatalf("the credential appeared in the response: %s", body)
	}
}

func TestPatchRegistryRenamesWithoutResendingTheToken(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleAdmin)
	before := seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	token := login(t, ts)

	status, _, body := doJSON(t, "PATCH", ts.URL+"/api/v1/registries/reg_1", token, `{"name":"ghcr-prod"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	if got := st.byID["reg_1"]; got.Name != "ghcr-prod" || string(got.TokenCT) != string(before.TokenCT) {
		t.Fatalf("stored = %+v, want the rename applied and the credential untouched", got)
	}
}

func TestPatchRegistryRefusedForAMember(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleMember)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	token := login(t, ts)

	status, _, _ := doJSON(t, "PATCH", ts.URL+"/api/v1/registries/reg_1", token, `{"name":"x"}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

// Deleting a credential applications depend on would break their next deploy
// at the moment nobody is looking.
func TestDeleteRegistryRefusedWhileApplicationsUseIt(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleAdmin)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	st.uses["reg_1"] = []domain.RegistryUse{{
		ApplicationID: "app_1", ApplicationName: "web", EnvironmentName: "production", ProjectName: "acme", Pulls: true,
	}}
	token := login(t, ts)

	status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/registries/reg_1", token, "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
	if _, still := st.byID["reg_1"]; !still {
		t.Fatal("the registry was deleted despite the refusal")
	}
}

func TestDeleteRegistryRemovesAnUnusedOne(t *testing.T) {
	ts, st, mem, box := newRegistryServer(t, domain.RoleAdmin)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, false)
	token := login(t, ts)

	status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/registries/reg_1", token, "")
	if status != http.StatusNoContent {
		t.Fatalf("status = %d body %s", status, body)
	}
	if _, still := st.byID["reg_1"]; still {
		t.Fatal("the registry survived its delete")
	}
	var deleted bool
	for _, e := range mem.events {
		if e.Action == audit.ActionRegistryDeleted {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("no registry.deleted row")
	}
}

// The 409 says "see its used-by list"; that list has to name things.
func TestRegistryUsedByNamesTheApplications(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleMember)
	seedRegistry(t, box, st, "reg_1", "tm_default", "ghcr", true, true)
	st.uses["reg_1"] = []domain.RegistryUse{
		{ApplicationID: "app_1", ApplicationName: "web", EnvironmentName: "production", ProjectName: "acme", Pulls: true},
		{ApplicationID: "app_2", ApplicationName: "api", EnvironmentName: "staging", ProjectName: "acme", Pushes: true},
	}
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/registries/reg_1/used-by", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var got []registryUseDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(got) != 2 || got[0].ApplicationName != "web" || !got[1].Pushes {
		t.Fatalf("used-by = %+v", got)
	}
}

// A failed connection test is a 200 with ok:false — the request succeeded, the
// connection did not, and conflating the two costs the caller the distinction.
func TestTestRegistryReportsAFailureAsTwoHundred(t *testing.T) {
	ts, st, _, box := newRegistryServer(t, domain.RoleAdmin)
	reg := seedRegistry(t, box, st, "reg_1", "tm_default", "local", true, false)
	// A closed port on loopback: contacted over plain HTTP, refused at once.
	reg.URL = "127.0.0.1:1"
	st.byID["reg_1"] = reg
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries/reg_1/test", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s, want 200 with ok:false", status, body)
	}
	var got connectionTestDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if got.OK || got.Detail == "" {
		t.Fatalf("result = %+v, want a refusal with a reason", got)
	}
}

func TestTestRegistryConfigValidatesBeforeDialing(t *testing.T) {
	ts, _, _, _ := newRegistryServer(t, domain.RoleAdmin)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/registries/test", token,
		`{"url":"https://ghcr.io","username":"u","token":"t"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400 for a URL with a scheme", status, body)
	}
}

// A panel that has not wired registries behaves as it did before the feature
// existed rather than 500-ing.
func TestRegistryRoutesAnswer501WhenNotWired(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/registries", ""},
		{"POST", "/api/v1/registries", `{"name":"n","url":"ghcr.io","token":"t"}`},
		{"GET", "/api/v1/registries/reg_1", ""},
		{"PATCH", "/api/v1/registries/reg_1", `{"name":"n"}`},
		{"DELETE", "/api/v1/registries/reg_1", ""},
		{"POST", "/api/v1/registries/reg_1/test", ""},
		{"POST", "/api/v1/registries/test", `{"url":"ghcr.io","token":"t"}`},
		{"GET", "/api/v1/registries/reg_1/used-by", ""},
	} {
		status, _, body := doJSON(t, tc.method, ts.URL+tc.path, token, tc.body)
		if status != http.StatusNotImplemented {
			t.Errorf("%s %s: status = %d body %s, want 501", tc.method, tc.path, status, body)
		}
	}
}

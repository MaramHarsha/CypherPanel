package rest

// HTTP + authorization tests for the audit log (audit-log.md §5, §7).
//
// Two halves, matching the handlers:
//
//   * READ — the scope is the authorization. A member of team A must not be
//     able to read team B's entries by any route or any parameter, and the
//     refusal is 404/empty rather than 403, so the log cannot be probed.
//   * WRITE — the actions a handler performs land as rows with the caller's
//     name on them, and a secret never rides along in the detail.
//
// The service under test is the real *audit.Service over an in-memory store, so
// the visibility rules exercised here are the ones the handlers actually run.
// The SQL that implements the same predicate is proven separately against real
// Postgres (core/store, TestStoreAuditVisibility).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// ─── in-memory audit store ──────────────────────────────────────────────────

// memAuditStore implements audit.Store. ListAuditEvents mirrors the SQL in
// queries/audit.sql: the visibility predicate first, the operator's filters
// inside it, newest first.
type memAuditStore struct {
	events []domain.AuditEvent
	teams  map[string][]domain.TeamWithRole
	// projectTeams and envProjects mirror the COALESCE in queries/audit.sql:
	// the INSERT completes the ownership chain from whichever link the handler
	// knew, so an entry that names only a project or only an environment still
	// lands in the team that can read it.
	projectTeams map[string]string
	envProjects  map[string]string
	// insertErr makes the next write fail, so the "never fail the request"
	// promise in §4 is testable.
	insertErr error
	// now advances by a second per insert so the (at, id) ordering is stable
	// and the cursor is meaningful without sleeping.
	now time.Time
}

func newMemAuditStore() *memAuditStore {
	return &memAuditStore{
		teams:        map[string][]domain.TeamWithRole{},
		projectTeams: map[string]string{"prj_test": "tm_default"},
		envProjects:  map[string]string{"env_test": "prj_test"},
		now:          time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC),
	}
}

func (m *memAuditStore) InsertAuditEvent(_ context.Context, e domain.AuditEvent) (domain.AuditEvent, error) {
	if m.insertErr != nil {
		return domain.AuditEvent{}, m.insertErr
	}
	if e.ProjectID == "" && e.EnvironmentID != "" {
		e.ProjectID = m.envProjects[e.EnvironmentID]
	}
	if e.TeamID == "" && e.ProjectID != "" {
		e.TeamID = m.projectTeams[e.ProjectID]
	}
	m.now = m.now.Add(time.Second)
	e.At = m.now
	m.events = append(m.events, e)
	return e, nil
}

func (m *memAuditStore) GetAuditEvent(_ context.Context, id string) (domain.AuditEvent, error) {
	for _, e := range m.events {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.AuditEvent{}, store.ErrNotFound
}

func (m *memAuditStore) ListAuditEvents(_ context.Context, f store.AuditFilter) ([]domain.AuditEvent, error) {
	visible := []domain.AuditEvent{}
	for i := len(m.events) - 1; i >= 0; i-- { // newest first
		if memVisible(m.events[i], f) {
			visible = append(visible, m.events[i])
		}
	}
	// The cursor resolves against the VISIBLE set, exactly as the SQL's CTE
	// does — an id the caller cannot see yields an empty page.
	start := 0
	if f.Before != "" {
		start = len(visible)
		for i, e := range visible {
			if e.ID == f.Before {
				start = i + 1
				break
			}
		}
	}
	out := []domain.AuditEvent{}
	for _, e := range visible[start:] {
		if int32(len(out)) == f.Limit {
			break
		}
		if memMatches(e, f) {
			out = append(out, e)
		}
	}
	return out, nil
}

func memVisible(e domain.AuditEvent, f store.AuditFilter) bool {
	switch {
	case f.AllScopes:
		return true
	case e.Actor.UserID != "" && e.Actor.UserID == f.ViewerID:
		return true
	case e.TeamID == "":
		return f.PanelScope
	}
	for _, id := range f.TeamIDs {
		if id == e.TeamID {
			return true
		}
	}
	return false
}

func memMatches(e domain.AuditEvent, f store.AuditFilter) bool {
	switch {
	case f.TeamID != "" && e.TeamID != f.TeamID:
		return false
	case f.ProjectID != "" && e.ProjectID != f.ProjectID:
		return false
	case f.ResourceID != "" && e.Resource.ID != f.ResourceID:
		return false
	case f.Outcome != "" && e.Outcome != f.Outcome:
		return false
	case !f.Since.IsZero() && e.At.Before(f.Since):
		return false
	case f.Actor != "" && e.Actor.UserID != f.Actor && e.Actor.Label != f.Actor:
		return false
	case f.Action != "" && e.Action != f.Action && !strings.HasPrefix(e.Action, f.Action+"."):
		return false
	}
	return true
}

func (m *memAuditStore) PurgeAuditEvents(context.Context, time.Time, int32) (int64, error) {
	return 0, nil
}

func (m *memAuditStore) ListTeamsByUser(_ context.Context, userID string) ([]domain.TeamWithRole, error) {
	return m.teams[userID], nil
}

// seed inserts an event directly, bypassing the service — these stand in for
// history written before the test's own requests.
func (m *memAuditStore) seed(id, action, teamID, actorID string) {
	actor := domain.AuditActor{Kind: domain.AuditActorSystem, Label: "seed"}
	if actorID != "" {
		actor = domain.AuditActor{Kind: domain.AuditActorUser, UserID: actorID, Label: actorID + "@example.com"}
	}
	_, _ = m.InsertAuditEvent(context.Background(), domain.AuditEvent{
		ID: id, Action: action, Outcome: domain.AuditSuccess, Actor: actor,
		Resource: domain.AuditResource{Kind: "project", ID: "prj_" + teamID, Name: "seeded"},
		TeamID:   teamID,
	})
}

// ─── harness ────────────────────────────────────────────────────────────────

// newAuditServer wires a panel whose single user holds panelRole, belongs to
// the named teams, and has RoleOwner on prj_test when memberOfProject is set.
func newAuditServer(t *testing.T, panelRole string, teamIDs []string, memberOfProject bool, extra ...func(*Deps)) (*httptest.Server, *memAuditStore, *fakeAppsStore) {
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

	mem := newMemAuditStore()
	for _, id := range teamIDs {
		mem.teams["usr_test"] = append(mem.teams["usr_test"], domain.TeamWithRole{
			Team: domain.Team{ID: id}, Role: domain.RoleMember,
		})
	}
	ft := newFakeTeams()
	ft.teams = nil
	if memberOfProject {
		ft.projectRoles["usr_test"] = map[string]string{"prj_test": domain.RoleOwner}
	}
	appsStore := newFakeAppsStore()
	deps := Deps{
		Auth:         auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(appsStore, box),
		Teams:        ft,
		Audit:        audit.NewService(mem, 0, log),
		Scheduler:    &fakeDeployer{},
		Deployments:  fakeDeploymentReader{},
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          log,
	}
	// extra wires the services a particular scope test needs (a protection
	// gate, a backup service) without changing what every other audit test
	// observes.
	for _, f := range extra {
		f(&deps)
	}
	api := New(deps)
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, mem, appsStore
}

type auditPageBody struct {
	Events []struct {
		ID       string         `json:"id"`
		Action   string         `json:"action"`
		Outcome  string         `json:"outcome"`
		TeamID   string         `json:"team_id"`
		Detail   map[string]any `json:"detail"`
		TraceID  string         `json:"trace_id"`
		ClientIP string         `json:"client_ip"`
		Actor    struct {
			Kind    string `json:"kind"`
			UserID  string `json:"user_id"`
			TokenID string `json:"token_id"`
			Label   string `json:"label"`
		} `json:"actor"`
		Resource struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"resource"`
	} `json:"events"`
	NextBefore string `json:"next_before"`
}

func listAudit(t *testing.T, ts *httptest.Server, token, query string) auditPageBody {
	t.Helper()
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/audit"+query, token, "")
	if status != http.StatusOK {
		t.Fatalf("GET /audit%s = %d (body %s)", query, status, body)
	}
	var page auditPageBody
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return page
}

func auditIDs(page auditPageBody) map[string]bool {
	seen := map[string]bool{}
	for _, e := range page.Events {
		seen[e.ID] = true
	}
	return seen
}

// ─── read: authentication and scoping (§5) ──────────────────────────────────

func TestAuditRoutesRequireAuthentication(t *testing.T) {
	ts, _, _ := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, false)
	for _, path := range []string{"/api/v1/audit", "/api/v1/audit/aud_x"} {
		if status, _, _ := doJSON(t, "GET", ts.URL+path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, status)
		}
	}
}

// The property the feature turns on: a member of team A cannot read team B's
// entries — not by listing, not by asking for them by id, and not by naming
// team B in the filter.
func TestMemberCannotReadAnotherTeamsAuditEvents(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, false)
	mem.seed("aud_mine", "project.created", "tm_a", "usr_someone")
	mem.seed("aud_theirs", "project.deleted", "tm_b", "usr_someone")
	mem.seed("aud_panel", "server.created", "", "usr_someone")
	token := login(t, ts)

	seen := auditIDs(listAudit(t, ts, token, ""))
	if !seen["aud_mine"] {
		t.Error("a member cannot see their own team's entry")
	}
	if seen["aud_theirs"] {
		t.Error("another team's entry appeared in the page")
	}
	if seen["aud_panel"] {
		t.Error("a panel-level entry appeared for a plain member")
	}

	// By id: 404, the same answer as an entry that does not exist, so the log
	// cannot be probed for what happened elsewhere.
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/audit/aud_theirs", token, ""); status != http.StatusNotFound {
		t.Errorf("GET another team's entry = %d, want 404", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/audit/aud_nope", token, ""); status != http.StatusNotFound {
		t.Errorf("GET a missing entry = %d, want 404", status)
	}

	// By filter: an empty page, never a 403 — a refusal would confirm the team
	// exists.
	page := listAudit(t, ts, token, "?team_id=tm_b")
	if len(page.Events) != 0 {
		t.Errorf("filtering by another team returned %d events", len(page.Events))
	}
}

// Your own actions follow you out of a team you have left.
func TestMemberAlwaysSeesTheirOwnAuditEvents(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, false)
	mem.seed("aud_mine_elsewhere", "team.member_removed", "tm_b", "usr_test")
	token := login(t, ts)

	if !auditIDs(listAudit(t, ts, token, ""))["aud_mine_elsewhere"] {
		t.Error("a member cannot see an action they performed themselves")
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/audit/aud_mine_elsewhere", token, ""); status != http.StatusOK {
		t.Errorf("GET own entry = %d, want 200", status)
	}
}

func TestPanelAdminSeesPanelScopedAuditEvents(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleAdmin, []string{"tm_a"}, false)
	mem.seed("aud_panel", "server.created", "", "usr_someone")
	mem.seed("aud_theirs", "project.deleted", "tm_b", "usr_someone")
	token := login(t, ts)

	seen := auditIDs(listAudit(t, ts, token, ""))
	if !seen["aud_panel"] {
		t.Error("a panel admin cannot read a panel-level entry")
	}
	if seen["aud_theirs"] {
		t.Error("panel scope leaked another team's entry to an admin")
	}
}

func TestPanelOwnerSeesEveryTeamsAuditEvents(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	mem.seed("aud_a", "project.created", "tm_a", "usr_someone")
	mem.seed("aud_b", "project.deleted", "tm_b", "usr_someone")
	token := login(t, ts)

	seen := auditIDs(listAudit(t, ts, token, ""))
	if !seen["aud_a"] || !seen["aud_b"] {
		t.Errorf("a panel owner is missing entries: %v", seen)
	}
}

// ─── read: filters, paging and validation (§7) ──────────────────────────────

func TestAuditFiltersNarrowThePage(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	mem.seed("aud_deploy", "deploy.started", "tm_a", "usr_priya")
	mem.seed("aud_rollback", "deploy.rolled_back", "tm_a", "usr_sam")
	mem.seed("aud_app", "application.created", "tm_a", "usr_sam")
	token := login(t, ts)

	for _, tc := range []struct {
		name    string
		query   string
		want    []string
		notWant []string
	}{
		{"a whole family", "?action=deploy", []string{"aud_deploy", "aud_rollback"}, []string{"aud_app"}},
		{"an exact verb", "?action=deploy.started", []string{"aud_deploy"}, []string{"aud_rollback"}},
		{"an actor by label", "?actor=usr_priya@example.com", []string{"aud_deploy"}, []string{"aud_rollback"}},
		{"an actor by id", "?actor=usr_sam", []string{"aud_rollback", "aud_app"}, []string{"aud_deploy"}},
		{"a team", "?team_id=tm_a", []string{"aud_deploy"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := auditIDs(listAudit(t, ts, token, tc.query))
			for _, id := range tc.want {
				if !seen[id] {
					t.Errorf("%s missing from %s", id, tc.query)
				}
			}
			for _, id := range tc.notWant {
				if seen[id] {
					t.Errorf("%s should not match %s", id, tc.query)
				}
			}
		})
	}
}

// Walking with the cursor visits every entry exactly once. The filter narrows
// to one action so the sign-in this test's own login records cannot change the
// arithmetic.
func TestAuditPagesWithACursor(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	token := login(t, ts)
	seeded := []string{"aud_1", "aud_2", "aud_3", "aud_4", "aud_5"}
	for _, id := range seeded {
		mem.seed(id, "project.created", "tm_a", "usr_someone")
	}

	visited := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		q := "?action=project.created&limit=2"
		if cursor != "" {
			q += "&before=" + cursor
		}
		page := listAudit(t, ts, token, q)
		pages++
		if len(page.Events) == 0 {
			break
		}
		for _, e := range page.Events {
			if visited[e.ID] {
				t.Fatalf("%s appeared on two pages", e.ID)
			}
			visited[e.ID] = true
		}
		if page.NextBefore == "" {
			break
		}
		cursor = page.NextBefore
		if pages > len(seeded)+2 {
			t.Fatal("the cursor never terminated")
		}
	}
	for _, id := range seeded {
		if !visited[id] {
			t.Errorf("%s was skipped by the cursor walk", id)
		}
	}
	if len(visited) != len(seeded) {
		t.Errorf("visited %d entries, want %d", len(visited), len(seeded))
	}
}

func TestAuditListRejectsMalformedParameters(t *testing.T) {
	ts, _, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	token := login(t, ts)
	for _, q := range []string{"?limit=0", "?limit=-3", "?limit=abc", "?since=yesterday", "?outcome=partly"} {
		status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/audit"+q, token, "")
		if status != http.StatusBadRequest {
			t.Errorf("GET /audit%s = %d, want 400 (body %s)", q, status, body)
		}
	}
}

// A panel with no audit service answers an empty log rather than failing: the
// dependency is optional in Deps, and every read has to survive it being nil.
func TestAuditReadsSurviveAnUnwiredService(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)
	page := listAudit(t, ts, token, "")
	if len(page.Events) != 0 || page.NextBefore != "" {
		t.Errorf("an unwired audit log answered %+v", page)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/audit/aud_x", token, ""); status != http.StatusNotFound {
		t.Errorf("GET on an unwired audit log = %d, want 404", status)
	}
}

// ─── write: the rows handlers actually produce (§3, §4) ─────────────────────

// The promise ConfirmDestructive makes on every destructive dialog — "this is
// audit-logged with your name" — has to be true.
func TestDeletingAnApplicationIsAuditedWithTheCallersName(t *testing.T) {
	ts, mem, apps := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, true)
	apps.apps["app_doomed"] = domain.Application{
		ID: "app_doomed", EnvironmentID: "env_test", Name: "notify-svc",
		Runtime: domain.AppRuntime{ServerID: "srv_test"},
	}
	token := login(t, ts)

	if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/applications/app_doomed", token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE application = %d (body %s)", status, body)
	}
	ev := lastEvent(t, mem, audit.ActionApplicationDeleted)
	if ev.Actor.Kind != domain.AuditActorUser || ev.Actor.UserID != "usr_test" || ev.Actor.Label != testEmail {
		t.Errorf("actor = %+v, want the signed-in user by name", ev.Actor)
	}
	if ev.Resource.ID != "app_doomed" || ev.Resource.Name != "notify-svc" {
		t.Errorf("resource = %+v, want the application it deleted, by name", ev.Resource)
	}
	if ev.EnvironmentID != "env_test" {
		t.Errorf("environment_id = %q, want the scope the row is read through", ev.EnvironmentID)
	}
	// The provenance the hardening middleware already stamps on the response.
	if ev.TraceID == "" {
		t.Error("the entry carries no trace id")
	}
	if ev.ClientIP == "" {
		t.Error("the entry carries no client address")
	}
}

// An env-var change records the KEY and never the value — the one place a
// sealed value could plausibly leak back out (§6).
func TestEnvVarChangesAreAuditedByKeyNeverByValue(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, true)
	token := login(t, ts)
	const secret = "postgres://user:hunter2@db/app"

	if status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/applications/app_x/env/DATABASE_URL", token,
		`{"value":"`+secret+`"}`); status != http.StatusNoContent {
		t.Fatalf("PUT env var = %d (body %s)", status, body)
	}
	ev := lastEvent(t, mem, audit.ActionEnvVarSet)
	if ev.Detail["key"] != "DATABASE_URL" {
		t.Errorf("detail = %+v, want the key name", ev.Detail)
	}
	if ev.Resource.ID != "app_x" {
		t.Errorf("resource = %+v, want the application (an env var is read from its timeline)", ev.Resource)
	}
	// Belt and braces: the value must not appear anywhere in the entry, nor in
	// the API's rendering of it.
	encoded, err := json.Marshal(toAuditEventDTO(ev))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "hunter2") {
		t.Fatalf("an env var value reached the audit log: %s", encoded)
	}

	if status, _, _ := doJSON(t, "DELETE", ts.URL+"/api/v1/applications/app_x/env/DATABASE_URL", token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE env var = %d", status)
	}
	if got := lastEvent(t, mem, audit.ActionEnvVarRemoved); got.Detail["key"] != "DATABASE_URL" {
		t.Errorf("removal detail = %+v, want the key name", got.Detail)
	}
}

// "Every failure is in the audit log" (canvas 13t): a refused sign-in is a row,
// attributed to the address it claimed to be, with the password nowhere near it.
func TestFailedSignInIsRecordedAsAFailure(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"not-the-password"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d", status)
	}
	ev := lastEvent(t, mem, audit.ActionLogin)
	if ev.Outcome != domain.AuditFailure {
		t.Errorf("outcome = %q, want failure", ev.Outcome)
	}
	if ev.Actor.Kind != domain.AuditActorAnonymous || ev.Actor.Label != testEmail {
		t.Errorf("actor = %+v, want an anonymous actor labelled with the attempted address", ev.Actor)
	}
	if ev.Actor.UserID != "" {
		t.Error("a refused sign-in was attributed to a user id")
	}
	if ev.Detail["reason"] != "invalid credentials" {
		t.Errorf("detail = %+v, want a reason", ev.Detail)
	}
	encoded, err := json.Marshal(toAuditEventDTO(ev))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "not-the-password") {
		t.Fatalf("the attempted password reached the audit log: %s", encoded)
	}
}

// A successful sign-in is attributed to the account, not to the string typed.
func TestSuccessfulSignInIsRecordedAgainstTheAccount(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	login(t, ts)
	ev := lastEvent(t, mem, audit.ActionLogin)
	if ev.Outcome != domain.AuditSuccess {
		t.Errorf("outcome = %q, want success", ev.Outcome)
	}
	if ev.Actor.Kind != domain.AuditActorUser || ev.Actor.UserID != "usr_test" {
		t.Errorf("actor = %+v, want the signed-in account", ev.Actor)
	}
}

// A token acts as its owner: the row names both, so a leak is traced to a
// credential as well as to a person.
func TestAnAPITokensActionsNameTheTokenAndItsOwner(t *testing.T) {
	ts, mem, apps := newAuditServer(t, domain.RoleOwner, nil, true)
	apps.apps["app_tok"] = domain.Application{
		ID: "app_tok", EnvironmentID: "env_test", Name: "ci-target",
		Runtime: domain.AppRuntime{ServerID: "srv_test"},
	}
	session := login(t, ts)
	writeToken := createToken(t, ts, session, "ci", `["read","write"]`)

	if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/applications/app_tok", writeToken, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE as a token = %d (body %s)", status, body)
	}
	ev := lastEvent(t, mem, audit.ActionApplicationDeleted)
	if ev.Actor.Kind != domain.AuditActorToken {
		t.Errorf("actor kind = %q, want token", ev.Actor.Kind)
	}
	if ev.Actor.UserID != "usr_test" {
		t.Errorf("actor user = %q, want the token's owner", ev.Actor.UserID)
	}
	if ev.Actor.TokenID == "" {
		t.Error("the entry does not name the token to revoke")
	}
}

// Minting a token is itself a row — with its abilities and never its secret.
func TestTokenCreationIsAudited(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, false)
	session := login(t, ts)
	raw := createToken(t, ts, session, "ci", `["read"]`)

	ev := lastEvent(t, mem, audit.ActionTokenCreated)
	if ev.Resource.Name != "ci" {
		t.Errorf("resource = %+v, want the token's name", ev.Resource)
	}
	encoded, err := json.Marshal(toAuditEventDTO(ev))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), raw) {
		t.Fatal("the token secret reached the audit log")
	}
	if !strings.Contains(string(encoded), "read") {
		t.Errorf("the abilities are not recorded: %s", encoded)
	}
}

// A deploy is recorded against the DEPLOYMENT it created, with the application
// it belongs to in the detail — so an application's timeline and a deployment's
// permalink both find it.
func TestDeployIsAuditedAgainstTheDeployment(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, true)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/applications/app_x/deploy", token, `{"ref":"main"}`)
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d (body %s)", status, body)
	}
	ev := lastEvent(t, mem, audit.ActionDeployStarted)
	if ev.Resource.Kind != "deployment" || ev.Resource.ID == "" {
		t.Errorf("resource = %+v, want the deployment", ev.Resource)
	}
	if ev.Detail["application_id"] != "app_x" {
		t.Errorf("detail = %+v, want the application it deployed", ev.Detail)
	}
	if ev.Detail["trigger"] != "manual" || ev.Detail["ref"] != "main" {
		t.Errorf("detail = %+v, want the trigger and ref", ev.Detail)
	}
	if ev.EnvironmentID != "env_test" {
		t.Errorf("environment_id = %q, want the scope the row is read through", ev.EnvironmentID)
	}
}

// ─── write: the ownership chain on decisions and backups (§4, §5) ───────────

// An approval decision belongs to the team that owns the deploy. Passing no
// chain would resolve team_id to NULL and make the row PANEL-scoped: the owner
// who wants to know who approved a deploy in their own project could not read
// it, while a panel admin outside the team could. This is the in-team half the
// cross-team denial tests never asserted.
func TestProtectionDecisionsAreScopedToTheirProject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		body   string
		status int
		action string
	}{
		{"approve", "/api/v1/deployments/dep_test/approve", "", http.StatusAccepted, audit.ActionDeployApproved},
		{"reject", "/api/v1/deployments/dep_test/reject", `{"reason":"shipping Monday"}`, http.StatusOK, audit.ActionDeployRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_default"}, true, func(d *Deps) {
				d.Protection = newFakeProtection()
			})
			token := login(t, ts)
			if status, _, body := doJSON(t, "POST", ts.URL+tc.path, token, tc.body); status != tc.status {
				t.Fatalf("%s = %d, want %d (body %s)", tc.name, status, tc.status, body)
			}
			ev := lastEvent(t, mem, tc.action)
			if ev.ProjectID != "prj_test" {
				t.Errorf("project_id = %q, want the project the deploy lives in", ev.ProjectID)
			}
			if ev.TeamID != "tm_default" {
				t.Errorf("team_id = %q — a decision with no chain is a panel-scoped row", ev.TeamID)
			}
			// The point of the scope: the team reads its own decision back.
			if !auditIDs(listAudit(t, ts, token, "?action=protection"))[ev.ID] {
				t.Error("a member of the team cannot read the decision made in their own project")
			}
		})
	}
}

// The other side of the same property: a panel admin who is not in the team
// must NOT see a decision made inside it. A panel-scoped row would leak to
// every admin, which is what the missing chain caused.
func TestAPanelAdminOutsideTheTeamCannotReadItsDecisions(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleAdmin, []string{"tm_other"}, false)
	// Written the way the handler writes it: the project only, chain resolved
	// on insert.
	_, _ = mem.InsertAuditEvent(context.Background(), domain.AuditEvent{
		ID: "aud_decision", Action: audit.ActionDeployApproved, Outcome: domain.AuditSuccess,
		Actor:     domain.AuditActor{Kind: domain.AuditActorUser, UserID: "usr_someone", Label: "someone@example.com"},
		Resource:  audit.Resource(audit.ResourceDeployment, "dep_test", ""),
		ProjectID: "prj_test",
	})
	token := login(t, ts)
	if auditIDs(listAudit(t, ts, token, ""))["aud_decision"] {
		t.Error("a panel admin outside the team read its approval decision")
	}
}

// Backup schedules and runs happen NEAR a database rather than to it, so the
// handler has to resolve the database's environment itself; without it the rows
// land at panel scope, invisible to the team that owns the database.
func TestBackupScheduleActionsAreScopedToTheDatabasesTeam(t *testing.T) {
	dbStore := newFakeDatabasesStore()
	dbStore.dbs["db_test"] = domain.Database{
		ID: "db_test", EnvironmentID: "env_test", Name: "orders-pg", Engine: domain.EnginePostgreSQL,
	}
	bak := newFakeBackupSchedules(dbStore)
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_default"}, true, func(d *Deps) {
		d.Databases = databases.NewService(dbStore, testBox(t), &fakeDbReconciler{})
		d.BackupSchedules = databases.NewBackupScheduleService(bak)
		d.Backups = bak
	})
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/databases/db_test/backups", token,
		`{"target_id":"bkt_test","schedule":"0 3 * * *","retention_count":7}`)
	if status != http.StatusCreated {
		t.Fatalf("create schedule = %d (body %s)", status, body)
	}
	ev := lastEvent(t, mem, audit.ActionBackupScheduleCreated)
	if ev.EnvironmentID != "env_test" || ev.TeamID != "tm_default" {
		t.Fatalf("schedule row scope = env %q team %q, want the database's environment and team", ev.EnvironmentID, ev.TeamID)
	}
	if !auditIDs(listAudit(t, ts, token, "?action=backup_schedule"))[ev.ID] {
		t.Error("a member of the team cannot read the schedule they created")
	}

	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/databases/db_test/backups/"+bak.scheduleID+"/run", token, ""); status != http.StatusAccepted {
		t.Fatalf("run backup = %d (body %s)", status, body)
	}
	if run := lastEvent(t, mem, audit.ActionBackupRunRequested); run.TeamID != "tm_default" {
		t.Errorf("run row team = %q, want the database's team", run.TeamID)
	}

	if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/databases/db_test/backups/"+bak.scheduleID, token, ""); status != http.StatusNoContent {
		t.Fatalf("delete schedule = %d (body %s)", status, body)
	}
	if del := lastEvent(t, mem, audit.ActionBackupScheduleDeleted); del.TeamID != "tm_default" {
		t.Errorf("delete row team = %q, want the database's team", del.TeamID)
	}
}

// ─── write: invitations and access requests (§4, §5) ────────────────────────

// newAccessAuditServer wires the invitation and access-request routes over the
// REAL audit service, with usr_test holding teamRole in tm_test. The panel role
// stays `member` so the team rank is the only thing granting anything — a panel
// owner would bypass it.
func newAccessAuditServer(t *testing.T, teamRole string) (*httptest.Server, *memAuditStore, *fakeInviteService, *fakeAccessRequestService) {
	t.Helper()
	invites := newFakeInviteService()
	requests := newFakeAccessRequestService()
	ts, mem, _ := newAuditServer(t, domain.RoleMember, []string{"tm_test"}, false, func(d *Deps) {
		ft, ok := d.Teams.(*fakeTeams)
		if !ok {
			t.Fatalf("Teams is %T, not the fake this test configures", d.Teams)
		}
		ft.teamRoles["usr_test"] = map[string]string{"tm_test": teamRole}
		d.Invites = invites
		d.AccessRequests = requests
	})
	return ts, mem, invites, requests
}

// Every verb this feature performs lands as a row scoped to the TEAM it
// happened in (invitations-and-access-requests.md §6, acceptance 9 and 13).
//
// The team scope is the load-bearing part: `team_id` is what authorizes the
// read, so a row without it is PANEL-scoped — invisible to the owner who
// approved the promotion and visible to every panel admin outside the team.
// Each case therefore asserts the scope, the resource, the detail keys an
// operator reads the row for, and that the team can read it back.
func TestInvitationAndAccessVerbsAreAuditedAgainstTheirTeam(t *testing.T) {
	for _, tc := range []struct {
		teamRole           string
		public             bool
		method, path, body string
		want               int
		action, resource   string
		actorID            string
		detail             map[string]any
	}{
		{
			teamRole: domain.RoleAdmin,
			method:   "POST", path: "/api/v1/teams/tm_test/invites",
			body: `{"email":"new@example.test","role":"member"}`, want: http.StatusCreated,
			action: audit.ActionInviteCreated, resource: audit.ResourceTeamInvite, actorID: "usr_test",
			detail: map[string]any{"email": "new@example.test", "role": domain.RoleMember, "mail_sent": true},
		},
		{
			teamRole: domain.RoleAdmin,
			method:   "DELETE", path: "/api/v1/teams/tm_test/invites/" + testInviteID,
			want:   http.StatusNoContent,
			action: audit.ActionInviteRevoked, resource: audit.ResourceTeamInvite, actorID: "usr_test",
			detail: map[string]any{"email": "priya@meridian.dev", "role": domain.RoleAdmin},
		},
		{
			// The public route has no principal, so the row is attributed to the
			// account that joined — the person who actually did it.
			teamRole: domain.RoleAdmin, public: true,
			method: "POST", path: "/api/v1/invites/" + testInviteToken + "/accept",
			body: `{"password":"correct-horse"}`, want: http.StatusOK,
			action: audit.ActionInviteAccepted, resource: audit.ResourceTeamInvite, actorID: "usr_priya",
			detail: map[string]any{"email": "priya@meridian.dev", "role": domain.RoleAdmin, "account_created": false},
		},
		{
			teamRole: domain.RoleAdmin,
			method:   "POST", path: "/api/v1/teams/tm_test/access-requests",
			body: `{"requested_role":"owner","message":"Need to add a server."}`, want: http.StatusCreated,
			action: audit.ActionAccessRequested, resource: audit.ResourceAccessRequest, actorID: "usr_test",
			detail: map[string]any{"requested_role": domain.RoleOwner, "current_role": domain.RoleAdmin},
		},
		{
			teamRole: domain.RoleOwner,
			method:   "POST", path: "/api/v1/access-requests/acr_test/grant",
			want:   http.StatusOK,
			action: audit.ActionAccessGranted, resource: audit.ResourceAccessRequest, actorID: "usr_test",
			detail: map[string]any{
				"requested_role": domain.RoleAdmin,
				"requester":      "priya@meridian.dev",
				"member_user_id": "usr_priya",
			},
		},
		{
			teamRole: domain.RoleOwner,
			method:   "POST", path: "/api/v1/access-requests/acr_test/deny",
			body: `{"reason":"ask again after the audit"}`, want: http.StatusOK,
			action: audit.ActionAccessDenied, resource: audit.ResourceAccessRequest, actorID: "usr_test",
			detail: map[string]any{
				"requested_role": domain.RoleAdmin,
				"requester":      "priya@meridian.dev",
				"member_user_id": "usr_priya",
				"reason":         "ask again after the audit",
			},
		},
	} {
		t.Run(tc.action, func(t *testing.T) {
			ts, mem, _, _ := newAccessAuditServer(t, tc.teamRole)
			session := login(t, ts)
			caller := session
			if tc.public {
				caller = ""
			}
			if status, _, body := doJSON(t, tc.method, ts.URL+tc.path, caller, tc.body); status != tc.want {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, status, tc.want, body)
			}
			ev := lastEvent(t, mem, tc.action)
			if ev.TeamID != "tm_test" {
				t.Errorf("team_id = %q — a row with no team is panel-scoped, readable by admins outside the team and not by its owner", ev.TeamID)
			}
			if ev.Resource.Kind != tc.resource || ev.Resource.ID == "" {
				t.Errorf("resource = %+v, want a %s with an id", ev.Resource, tc.resource)
			}
			if ev.Outcome != domain.AuditSuccess {
				t.Errorf("outcome = %q, want success", ev.Outcome)
			}
			if ev.Actor.Kind != domain.AuditActorUser || ev.Actor.UserID != tc.actorID {
				t.Errorf("actor = %+v, want the user %s", ev.Actor, tc.actorID)
			}
			for k, want := range tc.detail {
				if got := ev.Detail[k]; got != want {
					t.Errorf("detail[%q] = %v, want %v (whole detail %+v)", k, got, want, ev.Detail)
				}
			}
			// The point of the scope: the team reads its own row back.
			if !auditIDs(listAudit(t, ts, session, "?team_id=tm_test"))[ev.ID] {
				t.Error("a member of the team cannot read the row their own team produced")
			}
		})
	}
}

// The accept URL is a bearer credential that grants a membership: it is
// readable exactly once, in the response that created it. No row this feature
// writes may carry it — not the creation, and not the accept that spends it —
// or `GET /api/v1/audit` would hand a live invitation to every admin of the
// team (spec §8, acceptance 13).
func TestInvitationAuditRowsCarryNoToken(t *testing.T) {
	ts, mem, _, _ := newAccessAuditServer(t, domain.RoleAdmin)
	session := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/teams/tm_test/invites", session,
		`{"email":"new@example.test","role":"member"}`)
	if status != http.StatusCreated {
		t.Fatalf("create invite = %d (body %s)", status, body)
	}
	var created struct {
		AcceptURL string `json:"accept_url"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	cut := strings.LastIndexByte(created.AcceptURL, '.')
	if !strings.Contains(created.AcceptURL, "/invite/") || cut < 0 {
		t.Fatalf("accept_url %q is not the link the mail sends", created.AcceptURL)
	}
	secret := created.AcceptURL[cut+1:]

	// The other place a token is spoken: the public accept that spends one.
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"correct-horse"}`); status != http.StatusOK {
		t.Fatalf("accept = %d (body %s)", status, body)
	}

	if len(mem.events) < 2 {
		t.Fatalf("%d rows recorded, want at least the create and the accept", len(mem.events))
	}
	for _, ev := range mem.events {
		encoded, err := json.Marshal(toAuditEventDTO(ev))
		if err != nil {
			t.Fatalf("marshal %s: %v", ev.Action, err)
		}
		for _, leak := range []string{created.AcceptURL, secret, testInviteToken} {
			if strings.Contains(string(encoded), leak) {
				t.Errorf("the %s row carries %q: %s", ev.Action, leak, encoded)
			}
		}
	}
}

// A throttled sign-in never touches the database, so recording one row per
// refused packet would let an anonymous caller drive unbounded durable writes
// at their own request rate — the very work the login throttle bounds
// (control-plane-hardening.md §5). One row per throttle EPISODE.
func TestAThrottledSignInIsRecordedOncePerEpisode(t *testing.T) {
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mem := newMemAuditStore()
	// One failure per window, so the second attempt is already refused.
	api := New(Deps{
		Auth: auth.NewAuthenticator(
			&fakeAuthStore{
				user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
				sessions: map[string]domain.User{},
			},
			fakeBox{}, auth.NewLimiter(1, time.Minute), time.Hour),
		Audit:     audit.NewService(mem, 0, log),
		Teams:     newFakeTeams(),
		Opener:    testBox(t),
		Pinger:    okPinger{},
		CACertPEM: []byte("x"),
		Log:       log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)

	body := `{"email":"` + testEmail + `","password":"not-the-password"}`
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "", body); status != http.StatusUnauthorized {
		t.Fatal("the first wrong password did not answer 401")
	}
	for range 5 {
		if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "", body); status != http.StatusTooManyRequests {
			t.Fatal("the address was not throttled")
		}
	}
	throttled := 0
	for _, e := range mem.events {
		if e.Action == audit.ActionLogin && e.Detail["reason"] == "throttled" {
			throttled++
		}
	}
	if throttled != 1 {
		t.Errorf("%d throttle rows for 5 refused attempts, want exactly 1 — the transition, not the packets", throttled)
	}
}

// The record is written but never fails the request: the action has already
// happened, and answering 500 would report a failure for something that
// succeeded — and invite a retry that does it twice (§4).
func TestAFailingAuditWriteDoesNotFailTheRequest(t *testing.T) {
	ts, mem, apps := newAuditServer(t, domain.RoleMember, []string{"tm_a"}, true)
	apps.apps["app_boom"] = domain.Application{
		ID: "app_boom", EnvironmentID: "env_test", Name: "boom",
		Runtime: domain.AppRuntime{ServerID: "srv_test"},
	}
	token := login(t, ts)
	mem.insertErr = errors.New("the audit table is unavailable")

	if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/applications/app_boom", token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE with a failing audit write = %d, want it to still succeed (body %s)", status, body)
	}
	if _, ok := apps.apps["app_boom"]; ok {
		t.Error("the application survived a delete that answered 204")
	}
}

// Every entry a handler writes uses a verb from the closed vocabulary — a
// typo'd constant would make its rows unfindable by the filter that exists to
// find them.
func TestEveryRecordedActionIsInTheVocabulary(t *testing.T) {
	ts, mem, _ := newAuditServer(t, domain.RoleOwner, nil, true)
	token := login(t, ts)
	doJSON(t, "PUT", ts.URL+"/api/v1/applications/app_x/env/KEY", token, `{"value":"v"}`)
	doJSON(t, "POST", ts.URL+"/api/v1/auth/logout", token, "")

	if len(mem.events) == 0 {
		t.Fatal("nothing was recorded")
	}
	for _, e := range mem.events {
		if !audit.ValidAction(e.Action) {
			t.Errorf("handler recorded %q, which is outside the vocabulary", e.Action)
		}
	}
}

// lastEvent returns the most recent entry with the given action.
func lastEvent(t *testing.T, mem *memAuditStore, action string) domain.AuditEvent {
	t.Helper()
	for i := len(mem.events) - 1; i >= 0; i-- {
		if mem.events[i].Action == action {
			return mem.events[i]
		}
	}
	t.Fatalf("no %s entry was recorded (have %d events)", action, len(mem.events))
	return domain.AuditEvent{}
}

// ─── fakes for the backup-scope test ────────────────────────────────────────

// fakeBackupSchedules is the smallest BackupScheduleStore and BackupOps that
// can create, run and delete one schedule for db_test. The backup MACHINERY is
// tested in core/databases; what this proves here is which ownership chain the
// handler puts on the row it writes.
type fakeBackupSchedules struct {
	dbs *fakeDatabasesStore
	// scheduleID is the id of the schedule the test created, so the run and
	// delete routes can address it without parsing the response.
	scheduleID string
	schedules  map[string]domain.DatabaseBackup
}

func newFakeBackupSchedules(dbs *fakeDatabasesStore) *fakeBackupSchedules {
	return &fakeBackupSchedules{dbs: dbs, schedules: map[string]domain.DatabaseBackup{}}
}

func (f *fakeBackupSchedules) CreateDatabaseBackup(_ context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	f.schedules[b.ID] = b
	f.scheduleID = b.ID
	return b, nil
}

func (f *fakeBackupSchedules) GetDatabaseBackup(_ context.Context, id string) (domain.DatabaseBackup, error) {
	b, ok := f.schedules[id]
	if !ok {
		return domain.DatabaseBackup{}, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeBackupSchedules) ListDatabaseBackups(_ context.Context, dbID string) ([]domain.DatabaseBackup, error) {
	out := []domain.DatabaseBackup{}
	for _, b := range f.schedules {
		if b.DatabaseID == dbID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBackupSchedules) UpdateDatabaseBackup(_ context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	f.schedules[b.ID] = b
	return b, nil
}

func (f *fakeBackupSchedules) DeleteDatabaseBackup(_ context.Context, id string) error {
	delete(f.schedules, id)
	return nil
}

func (f *fakeBackupSchedules) GetDatabase(ctx context.Context, id string) (domain.Database, error) {
	return f.dbs.GetDatabase(ctx, id)
}

func (f *fakeBackupSchedules) GetBackupTarget(_ context.Context, id string) (domain.BackupTarget, error) {
	if id != "bkt_test" {
		return domain.BackupTarget{}, store.ErrNotFound
	}
	return domain.BackupTarget{ID: id, Name: "offsite", Bucket: "backups"}, nil
}

func (f *fakeBackupSchedules) ListBackupRecords(_ context.Context, _ string) ([]domain.BackupRecord, error) {
	return []domain.BackupRecord{}, nil
}

func (f *fakeBackupSchedules) RunBackup(_ context.Context, scheduleID string) (domain.BackupRecord, error) {
	if _, ok := f.schedules[scheduleID]; !ok {
		return domain.BackupRecord{}, store.ErrNotFound
	}
	return domain.BackupRecord{ID: "bkr_test", DatabaseBackupID: scheduleID, Status: domain.BackupRunning}, nil
}

func (f *fakeBackupSchedules) RunRestore(context.Context, string, string, bool) error { return nil }

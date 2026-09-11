package rest

// HTTP tests for a project-scoped API token (api-tokens.md §"Narrowing a
// token").
//
// Abilities say what a credential may do; a scope says where. The two questions
// this exercises are the ones a scope exists to answer: a token minted for one
// project cannot reach another, and cannot reach the panel-wide routes that
// belong to no project at all.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
)

// newScopedTokenServer wires a panel where usr_test is a member of two
// projects, and mints one API token confined to the first. It returns the
// server and the raw token.
func newScopedTokenServer(t *testing.T, scope string) (*httptest.Server, string) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
		sessions: map[string]domain.User{},
	}
	ft := newFakeTeams()
	ft.projectRoles["usr_test"] = map[string]string{
		"prj_test":  domain.RoleOwner,
		"prj_other": domain.RoleOwner,
	}
	ft.teams = nil

	authn := auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour)
	box := testBox(t)
	api := New(Deps{
		Auth:         authn,
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

	raw, _, err := authn.CreateToken(context.Background(), "usr_test", "ci", domain.AllAbilities(), nil, scope)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return ts, raw
}

// The project it was minted for is reachable; any other project answers 404,
// the same answer a non-member gets. Anything more specific would let one token
// enumerate which projects its owner can see.
func TestScopedTokenReachesOnlyItsProject(t *testing.T) {
	ts, token := newScopedTokenServer(t, "prj_test")

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_test", token, "")
	if status == http.StatusNotFound || status == http.StatusForbidden {
		t.Fatalf("the scoped token cannot reach its own project: %d (%s)", status, body)
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_other", token, "")
	if status != http.StatusNotFound {
		t.Fatalf("reaching another project = %d, want 404 (body %s)", status, body)
	}
}

// An unscoped token is unchanged: both projects remain reachable, which is what
// every token issued before scoping existed must keep doing.
func TestUnscopedTokenIsUnaffected(t *testing.T) {
	ts, token := newScopedTokenServer(t, "")

	for _, id := range []string{"prj_test", "prj_other"} {
		status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/projects/"+id, token, "")
		if status == http.StatusNotFound || status == http.StatusForbidden {
			t.Fatalf("an unscoped token cannot reach %s: %d (%s)", id, status, body)
		}
	}
}

// Panel- and team-level routes belong to no project, so "which project?" has no
// answer for them and a scoped token is refused outright.
func TestScopedTokenCannotReachPanelRoutes(t *testing.T) {
	ts, token := newScopedTokenServer(t, "prj_test")

	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/teams"},
		{"GET", "/api/v1/users"},
		{"GET", "/api/v1/servers"},
		{"GET", "/api/v1/panel/version"},
		{"GET", "/api/v1/audit"},
		{"GET", "/api/v1/deploy-keys"},
		{"GET", "/api/v1/backup-targets"},
	} {
		t.Run(route.path, func(t *testing.T) {
			status, _, body := doJSON(t, route.method, ts.URL+route.path, token, "")
			if status != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 (body %s)", route.method, route.path, status, body)
			}
		})
	}

	// The same routes answer something other than 403 for an unscoped token, so
	// the refusal above is the scope talking rather than a missing dependency.
	tsOpen, open := newScopedTokenServer(t, "")
	if status, _, body := doJSON(t, "GET", tsOpen.URL+"/api/v1/teams", open, ""); status == http.StatusForbidden {
		t.Fatalf("an unscoped token was also refused /teams: %d (%s)", status, body)
	}
}

// requiredAbility is the table that decides which ability a route needs. It is
// checked directly because getting it wrong is silent: a route that falls
// through to `write` is reachable by every legacy token, and one that lands on
// the wrong narrow ability is unreachable by the token minted for it.
func TestRequiredAbilityPerRoute(t *testing.T) {
	cases := []struct {
		method, pattern string
		want            domain.Ability
	}{
		{"GET", "GET /api/v1/applications/{id}", domain.AbilityRead},
		{"GET", "GET /api/v1/applications/{id}/env", domain.AbilityRead},
		{"POST", "POST /api/v1/applications/{id}/deploy", domain.AbilityDeploy},
		{"POST", "POST /api/v1/deployments/{id}/rollback", domain.AbilityDeploy},
		{"PUT", "PUT /api/v1/applications/{id}/env/{key}", domain.AbilityEnv},
		{"DELETE", "DELETE /api/v1/applications/{id}/env/{key}", domain.AbilityEnv},
		{"POST", "POST /api/v1/servers", domain.AbilityServers},
		{"DELETE", "DELETE /api/v1/servers/{id}", domain.AbilityServers},
		{"POST", "POST /api/v1/teams", domain.AbilityAdmin},
		{"POST", "POST /api/v1/users", domain.AbilityAdmin},
		{"PUT", "PUT /api/v1/panel/tls", domain.AbilityAdmin},
		// Anything not named falls through to write, never to a narrower one.
		{"POST", "POST /api/v1/projects", domain.AbilityWrite},
		{"PATCH", "PATCH /api/v1/applications/{id}", domain.AbilityWrite},
		{"POST", "", domain.AbilityWrite}, // no pattern: the safe default
	}
	for _, c := range cases {
		r := &http.Request{Method: c.method, Pattern: c.pattern}
		if got := requiredAbility(r); got != c.want {
			t.Errorf("requiredAbility(%s %s) = %q, want %q", c.method, c.pattern, got, c.want)
		}
	}
}

// A legacy token holding write must still satisfy every narrow route, or the
// day this ships is the day someone's automation stops.
func TestLegacyWriteTokenStillSatisfiesNarrowRoutes(t *testing.T) {
	legacy := auth.Principal{Kind: auth.KindAPIToken, Abilities: domain.AllAbilities()}
	for _, pattern := range []string{
		"PUT /api/v1/applications/{id}/env/{key}",
		"POST /api/v1/servers",
		"POST /api/v1/teams",
		"POST /api/v1/projects",
	} {
		need := requiredAbility(&http.Request{Method: "POST", Pattern: pattern})
		if !legacy.Can(need) {
			t.Errorf("a legacy read/write/deploy token lost %s (needs %q)", pattern, need)
		}
	}
}

func TestOutsideProjectScope(t *testing.T) {
	for _, p := range []string{
		"/api/v1/teams", "/api/v1/teams/tm_1/members", "/api/v1/users/usr_1",
		"/api/v1/panel/version", "/api/v1/servers", "/api/v1/audit",
		"/api/v1/invites/abc", "/api/v1/access-requests/acr_1/grant",
	} {
		if !outsideProjectScope(p) {
			t.Errorf("outsideProjectScope(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/api/v1/projects/prj_1", "/api/v1/applications/app_1",
		"/api/v1/environments/env_1", "/api/v1/databases/db_1",
		"/api/v1/deployments/dep_1/rollback",
	} {
		if outsideProjectScope(p) {
			t.Errorf("outsideProjectScope(%q) = true, want false", p)
		}
	}
}

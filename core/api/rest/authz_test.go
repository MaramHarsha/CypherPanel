package rest

// Authorization boundary tests (teams-and-roles.md §3): a non-member sees a
// resource as 404 (no tenancy probing); a member of insufficient rank sees 403;
// panel-role gates guard shared infrastructure.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/servers"
)

// newAuthzServer builds a server whose logged-in user has the given panel role
// and the given per-project role (via fakeTeams), so route enforcement can be
// exercised directly. The seeded app_x lives in project prj_test.
func newAuthzServer(t *testing.T, panelRole, projectRole string) *httptest.Server {
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
	// A non-owner belongs to no team by default (empty ListFor), matching a
	// stranger; membership is only what we grant above.
	ft.teams = nil

	api := New(Deps{
		Auth:         auth.NewAuthenticator(authStore, auth.NewLimiter(100, time.Minute), time.Hour),
		Servers:      servers.NewService(&fakeServersStore{inUse: map[string]bool{}}, noopAgentBus{}, 15*time.Minute, log),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), box),
		DeployKeys:   deploykeys.NewService(newFakeDeployKeysStore(), box),
		Databases:    databases.NewService(newFakeDatabasesStore(), box, &fakeDbReconciler{}),
		Teams:        ft,
		Scheduler:    &fakeDeployer{},
		Deployments:  fakeDeploymentReader{},
		Opener:       box,
		Logs:         newFakeLogs(),
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestNonMemberGets404OnForeignApp(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleMember, "") // panel member, no project role
	token := login(t, ts)
	// app_x resolves to prj_test, where this user has no role → 404, not 403,
	// so the resource's existence is not leaked.
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/applications/app_x", token, ""); status != http.StatusNotFound {
		t.Fatalf("foreign app GET = %d, want 404", status)
	}
	// A database in the same project the user cannot see is likewise 404.
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/databases/db_test", token, ""); status != http.StatusNotFound {
		t.Fatalf("foreign db GET = %d, want 404", status)
	}
}

func TestMemberCanReadButNotDelete(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleMember, domain.RoleMember) // member of prj_test's team
	token := login(t, ts)
	// A member may read the app...
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/applications/app_x", token, ""); status != http.StatusOK {
		t.Fatalf("member app GET = %d, want 200", status)
	}
	// ...but deleting the project needs admin → 403.
	if status, _, _ := doJSON(t, "DELETE", ts.URL+"/api/v1/projects/prj_test", token, ""); status != http.StatusForbidden {
		t.Fatalf("member project DELETE = %d, want 403", status)
	}
}

func TestAdminCanDeleteProject(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleMember, domain.RoleAdmin)
	token := login(t, ts)
	if status, _, _ := doJSON(t, "DELETE", ts.URL+"/api/v1/projects/prj_test", token, ""); status != http.StatusNoContent {
		t.Fatalf("admin project DELETE = %d, want 204", status)
	}
}

func TestPanelMemberCannotCreateServerOrDeployKey(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleMember, "")
	token := login(t, ts)
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"box"}`); status != http.StatusForbidden {
		t.Fatalf("panel member create server = %d, want 403", status)
	}
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/deploy-keys", token, `{"name":"k"}`); status != http.StatusForbidden {
		t.Fatalf("panel member create deploy key = %d, want 403", status)
	}
}

func TestPanelAdminCanCreateServer(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleAdmin, "")
	token := login(t, ts)
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"box"}`); status != http.StatusCreated {
		t.Fatalf("panel admin create server = %d, want 201", status)
	}
}

func TestOnlyPanelOwnerChangesUserRole(t *testing.T) {
	ts := newAuthzServer(t, domain.RoleAdmin, "")
	token := login(t, ts)
	if status, _, _ := doJSON(t, "PATCH", ts.URL+"/api/v1/users/usr_other", token, `{"role":"admin"}`); status != http.StatusForbidden {
		t.Fatalf("panel admin set role = %d, want 403", status)
	}
}

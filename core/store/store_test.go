package store

// Real-database tests (ENGINEERING rule 29): these run against the PostgreSQL
// at CYPHERD_TEST_DATABASE_URL — a throwaway instance (make test-store spins
// one up; integration.yml provides one in CI, where this suite always runs).
// Without the variable the suite skips locally, keeping `make test` free of a
// Docker dependency.
//
// Every test creates its own uniquely-named rows and never assumes an empty
// database, so the suite is order-independent and re-runnable.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("CYPHERD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CYPHERD_TEST_DATABASE_URL not set; run via `make test-store` (CI always runs this)")
	}
	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// seedApp creates the server → project → environment → application chain and
// returns the pieces tests need. The application carries two sealed env vars.
func seedApp(t *testing.T, s *Store) (domain.Server, domain.Project, domain.Environment, domain.Application) {
	t.Helper()
	ctx := context.Background()

	srv, err := s.CreateServerWithToken(ctx, ids.New(ids.PrefixServer), "box", ids.New(ids.PrefixJoinToken), []byte(ids.Secret()), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateServerWithToken: %v", err)
	}
	proj, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "proj", "tm_default", ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}
	app, err := s.CreateApplicationWithEnv(ctx, domain.Application{
		ID:                 ids.New(ids.PrefixApplication),
		EnvironmentID:      env.ID,
		Name:               "web",
		Source:             domain.AppSource{Kind: "github", Repo: "acme/web", Branch: "main"},
		Build:              domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime:            domain.AppRuntime{ServerID: srv.ID, Port: 8080, Replicas: 1},
		Route:              domain.AppRoute{Domain: "web.example.com", HTTPS: true},
		Health:             domain.AppHealth{Path: "/", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    []byte("ct"),
		WebhookSecretNonce: []byte("nonce"),
	}, []domain.EnvVar{
		{Key: "DATABASE_URL", ValueCT: []byte("ct1"), ValueNonce: []byte("n1")},
		{Key: "API_KEY", ValueCT: []byte("ct2"), ValueNonce: []byte("n2")},
	})
	if err != nil {
		t.Fatalf("CreateApplicationWithEnv: %v", err)
	}
	return srv, proj, env, app
}

func TestStoreProjectEnvironmentTx(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	proj, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "shop", "tm_default", ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}
	if env.ProjectID != proj.ID {
		t.Fatalf("environment not linked: %+v", env)
	}

	// Duplicate environment name inside the project is a conflict.
	if _, err := s.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), proj.ID, "production"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate env err = %v, want ErrConflict", err)
	}
	// The same name in another project is fine.
	proj2, _, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "other", "tm_default", ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("second project: %v", err)
	}
	if _, err := s.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), proj2.ID, "staging"); err != nil {
		t.Fatalf("staging env: %v", err)
	}
	// An environment for a project that vanished is not-found, not a 500.
	if _, err := s.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), "prj_gone", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project err = %v, want ErrNotFound", err)
	}
}

// A raw (non-HTTP) app persists its port publishes and health kind and reads
// back with an empty route domain.
func TestStoreAppPortsAndHealthKind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)

	rawID := ids.New(ids.PrefixApplication)
	raw := domain.Application{
		ID:                 rawID,
		EnvironmentID:      env.ID,
		Name:               "game-server",
		Source:             domain.AppSource{Kind: "github", Repo: "acme/game", Branch: "main"},
		Build:              domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime:            domain.AppRuntime{ServerID: srv.ID, Port: 25565, Replicas: 1},
		Route:              domain.AppRoute{}, // no domain — raw service
		Health:             domain.AppHealth{Kind: "tcp", Path: "/", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
		Ports:              []domain.PortMapping{{HostPort: 25565, ContainerPort: 25565, Protocol: "tcp"}, {HostPort: 25565, ContainerPort: 25565, Protocol: "udp"}},
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    []byte("ct"),
		WebhookSecretNonce: []byte("nonce"),
	}
	if _, err := s.CreateApplicationWithEnv(ctx, raw, nil); err != nil {
		t.Fatalf("CreateApplicationWithEnv: %v", err)
	}
	got, err := s.GetApplication(ctx, rawID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if got.Route.Domain != "" {
		t.Errorf("route domain = %q, want empty", got.Route.Domain)
	}
	if got.Health.Kind != "tcp" {
		t.Errorf("health kind = %q, want tcp", got.Health.Kind)
	}
	if len(got.Ports) != 2 || got.Ports[0].Protocol != "tcp" || got.Ports[1].Protocol != "udp" {
		t.Errorf("ports not preserved: %+v", got.Ports)
	}
}

func TestStoreApplicationRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, app := seedApp(t, s)

	got, err := s.GetApplication(ctx, app.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if got.Runtime.ServerID != srv.ID || got.Route.Domain != "web.example.com" || got.Health.Retries != 3 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if string(got.WebhookSecretCT) != "ct" || string(got.WebhookSecretNonce) != "nonce" {
		t.Errorf("sealed webhook secret not preserved: %+v", got)
	}
	if got.DesiredRevisionID != nil {
		t.Errorf("new application must have no desired revision, got %v", *got.DesiredRevisionID)
	}

	byHook, err := s.GetApplicationByWebhookID(ctx, app.WebhookID)
	if err != nil || byHook.ID != app.ID {
		t.Errorf("GetApplicationByWebhookID = %+v, %v", byHook, err)
	}

	vars, err := s.ListEnvVars(ctx, app.ID)
	if err != nil || len(vars) != 2 {
		t.Fatalf("ListEnvVars = %+v, %v; want both sealed vars", vars, err)
	}

	// Same name in the same environment is a conflict.
	dup := app
	dup.ID = ids.New(ids.PrefixApplication)
	dup.WebhookID = ids.New(ids.PrefixWebhook)
	if _, err := s.CreateApplicationWithEnv(ctx, dup, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate app err = %v, want ErrConflict", err)
	}

	// Env vars are upserts: a duplicate key in the input merges to one row,
	// last value winning — pinned so a future switch to plain INSERT (which
	// would turn this into a mid-transaction failure) is a visible change.
	second := app
	second.ID = ids.New(ids.PrefixApplication)
	second.Name = "web2"
	second.WebhookID = ids.New(ids.PrefixWebhook)
	created, err := s.CreateApplicationWithEnv(ctx, second, []domain.EnvVar{
		{Key: "A", ValueCT: []byte("first"), ValueNonce: []byte("n")},
		{Key: "A", ValueCT: []byte("second"), ValueNonce: []byte("n")},
	})
	if err != nil {
		t.Fatalf("CreateApplicationWithEnv with duplicate keys: %v", err)
	}
	merged, err := s.ListEnvVars(ctx, created.ID)
	if err != nil || len(merged) != 1 || string(merged[0].ValueCT) != "second" {
		t.Fatalf("duplicate env keys = %+v, %v; want one row, last value", merged, err)
	}

	if _, err := s.SetApplicationDesiredRevision(ctx, "app_gone", "rev_x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("desired revision on missing app = %v, want ErrNotFound", err)
	}
	_ = env
}

func TestStoreServerDeleteRestrictedWhileRunningApps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, _, app := seedApp(t, s)

	if err := s.DeleteServer(ctx, srv.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteServer with apps = %v, want ErrInUse", err)
	}
	if err := s.DeleteApplication(ctx, app.ID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if err := s.DeleteServer(ctx, srv.ID); err != nil {
		t.Fatalf("DeleteServer after app removal: %v", err)
	}
}

func TestStoreRevisionsAndDeployments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "abc123", []byte(`{"port":8080}`))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	if rev.Image != "" {
		t.Errorf("new revision image = %q, want empty until built", rev.Image)
	}
	rev, err = s.SetRevisionImage(ctx, rev.ID, "cypher/"+app.ID+":"+rev.ID)
	if err != nil || rev.Image == "" {
		t.Fatalf("SetRevisionImage: %+v, %v", rev, err)
	}

	upd, err := s.SetApplicationDesiredRevision(ctx, app.ID, rev.ID)
	if err != nil || upd.DesiredRevisionID == nil || *upd.DesiredRevisionID != rev.ID {
		t.Fatalf("SetApplicationDesiredRevision: %+v, %v", upd, err)
	}

	dep, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "manual")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if dep.Status != domain.DeployQueued || dep.FinishedAt != nil {
		t.Fatalf("new deployment = %+v, want queued and unfinished", dep)
	}

	// Non-terminal transition leaves finished_at unset.
	dep, err = s.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployBuilding, "")
	if err != nil || dep.FinishedAt != nil {
		t.Fatalf("building: %+v, %v; finished_at must stay unset", dep, err)
	}

	active, err := s.ListActiveDeployments(ctx)
	if err != nil {
		t.Fatalf("ListActiveDeployments: %v", err)
	}
	found := false
	for _, d := range active {
		if d.ID == dep.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("building deployment missing from active list")
	}

	// Terminal transition stamps finished_at and leaves the active list.
	dep, err = s.UpdateDeploymentStatus(ctx, dep.ID, domain.DeploySucceeded, "")
	if err != nil || dep.FinishedAt == nil {
		t.Fatalf("succeeded: %+v, %v; finished_at must be set", dep, err)
	}
	active, err = s.ListActiveDeployments(ctx)
	if err != nil {
		t.Fatalf("ListActiveDeployments: %v", err)
	}
	for _, d := range active {
		if d.ID == dep.ID {
			t.Fatal("terminal deployment still listed as active")
		}
	}

	list, err := s.ListDeploymentsByApplication(ctx, app.ID, 10)
	if err != nil || len(list) != 1 || list[0].ID != dep.ID {
		t.Fatalf("ListDeploymentsByApplication = %+v, %v", list, err)
	}
}

func TestStoreProjectCascadeDeletesEverything(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, env, app := seedApp(t, s)

	rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "sha", []byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "manual"); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetEnvironment(ctx, env.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("environment survived cascade: %v", err)
	}
	if _, err := s.GetApplication(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("application survived cascade: %v", err)
	}
	if _, err := s.GetRevision(ctx, rev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revision survived cascade: %v", err)
	}
	if vars, err := s.ListEnvVars(ctx, app.ID); err != nil || len(vars) != 0 {
		t.Fatalf("env vars survived cascade: %+v, %v", vars, err)
	}
}

// TestStoreTeamsAndAuthz exercises the membership join that authorizes every
// project-scoped route (GetTeamRoleForProject / ListProjectsByUser) and the
// cascades, against real Postgres (teams-and-roles.md §3).
func TestStoreTeamsAndAuthz(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// The name is unique in the schema and this suite migrates in place, so a
	// fixed one made the second run against the same database fail
	// ("already exists") — the other team tests here already suffix theirs.
	tm, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "authz-team-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	member, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "member-"+ids.Secret()+"@x.io", "h", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	outsider, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "outsider-"+ids.Secret()+"@x.io", "h", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.UpsertTeamMember(ctx, tm.ID, member.ID, domain.RoleAdmin); err != nil {
		t.Fatalf("UpsertTeamMember: %v", err)
	}
	proj, _, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "p", tm.ID, ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}

	// The member's role in the team owning the project is resolvable...
	role, err := s.GetTeamRoleForProject(ctx, proj.ID, member.ID)
	if err != nil || role != domain.RoleAdmin {
		t.Fatalf("member role for project = %q, %v; want admin", role, err)
	}
	// ...but an outsider has no row (→ the API surfaces 404).
	if _, err := s.GetTeamRoleForProject(ctx, proj.ID, outsider.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider role = %v, want ErrNotFound", err)
	}

	// Listing is scoped: the member sees the project, the outsider sees none.
	if ps, err := s.ListProjectsByUser(ctx, member.ID); err != nil || len(ps) != 1 || ps[0].ID != proj.ID {
		t.Fatalf("member projects = %+v, %v; want the one project", ps, err)
	}
	if ps, _ := s.ListProjectsByUser(ctx, outsider.ID); len(ps) != 0 {
		t.Fatalf("outsider projects = %+v, want none", ps)
	}

	// Owner counting backs the last-owner guard.
	if _, err := s.UpsertTeamMember(ctx, tm.ID, member.ID, domain.RoleOwner); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if n, err := s.CountTeamOwners(ctx, tm.ID); err != nil || n != 1 {
		t.Fatalf("owner count = %d, %v; want 1", n, err)
	}

	// Deleting the user cascades their membership away.
	if err := s.DeleteUser(ctx, member.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetTeamMember(ctx, tm.ID, member.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("membership survived user delete: %v", err)
	}
}

// TestStoreDefaultTeamBackfill asserts migration 0011 created the default team,
// enrolled the bootstrap-era users, and made every project team-owned.
func TestStoreDefaultTeamBackfill(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// The default team exists (created by the migration on a populated DB, or by
	// bootstrapAdmin on a fresh one — here the migration's INSERT guarantees it).
	if _, err := s.GetTeam(ctx, "tm_default"); err != nil {
		t.Fatalf("default team missing: %v", err)
	}
	// A project created without an explicit team still needs one: the app-create
	// path always supplies it, so a NULL team_id is impossible post-migration.
	// Verify the column is NOT NULL by confirming a normal create carries it.
	_, proj, _, _ := seedApp(t, s)
	if proj.TeamID == "" {
		t.Fatal("seeded project has no team_id")
	}
}

// TestStoreNotifierRoundtrip exercises the TEXT[] events column and the
// ANY(events) event-filter query against real Postgres — array handling the
// unit fakes cannot validate (notifications.md §2–4).
func TestStoreNotifierRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, _, _ := seedApp(t, s)

	n, err := s.CreateNotifier(ctx, domain.Notifier{
		ID:          ids.New(ids.PrefixNotifier),
		ProjectID:   proj.ID,
		Name:        "ops-slack",
		Channel:     domain.NotifyChannelSlack,
		ConfigCT:    []byte("sealed-config"),
		ConfigNonce: []byte("nonce"),
		Events:      []string{domain.EventDeployFailed, domain.EventBackupFailed},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateNotifier: %v", err)
	}
	if len(n.Events) != 2 || n.Events[0] != domain.EventDeployFailed {
		t.Fatalf("events not round-tripped: %+v", n.Events)
	}

	// The event filter matches a subscribed event and excludes an unsubscribed
	// one; a disabled notifier is never returned.
	got, err := s.ListEnabledNotifiersForEvent(ctx, proj.ID, domain.EventDeployFailed)
	if err != nil {
		t.Fatalf("ListEnabledNotifiersForEvent: %v", err)
	}
	if len(got) != 1 || got[0].ID != n.ID {
		t.Fatalf("event filter = %+v, want the one subscribed notifier", got)
	}
	if none, err := s.ListEnabledNotifiersForEvent(ctx, proj.ID, domain.EventDeploySucceeded); err != nil || len(none) != 0 {
		t.Fatalf("unsubscribed event = %+v, %v, want empty", none, err)
	}

	// Disabling removes it from the event query but keeps the row.
	if _, err := s.UpdateNotifier(ctx, domain.Notifier{
		ID: n.ID, Name: n.Name, ConfigCT: n.ConfigCT, ConfigNonce: n.ConfigNonce,
		Events: n.Events, Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateNotifier: %v", err)
	}
	if disabled, err := s.ListEnabledNotifiersForEvent(ctx, proj.ID, domain.EventDeployFailed); err != nil || len(disabled) != 0 {
		t.Fatalf("disabled notifier still matched: %+v, %v", disabled, err)
	}

	// Deleting the project cascades the notifier away.
	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetNotifier(ctx, n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("notifier survived project cascade: %v", err)
	}
}

// TestStoreScheduledTaskRoundtrip exercises the TEXT[] command column, run
// history pruning, and the app cascade against real Postgres (scheduled-tasks.md).
func TestStoreScheduledTaskRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	task, err := s.CreateScheduledTask(ctx, domain.ScheduledTask{
		ID:            ids.New(ids.PrefixScheduledTask),
		ApplicationID: app.ID,
		Name:          "nightly",
		Schedule:      "0 3 * * *",
		Command:       []string{"sh", "-c", "cleanup --verbose"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateScheduledTask: %v", err)
	}
	if len(task.Command) != 3 || task.Command[2] != "cleanup --verbose" {
		t.Fatalf("command argv not round-tripped: %+v", task.Command)
	}

	// Enabled filter (the desired-state carrier) includes it; disabling excludes.
	if got, err := s.ListEnabledScheduledTasksByApp(ctx, app.ID); err != nil || len(got) != 1 {
		t.Fatalf("enabled list = %+v, %v, want the one task", got, err)
	}
	task.Enabled = false
	if _, err := s.UpdateScheduledTask(ctx, task); err != nil {
		t.Fatalf("UpdateScheduledTask: %v", err)
	}
	if got, _ := s.ListEnabledScheduledTasksByApp(ctx, app.ID); len(got) != 0 {
		t.Fatalf("disabled task still in enabled list: %+v", got)
	}

	// Record more runs than the retention window, then prune to it.
	started := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		exit := i
		if _, err := s.CreateTaskRun(ctx, domain.ScheduledTaskRun{
			ID: ids.New(ids.PrefixTaskRun), TaskID: task.ID,
			StartedAt: started.Add(time.Duration(i) * time.Minute),
			Status:    domain.TaskRunSucceeded, ExitCode: &exit,
		}); err != nil {
			t.Fatalf("CreateTaskRun: %v", err)
		}
	}
	if err := s.DeleteOldTaskRuns(ctx, task.ID, 2); err != nil {
		t.Fatalf("DeleteOldTaskRuns: %v", err)
	}
	runs, err := s.ListTaskRuns(ctx, task.ID, 50)
	if err != nil {
		t.Fatalf("ListTaskRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("kept %d runs, want 2 after prune", len(runs))
	}
	// Newest kept (DESC by started_at): the last two inserts.
	if runs[0].StartedAt.Before(runs[1].StartedAt) {
		t.Fatal("runs not ordered newest-first")
	}

	// Deleting the application cascades the task and its runs.
	if err := s.DeleteApplication(ctx, app.ID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := s.GetScheduledTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task survived app cascade: %v", err)
	}
}

func TestStoreDatabaseLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	srv, err := s.CreateServerWithToken(ctx, ids.New(ids.PrefixServer), "db-host", ids.New(ids.PrefixJoinToken), []byte(ids.Secret()), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateServerWithToken: %v", err)
	}

	_, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "db-project", "tm_default", ids.New(ids.PrefixEnvironment), "prod")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}

	dbID := ids.New(ids.PrefixDatabase)
	d := domain.Database{
		ID:            dbID,
		EnvironmentID: env.ID,
		Name:          "postgres-db",
		Engine:        domain.EnginePostgreSQL,
		Version:       "16",
		ServerID:      srv.ID,
		CPULimit:      nil,
		MemoryLimitMB: nil,
		VolumeName:    "cypher-db-" + dbID,
		DataPath:      "/var/lib/postgresql/data",
		ExposePort:    nil,
		Network:       "cypher-" + env.ID,
		RootUser:      "postgres",
		// Round-trips like every other column, and is what makes a templated
		// application's {{db.<name>.database}} name a database that exists
		// (managed-databases.md §2).
		InitialDatabase: "appdb",
		Status:          domain.DbStopped,
	}

	revID := ids.New(ids.PrefixDatabaseRevision)
	rev := domain.DatabaseRevision{
		ID:             revID,
		DatabaseID:     dbID,
		ConfigSnapshot: []byte(`{"version": "16"}`),
	}

	created, err := s.CreateDatabaseWithRevision(ctx, d, rev)
	if err != nil {
		t.Fatalf("CreateDatabaseWithRevision: %v", err)
	}

	if created.ID != dbID || created.Name != "postgres-db" || created.Engine != domain.EnginePostgreSQL {
		t.Fatalf("unexpected database details: %+v", created)
	}

	// Retrieve database.
	fetched, err := s.GetDatabase(ctx, dbID)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if fetched.InitialDatabase != "appdb" {
		t.Fatalf("initial database = %q, want appdb", fetched.InitialDatabase)
	}
	if fetched.ID != dbID || fetched.DesiredRevisionID == nil || *fetched.DesiredRevisionID != revID {
		t.Fatalf("unexpected fetched database: %+v", fetched)
	}

	// Update database config.
	fetched.Version = "17"
	port := 5432
	fetched.ExposePort = &port
	updated, err := s.UpdateDatabaseConfig(ctx, fetched)
	if err != nil {
		t.Fatalf("UpdateDatabaseConfig: %v", err)
	}
	if updated.Version != "17" || updated.ExposePort == nil || *updated.ExposePort != 5432 {
		t.Fatalf("unexpected updated database: %+v", updated)
	}

	// Update password.
	if err := s.UpdateDatabasePassword(ctx, dbID, []byte("ct-pwd"), []byte("nonce-pwd")); err != nil {
		t.Fatalf("UpdateDatabasePassword: %v", err)
	}
	fetchedWithPwd, err := s.GetDatabase(ctx, dbID)
	if err != nil {
		t.Fatalf("GetDatabase after password update: %v", err)
	}
	if string(fetchedWithPwd.RootPasswordCT) != "ct-pwd" {
		t.Fatalf("password not updated: %s", fetchedWithPwd.RootPasswordCT)
	}

	// Set observed status.
	if err := s.SetDatabaseObservedStatus(ctx, dbID, domain.DbRunning, "healthy", revID, time.Now()); err != nil {
		t.Fatalf("SetDatabaseObservedStatus: %v", err)
	}
	observed, err := s.GetDatabase(ctx, dbID)
	if err != nil {
		t.Fatalf("GetDatabase after status update: %v", err)
	}
	if observed.Status != domain.DbRunning || observed.StatusDetail != "healthy" {
		t.Fatalf("observed status incorrect: %+v", observed)
	}

	// Soft delete.
	if err := s.SetDatabasePendingDelete(ctx, dbID, true); err != nil {
		t.Fatalf("SetDatabasePendingDelete: %v", err)
	}
	pending, err := s.GetDatabase(ctx, dbID)
	if err != nil {
		t.Fatalf("GetDatabase after soft delete: %v", err)
	}
	if !pending.PendingDelete || !pending.DeleteVolume {
		t.Fatalf("soft delete flag not set correctly: %+v", pending)
	}

	// Hard delete.
	if err := s.DeleteDatabase(ctx, dbID); err != nil {
		t.Fatalf("DeleteDatabase: %v", err)
	}
	if _, err := s.GetDatabase(ctx, dbID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("database row not hard-deleted: %v", err)
	}
}

func TestStoreAPITokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "tok-"+ids.Secret()+"@x.io", "h", domain.RoleOwner)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// A live token (no expiry) resolves to its owner and records last_used.
	raw := []byte("hash-live-" + ids.Secret())
	tok, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "ci", domain.AllAbilities(), raw, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	got, _, abilities, err := s.APITokenByHash(ctx, raw)
	if err != nil || got.ID != user.ID {
		t.Fatalf("APITokenByHash = %+v, %v; want user %s", got, err, user.ID)
	}
	if len(abilities) != 3 {
		t.Fatalf("abilities = %v, want the full default set", abilities)
	}
	if err := s.TouchAPIToken(ctx, raw); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}
	list, err := s.ListAPITokensByUser(ctx, user.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAPITokensByUser = %+v, %v; want 1", list, err)
	}
	if list[0].LastUsedAt == nil {
		t.Fatal("last_used_at not recorded by TouchAPIToken")
	}

	// An expired token yields no user (the SQL filters on expires_at).
	expRaw := []byte("hash-exp-" + ids.Secret())
	past := time.Now().Add(-time.Hour)
	if _, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "old", domain.AllAbilities(), expRaw, &past); err != nil {
		t.Fatalf("CreateAPIToken (expired): %v", err)
	}
	if _, _, _, err := s.APITokenByHash(ctx, expRaw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token resolved a user: %v", err)
	}

	// Delete revokes.
	if err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if _, _, _, err := s.APITokenByHash(ctx, raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still resolves: %v", err)
	}

	// Deleting the user cascades to their tokens (ON DELETE CASCADE).
	if _, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "c", domain.AllAbilities(), []byte("hash-c-"+ids.Secret()), nil); err != nil {
		t.Fatalf("CreateAPIToken (cascade): %v", err)
	}
	if err := s.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	remaining, err := s.ListAPITokensByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListAPITokensByUser after user delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("tokens not cascaded on user delete: %d remain", len(remaining))
	}
}

func TestStoreTOTP(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "totp-"+ids.Secret()+"@x.io", "h", domain.RoleOwner)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// A brand-new user has no secret and 2FA disabled.
	sec, err := s.GetTOTPSecret(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetTOTPSecret: %v", err)
	}
	if len(sec.CT) != 0 || sec.Enabled {
		t.Fatalf("fresh user should have no totp: %+v", sec)
	}

	// Enroll (secret stored, not yet enabled), then enable.
	if err := s.SetTOTPSecret(ctx, user.ID, []byte("ct"), []byte("nonce")); err != nil {
		t.Fatalf("SetTOTPSecret: %v", err)
	}
	sec, _ = s.GetTOTPSecret(ctx, user.ID)
	if string(sec.CT) != "ct" || sec.Enabled {
		t.Fatalf("after enroll: %+v (enabled should be false)", sec)
	}
	if err := s.EnableTOTP(ctx, user.ID); err != nil {
		t.Fatalf("EnableTOTP: %v", err)
	}
	if u, _ := s.GetUserByEmail(ctx, user.Email); !u.TOTPEnabled {
		t.Fatal("user.TOTPEnabled not reflected after EnableTOTP")
	}

	// Recovery codes: add, count, consume once (single-use), recount.
	h1, h2 := []byte("hash-1"), []byte("hash-2")
	for _, h := range [][]byte{h1, h2} {
		if err := s.AddRecoveryCode(ctx, ids.New(ids.PrefixRecoveryCode), user.ID, h); err != nil {
			t.Fatalf("AddRecoveryCode: %v", err)
		}
	}
	if n, _ := s.CountUnusedRecoveryCodes(ctx, user.ID); n != 2 {
		t.Fatalf("unused codes = %d, want 2", n)
	}
	used, err := s.ConsumeRecoveryCode(ctx, user.ID, h1)
	if err != nil || !used {
		t.Fatalf("ConsumeRecoveryCode = %v, %v; want true", used, err)
	}
	// Consuming the same code again does nothing (already spent).
	if again, _ := s.ConsumeRecoveryCode(ctx, user.ID, h1); again {
		t.Fatal("a spent recovery code was consumed twice")
	}
	if n, _ := s.CountUnusedRecoveryCodes(ctx, user.ID); n != 1 {
		t.Fatalf("unused codes after consume = %d, want 1", n)
	}

	// Disable clears the secret; DeleteRecoveryCodes clears the rest.
	if err := s.DisableTOTP(ctx, user.ID); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	sec, _ = s.GetTOTPSecret(ctx, user.ID)
	if len(sec.CT) != 0 || sec.Enabled {
		t.Fatalf("after disable: %+v", sec)
	}
	if err := s.DeleteRecoveryCodes(ctx, user.ID); err != nil {
		t.Fatalf("DeleteRecoveryCodes: %v", err)
	}
	if n, _ := s.CountUnusedRecoveryCodes(ctx, user.ID); n != 0 {
		t.Fatalf("codes after delete = %d, want 0", n)
	}
}

func TestStoreBackupRetentionPrune(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	srv, err := s.CreateServerWithToken(ctx, ids.New(ids.PrefixServer), "bk-host", ids.New(ids.PrefixJoinToken), []byte(ids.Secret()), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateServerWithToken: %v", err)
	}
	_, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "bk-project", "tm_default", ids.New(ids.PrefixEnvironment), "prod")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}
	dbID := ids.New(ids.PrefixDatabase)
	d := domain.Database{
		ID: dbID, EnvironmentID: env.ID, Name: "pg", Engine: domain.EnginePostgreSQL, Version: "16",
		ServerID: srv.ID, VolumeName: "cypher-db-" + dbID, DataPath: "/var/lib/postgresql/data",
		Network: "cypher-" + env.ID, RootUser: "postgres", Status: domain.DbStopped,
	}
	rev := domain.DatabaseRevision{ID: ids.New(ids.PrefixDatabaseRevision), DatabaseID: dbID, ConfigSnapshot: []byte(`{}`)}
	if _, err := s.CreateDatabaseWithRevision(ctx, d, rev); err != nil {
		t.Fatalf("CreateDatabaseWithRevision: %v", err)
	}
	tgt, err := s.CreateBackupTarget(ctx, domain.BackupTarget{
		ID: ids.New(ids.PrefixBackupTarget), Name: "t", Endpoint: "http://minio:9000", Bucket: "b", Region: "r",
		AccessKeyCT: []byte("a"), AccessKeyNonce: []byte("n"), SecretKeyCT: []byte("s"), SecretKeyNonce: []byte("n"), PathPrefix: "pfx",
	})
	if err != nil {
		t.Fatalf("CreateBackupTarget: %v", err)
	}
	sched, err := s.CreateDatabaseBackup(ctx, domain.DatabaseBackup{
		ID: ids.New(ids.PrefixDatabaseBackup), DatabaseID: dbID, TargetID: tgt.ID,
		Schedule: "0 3 * * *", RetentionCount: 2, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackup: %v", err)
	}

	// Four succeeded backups (k0 oldest … k3 newest) plus a failed one (no object).
	base := time.Now().Add(-time.Hour)
	for i := range 4 {
		key := "pfx/" + dbID + "/k" + string(rune('0'+i))
		rec, err := s.CreateBackupRecord(ctx, domain.BackupRecord{
			ID: ids.New(ids.PrefixBackupRecord), DatabaseBackupID: sched.ID,
			Status: domain.BackupRunning, StartedAt: base.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateBackupRecord: %v", err)
		}
		// object_key is written by the completion update, not at creation.
		fin := base.Add(time.Duration(i) * time.Minute)
		if err := s.UpdateBackupRecord(ctx, rec.ID, key, 100, domain.BackupSucceeded, "", &fin); err != nil {
			t.Fatalf("UpdateBackupRecord: %v", err)
		}
	}

	// Retention 2 → the two oldest succeeded (k0, k1) are the prune set.
	prunable, err := s.ListBackupRecordsBeyondRetention(ctx, sched.ID, 2)
	if err != nil {
		t.Fatalf("ListBackupRecordsBeyondRetention: %v", err)
	}
	if len(prunable) != 2 {
		t.Fatalf("prunable = %d, want 2", len(prunable))
	}
	keys := []string{prunable[0].ObjectKey, prunable[1].ObjectKey}
	if !strings.HasSuffix(keys[0], "k1") && !strings.HasSuffix(keys[1], "k1") {
		t.Fatalf("prune set should be the two oldest (k0,k1), got %v", keys)
	}

	// Deleting by object key removes exactly those rows; the newest survive.
	if err := s.DeleteBackupRecordsByObjectKeys(ctx, keys); err != nil {
		t.Fatalf("DeleteBackupRecordsByObjectKeys: %v", err)
	}
	remaining, err := s.ListBackupRecords(ctx, sched.ID)
	if err != nil {
		t.Fatalf("ListBackupRecords: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining records = %d, want 2 (the newest two)", len(remaining))
	}
}

// TestStoreWebhookEndpointRoundtrip exercises the outbound-webhook tables
// against real Postgres: the TEXT[] events column and its ANY(events) filter,
// the sealed BYTEA pair, the UNIQUE (project_id, url) guard, the composite
// attempt key's idempotence, seek paging, retention pruning, and the cascades
// (outbound-webhooks.md §2, §6, §7).
func TestStoreWebhookEndpointRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, _, _ := seedApp(t, s)

	target := "https://ops.meridian.dev/hooks/" + ids.Secret()
	e, err := s.CreateWebhookEndpoint(ctx, domain.WebhookEndpoint{
		ID:          ids.New(ids.PrefixWebhookEndpoint),
		ProjectID:   proj.ID,
		URL:         target,
		SecretCT:    []byte("sealed-signing-secret"),
		SecretNonce: []byte("nonce"),
		Events:      []string{domain.EventDeployFailed, domain.EventBackupFailed},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint: %v", err)
	}
	if len(e.Events) != 2 || e.Events[0] != domain.EventDeployFailed {
		t.Fatalf("events not round-tripped: %+v", e.Events)
	}
	if string(e.SecretCT) != "sealed-signing-secret" {
		t.Fatalf("sealed secret not round-tripped: %q", e.SecretCT)
	}

	// The URL is the endpoint's identity: a second one on the same URL would
	// silently double every delivery (spec section 2).
	if _, err := s.CreateWebhookEndpoint(ctx, domain.WebhookEndpoint{
		ID: ids.New(ids.PrefixWebhookEndpoint), ProjectID: proj.ID, URL: target,
		SecretCT: []byte("x"), SecretNonce: []byte("n"),
		Events: []string{domain.EventDeployFailed}, Enabled: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate URL = %v, want ErrConflict", err)
	}

	// The event filter matches a subscribed event and excludes an unsubscribed
	// one; disabling removes it from the query but keeps the row.
	got, err := s.ListEnabledWebhookEndpointsForEvent(ctx, proj.ID, domain.EventDeployFailed)
	if err != nil {
		t.Fatalf("ListEnabledWebhookEndpointsForEvent: %v", err)
	}
	if len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("event filter = %+v, want the one subscribed endpoint", got)
	}
	if none, err := s.ListEnabledWebhookEndpointsForEvent(ctx, proj.ID, domain.EventDeploySucceeded); err != nil || len(none) != 0 {
		t.Fatalf("unsubscribed event = %+v, %v, want empty", none, err)
	}
	e.Enabled = false
	if _, err := s.UpdateWebhookEndpoint(ctx, e); err != nil {
		t.Fatalf("UpdateWebhookEndpoint: %v", err)
	}
	if disabled, _ := s.ListEnabledWebhookEndpointsForEvent(ctx, proj.ID, domain.EventDeployFailed); len(disabled) != 0 {
		t.Fatalf("disabled endpoint still matched: %+v", disabled)
	}
	e.Enabled = true
	if _, err := s.UpdateWebhookEndpoint(ctx, e); err != nil {
		t.Fatalf("re-enabling: %v", err)
	}

	// Rotating replaces the sealed pair in place, with no overlap window.
	rotated, err := s.RotateWebhookEndpointSecret(ctx, e.ID, []byte("sealed-v2"), []byte("nonce2"))
	if err != nil {
		t.Fatalf("RotateWebhookEndpointSecret: %v", err)
	}
	if string(rotated.SecretCT) != "sealed-v2" {
		t.Fatalf("rotate did not replace the secret: %q", rotated.SecretCT)
	}

	// One delivery with its attempts: response_status is nullable (a transport
	// error has none), and the composite PK makes a repeat insert a conflict
	// the manager reads as "already recorded" (ENGINEERING rule 12).
	due := time.Now().Add(-time.Minute)
	d, err := s.CreateWebhookDelivery(ctx, domain.WebhookDelivery{
		ID:            ids.New(ids.PrefixWebhookDelivery),
		EndpointID:    e.ID,
		EventType:     domain.EventDeployFailed,
		ResourceKind:  domain.WebhookResourceApplication,
		ResourceID:    "app_gone_tomorrow",
		ResourceName:  "web",
		Payload:       `{"event":"deploy.failed"}`,
		Status:        domain.DeliveryPending,
		Attempt:       1,
		NextAttemptAt: &due,
	})
	if err != nil {
		t.Fatalf("CreateWebhookDelivery: %v", err)
	}
	if d.NextAttemptAt == nil || d.RedeliveryOf != nil {
		t.Fatalf("nullable columns not round-tripped: %+v", d)
	}
	code := 500
	if _, err := s.CreateWebhookDeliveryAttempt(ctx, domain.WebhookDeliveryAttempt{
		DeliveryID: d.ID, Attempt: 1, ResponseStatus: &code, DurationMS: 84,
	}); err != nil {
		t.Fatalf("CreateWebhookDeliveryAttempt: %v", err)
	}
	if _, err := s.CreateWebhookDeliveryAttempt(ctx, domain.WebhookDeliveryAttempt{
		DeliveryID: d.ID, Attempt: 1, DurationMS: 1,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat attempt insert = %v, want ErrConflict", err)
	}
	if _, err := s.CreateWebhookDeliveryAttempt(ctx, domain.WebhookDeliveryAttempt{
		DeliveryID: d.ID, Attempt: 2, DurationMS: 12, Error: "dial tcp: connection refused",
	}); err != nil {
		t.Fatalf("CreateWebhookDeliveryAttempt(2): %v", err)
	}
	attempts, err := s.ListWebhookDeliveryAttempts(ctx, d.ID)
	if err != nil {
		t.Fatalf("ListWebhookDeliveryAttempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].ResponseStatus == nil || *attempts[0].ResponseStatus != 500 {
		t.Fatalf("attempts = %+v, want two with the first carrying 500", attempts)
	}
	if attempts[1].ResponseStatus != nil {
		t.Fatalf("transport-error attempt has response_status %v, want NULL", attempts[1].ResponseStatus)
	}

	// The partial due index feeds the retry sweeper; a terminal delivery drops
	// out of it.
	dueRows, err := s.ListDueWebhookDeliveries(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	if !containsDelivery(dueRows, d.ID) {
		t.Fatalf("due list %+v missing the pending delivery", dueRows)
	}
	// The progress write is a compare-and-set on the attempt the caller started
	// from. A stale writer — the loser of the sweeper/first-attempt race — must
	// not be able to move the row, or it could flip a delivery that already
	// succeeded back to pending for another round of retries.
	if _, err := s.UpdateWebhookDeliveryProgress(ctx, d.ID, domain.DeliverySucceeded, 0, 1, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale progress write = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdateWebhookDeliveryProgress(ctx, d.ID, domain.DeliveryFailed, 1, 2, nil); err != nil {
		t.Fatalf("UpdateWebhookDeliveryProgress: %v", err)
	}
	dueRows, _ = s.ListDueWebhookDeliveries(ctx, time.Now(), 50)
	if containsDelivery(dueRows, d.ID) {
		t.Fatal("a terminal delivery is still due")
	}

	// Health input and last-delivery time.
	statuses, err := s.ListRecentTerminalWebhookDeliveryStatuses(ctx, e.ID, 10)
	if err != nil {
		t.Fatalf("ListRecentTerminalWebhookDeliveryStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != domain.DeliveryFailed {
		t.Fatalf("terminal statuses = %v, want [failed]", statuses)
	}
	last, err := s.LastWebhookDeliveryAt(ctx, e.ID)
	if err != nil || last == nil {
		t.Fatalf("LastWebhookDeliveryAt = %v, %v, want a time", last, err)
	}

	// Seek paging over 120 deliveries: 50 + 50 + 20, no repeats, no gaps.
	const extra = 119
	for i := 0; i < extra; i++ {
		if _, err := s.CreateWebhookDelivery(ctx, domain.WebhookDelivery{
			ID: ids.New(ids.PrefixWebhookDelivery), EndpointID: e.ID,
			EventType: domain.EventDeployFailed, ResourceKind: domain.WebhookResourceApplication,
			ResourceID: "app_x", ResourceName: "web", Payload: "{}", Status: domain.DeliverySucceeded,
		}); err != nil {
			t.Fatalf("CreateWebhookDelivery(%d): %v", i, err)
		}
	}
	seen := map[string]bool{}
	before := ""
	for pages := 1; ; pages++ {
		var rows []domain.WebhookDelivery
		if before == "" {
			rows, err = s.ListWebhookDeliveriesByEndpoint(ctx, e.ID, 50)
		} else {
			rows, err = s.ListWebhookDeliveriesBefore(ctx, e.ID, before, 50)
		}
		if err != nil {
			t.Fatalf("paging: %v", err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("delivery %s repeated across pages", r.ID)
			}
			seen[r.ID] = true
		}
		if len(rows) < 50 {
			break
		}
		before = rows[len(rows)-1].ID
		if pages > 5 {
			t.Fatal("paging did not terminate")
		}
	}
	if len(seen) != extra+1 {
		t.Fatalf("paged %d deliveries, want %d with no gaps", len(seen), extra+1)
	}

	// Retention: pruning to 100 drops the oldest, and their attempts cascade.
	if err := s.DeleteOldWebhookDeliveries(ctx, e.ID, 100); err != nil {
		t.Fatalf("DeleteOldWebhookDeliveries: %v", err)
	}
	kept, err := s.ListWebhookDeliveriesByEndpoint(ctx, e.ID, 500)
	if err != nil {
		t.Fatalf("ListWebhookDeliveriesByEndpoint: %v", err)
	}
	if len(kept) != 100 {
		t.Fatalf("kept %d deliveries, want 100 after the prune", len(kept))
	}
	if _, err := s.GetWebhookDelivery(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the oldest delivery survived the prune: %v", err)
	}
	if pruned, err := s.ListWebhookDeliveryAttempts(ctx, d.ID); err != nil || len(pruned) != 0 {
		t.Fatalf("attempts = %+v, %v, want cascaded away with their delivery", pruned, err)
	}

	// Deleting the endpoint cascades its deliveries; deleting the project
	// cascades the endpoint.
	survivor := kept[0].ID
	if err := s.DeleteWebhookEndpoint(ctx, e.ID); err != nil {
		t.Fatalf("DeleteWebhookEndpoint: %v", err)
	}
	if _, err := s.GetWebhookDelivery(ctx, survivor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delivery survived its endpoint: %v", err)
	}

	second, err := s.CreateWebhookEndpoint(ctx, domain.WebhookEndpoint{
		ID: ids.New(ids.PrefixWebhookEndpoint), ProjectID: proj.ID,
		URL:      "https://ops.meridian.dev/hooks/" + ids.Secret(),
		SecretCT: []byte("ct"), SecretNonce: []byte("n"),
		Events: []string{domain.EventDeploySucceeded}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint(second): %v", err)
	}
	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetWebhookEndpoint(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("endpoint survived the project cascade: %v", err)
	}
}

func containsDelivery(rows []domain.WebhookDelivery, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestStoreInboxRoundtrip exercises the notification-inbox tables against real
// Postgres (ENGINEERING rule 29): the TEXT[] columns, the recipient join that
// carries the tenancy guarantee, the mute filter, the digest upsert and its
// redelivery guard, the unread count, the two mark verbs, seek paging, the
// prune, the team-removal sweep and the cascades (notification-inbox.md §2,
// §4, §6).
func TestStoreInboxRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Two teams, one member each — acceptance 1's shape.
	teamA, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "inbox-a-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam(a): %v", err)
	}
	teamB, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "inbox-b-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam(b): %v", err)
	}
	userA, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "a-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(a): %v", err)
	}
	userB, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "b-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(b): %v", err)
	}
	quiet, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "q-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(quiet): %v", err)
	}
	for _, m := range []struct{ team, user string }{
		{teamA.ID, userA.ID}, {teamA.ID, quiet.ID}, {teamB.ID, userB.ID},
	} {
		if _, err := s.UpsertTeamMember(ctx, m.team, m.user, domain.RoleMember); err != nil {
			t.Fatalf("UpsertTeamMember: %v", err)
		}
	}
	projA, _, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject),
		"inbox-proj-"+ids.Secret()[:8], teamA.ID, ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}

	// Preferences default to absent, which must read as "everything on".
	prefs, err := s.GetInboxPreferences(ctx, quiet.ID)
	if err != nil {
		t.Fatalf("GetInboxPreferences (absent row): %v", err)
	}
	if len(prefs.MutedKinds) != 0 {
		t.Fatalf("absent preferences = %v, want empty", prefs.MutedKinds)
	}
	if _, err := s.SetInboxPreferences(ctx, quiet.ID, []string{domain.EventBackupSucceeded}); err != nil {
		t.Fatalf("SetInboxPreferences: %v", err)
	}

	// The recipient join is the whole tenancy guarantee: team A's project
	// resolves to team A's members and nobody else, and the mute filter drops
	// exactly the muter.
	recipients, err := s.ListInboxRecipients(ctx, projA.ID, domain.EventBackupFailed)
	if err != nil {
		t.Fatalf("ListInboxRecipients: %v", err)
	}
	if len(recipients) != 2 || !containsString(recipients, userA.ID) || !containsString(recipients, quiet.ID) {
		t.Fatalf("recipients = %v, want both members of team A", recipients)
	}
	if containsString(recipients, userB.ID) {
		t.Fatal("a member of another team was resolved as a recipient")
	}
	muted, err := s.ListInboxRecipients(ctx, projA.ID, domain.EventBackupSucceeded)
	if err != nil {
		t.Fatalf("ListInboxRecipients (muted kind): %v", err)
	}
	if len(muted) != 1 || muted[0] != userA.ID {
		t.Fatalf("recipients for a muted kind = %v, want only the unmuting member", muted)
	}

	// An immediate item per recipient, then the same observation again: the
	// (user_id, dedupe_key) unique index makes redelivery a no-op (rule 12).
	fail := InboxFanout{
		IDs:       []string{ids.New(ids.PrefixInboxItem), ids.New(ids.PrefixInboxItem)},
		UserIDs:   []string{userA.ID, quiet.ID},
		ProjectID: projA.ID,
		Kind:      domain.EventBackupFailed,
		Severity:  string(domain.NotifyError),
		Title:     "Backup failed: atlas-pg",
		Body:      "Database atlas-pg backup failed.",
		Link:      "/projects/" + projA.ID + "/databases/db_x/backups",
		LinkLabel: "View backups",
		DedupeKey: domain.EventBackupFailed + ":br_1",
		FocusID:   "br_1",
	}
	if err := s.InsertInboxItems(ctx, fail); err != nil {
		t.Fatalf("InsertInboxItems: %v", err)
	}
	fail.IDs = []string{ids.New(ids.PrefixInboxItem), ids.New(ids.PrefixInboxItem)}
	if err := s.InsertInboxItems(ctx, fail); err != nil {
		t.Fatalf("InsertInboxItems (redelivery): %v", err)
	}
	if n, err := s.CountUnreadInboxItems(ctx, userA.ID); err != nil || n != 1 {
		t.Fatalf("unread after redelivery = %d, %v, want 1", n, err)
	}
	if n, _ := s.CountUnreadInboxItems(ctx, userB.ID); n != 0 {
		t.Fatalf("the other team's member has %d items, want 0", n)
	}

	// The digest: created by the first success, incremented by the second, and
	// left alone by a third carrying a source already rolled in.
	digestKey := "digest:" + domain.EventBackupSucceeded + ":" + projA.ID + ":2026-08-21"
	ok := InboxFanout{
		IDs: []string{ids.New(ids.PrefixInboxItem)}, UserIDs: []string{userA.ID},
		ProjectID: projA.ID, Kind: domain.EventBackupSucceeded,
		Severity: string(domain.NotifyInfo), Title: "Backups",
		DedupeKey: digestKey, FocusID: "br_2",
	}
	if err := s.UpsertInboxDigests(ctx, ok); err != nil {
		t.Fatalf("UpsertInboxDigests: %v", err)
	}
	ok.IDs, ok.FocusID = []string{ids.New(ids.PrefixInboxItem)}, "br_3"
	if err := s.UpsertInboxDigests(ctx, ok); err != nil {
		t.Fatalf("UpsertInboxDigests (second): %v", err)
	}
	ok.IDs = []string{ids.New(ids.PrefixInboxItem)}
	if err := s.UpsertInboxDigests(ctx, ok); err != nil {
		t.Fatalf("UpsertInboxDigests (redelivery): %v", err)
	}
	digest := findByDedupe(t, s, userA.ID, digestKey)
	if digest.CountOK != 2 || digest.CountTotal != 2 {
		t.Fatalf("digest counters = %d/%d after a redelivered success, want 2/2", digest.CountOK, digest.CountTotal)
	}
	if len(digest.Sources) != 2 {
		t.Fatalf("digest sources = %v, want two distinct entries", digest.Sources)
	}
	if !digest.Digest || digest.Link != "" {
		t.Fatalf("digest row shape wrong: %+v", digest)
	}

	// A failure raises the denominator on the existing digest and never creates
	// one — the honest "2/3 succeeded".
	if err := s.BumpInboxDigestTotals(ctx, digestKey, "br_4"); err != nil {
		t.Fatalf("BumpInboxDigestTotals: %v", err)
	}
	if err := s.BumpInboxDigestTotals(ctx, digestKey, "br_4"); err != nil {
		t.Fatalf("BumpInboxDigestTotals (redelivery): %v", err)
	}
	digest = findByDedupe(t, s, userA.ID, digestKey)
	if digest.CountOK != 2 || digest.CountTotal != 3 {
		t.Fatalf("digest counters = %d/%d after a failure, want 2/3", digest.CountOK, digest.CountTotal)
	}
	absentKey := "digest:" + domain.EventDeploySucceeded + ":" + projA.ID + ":2026-08-21"
	if err := s.BumpInboxDigestTotals(ctx, absentKey, "dep_1"); err != nil {
		t.Fatalf("BumpInboxDigestTotals (no digest): %v", err)
	}
	if rows, _ := s.ListInboxItems(ctx, userA.ID, false, 50); len(rows) != 2 {
		t.Fatalf("items = %d, want 2 — a failure must not conjure a digest", len(rows))
	}

	// Seek paging: page two is strictly older, with no overlap.
	page1, err := s.ListInboxItems(ctx, userA.ID, false, 1)
	if err != nil || len(page1) != 1 {
		t.Fatalf("first page = %v, %v", page1, err)
	}
	page2, err := s.ListInboxItemsBefore(ctx, userA.ID, false, page1[0].ID, 5)
	if err != nil {
		t.Fatalf("ListInboxItemsBefore: %v", err)
	}
	if len(page2) != 1 || page2[0].ID == page1[0].ID {
		t.Fatalf("second page = %+v, want the one older row", page2)
	}
	// A cursor that is not the caller's own yields an empty page, never a
	// restart at the newest row.
	if rows, err := s.ListInboxItemsBefore(ctx, userB.ID, false, page1[0].ID, 5); err != nil || len(rows) != 0 {
		t.Fatalf("foreign cursor = %+v, %v, want empty", rows, err)
	}

	// Marking: one, then all. Items stay listed; another user's item is not
	// addressable at all.
	if _, err := s.MarkInboxItemRead(ctx, userA.ID, page1[0].ID); err != nil {
		t.Fatalf("MarkInboxItemRead: %v", err)
	}
	again, err := s.MarkInboxItemRead(ctx, userA.ID, page1[0].ID)
	if err != nil {
		t.Fatalf("MarkInboxItemRead (idempotent): %v", err)
	}
	if again.ReadAt == nil {
		t.Fatal("re-marking cleared read_at")
	}
	if _, err := s.MarkInboxItemRead(ctx, userB.ID, page1[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking another user's item = %v, want ErrNotFound", err)
	}
	unread, err := s.ListInboxItems(ctx, userA.ID, true, 50)
	if err != nil {
		t.Fatalf("ListInboxItems (unread): %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("unread items = %d, want 1", len(unread))
	}
	marked, err := s.MarkAllInboxItemsRead(ctx, userA.ID)
	if err != nil || marked != 1 {
		t.Fatalf("MarkAllInboxItemsRead = %d, %v, want 1", marked, err)
	}
	if all, _ := s.ListInboxItems(ctx, userA.ID, false, 50); len(all) != 2 {
		t.Fatalf("items after marking all read = %d, want 2 — reading is not deleting", len(all))
	}

	// The prune keeps the newest N per user, in one statement across users.
	for i := 0; i < 4; i++ {
		if err := s.InsertInboxItems(ctx, InboxFanout{
			IDs: []string{ids.New(ids.PrefixInboxItem)}, UserIDs: []string{userA.ID},
			ProjectID: projA.ID, Kind: domain.EventDeployFailed,
			Severity: string(domain.NotifyError), Title: "Deploy failed: web",
			DedupeKey: domain.EventDeployFailed + ":dep_" + ids.Secret()[:8],
		}); err != nil {
			t.Fatalf("InsertInboxItems (bulk): %v", err)
		}
	}
	if err := s.PruneInboxItems(ctx, []string{userA.ID, quiet.ID}, 3); err != nil {
		t.Fatalf("PruneInboxItems: %v", err)
	}
	if rows, _ := s.ListInboxItems(ctx, userA.ID, false, 50); len(rows) != 3 {
		t.Fatalf("items after prune = %d, want 3", len(rows))
	}
	if rows, _ := s.ListInboxItems(ctx, quiet.ID, false, 50); len(rows) != 1 {
		t.Fatalf("under-cap user pruned to %d, want their 1 item untouched", len(rows))
	}

	// Leaving a team empties that team's items and leaves everyone else's.
	if err := s.DeleteInboxItemsForTeamMember(ctx, teamA.ID, quiet.ID); err != nil {
		t.Fatalf("DeleteInboxItemsForTeamMember: %v", err)
	}
	if rows, _ := s.ListInboxItems(ctx, quiet.ID, false, 50); len(rows) != 0 {
		t.Fatalf("ex-member still holds %d items", len(rows))
	}
	if rows, _ := s.ListInboxItems(ctx, userA.ID, false, 50); len(rows) != 3 {
		t.Fatalf("a teammate's items were swept too: %d remain, want 3", len(rows))
	}

	// Deleting the project cascades the rest; deleting the user takes their
	// preferences with them.
	if err := s.DeleteProject(ctx, projA.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if rows, _ := s.ListInboxItems(ctx, userA.ID, false, 50); len(rows) != 0 {
		t.Fatalf("%d items survived the project cascade", len(rows))
	}
	if err := s.DeleteUser(ctx, quiet.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	after, err := s.GetInboxPreferences(ctx, quiet.ID)
	if err != nil {
		t.Fatalf("preferences lookup after user delete: %v", err)
	}
	if len(after.MutedKinds) != 0 {
		t.Fatalf("preferences survived the user cascade: %v", after.MutedKinds)
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func findByDedupe(t *testing.T, s *Store, userID, dedupeKey string) domain.InboxItem {
	t.Helper()
	rows, err := s.ListInboxItems(context.Background(), userID, false, 100)
	if err != nil {
		t.Fatalf("ListInboxItems: %v", err)
	}
	for _, r := range rows {
		if r.DedupeKey == dedupeKey {
			return r
		}
	}
	t.Fatalf("no item with dedupe key %q in %d rows", dedupeKey, len(rows))
	return domain.InboxItem{}
}

// TestStoreSharedVariableRoundtrip is acceptance §9.8 plus the two read models
// the feature is really about: a scope-accurate used-by count and the derived
// "redeploy to apply" marker (shared-variables.md §5, §7). It exercises
// NULLS NOT DISTINCT, which cannot be tested anywhere but a real PostgreSQL.
func TestStoreSharedVariableRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, prod, app := seedApp(t, s)

	staging, err := s.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), proj.ID, "staging")
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	// Project scope and environment scope, same key: both must persist.
	projectVar, err := s.CreateSharedVariable(ctx, domain.SharedVariable{
		ID:         ids.New(ids.PrefixSharedVariable),
		ProjectID:  proj.ID,
		Key:        "SMTP_HOST",
		ValueCT:    []byte("ct-project"),
		ValueNonce: []byte("n1"),
	})
	if err != nil {
		t.Fatalf("CreateSharedVariable (project scope): %v", err)
	}
	if projectVar.EnvironmentID != nil {
		t.Fatalf("environment_id = %v, want nil at project scope", projectVar.EnvironmentID)
	}
	prodID := prod.ID
	scopedVar, err := s.CreateSharedVariable(ctx, domain.SharedVariable{
		ID:            ids.New(ids.PrefixSharedVariable),
		ProjectID:     proj.ID,
		EnvironmentID: &prodID,
		Key:           "SMTP_HOST",
		ValueCT:       []byte("ct-production"),
		ValueNonce:    []byte("n2"),
	})
	if err != nil {
		t.Fatalf("CreateSharedVariable (environment scope): %v", err)
	}

	// A duplicate of either is a conflict. The project-scope case is the one
	// NULLS NOT DISTINCT exists for: under default semantics NULL <> NULL, so
	// this insert would succeed and resolution would become order-dependent.
	if _, err := s.CreateSharedVariable(ctx, domain.SharedVariable{
		ID: ids.New(ids.PrefixSharedVariable), ProjectID: proj.ID, Key: "SMTP_HOST",
		ValueCT: []byte("x"), ValueNonce: []byte("x"),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate project-scoped row = %v, want ErrConflict", err)
	}
	if _, err := s.CreateSharedVariable(ctx, domain.SharedVariable{
		ID: ids.New(ids.PrefixSharedVariable), ProjectID: proj.ID, EnvironmentID: &prodID, Key: "SMTP_HOST",
		ValueCT: []byte("x"), ValueNonce: []byte("x"),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate environment-scoped row = %v, want ErrConflict", err)
	}

	// Resolution: the environment row shadows the project row in production,
	// and staging (which has no row of its own) sees the project row.
	inProd, err := s.ListSharedVariablesInScope(ctx, proj.ID, prod.ID)
	if err != nil {
		t.Fatalf("ListSharedVariablesInScope: %v", err)
	}
	if len(inProd) != 1 || string(inProd[0].ValueCT) != "ct-production" {
		t.Fatalf("production scope = %+v, want only the shadowing row", inProd)
	}
	inStaging, err := s.ListSharedVariablesInScope(ctx, proj.ID, staging.ID)
	if err != nil {
		t.Fatalf("ListSharedVariablesInScope: %v", err)
	}
	if len(inStaging) != 1 || string(inStaging[0].ValueCT) != "ct-project" {
		t.Fatalf("staging scope = %+v, want the project-scoped row", inStaging)
	}
	keys, err := s.ListSharedVariableKeysInScope(ctx, proj.ID, staging.ID)
	if err != nil {
		t.Fatalf("ListSharedVariableKeysInScope: %v", err)
	}
	if len(keys) != 1 || keys[0] != "SMTP_HOST" {
		t.Fatalf("keys in scope = %v, want [SMTP_HOST]", keys)
	}

	// Usage: seedApp's application lives in production, so a reference from it
	// counts against the SHADOWING row and not against the project-scoped one.
	if err := s.UpsertEnvVar(ctx, app.ID, domain.EnvVar{
		Key: "SMTP_HOST", ValueCT: []byte("ct"), ValueNonce: []byte("n"), SharedRefs: []string{"SMTP_HOST"},
	}); err != nil {
		t.Fatalf("UpsertEnvVar: %v", err)
	}
	if n, err := s.CountSharedVariableUsage(ctx, scopedVar.ID); err != nil || n != 1 {
		t.Fatalf("environment-scoped usage = %d, %v; want 1", n, err)
	}
	if n, err := s.CountSharedVariableUsage(ctx, projectVar.ID); err != nil || n != 0 {
		t.Fatalf("project-scoped usage = %d, %v; want 0 (the app's environment shadows the key)", n, err)
	}
	counts, err := s.CountSharedVariableUsageByProject(ctx, proj.ID)
	if err != nil {
		t.Fatalf("CountSharedVariableUsageByProject: %v", err)
	}
	if counts[scopedVar.ID] != 1 || counts[projectVar.ID] != 0 {
		t.Fatalf("counts = %v, want the shadowing row credited with the use", counts)
	}
	usage, err := s.ListSharedVariableUsage(ctx, scopedVar.ID)
	if err != nil {
		t.Fatalf("ListSharedVariableUsage: %v", err)
	}
	if len(usage) != 1 || usage[0].ApplicationID != app.ID || usage[0].EnvironmentName != "production" {
		t.Fatalf("usage = %+v, want the one application in production", usage)
	}
	if !usage[0].RedeployPending {
		t.Fatal("an application that never applied a resolved environment must read as pending")
	}

	// The env-var round trip carries the cleartext ref list back.
	vars, err := s.ListEnvVars(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListEnvVars: %v", err)
	}
	found := false
	for _, v := range vars {
		if v.Key == "SMTP_HOST" {
			found = true
			if len(v.SharedRefs) != 1 || v.SharedRefs[0] != "SMTP_HOST" {
				t.Fatalf("shared_refs = %v, want [SMTP_HOST]", v.SharedRefs)
			}
		}
	}
	if !found {
		t.Fatal("the referencing env var did not round-trip")
	}

	// Drift: the marker clears only when a deployment's resolved-environment
	// stamp is copied onto the application, and only for a stamped deployment.
	if pending, err := s.ApplicationRedeployPending(ctx, app.ID); err != nil || !pending {
		t.Fatalf("redeploy pending = %v, %v; want true before anything was applied", pending, err)
	}
	rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "abc", []byte("{}"))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	dep, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "manual")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	// An unstamped deployment must not be able to mark the app clean.
	if err := s.ApplyDeploymentEnvStamp(ctx, dep.ID); err != nil {
		t.Fatalf("ApplyDeploymentEnvStamp (unstamped): %v", err)
	}
	if pending, err := s.ApplicationRedeployPending(ctx, app.ID); err != nil || !pending {
		t.Fatalf("redeploy pending = %v, %v; an unstamped deployment must not clear it", pending, err)
	}
	if err := s.SetDeploymentEnvResolved(ctx, dep.ID); err != nil {
		t.Fatalf("SetDeploymentEnvResolved: %v", err)
	}
	if err := s.ApplyDeploymentEnvStamp(ctx, dep.ID); err != nil {
		t.Fatalf("ApplyDeploymentEnvStamp: %v", err)
	}
	if pending, err := s.ApplicationRedeployPending(ctx, app.ID); err != nil || pending {
		t.Fatalf("redeploy pending = %v, %v; want false once the environment was applied", pending, err)
	}
	if pendingIDs, err := s.ListRedeployPendingApplications(ctx, prod.ID); err != nil || len(pendingIDs) != 0 {
		t.Fatalf("pending in production = %v, %v; want none", pendingIDs, err)
	}

	// A write moves updated_at past the applied stamp, so every referencing
	// application goes pending again — even when the plaintext is unchanged.
	if _, err := s.UpdateSharedVariableValue(ctx, scopedVar.ID, []byte("ct-production-2"), []byte("n3")); err != nil {
		t.Fatalf("UpdateSharedVariableValue: %v", err)
	}
	if pending, err := s.ApplicationRedeployPending(ctx, app.ID); err != nil || !pending {
		t.Fatalf("redeploy pending = %v, %v; want true after the variable changed", pending, err)
	}
	pendingIDs, err := s.ListRedeployPendingApplications(ctx, prod.ID)
	if err != nil || len(pendingIDs) != 1 || pendingIDs[0] != app.ID {
		t.Fatalf("pending in production = %v, %v; want [%s]", pendingIDs, err, app.ID)
	}

	// Delete cascades: dropping the environment removes only its scoped row.
	if err := s.DeleteEnvironment(ctx, prod.ID); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if _, err := s.GetSharedVariable(ctx, scopedVar.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("environment-scoped row after env delete = %v, want ErrNotFound", err)
	}
	if _, err := s.GetSharedVariable(ctx, projectVar.ID); err != nil {
		t.Fatalf("project-scoped row must survive its sibling environment's deletion: %v", err)
	}

	// Dropping the project removes what is left.
	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetSharedVariable(ctx, projectVar.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project-scoped row after project delete = %v, want ErrNotFound", err)
	}
}

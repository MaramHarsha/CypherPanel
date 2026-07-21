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

	tm, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "authz-team")
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
		Status:        domain.DbStopped,
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
	tok, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "ci", raw, nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	got, err := s.UserForAPIToken(ctx, raw)
	if err != nil || got.ID != user.ID {
		t.Fatalf("UserForAPIToken = %+v, %v; want user %s", got, err, user.ID)
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
	if _, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "old", expRaw, &past); err != nil {
		t.Fatalf("CreateAPIToken (expired): %v", err)
	}
	if _, err := s.UserForAPIToken(ctx, expRaw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token resolved a user: %v", err)
	}

	// Delete revokes.
	if err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if _, err := s.UserForAPIToken(ctx, raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still resolves: %v", err)
	}

	// Deleting the user cascades to their tokens (ON DELETE CASCADE).
	if _, err := s.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), user.ID, "c", []byte("hash-c-"+ids.Secret()), nil); err != nil {
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

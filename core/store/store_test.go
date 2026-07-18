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
	proj, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "proj", ids.New(ids.PrefixEnvironment), "production")
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

	proj, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "shop", ids.New(ids.PrefixEnvironment), "production")
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
	proj2, _, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "other", ids.New(ids.PrefixEnvironment), "production")
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

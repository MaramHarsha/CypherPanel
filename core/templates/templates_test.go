package templates

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ── fakes ───────────────────────────────────────────────────────────────────

type fakeApps struct {
	created []applications.CreateInput
	deleted []string
	failOn  string // app name whose create fails
	nextID  int
	byID    map[string]domain.Application
}

func (f *fakeApps) Create(_ context.Context, envID string, in applications.CreateInput) (domain.Application, string, error) {
	if in.Name == f.failOn {
		return domain.Application{}, "", errors.New("boom")
	}
	f.nextID++
	id := "app_" + strings.Repeat("x", f.nextID)
	f.created = append(f.created, in)
	app := domain.Application{ID: id, EnvironmentID: envID, Name: in.Name, Runtime: in.Runtime, Source: in.Source}
	if f.byID == nil {
		f.byID = map[string]domain.Application{}
	}
	f.byID[id] = app
	return app, "whsec", nil
}

func (f *fakeApps) Get(_ context.Context, id string) (domain.Application, error) {
	a, ok := f.byID[id]
	if !ok {
		return domain.Application{}, errors.New("not found")
	}
	return a, nil
}

func (f *fakeApps) List(context.Context, string) ([]domain.Application, error) {
	out := make([]domain.Application, 0, len(f.byID))
	for _, a := range f.byID {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeApps) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	return nil
}

type fakeDbs struct {
	created   []databases.CreateInput
	deleted   []string
	nextID    int
	createErr error // injected failure for error-mapping tests
	deleteErr error // injected cleanup failure
	// createErrAfterPersist models databases.Service.Create's real contract:
	// the row is persisted and a populated record returned *alongside* an
	// error when reconciliation fails.
	createErrAfterPersist bool
	onCreate              func() // fired after a successful create (context-cancel test)
}

func (f *fakeDbs) Create(_ context.Context, envID string, in databases.CreateInput) (domain.Database, string, error) {
	if f.createErr != nil {
		return domain.Database{}, "", f.createErr
	}
	f.nextID++
	f.created = append(f.created, in)
	db := domain.Database{
		ID:            "db_" + strings.Repeat("y", f.nextID),
		EnvironmentID: envID,
		Name:          in.Name,
		Engine:        domain.DbEngine(in.Engine),
		RootUser:      "postgres",
	}
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createErrAfterPersist {
		return db, "s3cret-root", errors.New("databases: triggering reconciliation: agent unreachable")
	}
	return db, "s3cret-root", nil
}

func (f *fakeDbs) List(context.Context, string) ([]domain.Database, error) { return nil, nil }

func (f *fakeDbs) Delete(_ context.Context, id string, _ bool) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeDeployer struct {
	deployed []string
	removed  []string
	failOn   string
}

func (f *fakeDeployer) Deploy(_ context.Context, appID, _, _ string) (domain.Deployment, error) {
	if appID == f.failOn {
		return domain.Deployment{}, errors.New("deploy boom")
	}
	f.deployed = append(f.deployed, appID)
	return domain.Deployment{ID: "dep_1", ApplicationID: appID}, nil
}

func (f *fakeDeployer) RemoveApp(_ context.Context, _, appID string) error {
	f.removed = append(f.removed, appID)
	return nil
}

func newTestService(t *testing.T, apps *fakeApps, dbs *fakeDbs, dep *fakeDeployer) *Service {
	t.Helper()
	s, err := New(apps, dbs, dep, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ── catalog gate ────────────────────────────────────────────────────────────

// Every bundled template must parse and validate — an invalid file cannot
// ship (template-catalog.md §3).
func TestBundledCatalogIsValid(t *testing.T) {
	s := newTestService(t, &fakeApps{}, &fakeDbs{}, &fakeDeployer{})
	if len(s.List()) < 5 {
		t.Fatalf("catalog has %d templates — bundled set missing?", len(s.List()))
	}
	for _, tpl := range s.List() {
		if tpl.Slug == "" || tpl.Name == "" || tpl.Description == "" {
			t.Errorf("template %+v missing display fields", tpl.Slug)
		}
	}
}

// ── schema validation ───────────────────────────────────────────────────────

func minimalYAML(mut string) string {
	base := `
schema: v1
slug: demo
name: Demo
description: A demo.
category: other
version: "1"
resources:
  databases:
    - name: db
      engine: postgresql
  applications:
    - name: demo
      image: demo/demo:1
      port: 8080
      route: true
      env:
        KEY: "{{db.db.password}}"
`
	return base + mut
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field": minimalYAML("unknown_key: 1\n"),
		"bad schema":    strings.Replace(minimalYAML(""), "schema: v1", "schema: v2", 1),
		"bad engine":    strings.Replace(minimalYAML(""), "engine: postgresql", "engine: oracle", 1),
		"bad image":     strings.Replace(minimalYAML(""), "image: demo/demo:1", `image: "demo demo"`, 1),
		"undeclared db": strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{db.other.password}}", 1),
		"bad db field":  strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{db.db.socket}}", 1),
		"tiny secret":   strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{secret.4}}", 1),
		"huge secret":   strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{secret.128}}", 1),
		"malformed":     strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{db.db.password", 1),
		"unknown token": strings.Replace(minimalYAML(""), "{{db.db.password}}", "{{exec.whoami}}", 1),
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
	if _, err := Parse([]byte(minimalYAML(""))); err != nil {
		t.Fatalf("minimal template rejected: %v", err)
	}
}

func TestParseRejectsTwoRoutedApps(t *testing.T) {
	y := strings.Replace(minimalYAML(""), "    - name: demo", `    - name: second
      image: demo/second:1
      port: 8081
      route: true
    - name: demo`, 1)
	if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("two routed apps accepted (err=%v)", err)
	}
}

// ── placeholder resolution ──────────────────────────────────────────────────

func TestResolve(t *testing.T) {
	dbs := map[string]dbInfo{"db": {host: "cypher-db-1", port: 5432, user: "postgres", password: "pw", database: "postgres", url: "postgres://postgres:pw@cypher-db-1:5432/postgres"}}
	calls := 0
	sec := func(n int) (string, error) { calls++; return strings.Repeat("s", n), nil }

	got, err := resolve("host={{db.db.host}} port={{db.db.port}} url={{db.db.url}} d={{domain}} k={{secret.16}}", dbs, "app.example.com", sec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "host=cypher-db-1 port=5432 url=postgres://postgres:pw@cypher-db-1:5432/postgres d=app.example.com k=ssssssssssssssss"
	if got != want {
		t.Fatalf("resolve = %q, want %q", got, want)
	}
	if calls != 1 {
		t.Fatalf("secret generator called %d times, want 1", calls)
	}
}

// ── install ─────────────────────────────────────────────────────────────────

func TestInstallCreatesWiresAndDeploys(t *testing.T) {
	apps, dbs, dep := &fakeApps{}, &fakeDbs{}, &fakeDeployer{}
	s := newTestService(t, apps, dbs, dep)

	res, err := s.Install(context.Background(), "n8n", InstallInput{
		EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.DatabaseIDs) != 1 || len(res.ApplicationIDs) != 1 {
		t.Fatalf("result = %+v", res)
	}
	// Database first, password required (placeholders must never be empty).
	if len(dbs.created) != 1 || !dbs.created[0].RequirePassword || dbs.created[0].Engine != "postgresql" {
		t.Fatalf("db create = %+v", dbs.created)
	}
	if dbs.created[0].Name != "n8n-db" {
		t.Errorf("db name = %q, want n8n-db", dbs.created[0].Name)
	}
	// App: image source, single-app template takes the install name.
	app := apps.created[0]
	if app.Source.Kind != "image" || app.Source.Image != "n8nio/n8n:1.94.1" || app.Name != "n8n" {
		t.Fatalf("app create = %+v", app)
	}
	if app.Route.Domain != "n8n.example.com" || !app.Route.HTTPS {
		t.Errorf("route = %+v", app.Route)
	}
	// Wiring: the db password reached the app env resolved, the encryption key
	// was generated (64 hex chars for secret.32), domain substituted.
	env := app.EnvVars
	if env["DB_POSTGRESDB_PASSWORD"] != "s3cret-root" {
		t.Errorf("db password not wired: %q", env["DB_POSTGRESDB_PASSWORD"])
	}
	if env["DB_POSTGRESDB_HOST"] != "cypher-db-"+res.DatabaseIDs[0] {
		t.Errorf("db host = %q", env["DB_POSTGRESDB_HOST"])
	}
	if len(env["N8N_ENCRYPTION_KEY"]) != 64 {
		t.Errorf("encryption key = %q, want 64 hex chars", env["N8N_ENCRYPTION_KEY"])
	}
	if env["WEBHOOK_URL"] != "https://n8n.example.com/" {
		t.Errorf("webhook url = %q", env["WEBHOOK_URL"])
	}
	// Deployed through the ordinary pipeline.
	if len(dep.deployed) != 1 || dep.deployed[0] != res.ApplicationIDs[0] {
		t.Fatalf("deployed = %v", dep.deployed)
	}
}

func TestInstallNameCollisionRejected(t *testing.T) {
	apps := &fakeApps{byID: map[string]domain.Application{
		"app_existing": {ID: "app_existing", Name: "grafana"},
	}}
	s := newTestService(t, apps, &fakeDbs{}, &fakeDeployer{})

	_, err := s.Install(context.Background(), "grafana", InstallInput{EnvironmentID: "env_1", ServerID: "srv_1"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if len(apps.created) != 0 {
		t.Fatal("resources created despite collision")
	}
}

// An operator-correctable failure from a resource service (unknown server,
// rejected field) must surface as a ValidationError so the handler answers
// 400 with the reason — not a blank 500.
func TestInstallMapsOperatorErrors(t *testing.T) {
	cases := map[string]error{
		"unknown server":      databases.ErrServerNotFound,
		"unknown environment": applications.ErrEnvironmentNotFound,
		"rejected field":      &applications.ValidationError{Msg: "runtime.port must be between 1 and 65535"},
	}
	for name, cause := range cases {
		apps, dbs := &fakeApps{}, &fakeDbs{createErr: cause}
		s := newTestService(t, apps, dbs, &fakeDeployer{})

		_, err := s.Install(context.Background(), "n8n", InstallInput{EnvironmentID: "env_1", ServerID: "srv_bad", Domain: "n8n.example.com"})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func TestInstallUnknownSlug(t *testing.T) {
	s := newTestService(t, &fakeApps{}, &fakeDbs{}, &fakeDeployer{})
	if _, err := s.Install(context.Background(), "nope", InstallInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A failure after the database exists deletes it again (reverse-order
// cleanup, template-catalog.md §4) — nothing half-installed survives.
func TestInstallFailureCleansUp(t *testing.T) {
	apps := &fakeApps{failOn: "n8n"}
	dbs, dep := &fakeDbs{}, &fakeDeployer{}
	s := newTestService(t, apps, dbs, dep)

	_, err := s.Install(context.Background(), "n8n", InstallInput{EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the create failure", err)
	}
	if len(dbs.deleted) != 1 {
		t.Fatalf("created database not cleaned up: %v", dbs.deleted)
	}
}

// A deploy failure cleans up both the apps and the databases created.
func TestInstallDeployFailureCleansUpApps(t *testing.T) {
	apps, dbs := &fakeApps{}, &fakeDbs{}
	dep := &fakeDeployer{failOn: "app_x"} // first created app id
	s := newTestService(t, apps, dbs, dep)

	_, err := s.Install(context.Background(), "n8n", InstallInput{EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com"})
	if err == nil {
		t.Fatal("want deploy failure")
	}
	if len(apps.deleted) != 1 || len(dbs.deleted) != 1 {
		t.Fatalf("cleanup incomplete: apps=%v dbs=%v", apps.deleted, dbs.deleted)
	}
	if len(dep.removed) != 1 {
		t.Fatalf("desired absence not published for cleaned-up app: %v", dep.removed)
	}
}

// ── review findings (#47) ───────────────────────────────────────────────────

// A template that routes, or builds a URL from {{domain}}, cannot install
// without one: resolving it to "" would write settings like `https:///` into
// the container and still report success.
func TestInstallRequiresDomainForRoutedTemplates(t *testing.T) {
	apps, dbs := &fakeApps{}, &fakeDbs{}
	s := newTestService(t, apps, dbs, &fakeDeployer{})

	_, err := s.Install(context.Background(), "n8n", InstallInput{EnvironmentID: "env_1", ServerID: "srv_1"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError about the domain", err)
	}
	if len(apps.created) != 0 || len(dbs.created) != 0 {
		t.Fatal("resources were created despite the missing domain")
	}
}

// A database row can be persisted while Create still fails (reconciliation is
// triggered inside it). That id must be tracked, or cleanup cannot reach it and
// the install strands a database whose password was never returned.
func TestInstallCleansUpDatabasePersistedBeforeError(t *testing.T) {
	dbs := &fakeDbs{createErrAfterPersist: true}
	s := newTestService(t, &fakeApps{}, dbs, &fakeDeployer{})

	_, err := s.Install(context.Background(), "n8n", InstallInput{
		EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com",
	})
	if err == nil {
		t.Fatal("want the reconciliation failure")
	}
	if len(dbs.deleted) != 1 {
		t.Fatalf("deleted = %v, want the persisted database cleaned up", dbs.deleted)
	}
}

// Rollback must survive the request context being cancelled — an install that
// failed *because* the client disconnected is exactly when cleanup matters.
func TestInstallCleansUpAfterContextCancelled(t *testing.T) {
	apps, dbs := &fakeApps{failOn: "n8n"}, &fakeDbs{}
	s := newTestService(t, apps, dbs, &fakeDeployer{})

	ctx, cancel := context.WithCancel(context.Background())
	dbs.onCreate = cancel // the client goes away mid-install

	_, err := s.Install(ctx, "n8n", InstallInput{
		EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com",
	})
	if err == nil {
		t.Fatal("want the create failure")
	}
	if len(dbs.deleted) != 1 {
		t.Fatalf("deleted = %v, want cleanup to run on a detached context", dbs.deleted)
	}
}

// What a failed rollback left behind has to reach the operator: orphaned
// resources still hold names, volumes, and ports.
func TestInstallReportsIncompleteRollback(t *testing.T) {
	dbs := &fakeDbs{deleteErr: errors.New("daemon unreachable")}
	s := newTestService(t, &fakeApps{failOn: "n8n"}, dbs, &fakeDeployer{})

	_, err := s.Install(context.Background(), "n8n", InstallInput{
		EnvironmentID: "env_1", ServerID: "srv_1", Domain: "n8n.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("err = %v, want the surviving resources named", err)
	}
	if !strings.Contains(err.Error(), "db_") {
		t.Fatalf("err = %v, want the orphaned id included", err)
	}
}

// The health path is validated even when kind is omitted — Parse applies the
// http default only after Validate runs, so an unguarded empty kind would let
// a bad path through.
func TestParseValidatesHealthPathWithDefaultKind(t *testing.T) {
	y := strings.Replace(minimalYAML(""), "      route: true", "      route: true\n      health:\n        path: healthz", 1)
	if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "health path") {
		t.Fatalf("relative health path accepted with a defaulted kind (err=%v)", err)
	}
}

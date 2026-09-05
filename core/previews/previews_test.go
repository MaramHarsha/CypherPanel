package previews

// Preview manager tests (preview-environments.md): PR opened provisions a child
// env + cloned app + deploy; synchronize redeploys without a second env; closed
// (and the TTL sweeper) destroy by deleting the child environment.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	apps             map[string]domain.Application
	envs             map[string]domain.Environment
	previews         map[string]domain.Preview
	deleted          []string // deleted environment ids
	createPreviewErr error    // injected failure
	seq              int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		apps:     map[string]domain.Application{},
		envs:     map[string]domain.Environment{},
		previews: map[string]domain.Preview{},
	}
}

func (f *fakeStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}
func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	e, ok := f.envs[id]
	if !ok {
		return domain.Environment{}, store.ErrNotFound
	}
	return e, nil
}
func (f *fakeStore) CreateEnvironmentOfKind(_ context.Context, id, projectID, name, kind string) (domain.Environment, error) {
	e := domain.Environment{ID: id, ProjectID: projectID, Name: name, Kind: kind}
	f.envs[id] = e
	return e, nil
}
func (f *fakeStore) DeleteEnvironment(_ context.Context, id string) error {
	delete(f.envs, id)
	f.deleted = append(f.deleted, id)
	// Cascade: drop the preview whose child env this is.
	for pid, p := range f.previews {
		if p.EnvironmentID == id {
			delete(f.previews, pid)
		}
	}
	return nil
}
func (f *fakeStore) CreatePreview(_ context.Context, p domain.Preview) (domain.Preview, error) {
	if f.createPreviewErr != nil {
		return domain.Preview{}, f.createPreviewErr
	}
	f.previews[p.ID] = p
	return p, nil
}
func (f *fakeStore) GetPreview(_ context.Context, id string) (domain.Preview, error) {
	p, ok := f.previews[id]
	if !ok {
		return domain.Preview{}, store.ErrNotFound
	}
	return p, nil
}
func (f *fakeStore) GetPreviewByPR(_ context.Context, sourceAppID string, prNumber int) (domain.Preview, error) {
	for _, p := range f.previews {
		if p.SourceAppID == sourceAppID && p.PRNumber == prNumber {
			return p, nil
		}
	}
	return domain.Preview{}, store.ErrNotFound
}
func (f *fakeStore) ListPreviewsBySourceApp(_ context.Context, sourceAppID string) ([]domain.Preview, error) {
	var out []domain.Preview
	for _, p := range f.previews {
		if p.SourceAppID == sourceAppID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeStore) SetPreviewStatus(_ context.Context, id, status string) error {
	if p, ok := f.previews[id]; ok {
		p.Status = status
		f.previews[id] = p
	}
	return nil
}
func (f *fakeStore) ListExpiredPreviews(_ context.Context, cutoff time.Time) ([]domain.Preview, error) {
	var out []domain.Preview
	for _, p := range f.previews {
		if p.ExpiresAt != nil && p.ExpiresAt.Before(cutoff) && p.Status != domain.PreviewDestroying {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeStore) DeletePreview(_ context.Context, id string) error {
	delete(f.previews, id)
	return nil
}

type fakeApps struct {
	created   []applications.CreateInput
	envIDs    []string
	createErr error // injected failure
	store     *fakeStore
}

func (f *fakeApps) Create(_ context.Context, envID string, in applications.CreateInput) (domain.Application, string, error) {
	if f.createErr != nil {
		return domain.Application{}, "", f.createErr
	}
	f.created = append(f.created, in)
	f.envIDs = append(f.envIDs, envID)
	f.store.seq++
	app := domain.Application{
		ID: "app_clone", EnvironmentID: envID, Name: in.Name,
		Source: in.Source, Runtime: in.Runtime, Route: in.Route,
	}
	f.store.apps[app.ID] = app
	return app, "secret", nil
}

type fakeSched struct {
	deploys   []string // app ids deployed
	refs      []string
	removed   []string // app ids removed
	deployErr error    // injected failure
}

func (f *fakeSched) Deploy(_ context.Context, appID, _, ref string) (domain.Deployment, error) {
	if f.deployErr != nil {
		return domain.Deployment{}, f.deployErr
	}
	f.deploys = append(f.deploys, appID)
	f.refs = append(f.refs, ref)
	return domain.Deployment{ID: "dep_1", ApplicationID: appID}, nil
}
func (f *fakeSched) RemoveApp(_ context.Context, _, appID string) error {
	f.removed = append(f.removed, appID)
	return nil
}

func newManager(opts ...Option) (*Manager, *fakeStore, *fakeApps, *fakeSched) {
	fs := newFakeStore()
	fa := &fakeApps{store: fs}
	fsch := &fakeSched{}
	m := New(fs, fa, fsch, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	return m, fs, fa, fsch
}

// fakeRecorder collects the audit entries the manager writes. A preview
// environment appears and disappears with nobody signed in, so these rows are
// the only record that it ever existed.
type fakeRecorder struct{ entries []audit.Entry }

func (f *fakeRecorder) Record(_ context.Context, e audit.Entry) (domain.AuditEvent, error) {
	f.entries = append(f.entries, e)
	return domain.AuditEvent{ID: "aud_" + e.Action}, nil
}

func (f *fakeRecorder) find(action string) (audit.Entry, bool) {
	for _, e := range f.entries {
		if e.Action == action {
			return e, true
		}
	}
	return audit.Entry{}, false
}

// failingRecorder proves the promise in §9: a record that cannot be written
// must not undo the thing it describes.
type failingRecorder struct{}

func (failingRecorder) Record(context.Context, audit.Entry) (domain.AuditEvent, error) {
	return domain.AuditEvent{}, errors.New("the audit table is unavailable")
}

func sourceApp() domain.Application {
	return domain.Application{
		ID: "app_src", EnvironmentID: "env_prod", Name: "web",
		Source:            domain.AppSource{Kind: "git_url", Repo: "acme/web", Branch: "main"},
		Runtime:           domain.AppRuntime{ServerID: "srv_1", Port: 8080, Replicas: 1},
		Route:             domain.AppRoute{Domain: "web.acme.com", HTTPS: true},
		PreviewEnabled:    true,
		PreviewBaseDomain: "preview.acme.com",
		PreviewTTLHours:   72,
	}
}

func TestOpenedProvisionsPreview(t *testing.T) {
	m, fs, fa, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}

	if err := m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha42"); err != nil {
		t.Fatalf("OnPullRequest: %v", err)
	}
	// A child env in the same project, a cloned app, a preview row, one deploy.
	if len(fa.created) != 1 {
		t.Fatalf("clones = %d, want 1", len(fa.created))
	}
	clone := fa.created[0]
	if clone.Source.Branch != "feature/x" {
		t.Fatalf("clone branch = %q, want the PR head", clone.Source.Branch)
	}
	if clone.Route.Domain != "pr-42.preview.acme.com" {
		t.Fatalf("clone domain = %q, want pr-42.preview.acme.com", clone.Route.Domain)
	}
	if len(clone.EnvVars) != 0 {
		t.Fatalf("preview clone carries %d env vars, want 0 (fork-PR secret safety)", len(clone.EnvVars))
	}
	if len(fsch.deploys) != 1 || fsch.deploys[0] != "app_clone" || fsch.refs[0] != "sha42" {
		t.Fatalf("deploys = %v @ %v, want [app_clone]@[sha42]", fsch.deploys, fsch.refs)
	}
	prevs, _ := fs.ListPreviewsBySourceApp(context.Background(), src.ID)
	if len(prevs) != 1 || prevs[0].Status != domain.PreviewRunning || prevs[0].ExpiresAt == nil {
		t.Fatalf("preview = %+v, want one running preview with a TTL", prevs)
	}
}

func TestSynchronizeRedeploysWithoutNewEnv(t *testing.T) {
	m, fs, fa, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	// First open.
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1")
	envsAfterOpen := len(fs.envs)

	// Synchronize: new commits on the same PR.
	if err := m.OnPullRequest(context.Background(), src, ActionSynchronize, 42, "feature/x", "sha2"); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	if len(fs.envs) != envsAfterOpen {
		t.Fatalf("synchronize created a new environment (%d → %d)", envsAfterOpen, len(fs.envs))
	}
	if len(fa.created) != 1 {
		t.Fatalf("synchronize cloned a second app (%d clones)", len(fa.created))
	}
	if len(fsch.deploys) != 2 || fsch.refs[1] != "sha2" {
		t.Fatalf("synchronize did not redeploy the new SHA: deploys=%v refs=%v", fsch.deploys, fsch.refs)
	}
}

func TestClosedDestroysPreview(t *testing.T) {
	m, fs, _, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1")
	childEnv := fs.previews[firstPreviewID(fs)].EnvironmentID

	if err := m.OnPullRequest(context.Background(), src, ActionClosed, 42, "feature/x", ""); err != nil {
		t.Fatalf("closed: %v", err)
	}
	if len(fsch.removed) != 1 || fsch.removed[0] != "app_clone" {
		t.Fatalf("RemoveApp calls = %v, want [app_clone]", fsch.removed)
	}
	if _, ok := fs.envs[childEnv]; ok {
		t.Fatal("child environment not deleted on close")
	}
	if len(fs.previews) != 0 {
		t.Fatalf("preview row survived close: %+v", fs.previews)
	}
}

func TestClosedUnknownPRIsNoOp(t *testing.T) {
	m, fs, _, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	if err := m.OnPullRequest(context.Background(), src, ActionClosed, 99, "x", ""); err != nil {
		t.Fatalf("close of unknown PR should be a no-op, got %v", err)
	}
	if len(fsch.removed) != 0 {
		t.Fatal("close of unknown PR removed something")
	}
}

// A failed CreatePreview must roll back the child environment (and, via its
// cascade, the cloned app) — no orphans.
func TestCreatePreviewFailureRollsBackChildEnv(t *testing.T) {
	m, fs, _, _ := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	fs.createPreviewErr = errors.New("db down")

	err := m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1")
	if err == nil {
		t.Fatal("expected CreatePreview failure to surface")
	}
	// Only the source's production env should remain — the child env was rolled back.
	if _, ok := fs.envs["env_prod"]; !ok || len(fs.envs) != 1 {
		t.Fatalf("child environment not rolled back: envs=%v", fs.envs)
	}
	if len(fs.deleted) != 1 {
		t.Fatalf("expected one DeleteEnvironment rollback, got %d", len(fs.deleted))
	}
}

// A failed redeploy on synchronize marks the existing preview as error.
func TestRedeployFailureMarksPreviewError(t *testing.T) {
	m, fs, _, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1")
	pid := firstPreviewID(fs)

	fsch.deployErr = errors.New("scheduler unavailable")
	if err := m.OnPullRequest(context.Background(), src, ActionSynchronize, 42, "feature/x", "sha2"); err == nil {
		t.Fatal("expected redeploy failure to surface")
	}
	if got := fs.previews[pid].Status; got != domain.PreviewError {
		t.Fatalf("preview status after failed redeploy = %q, want error", got)
	}
}

func TestSweepDestroysExpired(t *testing.T) {
	m, fs, _, fsch := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1")
	// Force the preview to be expired.
	past := time.Now().Add(-time.Hour)
	for id, p := range fs.previews {
		p.ExpiresAt = &past
		fs.previews[id] = p
	}

	m.SweepExpired(context.Background())
	if len(fsch.removed) != 1 {
		t.Fatalf("sweep removed %d apps, want 1", len(fsch.removed))
	}
	if len(fs.previews) != 0 {
		t.Fatal("expired preview survived the sweep")
	}
}

func firstPreviewID(fs *fakeStore) string {
	for id := range fs.previews {
		return id
	}
	return ""
}

// ─── audit (audit-log.md §3) ────────────────────────────────────────────────

// A preview environment is created and destroyed with no operator in the loop.
// Without these two rows the `environment.created`/`environment.deleted` verbs
// would be true only of the environments a person made by hand.
func TestPreviewLifecycleIsAudited(t *testing.T) {
	rec := &fakeRecorder{}
	m, fs, _, _ := newManager(WithAudit(rec))
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}

	if err := m.OnPullRequest(context.Background(), src, ActionOpened, 42, "feature/x", "sha1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	created, ok := rec.find(audit.ActionEnvironmentCreated)
	if !ok {
		t.Fatalf("no environment.created row for a preview: %+v", rec.entries)
	}
	childEnv := fs.previews[firstPreviewID(fs)].EnvironmentID
	if created.EnvironmentID != childEnv {
		t.Errorf("created row scope = %q, want the child environment %q", created.EnvironmentID, childEnv)
	}
	if created.Actor.Kind != domain.AuditActorSystem {
		t.Errorf("actor = %+v, want the panel itself — nobody was signed in", created.Actor)
	}
	if created.Detail["pr"] != 42 || created.Detail["kind"] != domain.EnvPreview {
		t.Errorf("created detail = %+v, want the PR and the preview kind", created.Detail)
	}

	if err := m.OnPullRequest(context.Background(), src, ActionClosed, 42, "feature/x", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	deleted, ok := rec.find(audit.ActionEnvironmentDeleted)
	if !ok {
		t.Fatalf("no environment.deleted row for a torn-down preview: %+v", rec.entries)
	}
	// The environment is gone, so the row must carry the project it hung
	// under: there is nothing left for the INSERT to resolve the team from.
	if deleted.ProjectID != "prj_1" {
		t.Errorf("deleted row project = %q, want the chain snapshotted before the delete", deleted.ProjectID)
	}
	if deleted.Detail["reason"] != "pull request closed" {
		t.Errorf("deleted detail = %+v, want the reason it went away", deleted.Detail)
	}
}

// The TTL sweeper is the other automated teardown, and it must say so: an
// environment that vanished on a timer is exactly the disappearance an operator
// would otherwise have no record of.
func TestSweptPreviewIsAuditedWithItsReason(t *testing.T) {
	rec := &fakeRecorder{}
	m, fs, _, _ := newManager(WithAudit(rec))
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 7, "feature/x", "sha1")
	m.now = func() time.Time { return time.Now().Add(200 * time.Hour) }

	m.SweepExpired(context.Background())
	deleted, ok := rec.find(audit.ActionEnvironmentDeleted)
	if !ok {
		t.Fatalf("the sweeper tore an environment down without a row: %+v", rec.entries)
	}
	if deleted.Detail["reason"] != "ttl expired" {
		t.Errorf("swept row reason = %v, want \"ttl expired\"", deleted.Detail["reason"])
	}
	if len(fs.previews) != 0 {
		t.Fatalf("preview survived the sweep: %+v", fs.previews)
	}
}

// A failed audit write must not undo the teardown that already happened (§9).
func TestAFailingAuditWriteDoesNotFailTheTeardown(t *testing.T) {
	m, fs, _, _ := newManager(WithAudit(failingRecorder{}))
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	_ = m.OnPullRequest(context.Background(), src, ActionOpened, 3, "feature/x", "sha1")

	if err := m.OnPullRequest(context.Background(), src, ActionClosed, 3, "feature/x", ""); err != nil {
		t.Fatalf("close with a failing audit write = %v, want it to still succeed", err)
	}
	if len(fs.previews) != 0 {
		t.Fatalf("the preview survived a close that reported success: %+v", fs.previews)
	}
}

// A panel wired without the audit log keeps working, and pays for no lookups.
func TestPreviewsWorkWithoutAnAuditRecorder(t *testing.T) {
	m, fs, _, _ := newManager()
	src := sourceApp()
	fs.apps[src.ID] = src
	fs.envs["env_prod"] = domain.Environment{ID: "env_prod", ProjectID: "prj_1"}
	if err := m.OnPullRequest(context.Background(), src, ActionOpened, 5, "feature/x", "sha1"); err != nil {
		t.Fatalf("open without a recorder: %v", err)
	}
	if err := m.OnPullRequest(context.Background(), src, ActionClosed, 5, "feature/x", ""); err != nil {
		t.Fatalf("close without a recorder: %v", err)
	}
}

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
func (f *fakeStore) CreateEnvironment(_ context.Context, id, projectID, name string) (domain.Environment, error) {
	e := domain.Environment{ID: id, ProjectID: projectID, Name: name}
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

func newManager() (*Manager, *fakeStore, *fakeApps, *fakeSched) {
	fs := newFakeStore()
	fa := &fakeApps{store: fs}
	fsch := &fakeSched{}
	m := New(fs, fa, fsch, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, fs, fa, fsch
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

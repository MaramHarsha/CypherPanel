package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

type fakeStore struct {
	mu          sync.Mutex
	apps        map[string]domain.Application
	revisions   map[string]domain.Revision
	deployments map[string]domain.Deployment
	envVars     map[string][]domain.EnvVar
	deployKeys  map[string]domain.DeployKey
	dbs         map[string]domain.Database
	dbRevs      map[string]domain.DatabaseRevision
	servers     []domain.Server
	seq         int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		apps:        map[string]domain.Application{},
		revisions:   map[string]domain.Revision{},
		deployments: map[string]domain.Deployment{},
		envVars:     map[string][]domain.EnvVar{},
		deployKeys:  map[string]domain.DeployKey{},
		dbs:         map[string]domain.Database{},
		dbRevs:      map[string]domain.DatabaseRevision{},
	}
}

func (f *fakeStore) addApp(id, serverID string) domain.Application {
	app := domain.Application{
		ID:            id,
		EnvironmentID: "env_1",
		Name:          id,
		Source:        domain.AppSource{Kind: "github", Repo: "acme/" + id, Branch: "main"},
		Build:         domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime:       domain.AppRuntime{ServerID: serverID, Port: 8080, Replicas: 1},
		Route:         domain.AppRoute{Domain: id + ".example.com", HTTPS: true},
		Health:        domain.AppHealth{Path: "/", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
		Status:        domain.AppStopped,
	}
	f.apps[id] = app
	return app
}

func (f *fakeStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) ListApplicationsByServer(_ context.Context, serverID string) ([]domain.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Application
	for _, a := range f.apps {
		if a.Runtime.ServerID == serverID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) SetApplicationDesiredRevision(_ context.Context, appID, revisionID string) (domain.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[appID]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	a.DesiredRevisionID = &revisionID
	f.apps[appID] = a
	return a, nil
}

func (f *fakeStore) SetApplicationStatus(_ context.Context, appID, status, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.apps[appID]; ok {
		a.Status, a.StatusDetail = status, detail
		f.apps[appID] = a
	}
	return nil
}

func (f *fakeStore) SetApplicationObservedStatus(_ context.Context, appID, status, detail, observedRevisionID string, observedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.apps[appID]; ok {
		a.Status, a.StatusDetail, a.ObservedRevisionID = status, detail, observedRevisionID
		at := observedAt
		a.StatusObservedAt = &at
		f.apps[appID] = a
	}
	return nil
}

func (f *fakeStore) ListEnvVars(_ context.Context, appID string) ([]domain.EnvVar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.envVars[appID], nil
}

func (f *fakeStore) CreateRevision(_ context.Context, id, appID, sourceCommit string, snapshot []byte) (domain.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rev := domain.Revision{ID: id, ApplicationID: appID, SourceCommit: sourceCommit, ConfigSnapshot: snapshot, CreatedAt: time.Now()}
	f.revisions[id] = rev
	return rev, nil
}

func (f *fakeStore) GetRevision(_ context.Context, id string) (domain.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.revisions[id]
	if !ok {
		return domain.Revision{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) SetRevisionImage(_ context.Context, id, image string) (domain.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.revisions[id]
	if !ok {
		return domain.Revision{}, store.ErrNotFound
	}
	r.Image = image
	f.revisions[id] = r
	return r, nil
}

func (f *fakeStore) SetRevisionSourceCommit(_ context.Context, id, sha string) (domain.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.revisions[id]
	if !ok {
		return domain.Revision{}, store.ErrNotFound
	}
	r.SourceCommit = sha
	f.revisions[id] = r
	return r, nil
}

func (f *fakeStore) CreateDeployment(_ context.Context, id, appID, revisionID, trigger string) (domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	dep := domain.Deployment{
		ID: id, ApplicationID: appID, RevisionID: revisionID,
		Status: domain.DeployQueued, Trigger: trigger,
		CreatedAt: time.Unix(int64(f.seq), 0), UpdatedAt: time.Now(),
	}
	f.deployments[id] = dep
	return dep, nil
}

func (f *fakeStore) SetDeploymentBuilder(_ context.Context, id, builderServerID string) (domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return domain.Deployment{}, store.ErrNotFound
	}
	d.BuilderServerID = &builderServerID
	f.deployments[id] = d
	return d, nil
}

func (f *fakeStore) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return domain.Deployment{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) UpdateDeploymentStatus(_ context.Context, id string, status domain.DeploymentStatus, detail string) (domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return domain.Deployment{}, store.ErrNotFound
	}
	d.Status, d.Detail = status, detail
	if status.Terminal() {
		now := time.Now()
		d.FinishedAt = &now
	}
	f.deployments[id] = d
	return d, nil
}

func (f *fakeStore) activeFor(appID string) []domain.Deployment {
	var out []domain.Deployment
	for _, d := range f.deployments {
		if (appID == "" || d.ApplicationID == appID) && !d.Status.Terminal() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (f *fakeStore) ListActiveDeployments(context.Context) ([]domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeFor(""), nil
}

func (f *fakeStore) ListActiveDeploymentsByApplication(_ context.Context, appID string) ([]domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeFor(appID), nil
}

func (f *fakeStore) ListServers(context.Context) ([]domain.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers, nil
}

func (f *fakeStore) GetDeployKey(_ context.Context, id string) (domain.DeployKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dk, ok := f.deployKeys[id]
	if !ok {
		return domain.DeployKey{}, store.ErrNotFound
	}
	return dk, nil
}

func (f *fakeStore) GetDatabase(_ context.Context, id string) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dbs[id]
	if !ok {
		return domain.Database{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ListDatabasesByServer(_ context.Context, serverID string) ([]domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Database
	for _, d := range f.dbs {
		if d.ServerID == serverID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) GetDatabaseRevision(_ context.Context, id string) (domain.DatabaseRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.dbRevs[id]
	if !ok {
		return domain.DatabaseRevision{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) SetDatabaseObservedStatus(_ context.Context, id, status, detail, observedRevisionID string, observedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.dbs[id]; ok {
		d.Status = status
		d.StatusDetail = detail
		d.ObservedRevisionID = observedRevisionID
		d.StatusObservedAt = &observedAt
		f.dbs[id] = d
	}
	return nil
}

func (f *fakeStore) ListPendingDeleteDatabases(_ context.Context) ([]domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Database
	for _, d := range f.dbs {
		if d.PendingDelete {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteDatabase(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.dbs, id)
	return nil
}

type published struct {
	subject string
	msgID   string
	data    []byte
}

type fakeBus struct {
	mu        sync.Mutex
	work      []published
	consumers []string
}

func (f *fakeBus) PublishWork(_ context.Context, subject, msgID string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.work = append(f.work, published{subject, msgID, data})
	return nil
}

func (f *fakeBus) EnsureWorkConsumer(_ context.Context, serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumers = append(f.consumers, serverID)
	return nil
}

func (f *fakeBus) last() (published, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.work) == 0 {
		return published{}, false
	}
	return f.work[len(f.work)-1], true
}

func (f *fakeBus) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.work)
}

// fakeOpener reverses the fakeSealer convention used across service tests.
type fakeOpener struct{}

func (fakeOpener) Open(ct, _ []byte) ([]byte, error) {
	return []byte(strings.TrimPrefix(string(ct), "sealed:")), nil
}

// failingOpener simulates sealed data the master key cannot open.
type failingOpener struct{}

func (failingOpener) Open(_, _ []byte) ([]byte, error) {
	return nil, errors.New("cipher: message authentication failed")
}

func newScheduler(fs *fakeStore, fb *fakeBus) *Scheduler {
	return New(fs, fb, fakeOpener{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestDeployStartsBuild(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if dep.Status != domain.DeployBuilding {
		t.Fatalf("status = %s, want building", dep.Status)
	}
	if fs.apps["app_1"].Status != domain.AppDeploying {
		t.Fatalf("app status = %s, want deploying", fs.apps["app_1"].Status)
	}
	p, ok := fb.last()
	if !ok || p.subject != subjects.Build("srv_1") {
		t.Fatalf("work = %+v, want build on srv_1", p)
	}
	var bw agentv1.BuildWork
	if err := proto.Unmarshal(p.data, &bw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bw.GetImage() != "cypher/app_1:"+dep.RevisionID || bw.GetRepoUrl() != "acme/app_1" {
		t.Fatalf("build work = %+v", &bw)
	}
}

// An application referencing a deploy key gets the unsealed PEM in its
// BuildWork — decrypted only at spec-build time, carried only on the mTLS
// bus (deploy-key-private-repos.md §4).
func TestDeployWithDeployKeySendsUnsealedPem(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	keyID := "dk_1"
	fs.deployKeys[keyID] = domain.DeployKey{ID: keyID, PrivateKeyCT: []byte("sealed:PEM-BYTES"), PrivateKeyNonce: []byte("n")}
	app.Source.DeployKeyID = &keyID
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	if _, err := s.Deploy(context.Background(), "app_1", "manual", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	p, ok := fb.last()
	if !ok {
		t.Fatal("no work published")
	}
	var bw agentv1.BuildWork
	if err := proto.Unmarshal(p.data, &bw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bw.GetDeployKeyPem() != "PEM-BYTES" {
		t.Fatalf("deploy_key_pem = %q, want the unsealed PEM", bw.GetDeployKeyPem())
	}
}

// A dangling deploy-key reference fails the deployment instead of leaving it
// stuck in building (no work was published, so no event can ever advance it).
func TestDeployWithMissingDeployKeyFails(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	keyID := "dk_gone"
	app.Source.DeployKeyID = &keyID
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Deploy err = %v, want store.ErrNotFound", err)
	}
	if fb.count() != 0 {
		t.Fatal("work published despite missing deploy key")
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployFailed {
		t.Fatalf("deployment status = %s, want failed", got.Status)
	}
}

// An unsealable deploy key (e.g. sealed under a different master key) fails
// the deployment the same way.
func TestDeployWithUnsealableDeployKeyFails(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	keyID := "dk_1"
	fs.deployKeys[keyID] = domain.DeployKey{ID: keyID, PrivateKeyCT: []byte("sealed:PEM"), PrivateKeyNonce: []byte("n")}
	app.Source.DeployKeyID = &keyID
	fs.apps["app_1"] = app
	s := New(fs, fb, failingOpener{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err == nil {
		t.Fatal("Deploy succeeded, want an unseal error")
	}
	if fb.count() != 0 {
		t.Fatal("work published despite unsealable deploy key")
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployFailed {
		t.Fatalf("deployment status = %s, want failed", got.Status)
	}
}

func TestBuildSuccessStartsRolloutWithDecryptedEnv(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.envVars["app_1"] = []domain.EnvVar{{Key: "API_KEY", ValueCT: []byte("sealed:hunter2"), ValueNonce: []byte("n")}}
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_SUCCEEDED,
		CommitSha:    "abc123",
	})

	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployRollingOut {
		t.Fatalf("status = %s, want rolling_out", got.Status)
	}
	if rev := fs.revisions[dep.RevisionID]; rev.Image == "" || rev.SourceCommit != "abc123" {
		t.Fatalf("revision not stamped: %+v", rev)
	}
	if app := fs.apps["app_1"]; app.DesiredRevisionID == nil || *app.DesiredRevisionID != dep.RevisionID {
		t.Fatal("desired revision not set at rollout")
	}
	p, _ := fb.last()
	if p.subject != subjects.Rollout("srv_1") {
		t.Fatalf("subject = %s, want rollout", p.subject)
	}
	var rw agentv1.RolloutWork
	if err := proto.Unmarshal(p.data, &rw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rw.GetSpec().GetEnv()["API_KEY"] != "hunter2" {
		t.Fatal("env var not decrypted into the spec")
	}
	if rw.GetSpec().GetNetwork() != "cypher-env_1" {
		t.Fatalf("network = %q, want deterministic name", rw.GetSpec().GetNetwork())
	}
}

func TestObservedRunningCompletesDeployment(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	// Rollout succeeded event alone must NOT complete it (ADR-005).
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_ROLLOUT, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployRollingOut {
		t.Fatalf("status = %s; a work-item outcome alone must not complete a deployment", got.Status)
	}

	// The observation does.
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId: "app_1", RevisionId: dep.RevisionID, State: domain.AppRunning,
	})
	got, _ = fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeploySucceeded {
		t.Fatalf("status = %s, want succeeded after observation", got.Status)
	}
	if app := fs.apps["app_1"]; app.Status != domain.AppRunning || app.ObservedRevisionID != dep.RevisionID {
		t.Fatalf("app observation not recorded: %+v", app)
	}
}

func TestBuildFailureFailsDeploymentAndPromotesNext(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	first, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	second, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	if d, _ := fs.GetDeployment(context.Background(), second.ID); d.Status != domain.DeployQueued {
		t.Fatalf("second deploy = %s, want queued behind the first", d.Status)
	}

	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: first.ID, Stage: agentv1.DeployEvent_STAGE_BUILD,
		Outcome: agentv1.DeployEvent_OUTCOME_FAILED, Detail: "no Dockerfile",
	})
	got, _ := fs.GetDeployment(context.Background(), first.ID)
	if got.Status != domain.DeployFailed || !strings.Contains(got.Detail, "no Dockerfile") {
		t.Fatalf("first = %+v, want failed with detail", got)
	}
	got, _ = fs.GetDeployment(context.Background(), second.ID)
	if got.Status != domain.DeployBuilding {
		t.Fatalf("second = %s, want promoted to building", got.Status)
	}
}

func TestRollbackSkipsBuild(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	// Ship rev A completely.
	depA, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: depA.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{AppId: "app_1", RevisionId: depA.RevisionID, State: domain.AppRunning})

	// Roll back to it: no build stage, straight to rollout.
	before := fb.count()
	rb, err := s.Rollback(context.Background(), depA.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.Status != domain.DeployRollingOut || rb.Trigger != "rollback" {
		t.Fatalf("rollback = %+v, want rolling_out", rb)
	}
	p, _ := fb.last()
	if fb.count() != before+1 || p.subject != subjects.Rollout("srv_1") {
		t.Fatalf("rollback published %+v, want exactly one rollout", p)
	}
}

func TestRollbackOfUnbuiltRevisionRefused(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "") // still building
	if _, err := s.Rollback(context.Background(), dep.ID); !errors.Is(err, ErrRevisionNotBuilt) {
		t.Fatalf("err = %v, want ErrRevisionNotBuilt", err)
	}
}

func TestEventsFromWrongServerIgnored(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	// A different (compromised?) server reports the build done.
	s.HandleDeployEvent(context.Background(), "srv_evil", &agentv1.DeployEvent{
		DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployBuilding {
		t.Fatalf("status = %s; a foreign server advanced the pipeline", got.Status)
	}
	// And a foreign status report must not be recorded.
	s.HandleAppStatus(context.Background(), "srv_evil", &agentv1.AppStatus{AppId: "app_1", RevisionId: dep.RevisionID, State: domain.AppRunning})
	if fs.apps["app_1"].ObservedRevisionID != "" {
		t.Fatal("foreign observation recorded")
	}
}

func TestRedeliveredEventAfterTerminalIsNoop(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	fail := &agentv1.DeployEvent{DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_FAILED, Detail: "boom"}
	s.HandleDeployEvent(context.Background(), "srv_1", fail)
	before := fb.count()
	s.HandleDeployEvent(context.Background(), "srv_1", fail) // redelivery
	if fb.count() != before {
		t.Fatal("redelivered event published new work")
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployFailed {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestDesiredStateOmitsUnbuiltRevisions(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_built", "srv_1")
	fs.addApp("app_building", "srv_1")
	fs.addApp("app_new", "srv_1") // never deployed
	fs.envVars["app_built"] = []domain.EnvVar{{Key: "K", ValueCT: []byte("sealed:v"), ValueNonce: []byte("n")}}
	s := newScheduler(fs, fb)

	depA, _ := s.Deploy(context.Background(), "app_built", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: depA.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	if _, err := s.Deploy(context.Background(), "app_building", "manual", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	data, err := s.DesiredStateFor(context.Background(), "srv_1")
	if err != nil {
		t.Fatalf("DesiredStateFor: %v", err)
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ds.Specs) != 1 || ds.Specs[0].GetAppId() != "app_built" {
		t.Fatalf("desired = %+v, want only the built app", ds.Specs)
	}
	if ds.Specs[0].GetEnv()["K"] != "v" {
		t.Fatal("env not decrypted in desired state")
	}
}

func TestRecoverRepublishesInFlightWork(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.servers = []domain.Server{{ID: "srv_1"}}
	s := newScheduler(fs, fb)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "") // building
	countBefore := fb.count()

	// A fresh scheduler (new plane process) recovers over the same store.
	s2 := newScheduler(fs, fb)
	if err := s2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(fb.consumers) == 0 || fb.consumers[len(fb.consumers)-1] != "srv_1" {
		t.Fatal("recover did not re-assert the server's work consumer")
	}
	if fb.count() != countBefore+1 {
		t.Fatalf("recover published %d items, want 1 (the in-flight build)", fb.count()-countBefore)
	}
	p, _ := fb.last()
	if p.subject != subjects.Build("srv_1") || p.msgID != dep.ID+".build" {
		t.Fatalf("republished = %+v, want the same build work (same idempotency key)", p)
	}
}

// A private-repo build republished by Recover must carry the deploy-key PEM
// again — a plane restart must not downgrade the clone to credential-less.
func TestRecoverRepublishesBuildWithDeployKey(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	keyID := "dk_1"
	fs.deployKeys[keyID] = domain.DeployKey{ID: keyID, PrivateKeyCT: []byte("sealed:PEM-BYTES"), PrivateKeyNonce: []byte("n")}
	app.Source.DeployKeyID = &keyID
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	if _, err := s.Deploy(context.Background(), "app_1", "manual", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// A fresh scheduler (new plane process) recovers over the same store.
	s2 := newScheduler(fs, fb)
	if err := s2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	p, ok := fb.last()
	if !ok {
		t.Fatal("recover published nothing")
	}
	var bw agentv1.BuildWork
	if err := proto.Unmarshal(p.data, &bw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bw.GetDeployKeyPem() != "PEM-BYTES" {
		t.Fatalf("recovered deploy_key_pem = %q, want the unsealed PEM", bw.GetDeployKeyPem())
	}
}

// A deploy key that cannot be unsealed at recovery time fails the deployment
// instead of leaving it active-but-stuck, and republishes nothing for it.
func TestRecoverWithUnsealableDeployKeyFailsDeployment(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	keyID := "dk_1"
	fs.deployKeys[keyID] = domain.DeployKey{ID: keyID, PrivateKeyCT: []byte("sealed:PEM"), PrivateKeyNonce: []byte("n")}
	app.Source.DeployKeyID = &keyID
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb) // healthy opener: the deploy itself starts fine

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	countBefore := fb.count()

	// The recovering plane cannot open the sealed key (e.g. wrong master key).
	s2 := New(fs, fb, failingOpener{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got, _ := fs.GetDeployment(context.Background(), dep.ID); got.Status != domain.DeployFailed {
		t.Fatalf("deployment status = %s, want failed", got.Status)
	}
	if fb.count() != countBefore {
		t.Fatal("recover republished build work despite an unsealable deploy key")
	}
}

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
	restores       map[string]domain.DatabaseRestore
	dbStatuses     map[string]string
	mu             sync.Mutex
	apps           map[string]domain.Application
	revisions      map[string]domain.Revision
	deployments    map[string]domain.Deployment
	envVars        map[string][]domain.EnvVar
	deployKeys     map[string]domain.DeployKey
	dbs            map[string]domain.Database
	dbRevs         map[string]domain.DatabaseRevision
	targets        map[string]domain.BackupTarget
	schedules      map[string]domain.DatabaseBackup
	records        map[string]domain.BackupRecord
	scheduledTasks map[string]domain.ScheduledTask
	taskRuns       []domain.ScheduledTaskRun
	servers        []domain.Server
	seq            int

	// Phase 4: shared variables (shared-variables.md). sharedVars is keyed by
	// "<projectID>|<environmentID>" so a test can model project scope and
	// environment scope separately; envApplied and envResolved are the two
	// drift stamps.
	environments map[string]domain.Environment
	sharedVars   map[string][]domain.SharedVariable
	envResolved  map[string]time.Time
	envApplied   map[string]time.Time
	// scopeReads counts ListSharedVariablesInScope calls, so a test can prove
	// an application with no references never touches the shared table.
	scopeReads int

	// Compose Stacks (compose-stacks.md §4).
	composeStacks    map[string]domain.ComposeStack
	composeRevisions map[string]domain.ComposeRevision
	composeEnv       map[string][]domain.ComposeEnvVar

	// panelTLS is the panel's ACME account (agent-identity-and-tls.md §4);
	// nil models a panel that has never configured TLS.
	panelTLS *domain.PanelTLS
	// panelTLSErr models a store that cannot answer the TLS question at all.
	panelTLSErr error

	// revisionErr / dbRevisionErr model an infrastructure failure — a
	// connection that dropped mid-read, not a missing row. Desired state must
	// never read one as "this application is gone" (see DesiredStateFor).
	revisionErr   error
	dbRevisionErr error
}

// latestDeployment returns the most recently created deployment for an app —
// what a test needs after driving one through the pipeline.
func (f *fakeStore) latestDeployment(appID string) (domain.Deployment, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out domain.Deployment
	found := false
	for _, d := range f.deployments {
		if d.ApplicationID != appID {
			continue
		}
		if !found || d.CreatedAt.After(out.CreatedAt) {
			out, found = d, true
		}
	}
	return out, found
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		apps:             map[string]domain.Application{},
		revisions:        map[string]domain.Revision{},
		deployments:      map[string]domain.Deployment{},
		envVars:          map[string][]domain.EnvVar{},
		deployKeys:       map[string]domain.DeployKey{},
		dbs:              map[string]domain.Database{},
		dbRevs:           map[string]domain.DatabaseRevision{},
		targets:          map[string]domain.BackupTarget{},
		schedules:        map[string]domain.DatabaseBackup{},
		records:          map[string]domain.BackupRecord{},
		scheduledTasks:   map[string]domain.ScheduledTask{},
		environments:     map[string]domain.Environment{"env_1": {ID: "env_1", ProjectID: "prj_1", Name: "production"}},
		composeStacks:    map[string]domain.ComposeStack{},
		composeRevisions: map[string]domain.ComposeRevision{},
		composeEnv:       map[string][]domain.ComposeEnvVar{},
		sharedVars:       map[string][]domain.SharedVariable{},
		envResolved:      map[string]time.Time{},
		envApplied:       map[string]time.Time{},
	}
}

// ─── Compose Stacks (compose-stacks.md §4) ──────────────────────────────────

func (f *fakeStore) GetComposeStack(_ context.Context, id string) (domain.ComposeStack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.composeStacks[id]
	if !ok {
		return domain.ComposeStack{}, store.ErrNotFound
	}
	return st, nil
}

func (f *fakeStore) ListComposeStacksByServer(_ context.Context, serverID string) ([]domain.ComposeStack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.ComposeStack
	for _, st := range f.composeStacks {
		if st.ServerID == serverID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeStore) GetComposeRevision(_ context.Context, id string) (domain.ComposeRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rev, ok := f.composeRevisions[id]
	if !ok {
		return domain.ComposeRevision{}, store.ErrNotFound
	}
	return rev, nil
}

func (f *fakeStore) ListComposeEnvVars(_ context.Context, stackID string) ([]domain.ComposeEnvVar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.composeEnv[stackID], nil
}

func (f *fakeStore) SetComposeStackStatus(_ context.Context, id, status, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.composeStacks[id]
	if !ok {
		return store.ErrNotFound
	}
	st.Status, st.StatusDetail = status, detail
	f.composeStacks[id] = st
	return nil
}

func (f *fakeStore) SetComposeStackObservedStatus(_ context.Context, id, status, detail, revisionID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.composeStacks[id]
	if !ok {
		return store.ErrNotFound
	}
	st.Status, st.StatusDetail = status, detail
	st.ObservedRevisionID, st.StatusObservedAt = revisionID, &at
	f.composeStacks[id] = st
	return nil
}

// addComposeStack seeds a stack with one revision already deployed — the state
// a converge or an observation acts on.
func (f *fakeStore) addComposeStack(id, serverID, file string) domain.ComposeStack {
	revID := "csr_" + id
	f.composeRevisions[revID] = domain.ComposeRevision{ID: revID, StackID: id, ComposeYAML: file}
	st := domain.ComposeStack{
		ID: id, EnvironmentID: "env_1", Name: id, ServerID: serverID,
		DesiredRevisionID: &revID, Status: domain.AppStopped,
	}
	f.composeStacks[id] = st
	return st
}

// BumpApplicationRestartToken records a restart as desired state
// (deployment-control.md §3).
func (f *fakeStore) BumpApplicationRestartToken(_ context.Context, appID, token string) (domain.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[appID]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	app.RestartToken = token
	f.apps[appID] = app
	return app, nil
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

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	env, ok := f.environments[id]
	if !ok {
		return domain.Environment{}, store.ErrNotFound
	}
	return env, nil
}

func (f *fakeStore) ListSharedVariablesInScope(_ context.Context, projectID, envID string) ([]domain.SharedVariable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopeReads++
	// Shadowing, as the SQL does it: the environment-scoped row wins.
	byKey := map[string]domain.SharedVariable{}
	for _, v := range f.sharedVars[projectID+"|"] {
		byKey[v.Key] = v
	}
	for _, v := range f.sharedVars[projectID+"|"+envID] {
		byKey[v.Key] = v
	}
	out := make([]domain.SharedVariable, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeStore) SetDeploymentEnvResolved(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.envResolved[id] = time.Unix(0, 0).Add(time.Duration(f.seq) * time.Second)
	return nil
}

func (f *fakeStore) ApplyDeploymentEnvStamp(_ context.Context, deploymentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.envResolved[deploymentID]
	if !ok {
		return nil
	}
	dep, ok := f.deployments[deploymentID]
	if !ok {
		return nil
	}
	f.envApplied[dep.ApplicationID] = at
	return nil
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
	if f.revisionErr != nil {
		return domain.Revision{}, f.revisionErr
	}
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

// activeFor mirrors the two queue queries in
// core/store/queries/deployments.sql, which exclude the terminal states AND
// awaiting_approval: a parked deploy holds no pipeline slot, so it can neither
// block a later deploy nor be resumed by Recover (deploy-protection.md §3). A
// fake that used Terminal() alone would hide exactly that.
func (f *fakeStore) activeFor(appID string) []domain.Deployment {
	var out []domain.Deployment
	for _, d := range f.deployments {
		if (appID == "" || d.ApplicationID == appID) && !d.Status.Terminal() && !d.Status.Parked() {
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

func (f *fakeStore) GetPanelTLS(context.Context) (domain.PanelTLS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panelTLSErr != nil {
		return domain.PanelTLS{}, f.panelTLSErr
	}
	if f.panelTLS == nil {
		return domain.PanelTLS{}, store.ErrNotFound
	}
	return *f.panelTLS, nil
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
	if f.dbRevisionErr != nil {
		return domain.DatabaseRevision{}, f.dbRevisionErr
	}
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

func (f *fakeStore) GetDatabaseBackup(_ context.Context, id string) (domain.DatabaseBackup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.schedules[id]
	if !ok {
		return domain.DatabaseBackup{}, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) ListEnabledBackupSchedules(_ context.Context) ([]domain.DatabaseBackup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.DatabaseBackup
	for _, b := range f.schedules {
		if b.Enabled && b.Schedule != "" {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) GetBackupTarget(_ context.Context, id string) (domain.BackupTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.targets[id]
	if !ok {
		return domain.BackupTarget{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) CreateBackupRecord(_ context.Context, r domain.BackupRecord) (domain.BackupRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[r.ID] = r
	return r, nil
}

// Restore records, modelling the store's one rule: only a running restore
// moves, so a redelivered event is a no-op rather than a second finish.
func (f *fakeStore) CreateDatabaseRestore(_ context.Context, id, databaseID, backupRecordID, step string) (domain.DatabaseRestore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restores == nil {
		f.restores = map[string]domain.DatabaseRestore{}
	}
	rec := domain.DatabaseRestore{
		ID: id, DatabaseID: databaseID, BackupRecordID: backupRecordID,
		Status: domain.RestoreRunning, Step: step, StartedAt: time.Now(),
	}
	f.restores[id] = rec
	return rec, nil
}

func (f *fakeStore) GetDatabaseRestore(_ context.Context, id string) (domain.DatabaseRestore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.restores[id]
	if !ok {
		return domain.DatabaseRestore{}, store.ErrNotFound
	}
	return rec, nil
}

func (f *fakeStore) AdvanceDatabaseRestore(_ context.Context, id, step string, done, total int64) (domain.DatabaseRestore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.restores[id]
	if !ok || rec.Status != domain.RestoreRunning {
		return domain.DatabaseRestore{}, store.ErrNotFound
	}
	rec.Step, rec.BytesDone, rec.BytesTotal = step, done, total
	f.restores[id] = rec
	return rec, nil
}

func (f *fakeStore) FinishDatabaseRestore(_ context.Context, id, status, detail string) (domain.DatabaseRestore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.restores[id]
	if !ok || rec.Status != domain.RestoreRunning {
		return domain.DatabaseRestore{}, store.ErrNotFound
	}
	now := time.Now()
	rec.Status, rec.Detail, rec.Step, rec.FinishedAt = status, detail, "", &now
	f.restores[id] = rec
	return rec, nil
}

func (f *fakeStore) SetDatabaseStatus(_ context.Context, id, status, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dbStatuses == nil {
		f.dbStatuses = map[string]string{}
	}
	f.dbStatuses[id] = status
	_ = detail
	return nil
}

func (f *fakeStore) GetBackupRecord(_ context.Context, id string) (domain.BackupRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.records[id]
	if !ok {
		return domain.BackupRecord{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) UpdateBackupRecord(_ context.Context, id, objectKey string, sizeBytes int64, status, detail string, finishedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.records[id]
	if !ok {
		return store.ErrNotFound
	}
	r.ObjectKey, r.SizeBytes, r.Status, r.Detail, r.FinishedAt = objectKey, sizeBytes, status, detail, finishedAt
	f.records[id] = r
	return nil
}

func (f *fakeStore) SetDatabaseBackupLastRun(_ context.Context, id string, lastRunAt *time.Time, lastStatus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.schedules[id]
	if !ok {
		return store.ErrNotFound
	}
	b.LastRunAt, b.LastStatus = lastRunAt, lastStatus
	f.schedules[id] = b
	return nil
}

func (f *fakeStore) ListBackupRecords(_ context.Context, backupID string) ([]domain.BackupRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.BackupRecord
	for _, r := range f.records {
		if r.DatabaseBackupID == backupID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) ListBackupRecordsBeyondRetention(_ context.Context, backupID string, keep int32) ([]store.PrunableBackupRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var succeeded []domain.BackupRecord
	for _, r := range f.records {
		if r.DatabaseBackupID == backupID && r.Status == domain.BackupSucceeded && r.ObjectKey != "" {
			succeeded = append(succeeded, r)
		}
	}
	sort.Slice(succeeded, func(i, j int) bool { return succeeded[i].CreatedAt.After(succeeded[j].CreatedAt) })
	var out []store.PrunableBackupRecord
	for i, r := range succeeded {
		if i < int(keep) {
			continue
		}
		out = append(out, store.PrunableBackupRecord{ID: r.ID, ObjectKey: r.ObjectKey})
	}
	return out, nil
}

func (f *fakeStore) DeleteBackupRecordsByObjectKeys(_ context.Context, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	for id, r := range f.records {
		if set[r.ObjectKey] {
			delete(f.records, id)
		}
	}
	return nil
}

func (f *fakeStore) ListEnabledScheduledTasksByApp(_ context.Context, appID string) ([]domain.ScheduledTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.ScheduledTask
	for _, t := range f.scheduledTasks {
		if t.ApplicationID == appID && t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) GetScheduledTask(_ context.Context, id string) (domain.ScheduledTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.scheduledTasks[id]
	if !ok {
		return domain.ScheduledTask{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) CreateTaskRun(_ context.Context, r domain.ScheduledTaskRun) (domain.ScheduledTaskRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taskRuns = append(f.taskRuns, r)
	return r, nil
}

func (f *fakeStore) DeleteOldTaskRuns(_ context.Context, _ string, _ int32) error { return nil }

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

// An image-source application (deploy from container image, feature-matrix V1)
// skips build and distribute entirely: Deploy records the reference as an
// already-built revision and goes straight to rollout with a pull-marked spec —
// the same path rollback takes.
func TestDeployImageSourceGoesStraightToRollout(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghost:5"}
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if dep.Status != domain.DeployRollingOut {
		t.Fatalf("status = %s, want rolling_out (no build stage)", dep.Status)
	}
	p, ok := fb.last()
	if !ok || p.subject != subjects.Rollout("srv_1") {
		t.Fatalf("work = %+v, want rollout on srv_1", p)
	}
	var rw agentv1.RolloutWork
	if err := proto.Unmarshal(p.data, &rw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rw.GetSpec().GetImage() != "ghost:5" || !rw.GetSpec().GetPull() {
		t.Fatalf("spec = %+v, want image ghost:5 with pull set", rw.GetSpec())
	}
	if rev := fs.revisions[dep.RevisionID]; rev.Image != "ghost:5" || rev.SourceCommit != "ghost:5" {
		t.Fatalf("revision = %+v, want the reference recorded up front", rev)
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

// recordingNotifier captures the terminal-outcome calls the scheduler makes.
type recordingNotifier struct {
	deploys []domain.Deployment
	// health records app.crashed / app.recovered as "<event>/<detail>"
	// (deployment-control.md §5).
	health []string
}

func (r *recordingNotifier) NotifyDeploy(_ context.Context, _ domain.Application, dep domain.Deployment) {
	r.deploys = append(r.deploys, dep)
}
func (r *recordingNotifier) NotifyBackup(_ context.Context, _ domain.Database, _ domain.BackupRecord) {
}
func (r *recordingNotifier) NotifyAppHealth(_ context.Context, _ domain.Application, eventType, detail string) {
	r.health = append(r.health, eventType+"/"+detail)
}

// The notifier fires once, with the succeeded deployment, at the observed-
// running completion — never before (notifications.md §5).
func TestNotifierFiresOnDeploySuccess(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	if len(rec.deploys) != 0 {
		t.Fatalf("notifier fired before completion: %+v", rec.deploys)
	}
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId: "app_1", RevisionId: dep.RevisionID, State: domain.AppRunning,
	})
	if len(rec.deploys) != 1 || rec.deploys[0].Status != domain.DeploySucceeded {
		t.Fatalf("notifier calls = %+v, want one succeeded", rec.deploys)
	}
}

// A build failure notifies with the failed deployment (both outcomes route
// through one NotifyDeploy call, notifications.md §5).
func TestNotifierFiresOnDeployFailure(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	rec := &recordingNotifier{}
	s.AddSink(rec)

	dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD, Outcome: agentv1.DeployEvent_OUTCOME_FAILED, Detail: "boom",
	})
	if len(rec.deploys) != 1 || rec.deploys[0].Status != domain.DeployFailed {
		t.Fatalf("notifier calls = %+v, want one failed", rec.deploys)
	}
}

// A scheduled task run reported by the app's own server is recorded; one from
// another server is rejected (threat-model §5.2, scheduled-tasks.md §3).
func TestHandleScheduledTaskRunRecordsAndGuardsServer(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.scheduledTasks["sch_1"] = domain.ScheduledTask{ID: "sch_1", ApplicationID: "app_1", Enabled: true}
	s := newScheduler(fs, fb)

	// A different server may not report this app's task.
	s.HandleScheduledTaskRun(context.Background(), "srv_evil", &agentv1.ScheduledTaskRun{TaskId: "sch_1", RunId: "str_1"})
	if len(fs.taskRuns) != 0 {
		t.Fatalf("a foreign server's run was recorded: %+v", fs.taskRuns)
	}

	// The owning server's report is recorded with the right status.
	s.HandleScheduledTaskRun(context.Background(), "srv_1", &agentv1.ScheduledTaskRun{
		TaskId: "sch_1", RunId: "str_2", Failed: true, ExitCode: 2, OutputTail: "boom",
	})
	if len(fs.taskRuns) != 1 {
		t.Fatalf("run not recorded: %+v", fs.taskRuns)
	}
	if r := fs.taskRuns[0]; r.Status != domain.TaskRunFailed || r.ExitCode == nil || *r.ExitCode != 2 {
		t.Fatalf("run = %+v, want failed exit 2", r)
	}
}

// ConvergeApp publishes a ConvergeWork carrying the app's spec (including its
// scheduled tasks) so a task change propagates without a redeploy.
func TestConvergeAppPublishesSpecWithTasks(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	// A built desired revision so there is a container to converge.
	rev := domain.Revision{ID: "rev_1", ApplicationID: "app_1", Image: "img", ConfigSnapshot: []byte("{}")}
	fs.revisions["rev_1"] = rev
	app.DesiredRevisionID = &rev.ID
	fs.apps["app_1"] = app
	fs.scheduledTasks["sch_1"] = domain.ScheduledTask{ID: "sch_1", ApplicationID: "app_1", Schedule: "* * * * *", Command: []string{"true"}, Enabled: true}
	s := newScheduler(fs, fb)

	if err := s.ConvergeApp(context.Background(), "app_1"); err != nil {
		t.Fatalf("ConvergeApp: %v", err)
	}
	var found *agentv1.ConvergeWork
	for _, p := range fb.work {
		if p.subject == subjects.Converge("srv_1") {
			var cw agentv1.ConvergeWork
			if err := proto.Unmarshal(p.data, &cw); err != nil {
				t.Fatalf("unmarshal converge: %v", err)
			}
			found = &cw
		}
	}
	if found == nil || found.GetSpec().GetAppId() != "app_1" {
		t.Fatalf("no converge work published for app_1; got %+v", found)
	}
	if tasks := found.GetSpec().GetScheduledTasks(); len(tasks) != 1 || tasks[0].GetId() != "sch_1" {
		t.Fatalf("converge spec tasks = %+v, want sch_1 carried", tasks)
	}
}

// ConvergeApp is a no-op (no work) when the app has no built revision — there is
// no container to run tasks in yet.
func TestConvergeAppNoRevisionIsNoOp(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)
	if err := s.ConvergeApp(context.Background(), "app_1"); err != nil {
		t.Fatalf("ConvergeApp: %v", err)
	}
	for _, p := range fb.work {
		if p.subject == subjects.Converge("srv_1") {
			t.Fatal("converge published for an app with no built revision")
		}
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

// start() overrides the app's status with 'deploying'; fail() has to take that
// back. A build failure never touches a container, so the agent has nothing new
// to report and the override would otherwise stick forever — the Deployments
// tab saying FAILED while Overview pulsed DEPLOYING indefinitely
// (ui-principles §10).
func TestFailedDeployClearsDeployingStatus(t *testing.T) {
	t.Run("first deploy ever: nothing is serving, so error", func(t *testing.T) {
		fs, fb := newFakeStore(), &fakeBus{}
		fs.addApp("app_1", "srv_1")
		s := newScheduler(fs, fb)

		dep, _ := s.Deploy(context.Background(), "app_1", "manual", "")
		if got := fs.apps["app_1"].Status; got != domain.AppDeploying {
			t.Fatalf("status while building = %q, want deploying", got)
		}
		s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
			DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD,
			Outcome: agentv1.DeployEvent_OUTCOME_FAILED, Detail: "no Dockerfile at ./Dockerfile",
		})

		app := fs.apps["app_1"]
		if app.Status != domain.AppError {
			t.Errorf("status after failure = %q, want error", app.Status)
		}
		if !strings.Contains(app.StatusDetail, "Dockerfile") {
			t.Errorf("detail should carry the reason, got %q", app.StatusDetail)
		}
	})

	t.Run("a revision was serving: it still is, so running", func(t *testing.T) {
		fs, fb := newFakeStore(), &fakeBus{}
		fs.addApp("app_2", "srv_1")
		// Zero-downtime: a failed build never disturbs the live container.
		a := fs.apps["app_2"]
		a.ObservedRevisionID = "rev_live"
		fs.apps["app_2"] = a
		s := newScheduler(fs, fb)

		dep, _ := s.Deploy(context.Background(), "app_2", "manual", "")
		s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
			DeploymentId: dep.ID, Stage: agentv1.DeployEvent_STAGE_BUILD,
			Outcome: agentv1.DeployEvent_OUTCOME_FAILED, Detail: "compile error",
		})

		app := fs.apps["app_2"]
		if app.Status != domain.AppRunning {
			t.Errorf("status after failure = %q, want running (previous revision still serves)", app.Status)
		}
		if !strings.Contains(app.StatusDetail, "still serving") {
			t.Errorf("detail should say the old revision still serves, got %q", app.StatusDetail)
		}
	})
}

// The agent observes which image a registry-sourced revision actually runs; the
// plane pins the revision to that digest. Without this, rolling back to a
// revision created from a mutable tag would re-pull the tag and start whatever
// it points at now while reporting the old revision restored.
func TestObservedDigestPinsRevision(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghost:5"}
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got := fs.revisions[dep.RevisionID].Image; got != "ghost:5" {
		t.Fatalf("revision image = %q, want the configured tag before observation", got)
	}

	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId:         "app_1",
		RevisionId:    dep.RevisionID,
		State:         domain.AppRunning,
		ResolvedImage: "ghost@sha256:deadbeef",
	})

	if got := fs.revisions[dep.RevisionID].Image; got != "ghost@sha256:deadbeef" {
		t.Fatalf("revision image = %q, want it pinned to the observed digest", got)
	}
}

// An agent that reports no digest (a built image, or a lookup failure) leaves
// the revision exactly as it was.
func TestNoDigestLeavesRevisionUnchanged(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghost:5"}
	fs.apps["app_1"] = app
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId: "app_1", RevisionId: dep.RevisionID, State: domain.AppRunning,
	})
	if got := fs.revisions[dep.RevisionID].Image; got != "ghost:5" {
		t.Fatalf("revision image = %q, want it untouched", got)
	}
}

// A compromised agent must not be able to pin a revision belonging to another
// application: it could otherwise substitute an attacker-chosen image that a
// later rollback would pull and run — on a different server.
func TestDigestPinRejectsForeignRevision(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	victim := fs.addApp("app_victim", "srv_2")
	victim.Source = domain.AppSource{Kind: "image", Image: "trusted:1"}
	fs.apps["app_victim"] = victim
	attacker := fs.addApp("app_attacker", "srv_1")
	attacker.Source = domain.AppSource{Kind: "image", Image: "ghost:5"}
	fs.apps["app_attacker"] = attacker
	s := newScheduler(fs, fb)

	victimDep, err := s.Deploy(context.Background(), "app_victim", "manual", "")
	if err != nil {
		t.Fatalf("victim Deploy: %v", err)
	}

	// srv_1 legitimately owns app_attacker, but names the victim's revision.
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId:         "app_attacker",
		RevisionId:    victimDep.RevisionID,
		State:         domain.AppRunning,
		ResolvedImage: "evil@sha256:beef",
	})

	if got := fs.revisions[victimDep.RevisionID].Image; got != "trusted:1" {
		t.Fatalf("victim revision image = %q — a foreign agent overwrote it", got)
	}
}

// ─── managed database specs ─────────────────────────────────────────────────

func addDatabase(fs *fakeStore, id string, engine domain.DbEngine, initialDB string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	revID := id + "_rev1"
	fs.dbRevs[revID] = domain.DatabaseRevision{ID: revID, DatabaseID: id}
	fs.dbs[id] = domain.Database{
		ID:                id,
		EnvironmentID:     "env_1",
		Name:              id,
		Engine:            engine,
		Version:           "16",
		ServerID:          "srv_1",
		VolumeName:        "cypher-db-" + id,
		DataPath:          "/data",
		Network:           "cypher-env_1",
		RootUser:          "root",
		RequirePassword:   true,
		RootPasswordCT:    []byte("ct"),
		RootPasswordNonce: []byte("nonce"),
		InitialDatabase:   initialDB,
		DesiredRevisionID: &revID,
		DesiredState:      domain.DbDesiredRunning,
		Status:            domain.DbStopped,
	}
}

func provisionSpec(t *testing.T, fb *fakeBus) *agentv1.DbSpec {
	t.Helper()
	p, ok := fb.last()
	if !ok {
		t.Fatal("no work published")
	}
	var pw agentv1.DbProvisionWork
	if err := proto.Unmarshal(p.data, &pw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return pw.GetSpec()
}

// The engine image creates the application database from this variable while
// initializing an empty data directory (managed-databases.md §2). Each engine
// reads a different one, and Redis/Valkey read none.
func TestDbSpecCarriesTheInitialDatabase(t *testing.T) {
	cases := []struct {
		engine domain.DbEngine
		key    string
	}{
		{domain.EnginePostgreSQL, "POSTGRES_DB"},
		{domain.EngineMySQL, "MYSQL_DATABASE"},
		{domain.EngineMariaDB, "MARIADB_DATABASE"},
		{domain.EngineMongoDB, "MONGO_INITDB_DATABASE"},
	}
	for _, c := range cases {
		t.Run(string(c.engine), func(t *testing.T) {
			fs, fb := newFakeStore(), &fakeBus{}
			addDatabase(fs, "db_1", c.engine, "appdb")
			s := newScheduler(fs, fb)

			if err := s.ReconcileDatabase(context.Background(), "db_1"); err != nil {
				t.Fatalf("ReconcileDatabase: %v", err)
			}
			if got := provisionSpec(t, fb).GetEnv()[c.key]; got != "appdb" {
				t.Fatalf("%s = %q, want appdb", c.key, got)
			}
		})
	}

	// Redis has numbered databases, so there is no variable to set even if a
	// row somehow carried a name.
	fs, fb := newFakeStore(), &fakeBus{}
	addDatabase(fs, "db_2", domain.EngineRedis, "appdb")
	s := newScheduler(fs, fb)
	if err := s.ReconcileDatabase(context.Background(), "db_2"); err != nil {
		t.Fatalf("ReconcileDatabase: %v", err)
	}
	for k := range provisionSpec(t, fb).GetEnv() {
		if strings.Contains(k, "DATABASE") || strings.Contains(k, "_DB") {
			t.Fatalf("redis spec carries %q, want no database variable", k)
		}
	}
}

// A database created before the field existed keeps behaving exactly as it
// did: no variable, so the engine's own default applies and the container is
// not seen as drifted.
func TestDbSpecOmitsAnEmptyInitialDatabase(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	addDatabase(fs, "db_1", domain.EnginePostgreSQL, "")
	s := newScheduler(fs, fb)

	if err := s.ReconcileDatabase(context.Background(), "db_1"); err != nil {
		t.Fatalf("ReconcileDatabase: %v", err)
	}
	if _, present := provisionSpec(t, fb).GetEnv()["POSTGRES_DB"]; present {
		t.Fatal("POSTGRES_DB set for a database that asked for none")
	}
}

// The agent measures drift by comparing the spec it was told to converge to
// against the one the desired-state sync advertises. If the two were built
// separately, a field added to one and forgotten in the other would read as
// permanent drift and re-provision the container on every sync — so they are
// asserted byte-identical here, for every engine.
func TestProvisionWorkAndDesiredStateAgree(t *testing.T) {
	for _, engine := range []domain.DbEngine{
		domain.EnginePostgreSQL, domain.EngineMySQL, domain.EngineMariaDB,
		domain.EngineMongoDB, domain.EngineRedis, domain.EngineValkey,
	} {
		t.Run(string(engine), func(t *testing.T) {
			fs, fb := newFakeStore(), &fakeBus{}
			addDatabase(fs, "db_1", engine, "appdb")
			s := newScheduler(fs, fb)

			if err := s.ReconcileDatabase(context.Background(), "db_1"); err != nil {
				t.Fatalf("ReconcileDatabase: %v", err)
			}
			fromWork := provisionSpec(t, fb)

			data, err := s.DesiredStateFor(context.Background(), "srv_1")
			if err != nil {
				t.Fatalf("DesiredStateFor: %v", err)
			}
			var ds agentv1.DesiredState
			if err := proto.Unmarshal(data, &ds); err != nil {
				t.Fatalf("unmarshal desired state: %v", err)
			}
			if len(ds.GetDbSpecs()) != 1 {
				t.Fatalf("db specs = %d, want 1", len(ds.GetDbSpecs()))
			}
			if !proto.Equal(fromWork, ds.GetDbSpecs()[0]) {
				t.Fatalf("work spec and desired state disagree:\n work = %v\n sync = %v", fromWork, ds.GetDbSpecs()[0])
			}
		})
	}
}

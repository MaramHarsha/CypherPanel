package rest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/templates"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

type fakeAuthStore struct {
	user              domain.User
	sessions          map[string]domain.User     // key: string(tokenHash)
	tokens            map[string]domain.APIToken // token id → metadata
	byHash            map[string]string          // string(tokenHash) → token id
	totp              store.TOTPSecret
	recovery          [][]byte                      // unused recovery code-hashes
	avatars           map[string]domain.Avatar      // userID → profile photo
	emailChanges      map[string]domain.EmailChange // pending address moves
	emailChangeHashes map[string][]byte             // change id → token hash
}

// fakeBox is an identity SecretBox for handler tests.
type fakeBox struct{}

func (fakeBox) Seal(pt []byte) (ct, nonce []byte, err error) { return pt, []byte("n"), nil }
func (fakeBox) Open(ct, _ []byte) ([]byte, error)            { return ct, nil }

func (f *fakeAuthStore) SetTOTPSecret(_ context.Context, _ string, ct, nonce []byte) error {
	f.totp = store.TOTPSecret{CT: ct, Nonce: nonce, Enabled: false}
	return nil
}

func (f *fakeAuthStore) EnableTOTP(_ context.Context, uid string) error {
	f.totp.Enabled = true
	f.user.TOTPEnabled = true
	return nil
}

func (f *fakeAuthStore) DisableTOTP(_ context.Context, _ string) error {
	f.totp = store.TOTPSecret{}
	f.user.TOTPEnabled = false
	return nil
}

func (f *fakeAuthStore) GetTOTPSecret(_ context.Context, _ string) (store.TOTPSecret, error) {
	return f.totp, nil
}

func (f *fakeAuthStore) AddRecoveryCode(_ context.Context, _, _ string, codeHash []byte) error {
	f.recovery = append(f.recovery, codeHash)
	return nil
}

func (f *fakeAuthStore) ConsumeRecoveryCode(_ context.Context, _ string, codeHash []byte) (bool, error) {
	for i, h := range f.recovery {
		if string(h) == string(codeHash) {
			f.recovery = append(f.recovery[:i], f.recovery[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeAuthStore) CountUnusedRecoveryCodes(_ context.Context, _ string) (int, error) {
	return len(f.recovery), nil
}

func (f *fakeAuthStore) DeleteRecoveryCodes(_ context.Context, _ string) error {
	f.recovery = nil
	return nil
}

// DeleteExpiredSessions is the session purge (control-plane-hardening.md §7);
// handler tests never run it, so the fake reports that nothing was expired.
func (f *fakeAuthStore) DeleteExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeAuthStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	if email != f.user.Email {
		return domain.User{}, store.ErrNotFound
	}
	return f.user, nil
}

func (f *fakeAuthStore) SetUserAvatar(_ context.Context, userID, contentType string, data []byte, etag string) error {
	if f.avatars == nil {
		f.avatars = map[string]domain.Avatar{}
	}
	f.avatars[userID] = domain.Avatar{ContentType: contentType, Bytes: data, ETag: etag}
	return nil
}

func (f *fakeAuthStore) GetUserAvatar(_ context.Context, userID string) (domain.Avatar, error) {
	av, ok := f.avatars[userID]
	if !ok {
		return domain.Avatar{}, store.ErrNotFound
	}
	return av, nil
}

func (f *fakeAuthStore) DeleteUserAvatar(_ context.Context, userID string) error {
	delete(f.avatars, userID)
	return nil
}

func (f *fakeAuthStore) UpdateUserEmail(_ context.Context, userID, email string) (domain.User, error) {
	if userID != f.user.ID {
		return domain.User{}, store.ErrNotFound
	}
	f.user.Email = email
	return f.user, nil
}

func (f *fakeAuthStore) CreateEmailChange(_ context.Context, id, userID, newEmail string, tokenHash []byte, expiresAt time.Time) (domain.EmailChange, error) {
	if f.emailChanges == nil {
		f.emailChanges = map[string]domain.EmailChange{}
	}
	if f.emailChangeHashes == nil {
		f.emailChangeHashes = map[string][]byte{}
	}
	ec := domain.EmailChange{ID: id, UserID: userID, NewEmail: newEmail, ExpiresAt: expiresAt}
	f.emailChanges[id] = ec
	f.emailChangeHashes[id] = tokenHash
	return ec, nil
}

func (f *fakeAuthStore) EmailChangeTokenHash(_ context.Context, id string) (domain.EmailChange, []byte, error) {
	ec, ok := f.emailChanges[id]
	if !ok {
		return domain.EmailChange{}, nil, store.ErrNotFound
	}
	return ec, f.emailChangeHashes[id], nil
}

func (f *fakeAuthStore) ConsumeEmailChange(_ context.Context, id string) (domain.EmailChange, error) {
	ec, ok := f.emailChanges[id]
	if !ok || ec.ConsumedAt != nil {
		return domain.EmailChange{}, store.ErrNotFound
	}
	now := time.Now()
	ec.ConsumedAt = &now
	f.emailChanges[id] = ec
	return ec, nil
}

func (f *fakeAuthStore) GetUserByID(_ context.Context, id string) (domain.User, error) {
	if id != f.user.ID {
		return domain.User{}, store.ErrNotFound
	}
	return f.user, nil
}

func (f *fakeAuthStore) UpdateUserProfile(_ context.Context, userID, displayName, timezone string) (domain.User, error) {
	if userID != f.user.ID {
		return domain.User{}, store.ErrNotFound
	}
	f.user.DisplayName, f.user.Timezone = displayName, timezone
	return f.user, nil
}

func (f *fakeAuthStore) UpdateUserPassword(_ context.Context, userID, passwordHash string) error {
	if userID != f.user.ID {
		return store.ErrNotFound
	}
	f.user.PasswordHash = passwordHash
	return nil
}

func (f *fakeAuthStore) CreateSession(_ context.Context, _, _ string, tokenHash []byte, _ time.Time) error {
	f.sessions[string(tokenHash)] = f.user
	return nil
}

func (f *fakeAuthStore) UserForSessionToken(_ context.Context, tokenHash []byte) (domain.User, error) {
	u, ok := f.sessions[string(tokenHash)]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeAuthStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	delete(f.sessions, string(tokenHash))
	return nil
}

func (f *fakeAuthStore) CreateAPIToken(_ context.Context, id, userID, name string, abilities []domain.Ability, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error) {
	if f.tokens == nil {
		f.tokens, f.byHash = map[string]domain.APIToken{}, map[string]string{}
	}
	tok := domain.APIToken{ID: id, UserID: userID, Name: name, Abilities: abilities, ExpiresAt: expiresAt, CreatedAt: time.Now()}
	f.tokens[id] = tok
	f.byHash[string(tokenHash)] = id
	return tok, nil
}

func (f *fakeAuthStore) APITokenByHash(_ context.Context, tokenHash []byte) (domain.User, string, []domain.Ability, error) {
	id, ok := f.byHash[string(tokenHash)]
	if !ok {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	tok := f.tokens[id]
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now()) {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	if tok.UserID != f.user.ID {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	return f.user, tok.ID, tok.Abilities, nil
}

func (f *fakeAuthStore) SessionForToken(_ context.Context, tokenHash []byte) (domain.User, string, error) {
	u, ok := f.sessions[string(tokenHash)]
	if !ok {
		return domain.User{}, "", store.ErrNotFound
	}
	return u, "sess_" + string(tokenHash), nil
}

func (f *fakeAuthStore) ListSessionsByUser(_ context.Context, userID string) ([]domain.Session, error) {
	var out []domain.Session
	for hash, u := range f.sessions {
		if u.ID == userID {
			out = append(out, domain.Session{ID: "sess_" + hash, UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})
		}
	}
	return out, nil
}

func (f *fakeAuthStore) DeleteSessionForUser(_ context.Context, sessionID, userID string) (bool, error) {
	for hash, u := range f.sessions {
		if "sess_"+hash == sessionID && u.ID == userID {
			delete(f.sessions, hash)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeAuthStore) DeleteOtherSessionsForUser(_ context.Context, userID string, keepTokenHash []byte) (int64, error) {
	var n int64
	for hash, u := range f.sessions {
		if u.ID == userID && hash != string(keepTokenHash) {
			delete(f.sessions, hash)
			n++
		}
	}
	return n, nil
}

func (f *fakeAuthStore) TouchAPIToken(_ context.Context, _ []byte) error { return nil }

func (f *fakeAuthStore) ListAPITokensByUser(_ context.Context, userID string) ([]domain.APIToken, error) {
	var out []domain.APIToken
	for _, t := range f.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeAuthStore) GetAPIToken(_ context.Context, id string) (domain.APIToken, error) {
	t, ok := f.tokens[id]
	if !ok {
		return domain.APIToken{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeAuthStore) DeleteAPIToken(_ context.Context, id string) error {
	delete(f.tokens, id)
	return nil
}

type fakeServersStore struct {
	list  []domain.Server
	inUse map[string]bool // servers that still run applications (RESTRICT)
}

func (f *fakeServersStore) CreateServerWithToken(_ context.Context, serverID, name, _ string, _ []byte, _ time.Time) (domain.Server, error) {
	s := domain.Server{ID: serverID, Name: name, Status: domain.StatusUnknown, CreatedAt: time.Now()}
	f.list = append(f.list, s)
	return s, nil
}

func (f *fakeServersStore) ListServers(context.Context) ([]domain.Server, error) { return f.list, nil }

func (f *fakeServersStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	for _, s := range f.list {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Server{}, store.ErrNotFound
}

func (f *fakeServersStore) DeleteServer(_ context.Context, id string) error {
	if f.inUse[id] {
		return store.ErrInUse
	}
	return nil
}

type noopAgentBus struct{}

func (noopAgentBus) DisconnectAgent(string) error                     { return nil }
func (noopAgentBus) EnsureWorkConsumer(context.Context, string) error { return nil }
func (noopAgentBus) DeleteWorkConsumer(context.Context, string) error { return nil }

// fakeTeams implements TeamService: the panel-owner bypass mirrors the real
// service; explicit memberships (userID -> projectID/teamID -> role) let authz
// tests model cross-team boundaries. The default harness user has panel role
// "owner", so pre-teams tests keep their full-access behavior.
type fakeTeams struct {
	projectRoles map[string]map[string]string // userID -> projectID -> role
	teamRoles    map[string]map[string]string // userID -> teamID -> role
	teams        []domain.TeamWithRole        // ListFor result for non-owners
}

func newFakeTeams() *fakeTeams {
	return &fakeTeams{
		projectRoles: map[string]map[string]string{},
		teamRoles:    map[string]map[string]string{},
		teams:        []domain.TeamWithRole{{Team: domain.Team{ID: "tm_default", Name: "default"}, Role: "owner"}},
	}
}

func (f *fakeTeams) RoleForProject(_ context.Context, actor domain.User, projectID string) (string, error) {
	if actor.Role == domain.RoleOwner {
		return domain.RoleOwner, nil
	}
	return f.projectRoles[actor.ID][projectID], nil
}

func (f *fakeTeams) RoleInTeam(_ context.Context, actor domain.User, teamID string) (string, error) {
	if actor.Role == domain.RoleOwner {
		return domain.RoleOwner, nil
	}
	return f.teamRoles[actor.ID][teamID], nil
}

func (f *fakeTeams) Create(_ context.Context, name string, _ domain.User) (domain.Team, error) {
	return domain.Team{ID: "tm_new", Name: name}, nil
}
func (f *fakeTeams) Get(_ context.Context, id string) (domain.Team, error) {
	return domain.Team{ID: id, Name: "default"}, nil
}
func (f *fakeTeams) ListFor(_ context.Context, _ domain.User) ([]domain.TeamWithRole, error) {
	return f.teams, nil
}
func (f *fakeTeams) Rename(_ context.Context, id, name string) (domain.Team, error) {
	return domain.Team{ID: id, Name: name}, nil
}
func (f *fakeTeams) Delete(context.Context, string) error { return nil }
func (f *fakeTeams) Members(context.Context, string) ([]domain.TeamMember, error) {
	return nil, nil
}
func (f *fakeTeams) AddMember(_ context.Context, teamID, email, role, _ string) (domain.TeamMember, error) {
	return domain.TeamMember{TeamID: teamID, Email: email, Role: role}, nil
}
func (f *fakeTeams) ChangeMemberRole(_ context.Context, teamID, userID, role, _ string) (domain.TeamMember, error) {
	return domain.TeamMember{TeamID: teamID, UserID: userID, Role: role}, nil
}
func (f *fakeTeams) RemoveMember(context.Context, string, string, string) error { return nil }
func (f *fakeTeams) CreateUser(_ context.Context, email, _, role, _ string) (domain.User, error) {
	return domain.User{ID: "usr_new", Email: email, Role: role}, nil
}
func (f *fakeTeams) ListUsers(context.Context) ([]domain.User, error) { return nil, nil }
func (f *fakeTeams) SetUserRole(_ context.Context, userID, role string, _ domain.User) (domain.User, error) {
	return domain.User{ID: userID, Role: role}, nil
}
func (f *fakeTeams) DeleteUser(context.Context, string, domain.User) error { return nil }

type fakeProjectsStore struct {
	projects map[string]domain.Project
	envs     map[string][]domain.Environment
}

func newFakeProjectsStore() *fakeProjectsStore {
	// Seed the project/environment the resource fixtures reference, so the
	// authz layer's env -> project resolution works for the seeded env_test
	// (the harness user is a panel owner, so role checks then pass).
	return &fakeProjectsStore{
		projects: map[string]domain.Project{
			"prj_test": {ID: "prj_test", Name: "test", TeamID: "tm_default"},
		},
		envs: map[string][]domain.Environment{
			"prj_test": {{ID: "env_test", ProjectID: "prj_test", Name: "production"}},
		},
	}
}

func (f *fakeProjectsStore) CreateProjectWithEnvironment(_ context.Context, pid, name, teamID, eid, ename string) (domain.Project, domain.Environment, error) {
	_ = teamID
	p := domain.Project{ID: pid, Name: name, CreatedAt: time.Now()}
	e := domain.Environment{ID: eid, ProjectID: pid, Name: ename, CreatedAt: time.Now()}
	f.projects[pid] = p
	f.envs[pid] = append(f.envs[pid], e)
	return p, e, nil
}

func (f *fakeProjectsStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeProjectsStore) ListProjects(context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProjectsStore) DeleteProject(_ context.Context, id string) error {
	delete(f.projects, id)
	delete(f.envs, id)
	return nil
}

func (f *fakeProjectsStore) ListProjectsByUser(context.Context, string) ([]domain.Project, error) {
	return nil, nil
}

func (f *fakeProjectsStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	for _, envs := range f.envs {
		for _, e := range envs {
			if e.ID == id {
				return e, nil
			}
		}
	}
	return domain.Environment{}, store.ErrNotFound
}

func (f *fakeProjectsStore) CreateEnvironment(_ context.Context, id, pid, name string) (domain.Environment, error) {
	for _, e := range f.envs[pid] {
		if e.Name == name {
			return domain.Environment{}, store.ErrConflict
		}
	}
	e := domain.Environment{ID: id, ProjectID: pid, Name: name, CreatedAt: time.Now()}
	f.envs[pid] = append(f.envs[pid], e)
	return e, nil
}

func (f *fakeProjectsStore) ListEnvironmentsByProject(_ context.Context, pid string) ([]domain.Environment, error) {
	return f.envs[pid], nil
}

type fakeAppsStore struct {
	envs    map[string]bool
	servers map[string]bool
	apps    map[string]domain.Application
	env     map[string][]domain.EnvVar
}

func newFakeAppsStore() *fakeAppsStore {
	return &fakeAppsStore{
		envs:    map[string]bool{"env_test": true},
		servers: map[string]bool{"srv_test": true},
		apps: map[string]domain.Application{
			// app_x anchors fakeDeploymentReader's dep_test so the authz
			// layer's deployment -> app -> env -> project chain resolves.
			"app_x": {ID: "app_x", EnvironmentID: "env_test", Name: "x",
				Runtime: domain.AppRuntime{ServerID: "srv_test", Port: 8080}},
		},
		env: map[string][]domain.EnvVar{},
	}
}

func (f *fakeAppsStore) CreateApplicationWithEnv(_ context.Context, a domain.Application, vars []domain.EnvVar) (domain.Application, error) {
	for _, other := range f.apps {
		if other.EnvironmentID == a.EnvironmentID && other.Name == a.Name {
			return domain.Application{}, store.ErrConflict
		}
	}
	f.apps[a.ID] = a
	f.env[a.ID] = vars
	return a, nil
}

func (f *fakeAppsStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeAppsStore) GetApplicationByWebhookID(_ context.Context, webhookID string) (domain.Application, error) {
	for _, a := range f.apps {
		if a.WebhookID == webhookID {
			return a, nil
		}
	}
	return domain.Application{}, store.ErrNotFound
}

func (f *fakeAppsStore) UpdateApplicationConfig(_ context.Context, a domain.Application) (domain.Application, error) {
	if _, ok := f.apps[a.ID]; !ok {
		return domain.Application{}, store.ErrNotFound
	}
	f.apps[a.ID] = a
	return a, nil
}

func (f *fakeAppsStore) ListApplicationsByEnvironment(_ context.Context, envID string) ([]domain.Application, error) {
	var out []domain.Application
	for _, a := range f.apps {
		if a.EnvironmentID == envID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeAppsStore) DeleteApplication(_ context.Context, id string) error {
	delete(f.apps, id)
	return nil
}

func (f *fakeAppsStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if !f.envs[id] {
		return domain.Environment{}, store.ErrNotFound
	}
	return domain.Environment{ID: id, ProjectID: "prj_test", Name: "production"}, nil
}

// ListSharedVariableKeysInScope backs the write-time {{shared.KEY}} check
// (shared-variables.md §3). Empty: no shared variable resolves in these tests,
// so any reference an env-var write carries is a 400.
func (f *fakeAppsStore) ListSharedVariableKeysInScope(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (f *fakeAppsStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	if !f.servers[id] {
		return domain.Server{}, store.ErrNotFound
	}
	return domain.Server{ID: id}, nil
}

func (f *fakeAppsStore) UpsertEnvVar(_ context.Context, appID string, v domain.EnvVar) error {
	f.env[appID] = append(f.env[appID], v)
	return nil
}

func (f *fakeAppsStore) ListEnvVars(_ context.Context, appID string) ([]domain.EnvVar, error) {
	return f.env[appID], nil
}

func (f *fakeAppsStore) DeleteEnvVar(_ context.Context, appID, key string) error {
	kept := f.env[appID][:0]
	for _, v := range f.env[appID] {
		if v.Key != key {
			kept = append(kept, v)
		}
	}
	f.env[appID] = kept
	return nil
}

type fakeDeployer struct {
	deploys   []string // "appID/trigger/ref"
	removed   []string // "serverID/appID"
	rollbacks []string
}

func (f *fakeDeployer) Deploy(_ context.Context, appID, trigger, ref string) (domain.Deployment, error) {
	f.deploys = append(f.deploys, appID+"/"+trigger+"/"+ref)
	return domain.Deployment{ID: "dep_test", ApplicationID: appID, RevisionID: "rev_test", Status: domain.DeployBuilding, Trigger: trigger, CreatedAt: time.Now()}, nil
}

func (f *fakeDeployer) Rollback(_ context.Context, deploymentID string) (domain.Deployment, error) {
	if deploymentID == "dep_unbuilt" {
		return domain.Deployment{}, scheduler.ErrRevisionNotBuilt
	}
	if deploymentID != "dep_test" {
		return domain.Deployment{}, store.ErrNotFound
	}
	f.rollbacks = append(f.rollbacks, deploymentID)
	return domain.Deployment{ID: "dep_rb", ApplicationID: "app_x", RevisionID: "rev_test", Status: domain.DeployRollingOut, Trigger: "rollback", CreatedAt: time.Now()}, nil
}

func (f *fakeDeployer) RemoveApp(_ context.Context, serverID, appID string) error {
	f.removed = append(f.removed, serverID+"/"+appID)
	return nil
}

type fakeDeploymentReader struct{}

func (fakeDeploymentReader) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	// dep_unbuilt exists (so authz resolution succeeds) but its revision was
	// never built — the deployer answers its rollback with 409.
	if id != "dep_test" && id != "dep_unbuilt" {
		return domain.Deployment{}, store.ErrNotFound
	}
	return domain.Deployment{ID: id, ApplicationID: "app_x", RevisionID: "rev_test", Status: domain.DeploySucceeded, Trigger: "manual", CreatedAt: time.Now()}, nil
}

func (fakeDeploymentReader) ListDeploymentsByApplication(_ context.Context, appID string, _ int32) ([]domain.Deployment, error) {
	return []domain.Deployment{{ID: "dep_test", ApplicationID: appID, RevisionID: "rev_test", Status: domain.DeploySucceeded, CreatedAt: time.Now()}}, nil
}

// fakeLogs replays canned lines per subject and records stop calls.
type fakeLogs struct {
	mu      sync.Mutex
	lines   map[string][]string
	stopped int
	// status pushes test status observations to the /events subscriber.
	status func(subject string, data []byte)
}

func newFakeLogs() *fakeLogs { return &fakeLogs{lines: map[string][]string{}} }

func (f *fakeLogs) SubscribeStatus(_ context.Context, handle func(subject string, data []byte)) (func(), error) {
	f.mu.Lock()
	f.status = handle
	f.mu.Unlock()
	return func() {}, nil
}

// emitStatus feeds one observation to the live /events subscriber, if any.
func (f *fakeLogs) emitStatus(subject string) {
	f.mu.Lock()
	h := f.status
	f.mu.Unlock()
	if h != nil {
		h(subject, nil)
	}
}

func (f *fakeLogs) SubscribeLogs(_ context.Context, subject string, handle func(data []byte)) (func(), error) {
	return f.subscribe(subject, handle)
}

func (f *fakeLogs) SubscribeRuntimeLogs(_ context.Context, subject string, handle func(data []byte)) (func(), error) {
	return f.subscribe(subject, handle)
}

func (f *fakeLogs) subscribe(subject string, handle func(data []byte)) (func(), error) {
	f.mu.Lock()
	replay := append([]string(nil), f.lines[subject]...)
	f.mu.Unlock()
	go func() {
		for _, l := range replay {
			handle([]byte(l))
		}
	}()
	return func() {
		f.mu.Lock()
		f.stopped++
		f.mu.Unlock()
	}, nil
}

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	key := make([]byte, secret.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	box, err := secret.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// fakeDeployKeysStore is an in-memory deploykeys.Store; marking an id in
// inUse simulates the applications FK RESTRICT on delete.
type fakeDeployKeysStore struct {
	mu   sync.Mutex
	keys map[string]domain.DeployKey
	// inUse maps a key id to the applications referencing it — what the 409
	// names (deploy-key-private-repos.md §3).
	inUse map[string][]domain.ApplicationRef
}

func newFakeDeployKeysStore() *fakeDeployKeysStore {
	return &fakeDeployKeysStore{keys: map[string]domain.DeployKey{}, inUse: map[string][]domain.ApplicationRef{}}
}

func (f *fakeDeployKeysStore) ListApplicationsByDeployKey(_ context.Context, keyID string) ([]domain.ApplicationRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inUse[keyID], nil
}

func (f *fakeDeployKeysStore) CreateDeployKey(_ context.Context, dk domain.DeployKey) (domain.DeployKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[dk.ID] = dk
	return dk, nil
}

func (f *fakeDeployKeysStore) GetDeployKey(_ context.Context, id string) (domain.DeployKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dk, ok := f.keys[id]
	if !ok {
		return domain.DeployKey{}, store.ErrNotFound
	}
	return dk, nil
}

func (f *fakeDeployKeysStore) ListDeployKeys(context.Context) ([]domain.DeployKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.DeployKey, 0, len(f.keys))
	for _, dk := range f.keys {
		out = append(out, dk)
	}
	return out, nil
}

func (f *fakeDeployKeysStore) DeleteDeployKey(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inUse[id]) > 0 {
		return store.ErrInUse
	}
	delete(f.keys, id)
	return nil
}

// sealedPrivateKey returns the ciphertext the store holds for a key, so a test
// can assert that value never reaches a log or a response.
func (f *fakeDeployKeysStore) sealedPrivateKey(id string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dk, ok := f.keys[id]
	return dk.PrivateKeyCT, ok
}

func (f *fakeDeployKeysStore) markInUse(id string, apps ...domain.ApplicationRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(apps) == 0 {
		apps = []domain.ApplicationRef{{ID: "app_blocker", Name: "blocker"}}
	}
	f.inUse[id] = apps
}

// ─── harness ────────────────────────────────────────────────────────────────

const (
	testEmail    = "owner@example.com"
	testPassword = "correct-horse-battery"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts, _ := newTestServerWithStores(t)
	return ts
}

func newTestServerWithStores(t *testing.T) (*httptest.Server, *fakeServersStore) {
	ts, srv, _, _ := newTestServerFull(t)
	return ts, srv
}

func newTestServerFull(t *testing.T) (*httptest.Server, *fakeServersStore, *fakeLogs, *fakeDeployKeysStore) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: "owner"},
		sessions: map[string]domain.User{},
	}
	srvStore := &fakeServersStore{inUse: map[string]bool{}}
	logs := newFakeLogs()
	dkStore := newFakeDeployKeysStore()
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dbStore := newFakeDatabasesStore()
	dbReconciler := &fakeDbReconciler{}
	dbSvc := databases.NewService(dbStore, box, dbReconciler)

	appSvc := applications.NewService(newFakeAppsStore(), box)
	deployer := &fakeDeployer{}
	templateSvc, err := templates.New(appSvc, dbSvc, deployer, log)
	if err != nil {
		t.Fatalf("templates.New: %v", err)
	}
	api := New(Deps{
		Auth:         auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Servers:      servers.NewService(srvStore, noopAgentBus{}, 15*time.Minute, log),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: appSvc,
		DeployKeys:   deploykeys.NewService(dkStore, box),
		Databases:    dbSvc,
		Templates:    templateSvc,
		Teams:        newFakeTeams(),
		Scheduler:    deployer,
		Deployments:  fakeDeploymentReader{},
		Opener:       box,
		Logs:         logs,
		Pinger:       okPinger{},
		CACertPEM:    []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		EnrollAddr:   "localhost:8443",
		NATSURL:      "tls://localhost:4222",
		ConsoleURL:   "http://localhost:8080",
		Log:          log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, srvStore, logs, dkStore
}

func doJSON(t *testing.T, method, url, token, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

func login(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`)
	if status != http.StatusOK {
		t.Fatalf("login: status %d body %s", status, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &lr); err != nil || lr.Token == "" {
		t.Fatalf("login response %s: %v", body, err)
	}
	return lr.Token
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestProtectedRoutesRequireAuth(t *testing.T) {
	ts := newTestServer(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/servers"},
		{"POST", "/api/v1/servers"},
		{"GET", "/api/v1/servers/srv_x"},
		{"DELETE", "/api/v1/servers/srv_x"},
		{"GET", "/api/v1/auth/me"},
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/projects"},
		{"POST", "/api/v1/projects"},
		{"GET", "/api/v1/projects/prj_x"},
		{"DELETE", "/api/v1/projects/prj_x"},
		{"GET", "/api/v1/projects/prj_x/environments"},
		{"POST", "/api/v1/projects/prj_x/environments"},
		{"POST", "/api/v1/environments/env_x/applications"},
		{"GET", "/api/v1/environments/env_x/applications"},
		{"GET", "/api/v1/applications/app_x"},
		{"GET", "/api/v1/templates"},
		{"GET", "/api/v1/templates/n8n"},
		{"POST", "/api/v1/templates/n8n/install"},
		{"DELETE", "/api/v1/applications/app_x"},
		{"GET", "/api/v1/applications/app_x/env"},
		{"PUT", "/api/v1/applications/app_x/env/KEY"},
		{"DELETE", "/api/v1/applications/app_x/env/KEY"},
	} {
		status, _, _ := doJSON(t, route.method, ts.URL+route.path, "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s without token: status %d, want 401", route.method, route.path, status)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ts := newTestServer(t)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"wrong"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", status)
	}
	// The message must not reveal whether the email exists.
	if !strings.Contains(string(body), "invalid email or password") {
		t.Errorf("unexpected error body: %s", body)
	}
}

func TestServerLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create: join instructions appear exactly once, with the raw token.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"web-1"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, body)
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
		Join struct {
			Token          string `json:"token"`
			CAFingerprint  string `json:"ca_fingerprint"`
			Command        string `json:"command"`
			InstallCommand string `json:"install_command"`
		} `json:"join"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.Join.Token == "" || !strings.Contains(created.Join.Command, created.Join.Token) {
		t.Errorf("join instructions incomplete: %+v", created.Join)
	}
	// The curl|sh line must carry everything the installer needs: script URL,
	// token, and the CA fingerprint that guards the CA fetch.
	for _, part := range []string{"/install/agent.sh", created.Join.Token, created.Join.CAFingerprint, "CYPHER_PLANE_HTTP=http://localhost:8080"} {
		if !strings.Contains(created.Join.InstallCommand, part) {
			t.Errorf("install_command missing %q: %s", part, created.Join.InstallCommand)
		}
	}
	if len(created.Join.CAFingerprint) != 64 {
		t.Errorf("ca_fingerprint is not sha256 hex: %q", created.Join.CAFingerprint)
	}

	// List includes it; get returns it; unknown id is 404.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/servers", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), created.Server.ID) {
		t.Errorf("list: status %d body %s", status, body)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusOK {
		t.Errorf("get: status %d", status)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/servers/srv_missing", token, "")
	if status != http.StatusNotFound {
		t.Errorf("get missing: status %d, want 404", status)
	}

	// Invalid name is a 400, not a 500.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":""}`)
	if status != http.StatusBadRequest {
		t.Errorf("create empty name: status %d, want 400", status)
	}

	// Delete is a 204.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/logout", token, ""); status != http.StatusNoContent {
		t.Fatalf("logout: status %d", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/auth/me", token, ""); status != http.StatusUnauthorized {
		t.Errorf("me after logout: status %d, want 401", status)
	}
}

// TestOpenAPISpecServedAndCoversRoutes: the spec ships with the binary
// (ENGINEERING rule 19) and must document EVERY route the mux serves. The route
// list is derived from rest.go itself, so adding a handler without a spec entry
// fails this test — the drift-catcher for rule 19.
func TestOpenAPISpecServedAndCoversRoutes(t *testing.T) {
	ts := newTestServer(t)
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/openapi.yaml", "", "")
	if status != http.StatusOK {
		t.Fatalf("openapi: status %d", status)
	}
	spec := string(body)
	if !strings.Contains(spec, "openapi: 3.1.0") {
		t.Error("spec is not OpenAPI 3.1")
	}

	for _, path := range routePathsFromSource(t) {
		if !strings.Contains(spec, path+":") {
			t.Errorf("openapi.yaml does not document %s (ENGINEERING rule 19)", path)
		}
	}
}

// routePathsFromSource extracts the distinct URL path templates registered in
// rest.go's mux (the "METHOD /path" HandleFunc patterns), minus the SPA catch-
// all which is not an API contract entry.
func routePathsFromSource(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("rest.go")
	if err != nil {
		t.Fatalf("reading rest.go: %v", err)
	}
	re := regexp.MustCompile(`"(?:GET|POST|PATCH|PUT|DELETE) (/[^"]*)"`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		p := m[1]
		if p == "/" || seen[p] { // SPA/console catch-all is not a spec path
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) < 40 { // sanity: we have 80+ handlers; a near-empty match is a regex bug
		t.Fatalf("extracted only %d routes from rest.go — extraction is broken", len(out))
	}
	return out
}

// EVERY authenticated route can answer 403, and the spec has to say so — a
// generated client that does not model a response the endpoint actually
// produces cannot handle it (ENGINEERING rule 19).
//
// The reach is wider than role checks: the ability middleware refuses any
// request outside an API token's abilities, so a plain listing route returns
// 403 to a write-only token just as a project-scoped mutation does. Scoping
// this to role-guarded handlers (as an earlier version did) missed exactly
// those. Deriving the set from the router keeps it honest as routes are added.
func TestForbiddenResponsesAreDocumented(t *testing.T) {
	src, err := os.ReadFile("rest.go")
	if err != nil {
		t.Fatalf("reading rest.go: %v", err)
	}
	spec, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("reading openapi.yaml: %v", err)
	}

	re := regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|PATCH|PUT|DELETE) ([^"]+)",\s*a\.(?:authed|sessionOnly)\(`)
	checked := 0
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		method, path := m[1], m[2]
		checked++
		if !operationDocuments(string(spec), path, strings.ToLower(method), "403") {
			t.Errorf("%s %s is authenticated and can return 403, but openapi.yaml does not document it", method, path)
		}
	}
	if checked < 80 {
		t.Fatalf("only %d authenticated routes found — the extraction is broken", checked)
	}
}

// operationDocuments reports whether the spec's path/method block lists status.
// A deliberately simple scan: the block runs from the method key to the next
// line at the same or shallower indentation.
func operationDocuments(spec, path, method, status string) bool {
	pathIdx := strings.Index(spec, "\n  "+path+":\n")
	if pathIdx < 0 {
		return false
	}
	rest := spec[pathIdx+1:]
	if next := regexp.MustCompile(`\n  /`).FindStringIndex(rest[1:]); next != nil {
		rest = rest[:next[1]]
	}
	mIdx := strings.Index(rest, "\n    "+method+":\n")
	if mIdx < 0 {
		return false
	}
	block := rest[mIdx+1:]
	if next := regexp.MustCompile(`\n    \w+:\n`).FindStringIndex(block[1:]); next != nil {
		block = block[:next[0]+1]
	}
	return strings.Contains(block, `"`+status+`":`)
}

// The request bodies the OpenAPI schema describes must actually be accepted.
// decodeJSON runs with DisallowUnknownFields, so any property the spec marks
// required but the request struct omits turns every conforming client's call
// into a 400 "invalid request body" — which is exactly what happened to
// AppBuild.kind: the spec required it, the generated TypeScript client sent
// it, and creating an application through the documented contract was
// impossible. This asserts the spec-shaped body round-trips on both create and
// patch.
func TestApplicationAcceptsSpecShapedBuild(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	body := `{"name":"specshape","source":{"kind":"github","repo":"acme/web","branch":"main"},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080,"replicas":1},` +
		`"route":{"domain":"spec.example.com","https":true,"path_prefix":"/"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create with spec-shaped build.kind: status %d body %s", status, resp)
	}
	var created struct {
		Application struct {
			ID    string `json:"id"`
			Build struct {
				Kind           string `json:"kind"`
				DockerfilePath string `json:"dockerfile_path"`
			} `json:"build"`
		} `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.Application.Build.Kind != "dockerfile" {
		t.Errorf("build.kind = %q, want dockerfile", created.Application.Build.Kind)
	}

	// A client that reads an application and PATCHes it back must not be
	// rejected for echoing the kind it was just served.
	patch := `{"build":{"kind":"dockerfile","dockerfile_path":"./ops/Dockerfile","context":"."}}`
	status, _, resp = doJSON(t, "PATCH", ts.URL+"/api/v1/applications/"+created.Application.ID, token, patch)
	if status != http.StatusOK {
		t.Fatalf("patch with spec-shaped build.kind: status %d body %s", status, resp)
	}

	// An unsupported kind is still a validation error, not a decode error.
	bad := `{"name":"nope","source":{"kind":"github","repo":"acme/x"},` +
		`"build":{"kind":"nixpacks","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"n.example.com"}}`
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, bad)
	if status != http.StatusBadRequest {
		t.Fatalf("create with unsupported build.kind: status %d body %s", status, resp)
	}
	if !bytes.Contains(resp, []byte("dockerfile")) {
		t.Errorf("unsupported kind should name the supported one, got %s", resp)
	}
}

func TestApplicationLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create under the seeded env_test, targeting the seeded srv_test.
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"},` +
		`"env_vars":{"DATABASE_URL":"postgres://secret"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, resp)
	}
	var created struct {
		Application struct {
			ID     string `json:"id"`
			Source struct {
				Branch string `json:"branch"`
			} `json:"source"`
			Build struct {
				DockerfilePath string `json:"dockerfile_path"`
			} `json:"build"`
		} `json:"application"`
		Webhook struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	// Defaults filled; webhook secret present once; no plaintext env leaked.
	if created.Application.Source.Branch != "main" || created.Application.Build.DockerfilePath != "./Dockerfile" {
		t.Errorf("defaults not applied: %s", resp)
	}
	if created.Webhook.Secret == "" || !strings.Contains(created.Webhook.URL, "/webhooks/github/") {
		t.Errorf("webhook info incomplete: %+v", created.Webhook)
	}
	if strings.Contains(string(resp), "postgres://secret") {
		t.Error("plaintext env var leaked in the create response")
	}

	appID := created.Application.ID

	// Get + list.
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+appID, token, ""); status != http.StatusOK {
		t.Errorf("get: status %d", status)
	}
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/applications", token, "")
	if status != http.StatusOK || !strings.Contains(string(resp), appID) {
		t.Errorf("list: status %d body %s", status, resp)
	}

	// Env vars: value in, keys out (never the value).
	if status, _, _ = doJSON(t, "PUT", ts.URL+"/api/v1/applications/"+appID+"/env/API_KEY", token, `{"value":"supersecret"}`); status != http.StatusNoContent {
		t.Errorf("set env: status %d", status)
	}
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+appID+"/env", token, "")
	if status != http.StatusOK {
		t.Fatalf("list env: status %d", status)
	}
	if !strings.Contains(string(resp), "API_KEY") || strings.Contains(string(resp), "supersecret") {
		t.Errorf("env keys leaked a value or missed a key: %s", resp)
	}

	// Validation: bad port is a 400; unknown environment is a 404; unknown
	// target server is a 400.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token,
		`{"name":"bad","source":{"kind":"github","repo":"x/y"},"runtime":{"server_id":"srv_test","port":0},"route":{"domain":"d"}}`)
	if status != http.StatusBadRequest {
		t.Errorf("bad port: status %d, want 400", status)
	}
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_missing/applications", token, body)
	if status != http.StatusNotFound {
		t.Errorf("unknown env: status %d, want 404", status)
	}
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token,
		`{"name":"nosrv","source":{"kind":"github","repo":"x/y"},"runtime":{"server_id":"srv_missing","port":80},"route":{"domain":"d"}}`)
	if status != http.StatusBadRequest {
		t.Errorf("unknown server: status %d, want 400", status)
	}

	// Delete.
	if status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/applications/"+appID, token, ""); status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

func TestProjectLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create: a project comes with its default production environment.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":"acme"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, body)
	}
	var created struct {
		Project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
		DefaultEnvironment struct {
			Name string `json:"name"`
		} `json:"default_environment"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.Project.ID == "" || created.DefaultEnvironment.Name != "production" {
		t.Fatalf("unexpected create response: %s", body)
	}

	// Get returns the project with its environments.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/projects/"+created.Project.ID, token, "")
	if status != http.StatusOK || !strings.Contains(string(body), "production") {
		t.Fatalf("get: status %d body %s", status, body)
	}

	// Add a second environment.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/projects/"+created.Project.ID+"/environments", token, `{"name":"staging"}`)
	if status != http.StatusCreated {
		t.Fatalf("create environment: status %d", status)
	}
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/projects/"+created.Project.ID+"/environments", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), "staging") {
		t.Fatalf("list environments: status %d body %s", status, body)
	}

	// Invalid name is a 400; unknown project is a 404.
	status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":""}`)
	if status != http.StatusBadRequest {
		t.Errorf("empty name: status %d, want 400", status)
	}
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_missing", token, "")
	if status != http.StatusNotFound {
		t.Errorf("missing project: status %d, want 404", status)
	}

	// Delete is a 204.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/projects/"+created.Project.ID, token, "")
	if status != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", status)
	}
}

// TestInstallScriptServed: the join installer is public and secret-free.
func TestInstallScriptServed(t *testing.T) {
	ts := newTestServer(t)
	status, headers, body := doJSON(t, "GET", ts.URL+"/install/agent.sh", "", "")
	if status != http.StatusOK {
		t.Fatalf("install script: status %d", status)
	}
	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Errorf("content type %q", ct)
	}
	if !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Errorf("script does not start with a shebang: %.40s", body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	_, headers, _ := doJSON(t, "GET", ts.URL+"/healthz", "", "")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := headers.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// Duplicate names and referenced servers are client-state conflicts, not
// server faults: the API must answer 409 with a reason, never a generic 500.
func TestConflictAndInUseAre409(t *testing.T) {
	ts, srvStore := newTestServerWithStores(t)
	token := login(t, ts)

	// Duplicate environment name inside one project.
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":"shop"}`)
	if status != http.StatusCreated {
		t.Fatalf("create project: status %d", status)
	}
	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(resp, &proj); err != nil {
		t.Fatalf("project response: %v", err)
	}
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/projects/"+proj.Project.ID+"/environments", token, `{"name":"production"}`)
	if status != http.StatusConflict {
		t.Errorf("duplicate environment: status %d body %s, want 409", status, resp)
	}

	// Duplicate application name inside one environment.
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	if status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body); status != http.StatusCreated {
		t.Fatalf("create application: status %d", status)
	}
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusConflict {
		t.Errorf("duplicate application: status %d body %s, want 409", status, resp)
	}

	// Deleting a server that still runs applications is refused with a reason.
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"busy"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("server response: %v", err)
	}
	srvStore.inUse[created.Server.ID] = true
	status, _, resp = doJSON(t, "DELETE", ts.URL+"/api/v1/servers/"+created.Server.ID, token, "")
	if status != http.StatusConflict || !strings.Contains(string(resp), "still runs applications") {
		t.Errorf("in-use server delete: status %d body %s, want 409 with reason", status, resp)
	}
}

func TestDeployAndRollbackEndpoints(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	// Create an app to deploy.
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, resp)
	}
	var created struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatal(err)
	}

	// Deploy → 202 with the deployment record.
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/applications/"+created.Application.ID+"/deploy", token, `{"ref":"abc123"}`)
	if status != http.StatusAccepted || !strings.Contains(string(resp), "building") {
		t.Fatalf("deploy: %d %s, want 202 building", status, resp)
	}

	// Deployments listing + get.
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+created.Application.ID+"/deployments", token, ""); status != http.StatusOK {
		t.Errorf("list deployments: %d", status)
	}
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/deployments/dep_test", token, ""); status != http.StatusOK {
		t.Errorf("get deployment: %d", status)
	}

	// Rollback of a built revision → 202; unbuilt → 409.
	if status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/rollback", token, ""); status != http.StatusAccepted {
		t.Errorf("rollback: %d, want 202", status)
	}
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_unbuilt/rollback", token, "")
	if status != http.StatusConflict {
		t.Errorf("rollback unbuilt: %d %s, want 409", status, resp)
	}
}

func TestPatchApplication(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}
	var created struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatal(err)
	}

	// Patch just the route; everything else must survive.
	status, _, resp = doJSON(t, "PATCH", ts.URL+"/api/v1/applications/"+created.Application.ID, token,
		`{"route":{"domain":"new.example.com"}}`)
	if status != http.StatusOK || !strings.Contains(string(resp), "new.example.com") || !strings.Contains(string(resp), "acme/web") {
		t.Fatalf("patch: %d %s", status, resp)
	}
	// An invalid merge is rejected with the same create-time rules.
	status, _, _ = doJSON(t, "PATCH", ts.URL+"/api/v1/applications/"+created.Application.ID, token,
		`{"runtime":{"port":0}}`)
	if status != http.StatusBadRequest {
		t.Errorf("patch bad port: %d, want 400", status)
	}
}

func TestGitHubWebhook(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)
	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}
	var created struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
		Webhook struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatal(err)
	}
	hookPath := created.Webhook.URL[strings.Index(created.Webhook.URL, "/webhooks/"):]

	sign := func(payload, secret string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	post := func(payload, sig string) int {
		req, err := http.NewRequest("POST", ts.URL+hookPath, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-GitHub-Event", "push")
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}

	push := `{"ref":"refs/heads/main","after":"deadbeef","deleted":false}`
	// Valid signature on the configured branch → deploy accepted.
	if got := post(push, sign(push, created.Webhook.Secret)); got != http.StatusAccepted {
		t.Errorf("valid push: %d, want 202", got)
	}
	// Wrong secret → 401. No signature → 401.
	if got := post(push, sign(push, "wrong-secret")); got != http.StatusUnauthorized {
		t.Errorf("bad signature: %d, want 401", got)
	}
	if got := post(push, ""); got != http.StatusUnauthorized {
		t.Errorf("no signature: %d, want 401", got)
	}
	// Other branch (authenticated) → 204, no deploy.
	other := `{"ref":"refs/heads/feature","after":"cafe","deleted":false}`
	if got := post(other, sign(other, created.Webhook.Secret)); got != http.StatusNoContent {
		t.Errorf("other branch: %d, want 204", got)
	}
	// Unknown webhook id → 404.
	req, _ := http.NewRequest("POST", ts.URL+"/webhooks/github/wh_nope", strings.NewReader(push))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown webhook: %d, want 404", res.StatusCode)
	}
}

// The middleware wraps the ResponseWriter; that wrapper must still expose
// http.Flusher, or the SSE log endpoints silently fall back to "streaming
// unsupported". This guards the fix at the middleware layer directly.
func TestResponseWriterSupportsFlush(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(Deps{Log: log})

	sawFlusher := make(chan bool, 1)
	handler := api.logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, ok := w.(http.Flusher)
		sawFlusher <- ok
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !<-sawFlusher {
		t.Fatal("wrapped ResponseWriter does not expose http.Flusher; SSE streaming would be unavailable")
	}
}

// The SSE log endpoint must replay retained history as data frames after the
// connected event — a client attaching after the build still sees the log.
func TestApplicationLogsSSEReplaysHistory(t *testing.T) {
	ts, _, logs, _ := newTestServerFull(t)
	token := login(t, ts)

	body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"web.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}
	var created struct {
		Application struct {
			ID      string `json:"id"`
			Runtime struct {
				ServerID string `json:"server_id"`
			} `json:"runtime"`
		} `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatal(err)
	}
	appID := created.Application.ID
	subject := subjects.RuntimeLog(created.Application.Runtime.ServerID, appID)
	logs.mu.Lock()
	logs.lines[subject] = []string{"line-1", "line-2"}
	logs.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/applications/"+appID+"/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	// Read until both replayed lines arrive (the stream then stays open until
	// the context deadline tears it down).
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 256)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, rerr := res.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		got := string(buf)
		if strings.Contains(got, "event: connected") &&
			strings.Contains(got, "data: line-1") && strings.Contains(got, "data: line-2") {
			return // success
		}
		if rerr != nil {
			break
		}
	}
	t.Fatalf("SSE stream missing expected frames; got:\n%s", buf)
}

type fakeDbReconciler struct{}

func (f *fakeDbReconciler) ReconcileDatabase(_ context.Context, _ string) error {
	return nil
}

type fakeDatabasesStore struct {
	mu     sync.Mutex
	dbs    map[string]domain.Database
	dbRevs map[string]domain.DatabaseRevision
}

func newFakeDatabasesStore() *fakeDatabasesStore {
	return &fakeDatabasesStore{
		dbs:    map[string]domain.Database{},
		dbRevs: map[string]domain.DatabaseRevision{},
	}
}

func (f *fakeDatabasesStore) CreateDatabaseWithRevision(_ context.Context, d domain.Database, rev domain.DatabaseRevision) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbs[d.ID] = d
	f.dbRevs[rev.ID] = rev
	return d, nil
}

func (f *fakeDatabasesStore) GetDatabase(_ context.Context, id string) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dbs[id]
	if !ok {
		return domain.Database{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeDatabasesStore) ListDatabasesByEnvironment(_ context.Context, envID string) ([]domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Database
	for _, d := range f.dbs {
		if d.EnvironmentID == envID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDatabasesStore) UpdateDatabaseConfig(_ context.Context, d domain.Database) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbs[d.ID] = d
	return d, nil
}

func (f *fakeDatabasesStore) UpdateDatabasePassword(_ context.Context, id string, ct, nonce []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.dbs[id]; ok {
		d.RootPasswordCT = ct
		d.RootPasswordNonce = nonce
		f.dbs[id] = d
	}
	return nil
}

func (f *fakeDatabasesStore) SetDatabaseDesiredRevision(_ context.Context, id string, revID string) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dbs[id]
	if !ok {
		return domain.Database{}, store.ErrNotFound
	}
	d.DesiredRevisionID = &revID
	f.dbs[id] = d
	return d, nil
}

func (f *fakeDatabasesStore) SetDatabaseDesiredState(_ context.Context, id, desiredState string) (domain.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dbs[id]
	if !ok {
		return domain.Database{}, store.ErrNotFound
	}
	d.DesiredState = desiredState
	f.dbs[id] = d
	return d, nil
}

func (f *fakeDatabasesStore) SetDatabaseStatus(_ context.Context, id, status, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.dbs[id]; ok {
		d.Status = status
		d.StatusDetail = detail
		f.dbs[id] = d
	}
	return nil
}

func (f *fakeDatabasesStore) SetDatabasePendingDelete(_ context.Context, id string, deleteVolume bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.dbs[id]; ok {
		d.PendingDelete = true
		d.DeleteVolume = deleteVolume
		f.dbs[id] = d
	}
	return nil
}

func (f *fakeDatabasesStore) DeleteDatabase(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.dbs, id)
	return nil
}

func (f *fakeDatabasesStore) CreateDatabaseRevision(_ context.Context, rev domain.DatabaseRevision) (domain.DatabaseRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbRevs[rev.ID] = rev
	return rev, nil
}

func (f *fakeDatabasesStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	return domain.Environment{ID: id, Name: "prod"}, nil
}

func (f *fakeDatabasesStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	return domain.Server{ID: id, Name: "srv"}, nil
}

// Deleting a project cascades its rows away in one statement. Applications
// survive that — the driver tears down anything absent from desired state — but
// a managed database is removed by a two-phase flow keyed on its own row, so
// cascading it skips the teardown and strands the container and its data volume
// on the server with nothing left in the panel that knows they exist. The
// delete is refused while resources remain.
func TestDeleteProjectRefusesWhileResourcesRemain(t *testing.T) {
	// An empty project deletes cleanly. Uses a freshly created project so it
	// does not depend on what other tests left in the shared fixture.
	t.Run("empty project deletes", func(t *testing.T) {
		ts := newTestServer(t)
		token := login(t, ts)

		st, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/projects", token, `{"name":"guard-empty"}`)
		if st != http.StatusCreated {
			t.Fatalf("creating project: %d %s", st, resp)
		}
		var proj struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := json.Unmarshal(resp, &proj); err != nil {
			t.Fatal(err)
		}
		if st, _, b := doJSON(t, "DELETE", ts.URL+"/api/v1/projects/"+proj.Project.ID, token, ""); st != http.StatusNoContent {
			t.Fatalf("empty project should delete: %d %s", st, b)
		}
	})

	// A project holding an application is refused, because the cascade would
	// take its resources with it — and a managed database's container and data
	// volume would be stranded on the server, since its teardown is driven by
	// the row the cascade removes.
	t.Run("project with an application is refused", func(t *testing.T) {
		ts := newTestServer(t)
		token := login(t, ts)

		// The seeded env_test is the one the applications fixture knows.
		body := `{"name":"web","source":{"kind":"github","repo":"acme/web"},` +
			`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"guard.example.com"}}`
		if st, _, b := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body); st != http.StatusCreated {
			t.Fatalf("seeding an application: %d %s", st, b)
		}

		st, _, resp := doJSON(t, "DELETE", ts.URL+"/api/v1/projects/prj_test", token, "")
		if st != http.StatusConflict {
			t.Fatalf("delete with an app present: status %d, want 409; body %s", st, resp)
		}
		// The operator has to be told what is in the way, not just refused.
		if !bytes.Contains(resp, []byte("application")) {
			t.Errorf("refusal should name what remains, got %s", resp)
		}
	})
}

// ─── API token abilities and session-only routes ────────────────────────────

// createToken mints a token with the given abilities via the API and returns
// its raw secret.
func createToken(t *testing.T, ts *httptest.Server, session, name, abilitiesJSON string) string {
	t.Helper()
	body := `{"name":"` + name + `","abilities":` + abilitiesJSON + `}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, body)
	if status != http.StatusCreated {
		t.Fatalf("createToken(%s): status %d body %s", name, status, resp)
	}
	var tr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp, &tr); err != nil || tr.Token == "" {
		t.Fatalf("createToken response %s: %v", resp, err)
	}
	return tr.Token
}

// A token may only do what its abilities allow: read cannot mutate, write
// cannot trigger a deploy, deploy cannot mutate configuration.
func TestAPITokenAbilitiesAreEnforced(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)

	readOnly := createToken(t, ts, session, "readonly", `["read"]`)
	writeOnly := createToken(t, ts, session, "writeonly", `["write"]`)
	deployOnly := createToken(t, ts, session, "deployonly", `["deploy"]`)

	cases := []struct {
		name   string
		token  string
		method string
		path   string
		body   string
		want   string // "allowed" (not 403) or "forbidden"
	}{
		{"read token GETs", readOnly, "GET", "/api/v1/projects", "", "allowed"},
		{"read token cannot create", readOnly, "POST", "/api/v1/projects", `{"name":"x"}`, "forbidden"},
		{"read token cannot delete", readOnly, "DELETE", "/api/v1/applications/app_x", "", "forbidden"},
		{"read token cannot deploy", readOnly, "POST", "/api/v1/applications/app_x/deploy", `{}`, "forbidden"},
		{"write token creates", writeOnly, "POST", "/api/v1/projects", `{"name":"x"}`, "allowed"},
		{"write token cannot deploy", writeOnly, "POST", "/api/v1/applications/app_x/deploy", `{}`, "forbidden"},
		{"write token cannot rollback", writeOnly, "POST", "/api/v1/deployments/dep_x/rollback", `{}`, "forbidden"},
		{"write token cannot read", writeOnly, "GET", "/api/v1/projects", "", "forbidden"},
		{"deploy token deploys", deployOnly, "POST", "/api/v1/applications/app_x/deploy", `{}`, "allowed"},
		{"deploy token cannot create", deployOnly, "POST", "/api/v1/projects", `{"name":"x"}`, "forbidden"},
	}
	for _, c := range cases {
		status, _, body := doJSON(t, c.method, ts.URL+c.path, c.token, c.body)
		forbidden := status == http.StatusForbidden
		if c.want == "forbidden" && !forbidden {
			t.Errorf("%s: status %d, want 403 (body %s)", c.name, status, body)
		}
		if c.want == "allowed" && forbidden {
			t.Errorf("%s: got 403, want the ability to permit it (body %s)", c.name, body)
		}
	}
}

// The deploy ability is matched against the whole route, not the last URL
// segment. `PUT /applications/{id}/env/{key}` ends in the segment "deploy" when
// the variable happens to be named that — a deploy-only token must still be
// refused, or it could rewrite application configuration it has no write
// ability for.
func TestDeployAbilityMatchesRouteNotSuffix(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	deployOnly := createToken(t, ts, session, "deployonly", `["deploy"]`)

	for _, path := range []string{
		"/api/v1/applications/app_x/env/deploy",
		"/api/v1/applications/app_x/env/rollback",
	} {
		status, _, body := doJSON(t, "PUT", ts.URL+path, deployOnly, `{"value":"x"}`)
		if status != http.StatusForbidden {
			t.Errorf("PUT %s with a deploy-only token: status %d, want 403 (body %s)", path, status, body)
		}
	}
}

// An explicitly empty ability list is a request for no authority. Silently
// turning it into full access would be the opposite of what was asked.
func TestCreateTokenExplicitEmptyAbilitiesRejected(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)

	// Anything *present* that grants nothing is refused — including an explicit
	// null, which decodes identically to an omitted field unless presence is
	// tracked separately.
	for _, body := range []string{
		`{"name":"empty","abilities":[]}`,
		`{"name":"null","abilities":null}`,
	} {
		status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (resp %s)", body, status, resp)
		}
	}
	// An omitted list still means full access, so old clients keep working.
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, `{"name":"omitted"}`)
	if status != http.StatusCreated {
		t.Fatalf("omitted abilities: status %d, want 201 (resp %s)", status, resp)
	}
	var created struct {
		Abilities []string `json:"abilities"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || len(created.Abilities) != 3 {
		t.Fatalf("omitted abilities produced %v, want the full set", created.Abilities)
	}
}

// A session is never narrowed: interactive use keeps every ability.
func TestSessionUnaffectedByAbilities(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/projects", ""},
		{"POST", "/api/v1/projects", `{"name":"p"}`},
		{"POST", "/api/v1/applications/app_x/deploy", `{}`},
	} {
		if status, _, body := doJSON(t, c.method, ts.URL+c.path, session, c.body); status == http.StatusForbidden {
			t.Errorf("%s %s: session was refused (%s)", c.method, c.path, body)
		}
	}
}

// Credential management is session-only: a leaked API token must not be able to
// mint a wider token, cut off the operator's sessions, or disable two-factor.
func TestCredentialRoutesRejectAPITokens(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	full := createToken(t, ts, session, "full", `["read","write","deploy"]`)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/tokens", `{"name":"escalated"}`},
		{"GET", "/api/v1/tokens", ""},
		{"DELETE", "/api/v1/tokens/tok_x", ""},
		{"GET", "/api/v1/auth/sessions", ""},
		{"DELETE", "/api/v1/auth/sessions/sess_x", ""},
		{"POST", "/api/v1/auth/sessions/revoke-others", ""},
		{"GET", "/api/v1/auth/totp", ""},
		{"POST", "/api/v1/auth/totp/disable", `{"code":"000000"}`},
	} {
		status, _, body := doJSON(t, c.method, ts.URL+c.path, full, c.body)
		if status != http.StatusForbidden {
			t.Errorf("%s %s via API token: status %d, want 403 (body %s)", c.method, c.path, status, body)
		}
	}
}

// The session list marks the caller's own entry, and revoke-others leaves it
// as the only survivor.
func TestSessionListAndRevokeOthers(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	mine := login(t, ts)
	other := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/auth/sessions", mine, "")
	if status != http.StatusOK {
		t.Fatalf("list sessions: status %d body %s", status, body)
	}
	var sessions []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	}
	if err := json.Unmarshal(body, &sessions); err != nil {
		t.Fatalf("unmarshal sessions %s: %v", body, err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	current := 0
	for _, s := range sessions {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current, want exactly 1", current)
	}

	if status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/auth/sessions/revoke-others", mine, ""); status != http.StatusOK {
		t.Fatalf("revoke-others: status %d body %s", status, body)
	}
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/auth/me", mine, ""); status != http.StatusOK {
		t.Fatalf("caller's own session was revoked: status %d", status)
	}
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/auth/me", other, ""); status != http.StatusUnauthorized {
		t.Fatalf("other session survived revoke-others: status %d", status)
	}
}

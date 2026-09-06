package rest

// Compose Stacks at the HTTP edge (compose-stacks.md §7). What this layer owns
// is the rank split — writing the FILE is team admin, deploying one is a
// member — and the fact that a compose file's contents never reach the audit
// log.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/compose"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const testStackFile = "services:\n  web:\n    image: nginx:1.27\n"

// fakeCompose is the service surface; what varies between tests is the rank
// the caller holds, not what the service does.
type fakeCompose struct {
	stacks    map[string]domain.ComposeStack
	revisions map[string][]domain.ComposeRevision
	env       map[string][]string
	deployed  []string
	deleted   []string
	volumes   []bool
	lastFile  *string
	err       error
}

func newFakeCompose() *fakeCompose {
	return &fakeCompose{
		stacks: map[string]domain.ComposeStack{
			"cs_test": {ID: "cs_test", EnvironmentID: "env_test", Name: "monitoring", ServerID: "srv_test", Status: domain.AppStopped},
		},
		revisions: map[string][]domain.ComposeRevision{
			"cs_test": {{ID: "csr_1", StackID: "cs_test", ComposeYAML: testStackFile}},
		},
		env: map[string][]string{},
	}
}

func (f *fakeCompose) Create(_ context.Context, envID string, in compose.Input) (domain.ComposeStack, error) {
	if f.err != nil {
		return domain.ComposeStack{}, f.err
	}
	st := domain.ComposeStack{ID: "cs_new", EnvironmentID: envID, Name: in.Name, ServerID: in.ServerID}
	f.stacks[st.ID] = st
	return st, nil
}

func (f *fakeCompose) Update(_ context.Context, id string, in compose.UpdateInput) (domain.ComposeStack, error) {
	if f.err != nil {
		return domain.ComposeStack{}, f.err
	}
	f.lastFile = in.ComposeYAML
	st := f.stacks[id]
	if in.Name != nil {
		st.Name = *in.Name
	}
	f.stacks[id] = st
	return st, nil
}

func (f *fakeCompose) Get(_ context.Context, id string) (domain.ComposeStack, error) {
	st, ok := f.stacks[id]
	if !ok {
		return domain.ComposeStack{}, store.ErrNotFound
	}
	return st, nil
}

func (f *fakeCompose) List(_ context.Context, envID string) ([]domain.ComposeStack, error) {
	var out []domain.ComposeStack
	for _, st := range f.stacks {
		if st.EnvironmentID == envID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeCompose) Delete(_ context.Context, id string, deleteVolumes bool) error {
	f.deleted = append(f.deleted, id)
	f.volumes = append(f.volumes, deleteVolumes)
	return nil
}

func (f *fakeCompose) Deploy(_ context.Context, id string) (domain.ComposeStack, error) {
	if f.err != nil {
		return domain.ComposeStack{}, f.err
	}
	f.deployed = append(f.deployed, id)
	return f.stacks[id], nil
}

func (f *fakeCompose) Rollback(_ context.Context, id, _ string) (domain.ComposeStack, error) {
	if f.err != nil {
		return domain.ComposeStack{}, f.err
	}
	return f.stacks[id], nil
}

func (f *fakeCompose) Revisions(_ context.Context, id string) ([]domain.ComposeRevision, error) {
	return f.revisions[id], nil
}

func (f *fakeCompose) File(_ context.Context, id string) (domain.ComposeRevision, error) {
	revs := f.revisions[id]
	if len(revs) == 0 {
		return domain.ComposeRevision{}, compose.ErrNeverDeployed
	}
	return revs[0], nil
}

func (f *fakeCompose) SetEnvVar(_ context.Context, id, key, _ string) error {
	f.env[id] = append(f.env[id], key)
	return f.err
}

func (f *fakeCompose) EnvKeys(_ context.Context, id string) ([]string, error) {
	return f.env[id], nil
}

func (f *fakeCompose) DeleteEnvVar(_ context.Context, id, key string) error {
	kept := f.env[id][:0]
	for _, k := range f.env[id] {
		if k != key {
			kept = append(kept, k)
		}
	}
	f.env[id] = kept
	return nil
}

// newComposeServer wires an API whose caller holds exactly `role` in the
// project — the default harness user is a panel owner, for whom every rank
// check passes, which would hide the split this feature depends on.
func newComposeServer(t *testing.T, role string) (*httptest.Server, *fakeCompose, *memAuditStore) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
		sessions: map[string]domain.User{},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ft := newFakeTeams()
	ft.teams = nil
	ft.projectRoles["usr_test"] = map[string]string{"prj_test": role}

	svc := newFakeCompose()
	mem := newMemAuditStore()
	api := New(Deps{
		Auth:     auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects: projects.NewService(newFakeProjectsStore()),
		Teams:    ft,
		Compose:  svc,
		Audit:    audit.NewService(mem, 0, log),
		Log:      log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, svc, mem
}

// ─── the rank split (§7) ────────────────────────────────────────────────────

// A compose file can ask for privileged containers and host mounts, which is
// root on the node — so writing one must not be reachable at the rank that
// deploys an application.
func TestWritingAComposeFileNeedsAdmin(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	body := `{"name":"n","server_id":"srv_test","compose_yaml":"services:\n  web:\n    image: nginx\n"}`
	if status, _, got := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/compose-stacks", token, body); status != http.StatusForbidden {
		t.Fatalf("create: status = %d body %s, want 403 for a member", status, got)
	}
	if status, _, got := doJSON(t, "PATCH", ts.URL+"/api/v1/compose-stacks/cs_test", token,
		`{"compose_yaml":"services:\n  web:\n    image: nginx\n"}`); status != http.StatusForbidden {
		t.Fatalf("patch file: status = %d body %s, want 403 for a member", status, got)
	}
	if status, _, got := doJSON(t, "DELETE", ts.URL+"/api/v1/compose-stacks/cs_test", token, ""); status != http.StatusForbidden {
		t.Fatalf("delete: status = %d body %s, want 403 for a member", status, got)
	}
}

func TestAnAdminCanWriteAComposeFile(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleAdmin)
	token := login(t, ts)

	body := `{"name":"monitoring","server_id":"srv_test","compose_yaml":"services:\n  web:\n    image: nginx\n"}`
	status, _, got := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/compose-stacks", token, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body %s", status, got)
	}
}

// Redeploying a file an admin already reviewed grants nothing new, so a member
// can ship it — the same rank that deploys an application.
func TestAMemberCanDeployAndRollBack(t *testing.T) {
	ts, svc, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	if status, _, got := doJSON(t, "POST", ts.URL+"/api/v1/compose-stacks/cs_test/deploy", token, ""); status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d body %s", status, got)
	}
	if len(svc.deployed) != 1 {
		t.Fatalf("deployed = %v", svc.deployed)
	}
	if status, _, got := doJSON(t, "POST", ts.URL+"/api/v1/compose-stacks/cs_test/rollback", token,
		`{"revision_id":"csr_1"}`); status != http.StatusAccepted {
		t.Fatalf("rollback: status = %d body %s", status, got)
	}
}

// Renaming touches no capability, so it stays at member rank.
func TestAMemberCanRenameAStack(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	if status, _, got := doJSON(t, "PATCH", ts.URL+"/api/v1/compose-stacks/cs_test", token, `{"name":"renamed"}`); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, got)
	}
}

// ─── the rest of the surface ────────────────────────────────────────────────

func TestListAndGetComposeStacks(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/compose-stacks", token, "")
	if status != http.StatusOK {
		t.Fatalf("list: status = %d body %s", status, body)
	}
	var list []composeStackDTO
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(list) != 1 || list[0].ID != "cs_test" {
		t.Fatalf("list = %+v", list)
	}

	if status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/compose-stacks/cs_test", token, ""); status != http.StatusOK {
		t.Fatalf("get: status = %d body %s", status, body)
	}
}

// The file a deploy would ship is the newest revision, not the row, so what is
// shown and what would run cannot differ.
func TestGetComposeFileReturnsTheCurrentRevision(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/compose-stacks/cs_test/file", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var rev composeRevisionDTO
	if err := json.Unmarshal(body, &rev); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if rev.ComposeYAML != testStackFile {
		t.Fatalf("file = %q, want it verbatim", rev.ComposeYAML)
	}
}

// Convergence never removes a volume, so this flag is the only way a stack's
// data goes — and it is never the default.
func TestDeleteVolumesIsOptInOnly(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  bool
	}{{"", false}, {"?delete_volumes=true", true}, {"?delete_volumes=false", false}} {
		ts, svc, _ := newComposeServer(t, domain.RoleAdmin)
		token := login(t, ts)
		if status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/compose-stacks/cs_test"+tc.query, token, ""); status != http.StatusNoContent {
			t.Fatalf("status = %d body %s", status, body)
		}
		if svc.volumes[0] != tc.want {
			t.Errorf("%q: delete_volumes = %v, want %v", tc.query, svc.volumes[0], tc.want)
		}
	}
}

// A compose file can carry an inline secret an operator put there, and the
// audit log is not where it becomes permanent.
func TestTheComposeFileNeverReachesTheAuditLog(t *testing.T) {
	ts, _, mem := newComposeServer(t, domain.RoleAdmin)
	token := login(t, ts)

	secret := "s3cret-in-the-file"
	body := `{"compose_yaml":"services:\n  web:\n    image: nginx\n    environment:\n      TOKEN: ` + secret + `\n"}`
	if status, _, got := doJSON(t, "PATCH", ts.URL+"/api/v1/compose-stacks/cs_test", token, body); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, got)
	}
	for _, e := range mem.events {
		raw, _ := json.Marshal(e.Detail)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the compose file reached the audit detail: %s", raw)
		}
	}
	var found bool
	for _, e := range mem.events {
		if e.Action == audit.ActionComposeStackUpdated {
			found = true
		}
	}
	if !found {
		t.Fatal("no compose_stack.updated row")
	}
}

// A variable's KEY is recorded so a change is attributable; its value is not.
func TestEnvVarAuditRecordsTheKeyNotTheValue(t *testing.T) {
	ts, _, mem := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	if status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/compose-stacks/cs_test/env/TOKEN", token,
		`{"value":"s3cret"}`); status != http.StatusNoContent {
		t.Fatalf("status = %d body %s", status, body)
	}
	for _, e := range mem.events {
		raw, _ := json.Marshal(e.Detail)
		if strings.Contains(string(raw), "s3cret") {
			t.Fatalf("a variable's value reached the audit detail: %s", raw)
		}
	}
}

// Values are write-only; a listing returns keys (rule 20).
func TestEnvListingReturnsKeysOnly(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleMember)
	token := login(t, ts)

	if status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/compose-stacks/cs_test/env/TOKEN", token,
		`{"value":"s3cret"}`); status != http.StatusNoContent {
		t.Fatalf("set: status = %d body %s", status, body)
	}
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/compose-stacks/cs_test/env", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	if strings.Contains(string(body), "s3cret") {
		t.Fatalf("a value came back: %s", body)
	}
	if !strings.Contains(string(body), "TOKEN") {
		t.Fatalf("body = %s, want the key", body)
	}
}

func TestUnknownComposeStackIsNotFound(t *testing.T) {
	ts, _, _ := newComposeServer(t, domain.RoleAdmin)
	token := login(t, ts)

	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/compose-stacks/cs_nope", token, ""); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// A panel that has not wired compose stacks behaves as it did before the
// feature existed rather than 500-ing.
func TestComposeRoutesAnswer501WhenNotWired(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/environments/env_test/compose-stacks", ""},
		{"POST", "/api/v1/environments/env_test/compose-stacks", `{"name":"n","server_id":"srv_test","compose_yaml":"services:\n  w:\n    image: n\n"}`},
		{"GET", "/api/v1/compose-stacks/cs_test", ""},
		{"PATCH", "/api/v1/compose-stacks/cs_test", `{"name":"n"}`},
		{"DELETE", "/api/v1/compose-stacks/cs_test", ""},
		{"POST", "/api/v1/compose-stacks/cs_test/deploy", ""},
	} {
		status, _, body := doJSON(t, tc.method, ts.URL+tc.path, token, tc.body)
		if status != http.StatusNotImplemented {
			t.Errorf("%s %s: status = %d body %s, want 501", tc.method, tc.path, status, body)
		}
	}
}

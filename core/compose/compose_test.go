package compose

// What this package decides is which FILE the agent converges toward, and what
// it refuses to store in the first place. Both are tested here; the invocation
// itself belongs to the agent.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const validFile = `services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
`

type fakeStore struct {
	stacks    map[string]domain.ComposeStack
	revisions map[string]domain.ComposeRevision
	revOrder  []string
	env       map[string][]domain.ComposeEnvVar
	envs      map[string]bool
	servers   map[string]domain.Server
	deleted   []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		stacks:    map[string]domain.ComposeStack{},
		revisions: map[string]domain.ComposeRevision{},
		env:       map[string][]domain.ComposeEnvVar{},
		envs:      map[string]bool{"env_1": true},
		servers: map[string]domain.Server{
			"srv_1":       {ID: "srv_1", Role: domain.RoleAll},
			"srv_builder": {ID: "srv_builder", Role: domain.RoleBuilder},
		},
	}
}

func (f *fakeStore) CreateComposeStack(_ context.Context, st domain.ComposeStack) (domain.ComposeStack, error) {
	for _, other := range f.stacks {
		if other.EnvironmentID == st.EnvironmentID && other.Name == st.Name {
			return domain.ComposeStack{}, store.ErrConflict
		}
	}
	f.stacks[st.ID] = st
	return st, nil
}

func (f *fakeStore) GetComposeStack(_ context.Context, id string) (domain.ComposeStack, error) {
	st, ok := f.stacks[id]
	if !ok {
		return domain.ComposeStack{}, store.ErrNotFound
	}
	return st, nil
}

func (f *fakeStore) ListComposeStacksByEnvironment(_ context.Context, envID string) ([]domain.ComposeStack, error) {
	var out []domain.ComposeStack
	for _, st := range f.stacks {
		if st.EnvironmentID == envID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateComposeStackConfig(_ context.Context, st domain.ComposeStack) (domain.ComposeStack, error) {
	f.stacks[st.ID] = st
	return st, nil
}

func (f *fakeStore) SetComposeStackDesiredRevision(_ context.Context, id, revisionID string) (domain.ComposeStack, error) {
	st, ok := f.stacks[id]
	if !ok {
		return domain.ComposeStack{}, store.ErrNotFound
	}
	st.DesiredRevisionID = &revisionID
	f.stacks[id] = st
	return st, nil
}

func (f *fakeStore) DeleteComposeStack(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.stacks, id)
	return nil
}

func (f *fakeStore) CreateComposeRevision(_ context.Context, r domain.ComposeRevision) (domain.ComposeRevision, error) {
	f.revisions[r.ID] = r
	f.revOrder = append(f.revOrder, r.ID)
	return r, nil
}

func (f *fakeStore) GetComposeRevision(_ context.Context, id string) (domain.ComposeRevision, error) {
	r, ok := f.revisions[id]
	if !ok {
		return domain.ComposeRevision{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) ListComposeRevisions(_ context.Context, stackID string, _ int32) ([]domain.ComposeRevision, error) {
	var out []domain.ComposeRevision
	for i := len(f.revOrder) - 1; i >= 0; i-- {
		if r := f.revisions[f.revOrder[i]]; r.StackID == stackID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) LatestComposeRevision(_ context.Context, stackID string) (domain.ComposeRevision, error) {
	for i := len(f.revOrder) - 1; i >= 0; i-- {
		if r := f.revisions[f.revOrder[i]]; r.StackID == stackID {
			return r, nil
		}
	}
	return domain.ComposeRevision{}, store.ErrNotFound
}

func (f *fakeStore) UpsertComposeEnvVar(_ context.Context, stackID string, v domain.ComposeEnvVar) error {
	for i, existing := range f.env[stackID] {
		if existing.Key == v.Key {
			f.env[stackID][i] = v
			return nil
		}
	}
	f.env[stackID] = append(f.env[stackID], v)
	return nil
}

func (f *fakeStore) ListComposeEnvVars(_ context.Context, stackID string) ([]domain.ComposeEnvVar, error) {
	return f.env[stackID], nil
}

func (f *fakeStore) DeleteComposeEnvVar(_ context.Context, stackID, key string) error {
	kept := f.env[stackID][:0]
	for _, v := range f.env[stackID] {
		if v.Key != key {
			kept = append(kept, v)
		}
	}
	f.env[stackID] = kept
	return nil
}

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if !f.envs[id] {
		return domain.Environment{}, store.ErrNotFound
	}
	return domain.Environment{ID: id, ProjectID: "prj_1", Name: "production"}, nil
}

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	srv, ok := f.servers[id]
	if !ok {
		return domain.Server{}, store.ErrNotFound
	}
	return srv, nil
}

type fakeSealer struct{}

func (fakeSealer) Seal(pt []byte) ([]byte, []byte, error) {
	return append([]byte("sealed:"), pt...), []byte("nonce"), nil
}

// fakeAgent records what the plane asked the host to do.
type fakeAgent struct {
	converged []string
	removed   []string
	volumes   []bool
	err       error
}

func (a *fakeAgent) ConvergeStack(_ context.Context, stackID string) error {
	a.converged = append(a.converged, stackID)
	return a.err
}

func (a *fakeAgent) RemoveStack(_ context.Context, _, stackID string, deleteVolumes bool) error {
	a.removed = append(a.removed, stackID)
	a.volumes = append(a.volumes, deleteVolumes)
	return a.err
}

func newService() (*Service, *fakeStore, *fakeAgent) {
	fs, agent := newFakeStore(), &fakeAgent{}
	return NewService(fs, fakeSealer{}, agent), fs, agent
}

func validInput() Input {
	return Input{Name: "monitoring", ServerID: "srv_1", ComposeYAML: validFile}
}

// ─── creation ───────────────────────────────────────────────────────────────

// A stack is born stopped, exactly as an application is: creating is not
// deploying, so a file can be reviewed before it reaches a host.
func TestCreateStoresTheFileAndDeploysNothing(t *testing.T) {
	svc, fs, agent := newService()

	stack, err := svc.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(stack.ID, "cs_") {
		t.Fatalf("id = %q, want the cs_ prefix", stack.ID)
	}
	if stack.DesiredRevisionID != nil {
		t.Fatalf("desired revision = %v, want nothing deployed", stack.DesiredRevisionID)
	}
	if len(agent.converged) != 0 {
		t.Fatalf("converged %v, want nothing published by a create", agent.converged)
	}
	// The file is a revision from the start, so "what is stored" and "what a
	// deploy would ship" are never two different things.
	rev, err := fs.LatestComposeRevision(context.Background(), stack.ID)
	if err != nil || rev.ComposeYAML != validFile {
		t.Fatalf("latest revision = %+v, %v", rev, err)
	}
}

func TestCreateSealsEnvVars(t *testing.T) {
	svc, fs, _ := newService()
	in := validInput()
	in.EnvVars = map[string]string{"ADMIN_PASSWORD": "s3cret"}

	stack, err := svc.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	vars := fs.env[stack.ID]
	if len(vars) != 1 || string(vars[0].ValueCT) == "s3cret" {
		t.Fatalf("env = %+v, want it sealed", vars)
	}
	if !strings.HasPrefix(string(vars[0].ValueCT), "sealed:") {
		t.Fatalf("ciphertext = %q, want it sealed through the sealer", vars[0].ValueCT)
	}
}

// A builder-role node has no application driver, so the stack would exist with
// every converge failing. Refuse at creation rather than at the first deploy.
func TestCreateRefusesABuilderRoleServer(t *testing.T) {
	svc, _, _ := newService()
	in := validInput()
	in.ServerID = "srv_builder"

	_, err := svc.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "builder role") {
		t.Fatalf("err = %v, want the builder server refused", err)
	}
}

func TestCreateRefusesADuplicateNameInTheEnvironment(t *testing.T) {
	svc, _, _ := newService()
	if _, err := svc.Create(context.Background(), "env_1", validInput()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), "env_1", validInput()); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// ─── what the panel refuses (spec §3) ───────────────────────────────────────

func TestValidateFileRefusesWhatTheReconcilerCannotHonour(t *testing.T) {
	cases := map[string]struct{ file, says string }{
		"empty":       {"", "required"},
		"not yaml":    {"services: [oops", "not valid YAML"},
		"no services": {"version: \"3\"\n", "no services"},
		// There is no builder on a target host: ADR-008's build story is a
		// builder-role agent producing an image that travels by relay.
		"build": {"services:\n  web:\n    build: .\n", "build:"},
		// An absolute name collides across environments on one server.
		"container_name": {"services:\n  web:\n    image: nginx\n    container_name: web\n", "container_name:"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateFile(tc.file)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if !strings.Contains(ve.Msg, tc.says) {
				t.Fatalf("message = %q, want it to mention %q", ve.Msg, tc.says)
			}
		})
	}
}

// The point of this resource is that the file runs as-is. Everything the
// feature exists to provide has to survive validation.
func TestValidateFileAllowsWhatTheResourceExistsFor(t *testing.T) {
	file := `services:
  agent:
    image: datadog/agent:7
    privileged: true
    pid: host
    network_mode: host
    cap_add: [SYS_ADMIN]
    command: ["--flag", "value"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /proc:/host/proc:ro
volumes:
  data: {}
networks:
  internal: {}
`
	if err := ValidateFile(file); err != nil {
		t.Fatalf("ValidateFile: %v — privileged, host mounts and command overrides are the reason this resource exists", err)
	}
}

func TestValidateFileRefusesAnEnormousFile(t *testing.T) {
	huge := "services:\n  web:\n    image: nginx\n" + strings.Repeat("# padding\n", 60000)
	err := ValidateFile(huge)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "too large") {
		t.Fatalf("err = %v, want the size refused", err)
	}
}

// ─── routing (spec §5) ──────────────────────────────────────────────────────

// A domain with nothing to point at is a route that silently does nothing.
func TestRouteFieldsAreValidatedTogether(t *testing.T) {
	cases := map[string]domain.ComposeRoute{
		"domain without a service": {Domain: "app.example.com", Port: 80},
		"domain without a port":    {Domain: "app.example.com", Service: "web"},
		"service without a domain": {Service: "web"},
		"port out of range":        {Domain: "app.example.com", Service: "web", Port: 70000},
		"domain with a path":       {Domain: "app.example.com/x", Service: "web", Port: 80},
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := newService()
			in := validInput()
			in.Route = route
			_, err := svc.Create(context.Background(), "env_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestAStackNeedsNoRoute(t *testing.T) {
	svc, _, _ := newService()
	if _, err := svc.Create(context.Background(), "env_1", validInput()); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// ─── revisions, deploy and rollback ─────────────────────────────────────────

func TestDeployPointsDesiredStateAtTheCurrentFileAndConverges(t *testing.T) {
	svc, _, agent := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())

	deployed, err := svc.Deploy(context.Background(), stack.ID)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if deployed.DesiredRevisionID == nil {
		t.Fatal("desired revision is still nil after a deploy")
	}
	if len(agent.converged) != 1 || agent.converged[0] != stack.ID {
		t.Fatalf("converged = %v", agent.converged)
	}
}

// An edit that changes the file mints a revision; one that does not, does not —
// or the history stops meaning "the versions that were deployed".
func TestUpdateOnlyMintsARevisionWhenTheFileChanges(t *testing.T) {
	svc, fs, _ := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())
	before := len(fs.revOrder)

	name := "renamed"
	if _, err := svc.Update(context.Background(), stack.ID, UpdateInput{Name: &name}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	same := validFile
	if _, err := svc.Update(context.Background(), stack.ID, UpdateInput{ComposeYAML: &same}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fs.revOrder) != before {
		t.Fatalf("revisions %d -> %d, want none for an unchanged file", before, len(fs.revOrder))
	}

	changed := strings.Replace(validFile, "nginx:1.27", "nginx:1.28", 1)
	if _, err := svc.Update(context.Background(), stack.ID, UpdateInput{ComposeYAML: &changed}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fs.revOrder) != before+1 {
		t.Fatalf("revisions %d -> %d, want exactly one more", before, len(fs.revOrder))
	}
}

// Editing and shipping are separate acts, so a file can be reviewed before it
// reaches a host.
func TestUpdateDoesNotDeploy(t *testing.T) {
	svc, _, agent := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())

	changed := strings.Replace(validFile, "nginx:1.27", "nginx:1.28", 1)
	if _, err := svc.Update(context.Background(), stack.ID, UpdateInput{ComposeYAML: &changed}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(agent.converged) != 0 {
		t.Fatalf("converged %v, want an edit to publish nothing", agent.converged)
	}
}

func TestUpdateHoldsTheFileToTheSameRules(t *testing.T) {
	svc, _, _ := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())

	bad := "services:\n  web:\n    build: .\n"
	_, err := svc.Update(context.Background(), stack.ID, UpdateInput{ComposeYAML: &bad})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a PATCH held to the create rules", err)
	}
}

func TestRollbackRePointsAtAnOlderRevision(t *testing.T) {
	svc, fs, agent := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())
	first, _ := fs.LatestComposeRevision(context.Background(), stack.ID)
	if _, err := svc.Deploy(context.Background(), stack.ID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	changed := strings.Replace(validFile, "nginx:1.27", "nginx:1.28", 1)
	if _, err := svc.Update(context.Background(), stack.ID, UpdateInput{ComposeYAML: &changed}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := svc.Deploy(context.Background(), stack.ID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	rolled, err := svc.Rollback(context.Background(), stack.ID, first.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.DesiredRevisionID == nil || *rolled.DesiredRevisionID != first.ID {
		t.Fatalf("desired revision = %v, want the first one", rolled.DesiredRevisionID)
	}
	if len(agent.converged) != 3 {
		t.Fatalf("converges = %d, want one per deploy and one per rollback", len(agent.converged))
	}
}

// A revision id from another stack would otherwise deploy someone else's file
// under this stack's identity.
func TestRollbackRefusesAnotherStacksRevision(t *testing.T) {
	svc, fs, _ := newService()
	mine, _ := svc.Create(context.Background(), "env_1", validInput())
	other := validInput()
	other.Name = "other"
	theirs, _ := svc.Create(context.Background(), "env_1", other)
	theirRev, _ := fs.LatestComposeRevision(context.Background(), theirs.ID)

	_, err := svc.Rollback(context.Background(), mine.ID, theirRev.ID)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want it refused", err)
	}
}

func TestDeployBeforeAnyFileIsRefused(t *testing.T) {
	svc, fs, _ := newService()
	// A stack whose revision rows are gone models the only way this happens.
	fs.stacks["cs_bare"] = domain.ComposeStack{ID: "cs_bare", EnvironmentID: "env_1", Name: "bare", ServerID: "srv_1"}

	if _, err := svc.Deploy(context.Background(), "cs_bare"); !errors.Is(err, ErrNeverDeployed) {
		t.Fatalf("err = %v, want ErrNeverDeployed", err)
	}
}

// ─── deletion ───────────────────────────────────────────────────────────────

// Convergence never removes a volume, so this flag is the only way a stack's
// data goes — and it is never the default.
func TestDeletePassesTheVolumeDecisionThrough(t *testing.T) {
	for _, deleteVolumes := range []bool{false, true} {
		svc, _, agent := newService()
		stack, _ := svc.Create(context.Background(), "env_1", validInput())

		if err := svc.Delete(context.Background(), stack.ID, deleteVolumes); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(agent.removed) != 1 || agent.volumes[0] != deleteVolumes {
			t.Fatalf("removed = %v volumes = %v, want delete_volumes=%v carried", agent.removed, agent.volumes, deleteVolumes)
		}
	}
}

// ─── env vars ───────────────────────────────────────────────────────────────

// An env file is line-oriented, so a key carrying a newline or an '=' would
// write a second assignment nobody declared.
func TestEnvKeysMustBeShellSafe(t *testing.T) {
	svc, _, _ := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())

	for _, key := range []string{"", "WITH SPACE", "WITH=EQUALS", "WITH\nNEWLINE", "1LEADING_DIGIT", "with-dash"} {
		if err := svc.SetEnvVar(context.Background(), stack.ID, key, "v"); err == nil {
			t.Errorf("SetEnvVar(%q) succeeded, want it refused", key)
		}
	}
	for _, key := range []string{"ADMIN_PASSWORD", "_private", "PORT2"} {
		if err := svc.SetEnvVar(context.Background(), stack.ID, key, "v"); err != nil {
			t.Errorf("SetEnvVar(%q) = %v, want it accepted", key, err)
		}
	}
}

func TestEnvKeysAreReadableAndValuesAreNot(t *testing.T) {
	svc, _, _ := newService()
	stack, _ := svc.Create(context.Background(), "env_1", validInput())
	if err := svc.SetEnvVar(context.Background(), stack.ID, "TOKEN", "s3cret"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	keys, err := svc.EnvKeys(context.Background(), stack.ID)
	if err != nil {
		t.Fatalf("EnvKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "TOKEN" {
		t.Fatalf("keys = %v", keys)
	}
}

package applications

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeSealer makes sealing observable and reversible in tests: the ciphertext
// is a recognizable transform of the plaintext, never equal to it.
type fakeSealer struct{}

func (fakeSealer) Seal(pt []byte) (ct, nonce []byte, err error) {
	return append([]byte("sealed:"), pt...), []byte("nonce"), nil
}

type fakeStore struct {
	envs    map[string]bool
	servers map[string]bool
	apps    map[string]domain.Application
	envVars map[string][]domain.EnvVar // by appID
	// sharedKeys are the shared-variable keys in force for every environment
	// this fake knows about (shared-variables.md §3).
	sharedKeys []string
	// registries the panel knows about, by id (registries.md §5).
	registries map[string]domain.Registry
	// registryLookups counts GetRegistry calls, so "an application that names
	// no registry pays no lookup" is provable rather than assumed.
	registryLookups int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		envs:    map[string]bool{"env_1": true},
		servers: map[string]bool{"srv_1": true, "srv_builder": true},
		apps:    map[string]domain.Application{},
		envVars: map[string][]domain.EnvVar{},
		// Two credentials in the fake's own team and one in another, so the
		// capability check and the tenancy check are each provable.
		registries: map[string]domain.Registry{
			"reg_pull":  {ID: "reg_pull", TeamID: "team_1", Name: "ghcr", URL: "ghcr.io", CanPull: true},
			"reg_push":  {ID: "reg_push", TeamID: "team_1", Name: "push", URL: "ghcr.io", CanPull: true, CanPush: true},
			"reg_other": {ID: "reg_other", TeamID: "team_other", Name: "theirs", URL: "ghcr.io", CanPull: true, CanPush: true},
		},
	}
}

func (f *fakeStore) CreateApplicationWithEnv(_ context.Context, a domain.Application, vars []domain.EnvVar) (domain.Application, error) {
	f.apps[a.ID] = a
	f.envVars[a.ID] = vars
	return a, nil
}

func (f *fakeStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) GetApplicationByWebhookID(_ context.Context, webhookID string) (domain.Application, error) {
	for _, a := range f.apps {
		if a.WebhookID == webhookID {
			return a, nil
		}
	}
	return domain.Application{}, store.ErrNotFound
}

func (f *fakeStore) UpdateApplicationConfig(_ context.Context, a domain.Application) (domain.Application, error) {
	if _, ok := f.apps[a.ID]; !ok {
		return domain.Application{}, store.ErrNotFound
	}
	f.apps[a.ID] = a
	return a, nil
}

func (f *fakeStore) ListApplicationsByEnvironment(_ context.Context, envID string) ([]domain.Application, error) {
	var out []domain.Application
	for _, a := range f.apps {
		if a.EnvironmentID == envID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteApplication(_ context.Context, id string) error {
	delete(f.apps, id)
	return nil
}

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if !f.envs[id] {
		return domain.Environment{}, store.ErrNotFound
	}
	return domain.Environment{ID: id, ProjectID: "prj_1", Name: "production"}, nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	return domain.Project{ID: id, TeamID: "team_1", Name: "acme"}, nil
}

// GetRegistry backs the check that an attached registry exists, is the team's
// and allows what it is being attached for (registries.md §5). Seeded per test
// via fakeStore.registries; an unseeded id is not found.
func (f *fakeStore) GetRegistry(_ context.Context, id string) (domain.Registry, error) {
	f.registryLookups++
	reg, ok := f.registries[id]
	if !ok {
		return domain.Registry{}, store.ErrNotFound
	}
	return reg, nil
}

func (f *fakeStore) ListSharedVariableKeysInScope(_ context.Context, _, _ string) ([]string, error) {
	return f.sharedKeys, nil
}

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	if !f.servers[id] {
		return domain.Server{}, store.ErrNotFound
	}
	// srv_builder models a builder-only agent: it takes build work but has no
	// application driver, so it never runs workloads.
	if id == "srv_builder" {
		return domain.Server{ID: id, Role: domain.RoleBuilder}, nil
	}
	return domain.Server{ID: id}, nil
}

func (f *fakeStore) UpsertEnvVar(_ context.Context, appID string, v domain.EnvVar) error {
	for i, cur := range f.envVars[appID] {
		if cur.Key == v.Key {
			f.envVars[appID][i] = v
			return nil
		}
	}
	f.envVars[appID] = append(f.envVars[appID], v)
	return nil
}

func (f *fakeStore) ListEnvVars(_ context.Context, appID string) ([]domain.EnvVar, error) {
	return f.envVars[appID], nil
}

func (f *fakeStore) DeleteEnvVar(_ context.Context, appID, key string) error {
	kept := f.envVars[appID][:0]
	for _, v := range f.envVars[appID] {
		if v.Key != key {
			kept = append(kept, v)
		}
	}
	f.envVars[appID] = kept
	return nil
}

func validInput() CreateInput {
	return CreateInput{
		Name:    "web",
		Source:  domain.AppSource{Kind: "github", Repo: "acme/web"},
		Runtime: domain.AppRuntime{ServerID: "srv_1", Port: 8080},
		Route:   domain.AppRoute{Domain: "web.example.com", HTTPS: true},
		EnvVars: map[string]string{"DATABASE_URL": "postgres://secret"},
	}
}

func TestCreateSealsSecretsAndDefaults(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})

	app, secret, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if secret == "" {
		t.Fatal("webhook secret not returned")
	}
	// Defaults applied.
	if app.Source.Branch != "main" || app.Build.DockerfilePath != "./Dockerfile" || app.Build.Context != "." {
		t.Errorf("defaults not applied: %+v", app)
	}
	if app.Health.Path != "/" || app.Health.IntervalSeconds != 10 || app.Runtime.Replicas != 1 {
		t.Errorf("health/replica defaults not applied: %+v", app)
	}
	// Webhook secret sealed at rest, not plaintext.
	if bytes.Contains(app.WebhookSecretCT, []byte(secret)) == false {
		// fakeSealer prefixes; the plaintext must be inside the ciphertext but
		// the stored value must not equal the raw secret.
		t.Errorf("webhook secret not sealed via sealer")
	}
	if string(app.WebhookSecretCT) == secret {
		t.Error("webhook secret stored in plaintext")
	}
	// Env var sealed: stored ciphertext must not equal the plaintext value.
	vars := fs.envVars[app.ID]
	if len(vars) != 1 || string(vars[0].ValueCT) == "postgres://secret" {
		t.Errorf("env var not sealed: %+v", vars)
	}
	if !bytes.HasPrefix(vars[0].ValueCT, []byte("sealed:")) {
		t.Errorf("env var not sealed via sealer: %q", vars[0].ValueCT)
	}
}

func TestCreateValidation(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	cases := map[string]func(*CreateInput){
		"empty name": func(in *CreateInput) { in.Name = "" },
		"bad source": func(in *CreateInput) { in.Source.Kind = "svn" },
		"empty repo": func(in *CreateInput) { in.Source.Repo = "" },
		// Image sources (deploy from container image, feature-matrix V1).
		"image kind no ref": func(in *CreateInput) { in.Source = domain.AppSource{Kind: "image"} },
		"image ref junk": func(in *CreateInput) {
			in.Source = domain.AppSource{Kind: "image", Image: "ghost:5; rm -rf /"}
		},
		// Previews need a branch to match pull_request events against.
		"builder-role server": func(in *CreateInput) { in.Runtime.ServerID = "srv_builder" },
		"image with previews": func(in *CreateInput) {
			in.Source = domain.AppSource{Kind: "image", Image: "ghost:5"}
			in.PreviewEnabled = true
			in.PreviewBaseDomain = "preview.example.com"
		},
		"image with deploy key": func(in *CreateInput) {
			key := "dk_1"
			in.Source = domain.AppSource{Kind: "image", Image: "ghost:5", DeployKeyID: &key}
		},
		// A kind outside the closed set. "nixpacks" used to sit here and is
		// now supported (pack-builds.md), which is exactly why the assertion
		// has to name something that is not.
		"bad build":    func(in *CreateInput) { in.Build.Kind = "buildpacks" },
		"zero port":    func(in *CreateInput) { in.Runtime.Port = 0 },
		"huge port":    func(in *CreateInput) { in.Runtime.Port = 70000 },
		"two replicas": func(in *CreateInput) { in.Runtime.Replicas = 2 },
		"no server":    func(in *CreateInput) { in.Runtime.ServerID = "" },
		// Health kind must be a known gate (feature-matrix V1: non-HTTP apps).
		"bad health kind": func(in *CreateInput) { in.Health.Kind = "grpc" },
		// Raw port publishes (feature-matrix V1): valid ranges, protocol, uniqueness.
		"port bad protocol": func(in *CreateInput) {
			in.Ports = []domain.PortMapping{{HostPort: 25565, ContainerPort: 25565, Protocol: "sctp"}}
		},
		"port host too high":  func(in *CreateInput) { in.Ports = []domain.PortMapping{{HostPort: 70000, ContainerPort: 25565}} },
		"port host zero":      func(in *CreateInput) { in.Ports = []domain.PortMapping{{HostPort: 0, ContainerPort: 25565}} },
		"port container zero": func(in *CreateInput) { in.Ports = []domain.PortMapping{{HostPort: 25565, ContainerPort: 0}} },
		"port dup binding": func(in *CreateInput) {
			in.Ports = []domain.PortMapping{{HostPort: 25565, ContainerPort: 1}, {HostPort: 25565, ContainerPort: 2, Protocol: "tcp"}}
		},
		// Negative health values would wrap to huge uint32s on the wire.
		"negative interval": func(in *CreateInput) { in.Health.IntervalSeconds = -5 },
		"negative timeout":  func(in *CreateInput) { in.Health.TimeoutSeconds = -1 },
		"negative retries":  func(in *CreateInput) { in.Health.Retries = -1 },
		// Loose env keys would corrupt the container environment.
		"env key with equals":  func(in *CreateInput) { in.EnvVars = map[string]string{"FOO=BAR": "v"} },
		"env key with newline": func(in *CreateInput) { in.EnvVars = map[string]string{"FOO\nBAR": "v"} },
		"env key digit-first":  func(in *CreateInput) { in.EnvVars = map[string]string{"1FOO": "v"} },
		// Preview config (preview-environments.md §2).
		"preview enabled no base": func(in *CreateInput) { in.PreviewEnabled = true; in.PreviewBaseDomain = "" },
		"negative preview ttl":    func(in *CreateInput) { in.PreviewTTLHours = -1 },
		// TTL is persisted as int32; a value above the max would wrap on the cast.
		"overflowing preview ttl": func(in *CreateInput) { in.PreviewTTLHours = math.MaxInt32 + 1 },
		// Resource limits are bounded (feature-matrix V1; CWE-190 on the int32 cast).
		"negative cpu limit":    func(in *CreateInput) { c := -1.0; in.Runtime.CPULimit = &c },
		"overflowing mem limit": func(in *CreateInput) { m := math.MaxInt32 + 1; in.Runtime.MemoryLimitMB = &m },
		// Volume mounts (feature-matrix V1): safe name + absolute unique path.
		"volume bad name":  func(in *CreateInput) { in.Volumes = []domain.VolumeMount{{Name: "Bad Name", Path: "/data"}} },
		"volume rel path":  func(in *CreateInput) { in.Volumes = []domain.VolumeMount{{Name: "data", Path: "data"}} },
		"volume traversal": func(in *CreateInput) { in.Volumes = []domain.VolumeMount{{Name: "data", Path: "/a/../b"}} },
		"volume dup path": func(in *CreateInput) {
			in.Volumes = []domain.VolumeMount{{Name: "a", Path: "/data"}, {Name: "b", Path: "/data"}}
		},
		// Health values are persisted as int32 too — same wrap risk upward.
		"overflowing interval": func(in *CreateInput) { in.Health.IntervalSeconds = math.MaxInt32 + 1 },
		"overflowing timeout":  func(in *CreateInput) { in.Health.TimeoutSeconds = math.MaxInt32 + 1 },
		"overflowing retries":  func(in *CreateInput) { in.Health.Retries = math.MaxInt32 + 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			_, _, err := s.Create(context.Background(), "env_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
		})
	}
}

// A raw (non-HTTP) service is valid without a route domain: it uses a tcp
// health gate and publishes ports. Defaults are applied (health.kind and each
// port's protocol).
// An image source keeps only the OCI reference: git fields are cleared, and
// the reference survives round-tripping (the deploy pipeline reads it back).
func TestCreateImageSource(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Source = domain.AppSource{Kind: "image", Repo: "leftover", Branch: "stale", Image: " ghcr.io/acme/web:1.2 "}

	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Source.Image != "ghcr.io/acme/web:1.2" {
		t.Errorf("image = %q, want trimmed reference", app.Source.Image)
	}
	if app.Source.Repo != "" || app.Source.Branch != "" {
		t.Errorf("git fields not cleared: %+v", app.Source)
	}
}

func TestCreateRawServiceNoRoute(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Route.Domain = ""
	in.Health.Kind = "tcp"
	in.Ports = []domain.PortMapping{{HostPort: 25565, ContainerPort: 25565}} // protocol empty → tcp

	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("routeless raw service rejected: %v", err)
	}
	if app.Route.Domain != "" {
		t.Errorf("route domain = %q, want empty", app.Route.Domain)
	}
	if app.Health.Kind != "tcp" {
		t.Errorf("health kind = %q, want tcp", app.Health.Kind)
	}
	if len(app.Ports) != 1 || app.Ports[0].Protocol != "tcp" {
		t.Errorf("port protocol not defaulted to tcp: %+v", app.Ports)
	}
}

// An HTTP app with no explicit health kind defaults to "http" — unchanged
// behavior for every existing application.
func TestCreateDefaultsHealthKindHTTP(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	app, _, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Health.Kind != "http" {
		t.Errorf("health kind = %q, want http default", app.Health.Kind)
	}
}

// The largest int32 TTL is accepted (persists without wrapping); one more is
// the first rejected value.
func TestCreatePreviewTTLBoundary(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.PreviewTTLHours = math.MaxInt32
	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("max int32 ttl rejected: %v", err)
	}
	if app.PreviewTTLHours != math.MaxInt32 {
		t.Fatalf("ttl = %d, want %d preserved", app.PreviewTTLHours, math.MaxInt32)
	}
}

func TestCreateUnknownEnvironment(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	if _, _, err := s.Create(context.Background(), "env_missing", validInput()); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("err = %v, want ErrEnvironmentNotFound", err)
	}
}

func TestCreateUnknownServer(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Runtime.ServerID = "srv_missing"
	if _, _, err := s.Create(context.Background(), "env_1", in); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("err = %v, want ErrServerNotFound", err)
	}
}

func TestEnvVarKeysAreWriteOnly(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})
	app, _, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetEnvVar(context.Background(), app.ID, "API_KEY", "supersecret"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	view, err := s.ListEnv(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("ListEnv: %v", err)
	}
	keys := view.Keys
	for _, k := range keys {
		if k == "supersecret" {
			t.Fatal("a value leaked into the keys list")
		}
	}
	found := false
	for _, k := range keys {
		if k == "API_KEY" {
			found = true
		}
	}
	if !found {
		t.Errorf("API_KEY not listed: %v", keys)
	}
}

func TestSetEnvVarUnknownApp(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	if err := s.SetEnvVar(context.Background(), "app_missing", "K", "v"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

// ─── Shared-variable references (shared-variables.md §3) ────────────────────
//
// The write is where strictness lives: a reference that is not well formed, or
// that does not currently resolve, is a 400 and nothing is stored. Coolify
// instead ships the literal `{{project.FOO}}` into the container — that is the
// behaviour the spec says we pointedly do not port.

func TestSetEnvVarRecordsSharedRefs(t *testing.T) {
	fs := newFakeStore()
	fs.sharedKeys = []string{"DB_PASS", "DB_USER", "SENTRY_DSN"}
	s := NewService(fs, fakeSealer{})
	app, _, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const value = "postgres://{{shared.DB_USER}}:{{shared.DB_PASS}}@db:5432/app"
	if err := s.SetEnvVar(context.Background(), app.ID, "DATABASE_URL", value); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	view, err := s.ListEnv(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("ListEnv: %v", err)
	}
	refs := view.SharedRefs["DATABASE_URL"]
	if len(refs) != 2 || refs[0] != "DB_USER" || refs[1] != "DB_PASS" {
		t.Fatalf("shared_refs = %v, want [DB_USER DB_PASS]", refs)
	}
	// The reference wiring is cleartext key names; the value itself is sealed.
	for _, v := range fs.envVars[app.ID] {
		if v.Key == "DATABASE_URL" && string(v.ValueCT) != "sealed:"+value {
			t.Fatalf("value was not sealed: %q", v.ValueCT)
		}
	}
	// A value with no references records none, so the used-by count and the
	// drift marker stay accurate when a reference is edited away.
	if err := s.SetEnvVar(context.Background(), app.ID, "DATABASE_URL", "postgres://plain"); err != nil {
		t.Fatalf("SetEnvVar (rewrite): %v", err)
	}
	view, err = s.ListEnv(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("ListEnv: %v", err)
	}
	if got := view.SharedRefs["DATABASE_URL"]; len(got) != 0 {
		t.Fatalf("shared_refs after removing the references = %v, want none", got)
	}
}

func TestSetEnvVarRejectsBadReferences(t *testing.T) {
	cases := map[string]string{
		"unknown key":      "{{shared.NOPE}}",
		"inner whitespace": "{{ shared.SENTRY_DSN }}",
		"empty key":        "{{shared.}}",
		"other namespace":  "{{project.SENTRY_DSN}}",
		"unterminated":     "{{shared.SENTRY_DSN",
		"embedded unknown": "prefix-{{shared.NOPE}}-suffix",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			fs := newFakeStore()
			fs.sharedKeys = []string{"SENTRY_DSN"}
			s := NewService(fs, fakeSealer{})
			app, _, err := s.Create(context.Background(), "env_1", validInput())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			before := len(fs.envVars[app.ID])
			err = s.SetEnvVar(context.Background(), app.ID, "X", value)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("SetEnvVar(%q) = %v, want a ValidationError", value, err)
			}
			if len(fs.envVars[app.ID]) != before {
				t.Fatal("a rejected env var was stored anyway")
			}
		})
	}
}

// The rejection message names the env key and the shared key — both of which
// the API already returns — and never the value (ENGINEERING rule 20).
func TestReferenceErrorNamesKeysNotValues(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})
	app, _, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	err = s.SetEnvVar(context.Background(), app.ID, "SENTRY_DSN", "swordfish{{shared.NOPE}}swordfish")
	if err == nil {
		t.Fatal("SetEnvVar accepted an unresolvable reference")
	}
	msg := err.Error()
	if strings.Contains(msg, "swordfish") {
		t.Fatalf("the value leaked into the error: %q", msg)
	}
	for _, want := range []string{"SENTRY_DSN", "NOPE", "production"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// Create runs the same gate: an application cannot be born with an
// unresolvable reference and discover it at its first deploy.
func TestCreateRejectsUnresolvableEnvReference(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})
	in := validInput()
	in.EnvVars = map[string]string{"SENTRY_DSN": "{{shared.NOPE}}"}
	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Create = %v, want a ValidationError", err)
	}
	if len(fs.apps) != 0 {
		t.Fatal("the application was created despite the bad reference")
	}
}

func TestCreateAcceptsResolvableEnvReference(t *testing.T) {
	fs := newFakeStore()
	fs.sharedKeys = []string{"SENTRY_DSN"}
	s := NewService(fs, fakeSealer{})
	in := validInput()
	in.EnvVars = map[string]string{"SENTRY_DSN": "{{shared.SENTRY_DSN}}"}
	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	view, err := s.ListEnv(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("ListEnv: %v", err)
	}
	if got := view.SharedRefs["SENTRY_DSN"]; len(got) != 1 || got[0] != "SENTRY_DSN" {
		t.Fatalf("shared_refs = %v, want [SENTRY_DSN]", got)
	}
}

// ─── registries (registries.md §5) ──────────────────────────────────────────
//
// Every refusal below is one an operator would otherwise meet mid-deploy, as
// an unexplained 401 or 403 from a registry after a five-minute build.

func ptr[T any](v T) *T { return &v }

func TestAttachingAPullRegistryIsAccepted(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Source.RegistryID = ptr("reg_pull")

	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Source.RegistryID == nil || *app.Source.RegistryID != "reg_pull" {
		t.Fatalf("source.registry_id = %v, want it stored", app.Source.RegistryID)
	}
}

// A pull-only credential named as a push target fails at the registry after
// the build, not before it. Refusing here turns a wasted build into a message.
func TestAPullOnlyRegistryCannotBeAPushTarget(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Build.PushRegistryID = ptr("reg_pull")

	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "not allowed to push") {
		t.Fatalf("err = %v, want a refusal naming the missing capability", err)
	}
}

// A credential belongs to a team. One project borrowing another team's push
// token is exactly what the team boundary exists to stop — and the refusal
// says "no such registry", so the config screen is not a way to enumerate
// other teams' credentials.
func TestARegistryInAnotherTeamIsNotFound(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Source.RegistryID = ptr("reg_other")

	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "no such registry") {
		t.Fatalf("err = %v, want the same answer a missing registry gets", err)
	}
}

func TestAnUnknownRegistryIsRefused(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Build.PushRegistryID = ptr("reg_nope")

	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
}

// An image source is never built, so a push target on one silently does
// nothing — the failure an operator cannot see.
func TestAnImageSourceCannotPush(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Source = domain.AppSource{Kind: "image", Image: "ghcr.io/acme/web:1"}
	in.Build.PushRegistryID = ptr("reg_push")

	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "nothing to push") {
		t.Fatalf("err = %v, want the combination refused", err)
	}
}

func TestAPushRepositoryNeedsARegistry(t *testing.T) {
	s := NewService(newFakeStore(), fakeSealer{})
	in := validInput()
	in.Build.PushRepository = "acme/web"

	_, _, err := s.Create(context.Background(), "env_1", in)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "push_registry_id") {
		t.Fatalf("err = %v, want the orphaned repository refused", err)
	}
}

// Uppercase is refused rather than folded: a registry treats `Acme/Web` as a
// name it has never heard of, and lowercasing it here would push somewhere the
// operator did not type.
func TestPushRepositoryMustBeALegalImagePath(t *testing.T) {
	bad := []string{"Acme/Web", "acme//web", "/acme", "acme/", "acme/web:tag", "acme/-web", "acme/web-", "acme/_web"}
	good := []string{"", "web", "acme/web", "acme/team/web", "acme.io/web-1", "a_b/c.d-e"}
	for _, p := range bad {
		if validRepositoryPath(p) {
			t.Errorf("validRepositoryPath(%q) = true, want it refused", p)
		}
	}
	for _, p := range good {
		if !validRepositoryPath(p) {
			t.Errorf("validRepositoryPath(%q) = false, want it accepted", p)
		}
	}
}

// The check runs on the MERGED result, so attaching a push registry and
// switching to an image source across two PATCHes is refused just as the
// single-request form is.
func TestUpdateChecksTheMergedResult(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})
	in := validInput()
	in.Build.PushRegistryID = ptr("reg_push")
	app, _, err := s.Create(context.Background(), "env_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = s.Update(context.Background(), app.ID, UpdateInput{
		Source: &domain.AppSource{Kind: "image", Image: "ghcr.io/acme/web:1"},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, "nothing to push") {
		t.Fatalf("err = %v, want the merged combination refused", err)
	}
}

// The overwhelming majority of applications name no registry, and must not pay
// a lookup for one.
func TestNoRegistryMeansNoLookup(t *testing.T) {
	fs := newFakeStore()
	s := NewService(fs, fakeSealer{})

	app, _, err := s.Create(context.Background(), "env_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Update(context.Background(), app.ID, UpdateInput{Name: ptr("web2")}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fs.registryLookups != 0 {
		t.Fatalf("registry lookups = %d, want none for an application that names none", fs.registryLookups)
	}
}

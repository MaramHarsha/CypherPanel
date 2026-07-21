package applications

import (
	"bytes"
	"context"
	"errors"
	"math"
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
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		envs:    map[string]bool{"env_1": true},
		servers: map[string]bool{"srv_1": true},
		apps:    map[string]domain.Application{},
		envVars: map[string][]domain.EnvVar{},
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
	return domain.Environment{ID: id}, nil
}

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	if !f.servers[id] {
		return domain.Server{}, store.ErrNotFound
	}
	return domain.Server{ID: id}, nil
}

func (f *fakeStore) UpsertEnvVar(_ context.Context, appID string, v domain.EnvVar) error {
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
		"empty name":   func(in *CreateInput) { in.Name = "" },
		"bad source":   func(in *CreateInput) { in.Source.Kind = "svn" },
		"empty repo":   func(in *CreateInput) { in.Source.Repo = "" },
		"bad build":    func(in *CreateInput) { in.Build.Kind = "nixpacks" },
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
	keys, err := s.ListEnvVarKeys(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("ListEnvVarKeys: %v", err)
	}
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

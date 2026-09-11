package export

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// fakeStore returns configuration only — which is all the Store interface can
// ask for, and the point of the test below.
type fakeStore struct{}

func (fakeStore) GetProject(context.Context, string) (domain.Project, error) {
	return domain.Project{ID: "prj_1", Slug: "acme-website", Name: "Acme website", TeamID: "tm_1"}, nil
}
func (fakeStore) GetTeam(context.Context, string) (domain.Team, error) {
	return domain.Team{ID: "tm_1", Name: "Acme"}, nil
}
func (fakeStore) ListEnvironmentsByProject(context.Context, string) ([]domain.Environment, error) {
	return []domain.Environment{
		{ID: "env_prod", ProjectID: "prj_1", Name: "production", Kind: "standing"},
		// Excluded: a preview belongs to its pull request (spec §3).
		{ID: "env_pr7", ProjectID: "prj_1", Name: "pr-7", Kind: "preview"},
	}, nil
}
func (fakeStore) ListApplicationConfigsByEnvironment(_ context.Context, envID string) ([]domain.ApplicationConfig, error) {
	if envID != "env_prod" {
		return nil, nil
	}
	return []domain.ApplicationConfig{{
		ID: "app_api", EnvironmentID: envID, Name: "api",
		Source:  domain.AppSource{Kind: "git_url", Repo: "https://github.com/acme/api.git", Branch: "main"},
		Build:   domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime: domain.AppRuntime{ServerID: "srv_1", Port: 8080, Replicas: 2},
		Route:   domain.AppRoute{Domain: "api.acme.com", HTTPS: true},
		Health:  domain.AppHealth{Kind: "http", Path: "/healthz", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
		Volumes: []domain.VolumeMount{{Name: "uploads", Path: "/app/uploads"}},
	}}, nil
}
func (fakeStore) ListDatabaseConfigsByEnvironment(_ context.Context, envID string) ([]domain.DatabaseConfig, error) {
	if envID != "env_prod" {
		return nil, nil
	}
	return []domain.DatabaseConfig{{
		ID: "db_1", EnvironmentID: envID, Name: "postgres", Engine: "postgresql", Version: "16",
		ServerID: "srv_1", InitialDatabase: "acme", VolumeName: "cypher-db-db_1",
		DataPath: "/var/lib/postgresql/data", RootUser: "cypher",
	}}, nil
}
func (fakeStore) ListComposeStacksByEnvironment(context.Context, string) ([]domain.ComposeStack, error) {
	return nil, nil
}
func (fakeStore) ListEnvVarKeys(context.Context, string) ([]domain.EnvVarKey, error) {
	return []domain.EnvVarKey{
		{Key: "DATABASE_URL", SharedRefs: []string{"DB_PASS", "DB_USER"}},
		{Key: "STRIPE_KEY"},
	}, nil
}
func (fakeStore) ListComposeEnvVarKeys(context.Context, string) ([]string, error) { return nil, nil }
func (fakeStore) ListScheduledTasksByApp(context.Context, string) ([]domain.ScheduledTask, error) {
	return []domain.ScheduledTask{{Name: "nightly-prune", Schedule: "0 3 * * *", Command: []string{"bin/rails", "prune"}, Enabled: true}}, nil
}
func (fakeStore) ListSharedVariableKeysByProject(context.Context, string) ([]domain.SharedVariableKey, error) {
	return []domain.SharedVariableKey{{Key: "DB_USER"}}, nil
}
func (fakeStore) GetServer(context.Context, string) (domain.Server, error) {
	return domain.Server{ID: "srv_1", Name: "hetzner-fsn1"}, nil
}
func (fakeStore) LatestComposeRevision(context.Context, string) (domain.ComposeRevision, error) {
	return domain.ComposeRevision{}, nil
}

func archive(t *testing.T) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := New(fakeStore{}, "v0.9.3").WriteTo(context.Background(), &buf, "prj_1"); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", h.Name, err)
		}
		out[h.Name] = string(body)
	}
	return out
}

// The structural promise (spec §4), asserted at compile time rather than by
// reading the code: if someone adds a Store method that returns a ciphertext,
// this fails and says why.
func TestExporterStoreCannotReturnACiphertext(t *testing.T) {
	iface := reflect.TypeOf((*Store)(nil)).Elem()
	// Every domain type that carries sealed bytes. A Store method returning any
	// of them would put a ciphertext in the exporter's hands, which is exactly
	// what this package promises is impossible. There are no exemptions here on
	// purpose: an exemption is how the promise quietly stops being true.
	sealed := map[reflect.Type]string{
		reflect.TypeOf(domain.EnvVar{}):         "carries ValueCT/ValueNonce",
		reflect.TypeOf(domain.SharedVariable{}): "carries ValueCT/ValueNonce",
		reflect.TypeOf(domain.ComposeEnvVar{}):  "carries a sealed value",
		reflect.TypeOf(domain.Application{}):    "carries WebhookSecretCT",
	}
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		for j := 0; j < m.Type.NumOut(); j++ {
			out := m.Type.Out(j)
			for out.Kind() == reflect.Slice || out.Kind() == reflect.Ptr {
				out = out.Elem()
			}
			if why, bad := sealed[out]; bad {
				t.Fatalf("Store.%s returns %s, which %s. The exporter must be structurally unable to hold a sealed value (project-export.md §4) — narrow the interface instead of trusting the caller to ignore the field.", m.Name, out, why)
			}
		}
	}
}

// The behavioural half of the same promise: nothing that looks like a secret
// reaches any file, and the keys that should be there are.
func TestArchiveCarriesKeysNeverValues(t *testing.T) {
	files := archive(t)
	example := files["acme-website/production/env/api.env.example"]
	if example == "" {
		t.Fatal("no env example for the api application")
	}
	for _, key := range []string{"DATABASE_URL", "STRIPE_KEY"} {
		if !strings.Contains(example, key+"=") {
			t.Errorf("env example is missing the key %s", key)
		}
	}
	// Every key line ends at the '=' or at a comment — never with a value.
	for _, line := range strings.Split(example, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		if v != "" && !strings.HasPrefix(strings.TrimSpace(v), "#") {
			t.Errorf("env example assigns a value to %s: %q", k, line)
		}
	}
	// The shared refs are wiring, not secrets, and are recorded as such.
	if !strings.Contains(example, "{{shared.DB_PASS}}") {
		t.Error("env example does not record the shared-variable wiring")
	}
}

// Determinism (spec §5): two exports of an unchanged project are byte-identical,
// which is what makes sha256sum answer "did anything change".
func TestTwoExportsAreByteIdentical(t *testing.T) {
	var a, b bytes.Buffer
	e := New(fakeStore{}, "v0.9.3")
	if err := e.WriteTo(context.Background(), &a, "prj_1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := e.WriteTo(context.Background(), &b, "prj_1"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two exports of an unchanged project differ — something in the archive carries a clock or an unsorted map")
	}
}

func TestPreviewEnvironmentsAreExcluded(t *testing.T) {
	for name := range archive(t) {
		if strings.Contains(name, "pr-7") {
			t.Fatalf("a preview environment was exported: %s", name)
		}
	}
}

// The compose file must carry no proxy labels: the panel drove Traefik from the
// file provider, and the proxy on the far side of the archive is the reader's
// to choose (spec §3.5).
func TestComposeCarriesNoTraefikLabels(t *testing.T) {
	files := archive(t)
	compose := files["acme-website/production/docker-compose.yml"]
	if compose == "" {
		t.Fatal("no compose file for production")
	}
	if strings.Contains(strings.ToLower(compose), "traefik") {
		t.Error("the exported compose file mentions traefik; routing belongs in the README")
	}
	// The database keeps its panel hostname as an alias, so an application's
	// existing connection string still resolves (spec §3.3).
	if !strings.Contains(compose, "cypher-db-db_1") {
		t.Error("the database service has no alias for its panel hostname")
	}
	// The env file it references is deliberately absent, so `docker compose up`
	// fails naming the file rather than starting and crashing service by
	// service (spec §4).
	if _, ok := files["acme-website/production/env/api.env"]; ok {
		t.Error("the archive contains a real env file; only .env.example belongs in it")
	}
}

func TestReadmeLeadsWithWhatIsMissing(t *testing.T) {
	readme := archive(t)["acme-website/README.md"]
	for _, want := range []string{"No secret values", "No data", "No TLS certificates", "No images"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not state %q", want)
		}
	}
	// The routing table the archive cannot express for the reader.
	if !strings.Contains(readme, "api.acme.com") {
		t.Error("README has no routing table")
	}
}

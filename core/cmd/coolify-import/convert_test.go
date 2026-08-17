package main

import (
	"strings"
	"testing"
)

// The importer's whole value is that its refusals are trustworthy: a template
// it accepts is one an operator can install and use. These tests pin both
// halves — what a faithful translation looks like, and what must never
// translate at all.

const composeHeader = `# documentation: https://example.test
# slogan: An example application.
# category: monitoring
# port: 3000

`

func TestConvertSingleApplication(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - BASE_URL=$SERVICE_URL_WEB
      - SECRET=$SERVICE_PASSWORD_64_APP
      - MODE=${MODE:-production}
    volumes:
      - web_data:/var/lib/web
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	if len(tpl.Resources.Applications) != 1 {
		t.Fatalf("applications = %d, want 1", len(tpl.Resources.Applications))
	}
	app := tpl.Resources.Applications[0]
	if app.Image != "acme/web:1.2.3" || app.Port != 3000 || !app.Route {
		t.Errorf("app = %+v, want the routed 3000 image", app)
	}
	if app.Health.Kind != "tcp" {
		t.Errorf("health kind = %q, want tcp — a guessed HTTP path fails every app that redirects", app.Health.Kind)
	}
	if got := app.Env["BASE_URL"]; got != "https://{{domain}}" {
		t.Errorf("BASE_URL = %q, want the install-time domain", got)
	}
	if got := app.Env["SECRET"]; got != "{{secret.32}}" {
		t.Errorf("SECRET = %q, want a 32-byte generated secret", got)
	}
	if got := app.Env["MODE"]; got != "production" {
		t.Errorf("MODE = %q, want the compose default", got)
	}
	if len(app.Volumes) != 1 || app.Volumes[0].Name != "web-data" || app.Volumes[0].Path != "/var/lib/web" {
		t.Errorf("volumes = %+v, want the named volume", app.Volumes)
	}
	if tpl.Category != "monitoring" || tpl.Name != "Example" {
		t.Errorf("metadata = %q/%q", tpl.Category, tpl.Name)
	}
}

func TestConvertRewiresDatabases(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - DATABASE_URL=postgresql://$SERVICE_USER_POSTGRES:$SERVICE_PASSWORD_POSTGRES@postgresql/appdb
      - REDIS_URL=redis://redis:6379
    depends_on:
      - postgresql
      - redis
  postgresql:
    image: postgres:16-alpine
    environment:
      - POSTGRES_USER=$SERVICE_USER_POSTGRES
      - POSTGRES_PASSWORD=$SERVICE_PASSWORD_POSTGRES
      - POSTGRES_DB=appdb
    volumes:
      - pg_data:/var/lib/postgresql/data
  redis:
    image: redis:7.2-alpine
    command: redis-server --appendonly yes
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	if len(tpl.Resources.Databases) != 2 {
		t.Fatalf("databases = %+v, want a db and a cache", tpl.Resources.Databases)
	}
	if d := tpl.Resources.Databases[0]; d.Name != "db" || d.Engine != "postgresql" || d.Version != "16" {
		t.Errorf("database = %+v, want postgresql 16 named db", d)
	}
	if d := tpl.Resources.Databases[1]; d.Name != "cache" || d.Engine != "redis" || d.Version != "7.2" {
		t.Errorf("cache = %+v, want redis 7.2 named cache", d)
	}
	env := tpl.Resources.Applications[0].Env
	want := "postgresql://{{db.db.user}}:{{db.db.password}}@{{db.db.host}}/{{db.db.database}}"
	if env["DATABASE_URL"] != want {
		t.Errorf("DATABASE_URL = %q, want %q", env["DATABASE_URL"], want)
	}
	// The managed cache always requires a password, so a URL upstream wrote
	// without one has to gain it — otherwise the app cannot authenticate.
	if got := env["REDIS_URL"]; got != "redis://:{{db.cache.password}}@{{db.cache.host}}:6379" {
		t.Errorf("REDIS_URL = %q, want credentials injected", got)
	}
}

// A compose service is routinely named after the protocol it speaks, so the
// scheme in `redis://redis:6379` must not be read as a second hostname.
func TestConvertDoesNotRewriteURLSchemes(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - CACHE=redis://redis:6379
      - PW=$SERVICE_PASSWORD_REDIS
  redis:
    image: redis:7-alpine
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	if got := tpl.Resources.Applications[0].Env["CACHE"]; !strings.HasPrefix(got, "redis://") {
		t.Errorf("CACHE = %q, want the redis:// scheme intact", got)
	}
}

func TestConvertRefusals(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{{
		name: "command override",
		body: `services:
  web:
    image: acme/web:1
    command: ["serve", "--flag"]
    environment: [SERVICE_URL_WEB_3000]`,
		want: "overrides the image command",
	}, {
		name: "host mount",
		body: `services:
  web:
    image: acme/web:1
    environment: [SERVICE_URL_WEB_3000]
    volumes: ["./config:/config"]`,
		want: "from the host",
	}, {
		name: "privileged",
		body: `services:
  web:
    image: acme/web:1
    privileged: true
    environment: [SERVICE_URL_WEB_3000]`,
		want: "runs privileged",
	}, {
		name: "published host ports",
		body: `services:
  web:
    image: acme/web:1
    ports: ["8080:8080"]
    environment: [SERVICE_URL_WEB_3000]`,
		want: "publishes fixed host ports",
	}, {
		name: "built from source",
		body: `services:
  web:
    build: .
    environment: [SERVICE_URL_WEB_3000]`,
		want: "built from source",
	}, {
		name: "two routed services",
		body: `services:
  web:
    image: acme/web:1
    environment: [SERVICE_URL_WEB_3000]
  api:
    image: acme/api:1
    environment: [SERVICE_URL_API_4000]`,
		want: "both publish a public URL",
	}, {
		// {{secret.N}} resolves per occurrence, so a value upstream shares
		// between two settings would install as two different secrets.
		name: "generated value reused",
		body: `services:
  web:
    image: acme/web:1
    environment:
      - SERVICE_URL_WEB_3000
      - KEY=$SERVICE_PASSWORD_APP
      - KEY_AGAIN=$SERVICE_PASSWORD_APP`,
		want: "resolves freshly at each use",
	}, {
		name: "unsupported generator shape",
		body: `services:
  web:
    image: acme/web:1
    environment:
      - SERVICE_URL_WEB_3000
      - VAULT=$SERVICE_REALBASE64_32_TOTP`,
		want: "cannot reproduce it",
	}, {
		// Application containers are named per revision, so nothing can be
		// addressed by a stable hostname.
		name: "sibling application by hostname",
		body: `services:
  web:
    image: acme/web:1
    environment:
      - SERVICE_URL_WEB_3000
      - UPSTREAM=http://worker:9000
  worker:
    image: acme/worker:1
    expose: ["9000"]`,
		want: "dials the sibling service",
	}, {
		name: "one-shot job container",
		body: `services:
  web:
    image: acme/web:1
    environment: [SERVICE_URL_WEB_3000]
  migrate:
    image: acme/migrate:1
    restart: "no"
    expose: ["1"]`,
		want: "one-shot job",
	}, {
		name: "cache password cannot reach the app",
		body: `services:
  web:
    image: acme/web:1
    environment:
      - SERVICE_URL_WEB_3000
      - CACHE_HOST=cache
  cache:
    image: redis:7-alpine`,
		want: "password, which managed databases always require",
	}, {
		name: "variable with no default inside a larger value",
		body: `services:
  web:
    image: acme/web:1
    environment:
      - SERVICE_URL_WEB_3000
      - ENDPOINT=https://${TENANT}.example.test/api`,
		want: "the installed value would be malformed",
	}, {
		name: "not listed upstream",
		body: `services:
  web:
    image: acme/web:1
    environment: [SERVICE_URL_WEB_3000]`,
		want: "ignore: true",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			header := composeHeader
			if c.want == "ignore: true" {
				header = "# ignore: true\n" + composeHeader
			}
			_, reasons := convert("example", []byte(header+c.body), nil)
			if !containsSubstring(reasons, c.want) {
				t.Fatalf("reasons = %v, want one containing %q", reasons, c.want)
			}
		})
	}
}

// A refusal lists every reason at once: an operator deciding whether a
// template is worth unblocking needs the whole story, not the first sentence.
func TestConvertReportsEveryReason(t *testing.T) {
	_, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1
    command: ["serve"]
    privileged: true
    environment: [SERVICE_URL_WEB_3000]
`), nil)
	if len(reasons) < 2 {
		t.Fatalf("reasons = %v, want both the command and the privileged flag", reasons)
	}
}

// The oracle stands in for the registry: compose files habitually omit the
// port because Docker reads it from the image, and offline runs must refuse
// rather than guess.
func TestConvertUsesPortOracle(t *testing.T) {
	body := []byte(`# slogan: An example application.
# category: monitoring

services:
  web:
    image: acme/web:1
    environment: [SERVICE_URL_WEB]
`)
	if _, reasons := convert("example", body, nil); !containsSubstring(reasons, "exposes none unambiguously") {
		t.Fatalf("offline reasons = %v, want a refusal", reasons)
	}
	tpl, reasons := convert("example", body, func(string) (int, bool) { return 8080, true })
	if len(reasons) > 0 {
		t.Fatalf("with oracle: refused: %v", reasons)
	}
	if tpl.Resources.Applications[0].Port != 8080 {
		t.Errorf("port = %d, want the image's exposed port", tpl.Resources.Applications[0].Port)
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func TestEngineFor(t *testing.T) {
	cases := []struct {
		image, engine string
		ok            bool
	}{
		{"postgres:16-alpine", "postgresql", true},
		{"docker.io/library/mysql:8.0", "mysql", true},
		{"mariadb:11", "mariadb", true},
		{"mongo:7", "mongodb", true},
		{"redis:7.2-alpine", "redis", true},
		{"valkey/valkey:8", "valkey", true},
		// A fork bundling extensions is not the image the engine matrix would
		// provision, so it stays an application and the template is refused
		// for addressing it by hostname.
		{"pgvector/pgvector:pg16", "", false},
		{"ghcr.io/acme/postgres-ha:1", "", false},
		{"acme/web:1", "", false},
	}
	for _, c := range cases {
		got, ok := engineFor(c.image)
		if ok != c.ok || string(got) != c.engine {
			t.Errorf("engineFor(%q) = %q,%v; want %q,%v", c.image, got, ok, c.engine, c.ok)
		}
	}
}

func TestEngineVersion(t *testing.T) {
	cases := map[string]string{
		"postgres:16-alpine": "16",
		"mysql:8.0":          "8.0",
		"redis:7.2.4":        "7.2",
		"mongo:latest":       "",
		"postgres":           "",
	}
	for image, want := range cases {
		if got := engineVersion(image); got != want {
			t.Errorf("engineVersion(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestNamedVolume(t *testing.T) {
	cases := []struct {
		spec, name, path string
		ok               bool
	}{
		{"data:/var/lib/data", "data", "/var/lib/data", true},
		{"app_data:/data:rw", "app-data", "/data", true},
		{"./config:/config", "", "", false},
		{"/etc/localtime:/etc/localtime:ro", "", "", false},
		{"${DATA_DIR}:/data", "", "", false},
		{"data", "", "", false},
		{"data:relative/path", "", "", false},
	}
	for _, c := range cases {
		name, path, ok := namedVolume(c.spec)
		if ok != c.ok || name != c.name || path != c.path {
			t.Errorf("namedVolume(%q) = %q,%q,%v; want %q,%q,%v", c.spec, name, path, ok, c.name, c.path, c.ok)
		}
	}
}

func TestCategoryAndDescription(t *testing.T) {
	if got := category("productivity"); got != "other" {
		t.Errorf("category(productivity) = %q, want other", got)
	}
	if got := category("devtools, git"); got != "dev-tools" {
		t.Errorf("category(devtools) = %q, want dev-tools", got)
	}
	long := strings.Repeat("word ", 60)
	got := description(long)
	if len(got) > 200 {
		t.Errorf("description length = %d, want ≤200", len(got))
	}
	if strings.HasSuffix(got, "wor…") {
		t.Errorf("description = %q, want a cut at a word boundary", got)
	}
	if got := description(`"Quoted slogan."`); got != "Quoted slogan." {
		t.Errorf("description = %q, want the quotes stripped", got)
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"uptime-kuma":            "Uptime Kuma",
		"n8n":                    "n8n",
		"my-api-tool":            "My API Tool",
		"wordpress-with-mysql":   "WordPress with MySQL",
		"freshrss-with-postgres": "FreshRSS with PostgreSQL",
	}
	for slug, want := range cases {
		if got := displayName(slug); got != want {
			t.Errorf("displayName(%q) = %q, want %q", slug, got, want)
		}
	}
}

// MySQL and MariaDB stacks were refused wholesale until managed databases
// could create a named application database. They convert now, and their DSN
// resolves through the placeholder exactly as PostgreSQL's does.
func TestConvertMySQLBackedStack(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - DATABASE_URL=mysql://root:$SERVICE_PASSWORD_MYSQL@mysql:3306/${MYSQL_DATABASE:-appdb}
  mysql:
    image: mysql:8.4
    environment:
      - MYSQL_ROOT_PASSWORD=$SERVICE_PASSWORD_MYSQL
      - MYSQL_DATABASE=${MYSQL_DATABASE:-appdb}
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	if len(tpl.Resources.Databases) != 1 || tpl.Resources.Databases[0].Engine != "mysql" {
		t.Fatalf("databases = %+v, want one mysql", tpl.Resources.Databases)
	}
	want := "mysql://root:{{db.db.password}}@{{db.db.host}}:3306/{{db.db.database}}"
	if got := tpl.Resources.Applications[0].Env["DATABASE_URL"]; got != want {
		t.Fatalf("DATABASE_URL = %q, want %q", got, want)
	}
}

// A cache's path segment is a database *number*, not a name — rewriting it
// would point the application at something the protocol cannot address.
func TestConvertLeavesCacheDatabaseNumberAlone(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - REDIS_URL=redis://redis:6379/0
      - PW=$SERVICE_PASSWORD_REDIS
  redis:
    image: redis:7-alpine
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	if got := tpl.Resources.Applications[0].Env["REDIS_URL"]; !strings.HasSuffix(got, ":6379/0") {
		t.Fatalf("REDIS_URL = %q, want the database number intact", got)
	}
}

// Coolify's MySQL templates give the application a scoped database user named
// after the app — `MYSQL_USER=$SERVICE_USER_WORDPRESS`. A managed database
// creates no such user, so those references have to resolve to the root
// credentials it does have, and the literal database name has to resolve
// through the placeholder.
func TestConvertMapsScopedDatabaseUserToRoot(t *testing.T) {
	tpl, reasons := convert("example", []byte(composeHeader+`services:
  web:
    image: acme/web:1.2.3
    environment:
      - SERVICE_URL_WEB_3000
      - DB_HOST=mysql
      - DB_USER=$SERVICE_USER_MYAPP
      - DB_PASSWORD=$SERVICE_PASSWORD_MYAPP
      - DB_NAME=myapp
  mysql:
    image: mysql:8
    environment:
      - MYSQL_ROOT_PASSWORD=$SERVICE_PASSWORD_ROOT
      - MYSQL_DATABASE=myapp
      - MYSQL_USER=$SERVICE_USER_MYAPP
      - MYSQL_PASSWORD=$SERVICE_PASSWORD_MYAPP
`), nil)
	if len(reasons) > 0 {
		t.Fatalf("refused: %v", reasons)
	}
	env := tpl.Resources.Applications[0].Env
	want := map[string]string{
		"DB_HOST":     "{{db.db.host}}",
		"DB_USER":     "{{db.db.user}}",
		"DB_PASSWORD": "{{db.db.password}}",
		"DB_NAME":     "{{db.db.database}}",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

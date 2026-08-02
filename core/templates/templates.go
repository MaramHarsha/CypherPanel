package templates

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
)

//go:embed catalog/*.yaml
var catalogFS embed.FS

// AppService is the slice of the applications service an install consumes
// (consumer-defined interface, ENGINEERING rule 6).
type AppService interface {
	Create(ctx context.Context, envID string, in applications.CreateInput) (domain.Application, string, error)
	Get(ctx context.Context, id string) (domain.Application, error)
	List(ctx context.Context, envID string) ([]domain.Application, error)
	Delete(ctx context.Context, id string) error
}

// DbService is the slice of the databases service an install consumes.
type DbService interface {
	Create(ctx context.Context, envID string, in databases.CreateInput) (domain.Database, string, error)
	List(ctx context.Context, envID string) ([]domain.Database, error)
	Delete(ctx context.Context, id string, deleteVolume bool) error
}

// Deployer starts pipelines and publishes desired absence — the same calls
// the REST handlers make, so a templated resource lives and dies exactly like
// a hand-made one.
type Deployer interface {
	Deploy(ctx context.Context, appID, trigger, ref string) (domain.Deployment, error)
	RemoveApp(ctx context.Context, serverID, appID string) error
}

// Service owns the embedded catalog and installs templates.
type Service struct {
	apps     AppService
	dbs      DbService
	deployer Deployer
	log      *slog.Logger

	catalog []Template // sorted by slug
	bySlug  map[string]Template
}

// New loads and validates the embedded catalog. An invalid bundled template
// is a programming error: it fails construction (and the catalog unit test),
// never a runtime surprise.
func New(apps AppService, dbs DbService, deployer Deployer, log *slog.Logger) (*Service, error) {
	s := &Service{apps: apps, dbs: dbs, deployer: deployer, log: log, bySlug: map[string]Template{}}
	entries, err := catalogFS.ReadDir("catalog")
	if err != nil {
		return nil, fmt.Errorf("templates: reading embedded catalog: %w", err)
	}
	for _, e := range entries {
		data, err := catalogFS.ReadFile("catalog/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("templates: reading %s: %w", e.Name(), err)
		}
		t, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("templates: %s: %w", e.Name(), err)
		}
		if _, dup := s.bySlug[t.Slug]; dup {
			return nil, fmt.Errorf("templates: duplicate slug %q", t.Slug)
		}
		s.bySlug[t.Slug] = t
		s.catalog = append(s.catalog, t)
	}
	sort.Slice(s.catalog, func(i, j int) bool { return s.catalog[i].Slug < s.catalog[j].Slug })
	return s, nil
}

// List returns the whole catalog (static content; callers render summaries).
func (s *Service) List() []Template { return s.catalog }

// Get returns one template by slug.
func (s *Service) Get(slug string) (Template, bool) {
	t, ok := s.bySlug[slug]
	return t, ok
}

// InstallInput is the operator's install-time choice (template-catalog.md §4).
type InstallInput struct {
	EnvironmentID string
	ServerID      string
	Domain        string // taken by the template's route:true app, if any
	Name          string // resource name prefix; defaults to the slug
}

// InstallResult lists what an install created, in creation order.
type InstallResult struct {
	ApplicationIDs []string
	DatabaseIDs    []string
}

// ValidationError marks operator-correctable install failures (bad name,
// collision); handlers map it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ErrNotFound is returned for an unknown slug.
var ErrNotFound = errors.New("templates: template not found")

// PartialInstallError reports an install that failed *and* could not fully roll
// itself back. Remaining names the resources that survived, because an operator
// cannot clean up what they cannot identify — those rows still hold names,
// volumes, and ports.
type PartialInstallError struct {
	Cause     error
	Remaining []string
}

func (e *PartialInstallError) Error() string {
	return fmt.Sprintf("%v (rollback incomplete, these resources remain: %s)",
		e.Cause, strings.Join(e.Remaining, ", "))
}

func (e *PartialInstallError) Unwrap() error { return e.Cause }

// cleanupTimeout bounds rollback of a failed install. Generous enough for a
// handful of deletes, short enough that a wedged daemon cannot hold the
// request path open indefinitely.
const cleanupTimeout = 30 * time.Second

// needsDomain reports whether this template cannot be installed without one —
// either because an application takes the public route, or because some value
// interpolates {{domain}} and would otherwise resolve to an empty string.
func (t Template) needsDomain() bool {
	for _, a := range t.Resources.Applications {
		if a.Route {
			return true
		}
		for _, v := range a.Env {
			if strings.Contains(v, "{{domain}}") {
				return true
			}
		}
	}
	return false
}

// Install resolves a template into ordinary resources: databases first (their
// root passwords exist in plaintext only inside this call), then image-source
// applications with sealed env vars, then one deploy per application. On a
// mid-install failure everything created by this call is best-effort deleted
// in reverse and the underlying error returned (template-catalog.md §4).
func (s *Service) Install(ctx context.Context, slug string, in InstallInput) (InstallResult, error) {
	tpl, ok := s.bySlug[slug]
	if !ok {
		return InstallResult{}, ErrNotFound
	}
	base := in.Name
	if base == "" {
		base = tpl.Slug
	}
	if !slugRe.MatchString(base) {
		return InstallResult{}, &ValidationError{Msg: "name must be lowercase [a-z0-9-], ≤40 chars"}
	}
	in.Domain = strings.TrimSpace(in.Domain)
	// A template that routes, or that builds URLs from {{domain}}, cannot be
	// installed without one: resolving the placeholder to "" would write
	// settings like `https:///` into the container and report success. Refuse
	// up front rather than produce a running-but-misconfigured app.
	if in.Domain == "" && tpl.needsDomain() {
		return InstallResult{}, &ValidationError{Msg: "this template needs a domain: it publishes a public URL"}
	}

	// Collision check before creating anything: every name this install will
	// use must be free in the environment.
	names := map[string]bool{}
	for _, d := range tpl.Resources.Databases {
		names[base+"-"+d.Name] = true
	}
	for _, a := range tpl.Resources.Applications {
		names[appName(tpl, base, a)] = true
	}
	existingApps, err := s.apps.List(ctx, in.EnvironmentID)
	if err != nil {
		return InstallResult{}, fmt.Errorf("templates: listing applications: %w", err)
	}
	for _, a := range existingApps {
		if names[a.Name] {
			return InstallResult{}, &ValidationError{Msg: fmt.Sprintf("name %q is already used in this environment", a.Name)}
		}
	}
	existingDbs, err := s.dbs.List(ctx, in.EnvironmentID)
	if err != nil {
		return InstallResult{}, fmt.Errorf("templates: listing databases: %w", err)
	}
	for _, d := range existingDbs {
		if names[d.Name] {
			return InstallResult{}, &ValidationError{Msg: fmt.Sprintf("name %q is already used in this environment", d.Name)}
		}
	}

	var res InstallResult
	fail := func(cause error) (InstallResult, error) {
		// Roll back on a context detached from the request's: an install that
		// failed *because* the client disconnected or the deadline expired
		// would otherwise cancel every cleanup call immediately and strand
		// exactly the resources it was meant to remove.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if left := s.cleanup(cleanupCtx, res); len(left) > 0 {
			// The caller must be able to see what the failed install left
			// behind — otherwise orphaned resources are invisible and
			// unidentifiable (they still hold names, volumes, and ports). A
			// typed error carries the ids all the way to the HTTP response
			// instead of being flattened into a generic 500.
			return InstallResult{}, &PartialInstallError{Cause: cause, Remaining: left}
		}
		return InstallResult{}, cause
	}

	// 1. Databases. RequirePassword always: placeholder resolution must never
	// hand an application an empty credential (template-catalog.md §2).
	dbInfos := map[string]dbInfo{}
	for _, d := range tpl.Resources.Databases {
		created, rootPwd, err := s.dbs.Create(ctx, in.EnvironmentID, databases.CreateInput{
			Name:            base + "-" + d.Name,
			Engine:          d.Engine,
			Version:         d.Version,
			ServerID:        in.ServerID,
			RequirePassword: true,
		})
		// The row can be persisted and the call still fail (reconciliation is
		// triggered inside Create), so record any id we were handed *before*
		// failing — otherwise cleanup cannot reach it and the install strands a
		// database whose generated password was never returned to anyone.
		if created.ID != "" {
			res.DatabaseIDs = append(res.DatabaseIDs, created.ID)
		}
		if err != nil {
			return fail(operatorError(fmt.Errorf("templates: creating database %s: %w", d.Name, err), err))
		}
		dbInfos[d.Name] = infoFor(created, rootPwd)
	}

	// 2. Applications: resolve placeholders, create as image-source apps.
	var apps []domain.Application
	for _, a := range tpl.Resources.Applications {
		env := make(map[string]string, len(a.Env))
		for k, v := range a.Env {
			rv, err := resolve(v, dbInfos, in.Domain, newSecret)
			if err != nil {
				return fail(err)
			}
			env[k] = rv
		}
		route := domain.AppRoute{}
		if a.Route && in.Domain != "" {
			route = domain.AppRoute{Domain: in.Domain, HTTPS: true}
		}
		health := domain.AppHealth{Kind: a.Health.Kind, Path: a.Health.Path}
		var vols []domain.VolumeMount
		for _, v := range a.Volumes {
			vols = append(vols, domain.VolumeMount{Name: v.Name, Path: v.Path})
		}
		var ports []domain.PortMapping
		for _, p := range a.Ports {
			ports = append(ports, domain.PortMapping{HostPort: p.Host, ContainerPort: p.Container, Protocol: p.Protocol})
		}
		created, _, err := s.apps.Create(ctx, in.EnvironmentID, applications.CreateInput{
			Name:    appName(tpl, base, a),
			Source:  domain.AppSource{Kind: "image", Image: a.Image},
			Runtime: domain.AppRuntime{ServerID: in.ServerID, Port: a.Port},
			Route:   route,
			Health:  health,
			Volumes: vols,
			Ports:   ports,
			EnvVars: env,
		})
		// Same reasoning as databases: track anything persisted before failing.
		if created.ID != "" {
			res.ApplicationIDs = append(res.ApplicationIDs, created.ID)
		}
		if err != nil {
			return fail(operatorError(fmt.Errorf("templates: creating application %s: %w", a.Name, err), err))
		}
		apps = append(apps, created)
	}

	// 3. Deploys — the ordinary pipeline; image sources go straight to rollout.
	for _, app := range apps {
		if _, err := s.deployer.Deploy(ctx, app.ID, "manual", ""); err != nil {
			return fail(fmt.Errorf("templates: deploying %s: %w", app.Name, err))
		}
	}
	return res, nil
}

// operatorError reclassifies a resource-service failure the operator can fix —
// an unknown environment or server, or a rejected field — as a ValidationError
// so the handler answers 400 with the reason instead of a blank 500. Anything
// else keeps its wrapped form and stays a server error.
func operatorError(wrapped, cause error) error {
	var appVE *applications.ValidationError
	var dbVE *databases.ValidationError
	switch {
	case errors.Is(cause, applications.ErrServerNotFound), errors.Is(cause, databases.ErrServerNotFound):
		return &ValidationError{Msg: "server not found"}
	case errors.Is(cause, applications.ErrEnvironmentNotFound), errors.Is(cause, databases.ErrEnvironmentNotFound):
		return &ValidationError{Msg: "environment not found"}
	case errors.As(cause, &appVE):
		return &ValidationError{Msg: appVE.Msg}
	case errors.As(cause, &dbVE):
		return &ValidationError{Msg: dbVE.Msg}
	}
	return wrapped
}

// cleanup best-effort deletes what a failed install created, reverse order,
// matching the DELETE handlers' semantics (row + desired absence). Failures
// are logged, not returned — the install's root cause is the error that
// matters, and the operator can see whatever survived.
// It returns the ids it could not remove, so the caller can tell the operator
// exactly what survived a failed install.
func (s *Service) cleanup(ctx context.Context, res InstallResult) []string {
	var left []string
	for i := len(res.ApplicationIDs) - 1; i >= 0; i-- {
		id := res.ApplicationIDs[i]
		serverID := ""
		if a, err := s.apps.Get(ctx, id); err == nil {
			serverID = a.Runtime.ServerID
		}
		if err := s.apps.Delete(ctx, id); err != nil {
			s.log.Error("template install cleanup: deleting application", "app_id", id, "error", err)
			left = append(left, id)
			continue
		}
		if serverID != "" {
			// Same as the DELETE handler: publish desired absence; the periodic
			// sync converges it anyway if this fails.
			_ = s.deployer.RemoveApp(ctx, serverID, id)
		}
	}
	for i := len(res.DatabaseIDs) - 1; i >= 0; i-- {
		// The volume is minutes old and belongs to this failed install: delete it.
		if err := s.dbs.Delete(ctx, res.DatabaseIDs[i], true); err != nil {
			s.log.Error("template install cleanup: deleting database", "db_id", res.DatabaseIDs[i], "error", err)
			left = append(left, res.DatabaseIDs[i])
		}
	}
	return left
}

// appName gives a single-app template the install name itself; multi-app
// templates suffix each app (template-catalog.md §4).
func appName(tpl Template, base string, a TplApplication) string {
	if len(tpl.Resources.Applications) == 1 {
		return base
	}
	return base + "-" + a.Name
}

// infoFor derives the placeholder view of a created database: deterministic
// container DNS name on the shared environment network, engine port/user, and
// an engine-shaped URL (template-catalog.md §2).
func infoFor(d domain.Database, rootPwd string) dbInfo {
	info := dbInfo{
		host:     "cypher-db-" + d.ID,
		user:     d.RootUser,
		password: rootPwd,
		port:     enginePort(d.Engine),
		database: engineDefaultDB(d.Engine),
	}
	info.url = engineURL(d.Engine, info)
	return info
}

// enginePort mirrors the REST connection-info handler's canonical ports.
func enginePort(e domain.DbEngine) int {
	switch e {
	case domain.EnginePostgreSQL:
		return 5432
	case domain.EngineMySQL, domain.EngineMariaDB:
		return 3306
	case domain.EngineMongoDB:
		return 27017
	case domain.EngineRedis, domain.EngineValkey:
		return 6379
	}
	return 0
}

func engineDefaultDB(e domain.DbEngine) string {
	if e == domain.EnginePostgreSQL {
		return "postgres"
	}
	return ""
}

func engineURL(e domain.DbEngine, i dbInfo) string {
	hostport := fmt.Sprintf("%s:%d", i.host, i.port)
	switch e {
	case domain.EnginePostgreSQL:
		return fmt.Sprintf("postgres://%s:%s@%s/%s", i.user, i.password, hostport, i.database)
	case domain.EngineMySQL, domain.EngineMariaDB:
		return fmt.Sprintf("mysql://%s:%s@%s/", i.user, i.password, hostport)
	case domain.EngineMongoDB:
		return fmt.Sprintf("mongodb://%s:%s@%s/", i.user, i.password, hostport)
	case domain.EngineRedis, domain.EngineValkey:
		return fmt.Sprintf("redis://:%s@%s", i.password, hostport)
	}
	return ""
}

// newSecret returns n random bytes hex-encoded (2n characters).
func newSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("templates: generating secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

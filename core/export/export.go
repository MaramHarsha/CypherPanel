// Package export writes a project out as a portable archive
// (project-export.md). Leaving is deliberately easy: the archive runs anywhere
// Docker runs, so an operator is never held by the panel's data model.
//
// THE STRUCTURAL PROMISE (spec §4). This package cannot export a secret,
// because nothing in scope can unseal one: the Store interface below has no
// method that returns a ciphertext and this package has no dependency on
// secret.Box's Opener. That is why it deliberately does NOT reuse the
// scheduler's buildSpec, which would otherwise have been the obvious code
// reuse — buildSpec unseals every env var and expands shared variables,
// because an AppSpec on the wire to an agent carries plaintext env by design.
// Reusing it would have put every secret in the project one serialization
// mistake away from a download.
//
// DETERMINISM (spec §5). Two exports of an unchanged project are
// byte-identical: fixed entry order, zeroed mtime and uid/gid, sorted map keys,
// and no clock anywhere inside the archive. That makes `sha256sum` answer "did
// anything change", and makes golden-file tests possible at all.
package export

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Store is the read surface an export needs — and no more. Every method here
// returns configuration; none returns a sealed value. Adding one that did
// would break the guarantee this package's doc comment makes, so don't.
type Store interface {
	GetProject(ctx context.Context, id string) (domain.Project, error)
	GetTeam(ctx context.Context, id string) (domain.Team, error)
	ListEnvironmentsByProject(ctx context.Context, projectID string) ([]domain.Environment, error)
	ListApplicationConfigsByEnvironment(ctx context.Context, envID string) ([]domain.ApplicationConfig, error)
	ListDatabaseConfigsByEnvironment(ctx context.Context, envID string) ([]domain.DatabaseConfig, error)
	ListComposeStacksByEnvironment(ctx context.Context, envID string) ([]domain.ComposeStack, error)
	// Keys only — see the package comment.
	ListEnvVarKeys(ctx context.Context, appID string) ([]domain.EnvVarKey, error)
	ListComposeEnvVarKeys(ctx context.Context, stackID string) ([]string, error)
	ListScheduledTasksByApp(ctx context.Context, appID string) ([]domain.ScheduledTask, error)
	ListSharedVariableKeysByProject(ctx context.Context, projectID string) ([]domain.SharedVariableKey, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
	LatestComposeRevision(ctx context.Context, stackID string) (domain.ComposeRevision, error)
}

// Exporter builds archives. It holds no key material by construction.
type Exporter struct {
	store   Store
	version string // the cypherd build, recorded in the manifest
}

func New(store Store, version string) *Exporter {
	return &Exporter{store: store, version: version}
}

// gathered is everything the archive needs, read before a single byte is
// written. Spec §5: once the first byte is on the wire the status code is
// spent, so every operation that can fail happens here, in a phase that can
// still answer with one.
type gathered struct {
	project domain.Project
	team    domain.Team
	envs    []envData
	shared  []domain.SharedVariableKey
}

type envData struct {
	env    domain.Environment
	apps   []appData
	dbs    []domain.DatabaseConfig
	stacks []stackData
}

type appData struct {
	app      domain.ApplicationConfig
	server   domain.Server
	envKeys  []domain.EnvVarKey
	tasks    []domain.ScheduledTask
	hasImage bool
}

type stackData struct {
	stack   domain.ComposeStack
	envKeys []string
	file    string
}

// Filename is the archive's name: the project's immutable slug plus the date,
// so a rename does not change it. The date is passed in rather than read from
// a clock — nothing inside the archive carries one (§5), and this is the one
// place a date belongs.
func Filename(slug, date string) string {
	if slug == "" {
		slug = "project"
	}
	return fmt.Sprintf("%s-%s.tar.gz", slug, date)
}

// WriteTo gathers the project and streams it as a gzipped tar. It is never
// buffered and never spooled to disk: vision non-negotiable 1 is a control
// plane under 300 MB RSS, and a feature whose memory cost is "one operator's
// largest project" decides that budget for everyone.
//
// A write failure after the first byte truncates the archive, and a truncated
// gzip fails its own CRC and length trailer — so a partial download is refused
// loudly by tar rather than opening as a smaller, plausible-looking, wrong
// archive.
func (e *Exporter) WriteTo(ctx context.Context, w io.Writer, projectID string) error {
	g, err := e.gather(ctx, projectID)
	if err != nil {
		return err
	}

	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("export: gzip writer: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	root := g.project.Slug
	if root == "" {
		root = g.project.ID
	}

	files := e.render(g, root)
	// Fixed order, so two exports of an unchanged project are byte-identical.
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	for _, f := range files {
		if err := writeFile(tw, f.name, f.body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("export: closing tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("export: closing gzip: %w", err)
	}
	return nil
}

type file struct {
	name string
	body string
}

// writeFile emits one entry with every clock and ownership field zeroed, which
// is what makes the archive reproducible (§5).
func writeFile(tw *tar.Writer, name, body string) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:   name,
		Mode:   0o644,
		Size:   int64(len(body)),
		Format: tar.FormatUSTAR,
	}); err != nil {
		return fmt.Errorf("export: tar header for %s: %w", name, err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		return fmt.Errorf("export: writing %s: %w", name, err)
	}
	return nil
}

func (e *Exporter) gather(ctx context.Context, projectID string) (gathered, error) {
	var g gathered
	var err error
	if g.project, err = e.store.GetProject(ctx, projectID); err != nil {
		return g, fmt.Errorf("export: project: %w", err)
	}
	// A team that has gone missing is not a reason to refuse an export — the
	// manifest records what it can and the archive is still portable.
	g.team, _ = e.store.GetTeam(ctx, g.project.TeamID)

	if g.shared, err = e.store.ListSharedVariableKeysByProject(ctx, projectID); err != nil {
		return g, fmt.Errorf("export: shared variables: %w", err)
	}
	envs, err := e.store.ListEnvironmentsByProject(ctx, projectID)
	if err != nil {
		return g, fmt.Errorf("export: environments: %w", err)
	}
	for _, env := range envs {
		// Preview environments are excluded (§3): a preview belongs to its pull
		// request and is destroyed by a close or a TTL sweep, so exporting one
		// produces services whose reason to exist expired before the download
		// finished.
		if env.Kind == "preview" {
			continue
		}
		ed := envData{env: env}

		apps, err := e.store.ListApplicationConfigsByEnvironment(ctx, env.ID)
		if err != nil {
			return g, fmt.Errorf("export: applications: %w", err)
		}
		for _, app := range apps {
			ad := appData{app: app, hasImage: app.Source.Kind == "image"}
			ad.server, _ = e.store.GetServer(ctx, app.Runtime.ServerID)
			if ad.envKeys, err = e.store.ListEnvVarKeys(ctx, app.ID); err != nil {
				return g, fmt.Errorf("export: env var keys: %w", err)
			}
			if ad.tasks, err = e.store.ListScheduledTasksByApp(ctx, app.ID); err != nil {
				return g, fmt.Errorf("export: scheduled tasks: %w", err)
			}
			ed.apps = append(ed.apps, ad)
		}

		if ed.dbs, err = e.store.ListDatabaseConfigsByEnvironment(ctx, env.ID); err != nil {
			return g, fmt.Errorf("export: databases: %w", err)
		}

		stacks, err := e.store.ListComposeStacksByEnvironment(ctx, env.ID)
		if err != nil {
			return g, fmt.Errorf("export: compose stacks: %w", err)
		}
		for _, st := range stacks {
			sd := stackData{stack: st}
			if sd.envKeys, err = e.store.ListComposeEnvVarKeys(ctx, st.ID); err != nil {
				return g, fmt.Errorf("export: compose env var keys: %w", err)
			}
			// A stack with no revision yet has no file to copy, which is a
			// normal state rather than a failure.
			if rev, ferr := e.store.LatestComposeRevision(ctx, st.ID); ferr == nil {
				sd.file = rev.ComposeYAML
			}
			ed.stacks = append(ed.stacks, sd)
		}

		g.envs = append(g.envs, ed)
	}
	return g, nil
}

// ─── rendering ──────────────────────────────────────────────────────────────

func (e *Exporter) render(g gathered, root string) []file {
	files := []file{
		{name: root + "/README.md", body: e.readme(g)},
		{name: root + "/cypherpanel.yaml", body: e.manifest(g)},
	}
	for _, ed := range g.envs {
		dir := root + "/" + slug(ed.env.Name)
		files = append(files, file{name: dir + "/docker-compose.yml", body: e.compose(ed)})
		for _, ad := range ed.apps {
			files = append(files, file{
				name: dir + "/env/" + slug(ad.app.Name) + ".env.example",
				body: appEnvExample(ad),
			})
		}
		for _, db := range ed.dbs {
			files = append(files, file{
				name: dir + "/env/" + slug(db.Name) + ".env.example",
				body: dbEnvExample(db),
			})
		}
		for _, sd := range ed.stacks {
			if sd.file != "" {
				files = append(files, file{name: dir + "/stacks/" + slug(sd.stack.Name) + ".yml", body: sd.file})
			}
			if len(sd.envKeys) > 0 {
				files = append(files, file{
					name: dir + "/env/" + slug(sd.stack.Name) + ".env.example",
					body: stackEnvExample(sd),
				})
			}
		}
	}
	return files
}

// slug makes a filesystem-safe name. Resource names are already constrained,
// but an archive is extracted on machines with their own opinions.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	return out
}

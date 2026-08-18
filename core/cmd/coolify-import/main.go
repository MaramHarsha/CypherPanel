// coolify-import translates Coolify's compose template library into native
// CypherPanel catalog templates (docs/features/template-catalog.md §6).
//
// It is a build-time tool, not a runtime code path (ADR-007 §Decision 2). The
// panel never reads a compose file: this runs once, its output is reviewed and
// committed like a hand-written template, and the catalog unit test is the
// gate. That separation is the whole point — arbitrary compose is not
// expressible as desired state, so the translation happens where a human can
// read the result, and everything that will not translate is refused loudly
// with every reason listed rather than approximated.
//
// Coolify's templates are Apache-2.0 configuration data under
// `../coolify/templates/compose`, read read-only (CLAUDE.md rule 1). What is
// ported is the magic-env convention and the per-application settings — facts
// about how upstream software is configured — not code.
//
// Usage:
//
//	coolify-import -out core/templates/catalog ../coolify/templates/compose/*.yaml
//	coolify-import -report import-report.md ../coolify/templates/compose/*.yaml
//	coolify-import -pin -out core/templates/catalog ...   # resolve image tags
//
// Without -out nothing is written: the run only reports what would convert.
// With -pin every image is resolved against its registry and rewritten to an
// immutable reference, which is the only way a bundled template can mean the
// same thing on every panel running a given release.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/core/templates"
)

func main() {
	out := flag.String("out", "", "directory to write converted templates into (default: convert only, write nothing)")
	report := flag.String("report", "", "write a Markdown conversion report to this path")
	pin := flag.Bool("pin", false, "resolve every image against its registry and pin it to an immutable reference")
	cache := flag.String("cache", "", "reuse and update registry answers in this JSON file, so a re-run costs no requests")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall deadline for -pin registry lookups")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "coolify-import: no input files")
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, *report, *pin, *cache, *timeout, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "coolify-import: %v\n", err)
		os.Exit(1)
	}
}

// outcome is one source template's result: exactly one of tpl or reasons.
type outcome struct {
	slug    string
	tpl     templates.Template
	reasons []string
}

func run(outDir, reportPath string, pin bool, cachePath string, timeout time.Duration, paths []string) error {
	sort.Strings(paths)
	sources := make(map[string][]byte, len(paths))
	var slugs []string
	for _, p := range paths {
		src, err := os.ReadFile(p) //nolint:gosec // operator-supplied reference paths
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		slug := templateSlug(p)
		sources[slug] = src
		slugs = append(slugs, slug)
	}

	// Without -pin the conversion is entirely offline and images keep whatever
	// reference upstream wrote — useful for reviewing the mapping, never for
	// producing a catalog.
	ctx := context.Background()
	oracle := portOracle(nil)
	var reg *registry
	if pin {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		reg = newRegistry(loadCache(cachePath))

		// Reading an image's EXPOSE costs metered registry requests, and the
		// conversion pass is sequential — so a rate-limited lookup would stall
		// every template behind it. A dry pass first asks which images a port
		// would actually help, then those are resolved concurrently: the quota
		// is per-client, so waiting for it in parallel is strictly better than
		// waiting for it one image at a time.
		wanted := portsWanted(sources, slugs)
		fmt.Fprintf(os.Stderr, "reading EXPOSE for %d images with no declared port\n", len(wanted))
		warmPorts(ctx, reg, wanted)

		oracle = func(image string) (int, bool) { return reg.portFor(ctx, image) }
	}

	var results []outcome
	for _, slug := range slugs {
		tpl, reasons := convert(slug, sources[slug], oracle)
		r := outcome{slug: slug, tpl: tpl, reasons: reasons}
		if reg != nil && len(r.reasons) == 0 {
			applyPins(ctx, &r, reg)
		}
		results = append(results, r)
	}

	converted := 0
	for _, r := range results {
		if len(r.reasons) == 0 {
			converted++
		}
	}
	fmt.Fprintf(os.Stderr, "converted %d of %d templates\n", converted, len(results))

	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", outDir, err)
		}
		kept := 0
		for i := range results {
			r := &results[i]
			if len(r.reasons) > 0 {
				continue
			}
			path := filepath.Join(outDir, r.slug+".yaml")
			// A hand-written entry outranks a generated one: it was curated —
			// a real health path instead of a TCP probe, a description someone
			// wrote — and the importer has no way to reproduce that. Leave it
			// alone and say so, rather than silently regressing the catalog.
			if handWritten(path) {
				r.reasons = append(r.reasons,
					"a hand-written catalog entry already claims this slug; the curated one is kept")
				kept++
				continue
			}
			// Validate once more: pinning rewrote image references, and an
			// unwritable template must never reach the catalog directory.
			if err := r.tpl.Validate(); err != nil {
				return fmt.Errorf("%s: %w", r.slug, err)
			}
			body, err := render(r.tpl)
			if err != nil {
				return fmt.Errorf("%s: %w", r.slug, err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
		}
		if kept > 0 {
			fmt.Fprintf(os.Stderr, "kept %d hand-written templates the import would have replaced\n", kept)
		}
	}

	if reportPath != "" {
		if err := os.WriteFile(reportPath, renderReport(results), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", reportPath, err)
		}
	}
	return nil
}

// templateSlug takes the slug from the file name, which is what Coolify keys
// its own catalog by.
func templateSlug(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// portsWanted converts everything once with an oracle that answers nothing and
// records what it was asked. Its answers are discarded; the questions are the
// point.
//
// A question only counts when the missing port is the *whole* reason the
// template was refused. Most templates that leave a port undeclared also
// override a command or mount a host path, and no registry answer rescues
// those — asking anyway would spend a rate-limited quota on templates that
// cannot ship either way.
func portsWanted(sources map[string][]byte, slugs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, slug := range slugs {
		var asked []string
		_, reasons := convert(slug, sources[slug], func(image string) (int, bool) {
			asked = append(asked, image)
			return 0, false
		})
		if len(asked) == 0 || !onlyMissingPorts(reasons) {
			continue
		}
		for _, image := range asked {
			if !seen[image] {
				seen[image] = true
				out = append(out, image)
			}
		}
	}
	sort.Strings(out)
	return out
}

func onlyMissingPorts(reasons []string) bool {
	for _, r := range reasons {
		if !strings.Contains(r, "exposes none unambiguously") {
			return false
		}
	}
	return len(reasons) > 0
}

// emitted mirrors templates.Template purely to control the YAML we write: the
// schema's optional fields are meaningful when absent, and encoding the Go
// zero value would put `version: ""` and `ports: []` in every catalog file.
type emitted struct {
	Schema      string `yaml:"schema"`
	Slug        string `yaml:"slug"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Category    string `yaml:"category"`
	Version     string `yaml:"version,omitempty"`
	Resources   struct {
		Databases    []emittedDatabase    `yaml:"databases,omitempty"`
		Applications []emittedApplication `yaml:"applications"`
	} `yaml:"resources"`
}

type emittedDatabase struct {
	Name    string `yaml:"name"`
	Engine  string `yaml:"engine"`
	Version string `yaml:"version,omitempty"`
}

type emittedApplication struct {
	Name    string            `yaml:"name"`
	Image   string            `yaml:"image"`
	Port    int               `yaml:"port"`
	Route   bool              `yaml:"route,omitempty"`
	Health  emittedHealth     `yaml:"health"`
	Volumes []emittedVolume   `yaml:"volumes,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type emittedHealth struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path,omitempty"`
}

type emittedVolume struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

// generatedMarker is the first line of every file this tool writes. It is how
// a later run tells its own output apart from a curated entry — the two live
// in the same directory, and only one of them may be overwritten.
const generatedMarker = "# Converted from Coolify's compose template library by core/cmd/coolify-import."

// handWritten reports whether a catalog file exists and was not produced by
// this tool. An unreadable file counts as hand-written: refusing to overwrite
// what we cannot inspect is the safe direction.
func handWritten(path string) bool {
	body, err := os.ReadFile(path) //nolint:gosec // path is the caller's own output directory
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	return !strings.HasPrefix(string(body), generatedMarker)
}

// render emits a template the way a hand-written one is written: two-space
// indentation, and a header naming where it came from so a reviewer reading
// the catalog can go back to the source.
func render(t templates.Template) ([]byte, error) {
	var e emitted
	e.Schema, e.Slug, e.Name = t.Schema, t.Slug, t.Name
	e.Description, e.Category, e.Version = t.Description, t.Category, t.Version
	for _, d := range t.Resources.Databases {
		e.Resources.Databases = append(e.Resources.Databases,
			emittedDatabase{Name: d.Name, Engine: d.Engine, Version: d.Version})
	}
	for _, a := range t.Resources.Applications {
		app := emittedApplication{
			Name:   a.Name,
			Image:  a.Image,
			Port:   a.Port,
			Route:  a.Route,
			Health: emittedHealth{Kind: a.Health.Kind, Path: a.Health.Path},
			Env:    a.Env,
		}
		for _, v := range a.Volumes {
			app.Volumes = append(app.Volumes, emittedVolume{Name: v.Name, Path: v.Path})
		}
		e.Resources.Applications = append(e.Resources.Applications, app)
	}

	var b strings.Builder
	b.WriteString(generatedMarker + "\n")
	b.WriteString("# Reviewed and committed like a hand-written template; the catalog test is the gate.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(e); err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return []byte(b.String()), nil
}

// renderReport is the other half of the deliverable: a template that cannot be
// converted is not a silent omission, it is a documented one, with the reasons
// that would have to stop being true for it to ship.
func renderReport(results []outcome) []byte {
	byReason := map[string][]string{}
	var b strings.Builder
	converted, rejected := 0, 0
	for _, r := range results {
		if len(r.reasons) == 0 {
			converted++
			continue
		}
		rejected++
		// One template counts once per blocker, however many of its reasons
		// are instances of that blocker — the table answers "how many
		// templates would this unblock", not "how many sentences did we
		// write".
		for _, class := range dedupe(mapped(r.reasons)) {
			byReason[class] = append(byReason[class], r.slug)
		}
	}

	fmt.Fprintf(&b, "# Coolify template import report\n\n")
	fmt.Fprintf(&b, "Generated by `core/cmd/coolify-import` over %d source templates: "+
		"**%d converted**, %d refused.\n\n", len(results), converted, rejected)

	fmt.Fprintf(&b, "## Why templates were refused\n\n| Blocker | Templates |\n|---|---|\n")
	for _, class := range sortedByCount(byReason) {
		fmt.Fprintf(&b, "| %s | %d |\n", class, len(byReason[class]))
	}

	fmt.Fprintf(&b, "\n## Refused templates\n\n")
	for _, r := range results {
		if len(r.reasons) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", r.slug)
		for _, reason := range r.reasons {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Converted templates\n\n")
	for _, r := range results {
		if len(r.reasons) > 0 {
			continue
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", r.slug, r.tpl.Name)
	}
	return []byte(b.String())
}

// blockers collapse a specific reason ("service \"web\" overrides the image
// command…") into the cause it is an instance of, so the summary table counts
// causes rather than phrasings. Ordered, and matched first-wins: the report is
// a committed file, and Go's map iteration would let a reason matching two
// entries land in a different row on every run.
var blockers = []struct{ needle, class string }{
	{"ignore: true", "Not listed by Coolify either"},
	{"named application database", "Needs a named application database (MySQL/MariaDB)"},
	{"host ports", "Publishes fixed host ports"},
	{"from the host", "Mounts a host path"},
	{"overrides the image command", "Overrides the image command or entrypoint"},
	{"overrides the image entrypoint", "Overrides the image command or entrypoint"},
	{"sibling service", "Applications address each other by hostname"},
	{"still names", "Addresses a resource the install would not create"},
	{"resolves freshly", "Reuses one generated value in several places"},
	{"cannot reproduce", "Needs a generated value shape the schema has no token for"},
	{"always require", "Never tells the application its database password"},
	{"one-shot job", "Includes a one-shot job container"},
	{"both publish a public URL", "Publishes more than one public URL"},
	{"sub-path", "Routes a sub-path rather than a domain"},
	{"exposes none unambiguously", "Declares no port, and its image exposes none"},
	{"nothing to route", "Serves nothing over HTTP"},
	{"waits on", "Orders one application after another"},
	{"privileged", "Needs privileged access, capabilities, or devices"},
	{"capabilities", "Needs privileged access, capabilities, or devices"},
	{"devices", "Needs privileged access, capabilities, or devices"},
	{"kernel parameters", "Needs privileged access, capabilities, or devices"},
	{"security options", "Needs privileged access, capabilities, or devices"},
	{"ulimits", "Needs privileged access, capabilities, or devices"},
	{"shared memory", "Needs privileged access, capabilities, or devices"},
	{"fixed uid", "Needs a fixed identity or hostname"},
	{"fixed hostname", "Needs a fixed identity or hostname"},
	{"CPU architecture", "Pins a CPU architecture"},
	{"network", "Needs custom networking"},
	{"built from source", "Builds from source"},
	{"could not be pinned", "Image could not be resolved to an immutable reference"},
	{"hand-written catalog entry", "Superseded by a curated entry"},
	{"no default", "Interpolates a variable with no default into a larger value"},
	{"at most", "Exceeds a schema bound"},
	{"unsupported compose key", "Uses a compose feature the schema has no field for"},
	{"TTY", "Uses a compose feature the schema has no field for"},
	{"stdin", "Uses a compose feature the schema has no field for"},
	{"read-only root", "Uses a compose feature the schema has no field for"},
	{"working directory", "Uses a compose feature the schema has no field for"},
	{"stop signal", "Uses a compose feature the schema has no field for"},
	{"stop grace period", "Uses a compose feature the schema has no field for"},
	{"swarm deploy", "Uses a compose feature the schema has no field for"},
	{"another container's volumes", "Uses a compose feature the schema has no field for"},
	{"links containers", "Uses a compose feature the schema has no field for"},
	{"PID namespace", "Uses a compose feature the schema has no field for"},
	{"invalid template", "Produces a template the schema rejects"},
}

func classify(reason string) string {
	for _, b := range blockers {
		if strings.Contains(reason, b.needle) {
			return b.class
		}
	}
	return "Other"
}

func sortedByCount(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(m[keys[i]]) != len(m[keys[j]]) {
			return len(m[keys[i]]) > len(m[keys[j]])
		}
		return keys[i] < keys[j]
	})
	return keys
}

// mapped turns a template's reasons into the blockers they are instances of.
func mapped(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, classify(r))
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

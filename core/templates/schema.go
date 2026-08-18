// Package templates implements the ADR-007 template catalog: a native
// declarative schema that resolves to ordinary Applications and Managed
// Databases (docs/features/template-catalog.md). Templates are content, not
// code — this package parses, validates, and installs them; it owns no
// runtime behavior of the resources it creates.
package templates

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Template is one catalog entry (template-catalog.md §2).
type Template struct {
	Schema      string       `yaml:"schema" json:"schema"`
	Slug        string       `yaml:"slug" json:"slug"`
	Name        string       `yaml:"name" json:"name"`
	Description string       `yaml:"description" json:"description"`
	Category    string       `yaml:"category" json:"category"`
	Version     string       `yaml:"version" json:"version"`
	Resources   TplResources `yaml:"resources" json:"resources"`
}

type TplResources struct {
	Databases    []TplDatabase    `yaml:"databases" json:"databases"`
	Applications []TplApplication `yaml:"applications" json:"applications"`
}

type TplDatabase struct {
	Name    string `yaml:"name" json:"name"`
	Engine  string `yaml:"engine" json:"engine"`
	Version string `yaml:"version" json:"version"`
}

type TplApplication struct {
	Name    string            `yaml:"name" json:"name"`
	Image   string            `yaml:"image" json:"image"`
	Port    int               `yaml:"port" json:"port"`
	Route   bool              `yaml:"route" json:"route"`
	Health  TplHealth         `yaml:"health" json:"health"`
	Volumes []TplVolume       `yaml:"volumes" json:"volumes"`
	Ports   []TplPort         `yaml:"ports" json:"ports"`
	Env     map[string]string `yaml:"env" json:"env"`
}

type TplHealth struct {
	Kind string `yaml:"kind" json:"kind"`
	Path string `yaml:"path" json:"path"`
}

type TplVolume struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

type TplPort struct {
	Host      int    `yaml:"host" json:"host"`
	Container int    `yaml:"container" json:"container"`
	Protocol  string `yaml:"protocol" json:"protocol"`
}

// Parse decodes one template document strictly: unknown fields are errors, so
// a typo'd key fails the catalog test instead of silently shipping a template
// that half-works.
func Parse(data []byte) (Template, error) {
	var t Template
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Template{}, fmt.Errorf("templates: parsing: %w", err)
	}
	if err := t.Validate(); err != nil {
		return Template{}, err
	}
	// Keep the JSON API stable: optional YAML collections are represented as
	// empty arrays/objects, never null. Apply documented health defaults here
	// too, so every catalog consumer sees one resolved schema.
	if t.Resources.Databases == nil {
		t.Resources.Databases = []TplDatabase{}
	}
	for i := range t.Resources.Applications {
		a := &t.Resources.Applications[i]
		if a.Health.Kind == "" {
			a.Health = TplHealth{Kind: "http", Path: "/"}
		} else if a.Health.Kind == "http" && a.Health.Path == "" {
			a.Health.Path = "/"
		}
		if a.Volumes == nil {
			a.Volumes = []TplVolume{}
		}
		if a.Ports == nil {
			a.Ports = []TplPort{}
		}
		for j := range a.Ports {
			if a.Ports[j].Protocol == "" {
				a.Ports[j].Protocol = "tcp"
			}
		}
		if a.Env == nil {
			a.Env = map[string]string{}
		}
	}
	return t, nil
}

var (
	slugRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	resNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	volNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// One placeholder token (template-catalog.md §2): the whole grammar. A
	// `{{` that does not match is an error, never passed through.
	tokenRe = regexp.MustCompile(`\{\{\s*([a-z0-9._-]+)\s*\}\}`)
)

// categories is the catalog's whole vocabulary. It was sized for a curated set
// of seven templates; the importer's breadth made "other" the largest bucket by
// three times, which is a filter that filters nothing. Widening it is additive
// (rule 17): every previously valid value still validates.
var categories = map[string]bool{
	"ai": true, "analytics": true, "automation": true, "cms": true,
	"communication": true, "dev-tools": true, "finance": true, "media": true,
	"monitoring": true, "productivity": true, "security": true, "storage": true,
	"other": true,
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("templates: invalid template: "+format, args...)
}

// Validate enforces every bound in template-catalog.md §2. It is the only
// gate between a YAML file and the catalog — the embedded-catalog test runs
// it over every bundled file.
func (t Template) Validate() error {
	if t.Schema != "v1" {
		return invalid("schema must be v1")
	}
	if !slugRe.MatchString(t.Slug) {
		return invalid("slug %q must be lowercase [a-z0-9-], ≤40 chars", t.Slug)
	}
	if l := len(t.Name); l == 0 || l > 100 {
		return invalid("name must be 1–100 characters")
	}
	if len(t.Description) > 200 {
		return invalid("description must be ≤200 characters")
	}
	if !categories[t.Category] {
		return invalid("category %q unknown", t.Category)
	}
	if len(t.Version) > 40 {
		return invalid("version must be ≤40 characters")
	}

	if len(t.Resources.Databases) > 3 {
		return invalid("at most 3 databases")
	}
	dbNames := map[string]bool{}
	for _, d := range t.Resources.Databases {
		if !resNameRe.MatchString(d.Name) {
			return invalid("database name %q must be lowercase [a-z0-9-], ≤31 chars", d.Name)
		}
		if dbNames[d.Name] {
			return invalid("duplicate database name %q", d.Name)
		}
		dbNames[d.Name] = true
		if !domain.DbEngine(d.Engine).Valid() {
			return invalid("database %q: engine %q is not a managed engine", d.Name, d.Engine)
		}
	}

	if n := len(t.Resources.Applications); n == 0 || n > 5 {
		return invalid("between 1 and 5 applications")
	}
	appNames := map[string]bool{}
	routed := 0
	for _, a := range t.Resources.Applications {
		if !resNameRe.MatchString(a.Name) {
			return invalid("application name %q must be lowercase [a-z0-9-], ≤31 chars", a.Name)
		}
		if appNames[a.Name] || dbNames[a.Name] {
			return invalid("duplicate resource name %q", a.Name)
		}
		appNames[a.Name] = true
		if a.Image == "" || len(a.Image) > 512 || !validImageRef(a.Image) {
			return invalid("application %q: image must be a single OCI reference", a.Name)
		}
		if a.Port < 1 || a.Port > 65535 {
			return invalid("application %q: port must be 1–65535", a.Name)
		}
		if a.Route {
			routed++
		}
		switch a.Health.Kind {
		case "", "http", "tcp", "none":
		default:
			return invalid("application %q: health.kind must be http, tcp, or none", a.Name)
		}
		// An omitted kind defaults to http (applied in Parse, after this runs),
		// so the path must be checked for the empty kind too — otherwise a
		// template that sets only a path bypasses validation entirely.
		if (a.Health.Kind == "" || a.Health.Kind == "http") && a.Health.Path != "" && !strings.HasPrefix(a.Health.Path, "/") {
			return invalid("application %q: HTTP health path must start with /", a.Name)
		}
		if len(a.Volumes) > 5 {
			return invalid("application %q: at most 5 volumes", a.Name)
		}
		volNames, volPaths := map[string]bool{}, map[string]bool{}
		for _, v := range a.Volumes {
			if !volNameRe.MatchString(v.Name) {
				return invalid("application %q: volume name %q invalid", a.Name, v.Name)
			}
			if !strings.HasPrefix(v.Path, "/") || strings.Contains(v.Path, "..") {
				return invalid("application %q: volume path %q must be absolute without ..", a.Name, v.Path)
			}
			if volNames[v.Name] || volPaths[v.Path] {
				return invalid("application %q: duplicate volume name or path", a.Name)
			}
			volNames[v.Name], volPaths[v.Path] = true, true
		}
		if len(a.Ports) > 10 {
			return invalid("application %q: at most 10 port publishes", a.Name)
		}
		for _, p := range a.Ports {
			if p.Host < 1 || p.Host > 65535 || p.Container < 1 || p.Container > 65535 {
				return invalid("application %q: ports must be 1–65535", a.Name)
			}
			switch p.Protocol {
			case "", "tcp", "udp":
			default:
				return invalid("application %q: port protocol must be tcp or udp", a.Name)
			}
		}
		for k, v := range a.Env {
			if !envKeyRe.MatchString(k) {
				return invalid("application %q: env key %q invalid", a.Name, k)
			}
			if len(v) > 2000 {
				return invalid("application %q: env %s value too long", a.Name, k)
			}
			if err := validatePlaceholders(v, dbNames); err != nil {
				return invalid("application %q env %s: %v", a.Name, k, err)
			}
		}
	}
	if routed > 1 {
		return invalid("at most one application may set route: true")
	}
	return nil
}

// validImageRef mirrors the applications-service rule: the legal reference
// alphabet only (the engine's parser is the real gate at pull time).
func validImageRef(ref string) bool {
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}

// validatePlaceholders walks every {{…}} token in a value and checks it
// against the §2 grammar without resolving it. A stray "{{" outside the token
// grammar is an error — templates never smuggle un-resolved syntax into a
// container's environment.
func validatePlaceholders(v string, dbNames map[string]bool) error {
	stripped := tokenRe.ReplaceAllString(v, "")
	if strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
		return fmt.Errorf("malformed placeholder")
	}
	for _, m := range tokenRe.FindAllStringSubmatch(v, -1) {
		if err := checkToken(m[1], dbNames); err != nil {
			return err
		}
	}
	return nil
}

func checkToken(tok string, dbNames map[string]bool) error {
	parts := strings.Split(tok, ".")
	switch parts[0] {
	case "domain":
		if len(parts) != 1 {
			return fmt.Errorf("unknown placeholder %q", tok)
		}
	case "secret":
		if len(parts) != 2 {
			return fmt.Errorf("unknown placeholder %q", tok)
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 16 || n > 64 {
			return fmt.Errorf("secret length in %q must be 16–64", tok)
		}
	case "db":
		if len(parts) != 3 {
			return fmt.Errorf("unknown placeholder %q", tok)
		}
		if !dbNames[parts[1]] {
			return fmt.Errorf("placeholder %q references undeclared database %q", tok, parts[1])
		}
		switch parts[2] {
		case "host", "port", "user", "password", "database", "url":
		default:
			return fmt.Errorf("unknown database field in %q", tok)
		}
	default:
		return fmt.Errorf("unknown placeholder %q", tok)
	}
	return nil
}

// dbInfo is what a created database contributes to placeholder resolution.
// password is plaintext only inside the install call (template-catalog.md §4)
// — it is sealed the moment it lands in an application's env vars.
type dbInfo struct {
	host, user, password, database, url string
	port                                int
}

// resolve substitutes every placeholder in v. newSecret is called once per
// {{secret.N}} occurrence.
func resolve(v string, dbs map[string]dbInfo, domainName string, newSecret func(n int) (string, error)) (string, error) {
	var outerErr error
	out := tokenRe.ReplaceAllStringFunc(v, func(m string) string {
		if outerErr != nil {
			return ""
		}
		tok := tokenRe.FindStringSubmatch(m)[1]
		parts := strings.Split(tok, ".")
		switch parts[0] {
		case "domain":
			return domainName
		case "secret":
			n, _ := strconv.Atoi(parts[1])
			s, err := newSecret(n)
			if err != nil {
				outerErr = err
				return ""
			}
			return s
		case "db":
			info, ok := dbs[parts[1]]
			if !ok {
				outerErr = fmt.Errorf("templates: unresolved database %q", parts[1])
				return ""
			}
			switch parts[2] {
			case "host":
				return info.host
			case "port":
				return strconv.Itoa(info.port)
			case "user":
				return info.user
			case "password":
				return info.password
			case "database":
				return info.database
			case "url":
				return info.url
			}
		}
		outerErr = fmt.Errorf("templates: unresolvable placeholder %q", tok)
		return ""
	})
	return out, outerErr
}

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/templates"
)

// convert translates one Coolify compose template into a native template. It
// returns either a template or the complete list of reasons the source cannot
// be expressed — never a partial conversion. Every reason is phrased so a
// human reading the report knows what the source asked for and why the schema
// refuses it.
//
// It is deliberately pure: no network, no filesystem. Digest pinning happens
// afterwards (pin.go) so the whole mapping is unit-testable offline.
// portOracle answers "which port does this image serve on" from the image's
// own config. Compose templates routinely omit the port because Docker reads
// it from the image's EXPOSE, and guessing is not an option — so when the
// registry is reachable the importer asks the same source Docker would. It is
// nil for offline runs, which simply refuse those templates instead.
type portOracle func(image string) (int, bool)

func convert(slug string, src []byte, ports portOracle) (templates.Template, []string) {
	var r reject

	hdr := parseHeader(src)
	if strings.EqualFold(hdr["ignore"], "true") {
		return templates.Template{}, []string{"marked `# ignore: true` upstream — Coolify itself does not list it"}
	}

	svcs, top, err := parseCompose(src)
	if err != nil {
		return templates.Template{}, []string{err.Error()}
	}
	if len(svcs) == 0 {
		return templates.Template{}, []string{"no services"}
	}
	for _, key := range top {
		switch key {
		case "services", "volumes":
			// Top-level named-volume declarations carry no configuration we
			// need: the per-service mount is what names the volume.
		case "networks":
			r.add("declares custom networks; every resource joins its environment network")
		default:
			if !strings.HasPrefix(key, "x-") {
				r.add("unsupported top-level compose key %q", key)
			}
		}
	}

	// 1. Split services into managed databases and applications. Engine
	// services lose their compose configuration entirely — the engine matrix
	// owns the image, the command, the volume, and the health check — so
	// their unsupported keys are not a reason to reject the template.
	//
	// Image references are interpolated first: a tag written as `${TAG:-main}`
	// is not a reference until the variable is resolved, and both the engine
	// match and the registry lookup need the resolved form.
	defaults := collectDefaults(svcs)
	for i := range svcs {
		svcs[i].image = resolveImageRef(svcs[i].image, defaults)
	}
	var dbs []dbService
	var apps []appService
	for _, s := range svcs {
		if s.image == "" {
			if _, building := s.keys["build"]; building {
				r.add("service %q is built from source; templates install published images", s.name)
			} else {
				r.add("service %q has no image", s.name)
			}
			continue
		}
		if eng, ok := engineFor(s.image); ok {
			dbs = append(dbs, dbService{svc: s, engine: eng, version: engineVersion(s.image)})
			continue
		}
		apps = append(apps, appService{svc: s})
	}
	if len(apps) == 0 && !r.any() {
		r.add("no application services — every service maps to a managed database")
	}

	// 2. Reject anything an application asks for that the schema cannot say.
	for _, a := range apps {
		checkAppKeys(&r, a.svc)
	}
	dbNames := map[string]string{} // compose service name -> template database name
	for _, d := range dbs {
		dbNames[d.svc.name] = "" // filled in once names are assigned
	}
	for _, a := range apps {
		for _, dep := range a.svc.dependsOn {
			if _, isDB := dbNames[dep]; !isDB {
				r.add("service %q waits on %q; templates order databases before applications and nothing else", a.svc.name, dep)
			}
		}
	}

	// 3. Name the resources. Database names come from their role, not their
	// compose service name, so `teable-db` and `plausible-db` both become `db`.
	assignDatabaseNames(dbs, dbNames)
	appNames := map[string]string{}
	used := map[string]bool{}
	for _, d := range dbs {
		used[dbNames[d.svc.name]] = true
	}
	for _, a := range apps {
		appNames[a.svc.name] = uniqueName(resourceName(a.svc.name, slug), used)
	}

	// 4. Routing. Exactly one application may take the install-time domain.
	routed, routePort := "", 0
	for _, a := range apps {
		port, found := fqdnDeclaration(a.svc)
		if !found {
			continue
		}
		if routed != "" && routed != a.svc.name {
			r.add("services %q and %q both publish a public URL; a template routes at most one", routed, a.svc.name)
			continue
		}
		routed, routePort = a.svc.name, port
	}
	if routed != "" && routePort == 0 {
		// The magic key named no port. The header's documented port says which
		// one serves HTTP; failing that, the image's own EXPOSE does.
		if p, err := strconv.Atoi(strings.TrimSpace(hdr["port"])); err == nil && p > 0 && p < 65536 {
			routePort = p
		} else if p, ok := exposedPort(ports, apps, routed); ok {
			routePort = p
		} else {
			r.add("service %q publishes a public URL but names no port, and its image exposes none unambiguously", routed)
		}
	}
	if routed == "" && len(apps) == 1 {
		// No magic FQDN key: the header's documented port is the service's
		// HTTP port, which is exactly what the single application routes on.
		if p, err := strconv.Atoi(strings.TrimSpace(hdr["port"])); err == nil && p > 0 && p < 65536 {
			routed, routePort = apps[0].svc.name, p
		}
	}
	if routed == "" {
		r.add("no service declares a public port; there is nothing to route the install-time domain at")
	}

	// 5. Resolve compose variables. Defaults are project-wide in compose — a
	// `${POSTGRES_DB:-teable}` written on the database service is what the
	// application's `${POSTGRES_DB}` resolves to — so they are collected across
	// every service before any value is translated.
	env := newEnvResolver(svcs, dbs, dbNames)

	// 6. Translate each application. Generated values are counted across the
	// whole template, not per application, because upstream shares one value
	// under one name wherever it appears.
	secretUses := map[string]int{}
	var out []templates.TplApplication
	for _, a := range apps {
		app := templates.TplApplication{
			Name:  appNames[a.svc.name],
			Image: a.svc.image,
			// Deliberately not the compose healthcheck: those are shell
			// commands run inside the container, which the schema has no field
			// for. A TCP probe of the application's own port is the strongest
			// readiness signal that holds for every imported image — an HTTP
			// probe would need a path known to answer 200, and guessing "/"
			// fails every app that redirects to a login page.
			Health: templates.TplHealth{Kind: "tcp"},
			Env:    map[string]string{},
		}
		if a.svc.name == routed {
			app.Route = true
			app.Port = routePort
		} else if p, ok := singleExposedPort(a.svc); ok {
			app.Port = p
		} else if p, ok := lookupPort(ports, a.svc.image); ok {
			app.Port = p
		} else {
			r.add("service %q names no port and its image exposes none unambiguously; the schema requires one per application", a.svc.name)
		}

		for _, v := range a.svc.volumes {
			name, path, ok := namedVolume(v)
			if !ok {
				r.add("service %q mounts %q from the host; templates declare named volumes only", a.svc.name, v)
				continue
			}
			app.Volumes = append(app.Volumes, templates.TplVolume{Name: name, Path: path})
		}
		dedupeVolumes(&app)

		for _, kv := range a.svc.env {
			key, value, emit := translate(env, kv, a.svc.name, secretUses, &r)
			if !emit || key == "" {
				continue
			}
			app.Env[key] = value
		}
		out = append(out, app)
	}

	// 7. Safety nets. These catch mappings that produced a *plausible* but
	// wrong template rather than an outright unsupported feature — the failure
	// mode that would otherwise ship silently.
	checkResidualHostnames(&r, out, svcs, dbNames, appNames)
	checkResidualDatabaseNames(&r, out, env)
	checkCredentialsReach(&r, out, dbs, dbNames)
	checkSecretReuse(&r, secretUses)

	if r.any() {
		return templates.Template{}, r.reasons()
	}

	tpl := templates.Template{
		Schema:      "v1",
		Slug:        slug,
		Name:        displayName(slug),
		Description: description(hdr["slogan"]),
		Category:    category(hdr["category"]),
		Version:     "",
		Resources:   templates.TplResources{Applications: out},
	}
	for _, d := range dbs {
		tpl.Resources.Databases = append(tpl.Resources.Databases, templates.TplDatabase{
			Name:    dbNames[d.svc.name],
			Engine:  string(d.engine),
			Version: d.version,
		})
	}
	if err := tpl.Validate(); err != nil {
		return templates.Template{}, []string{err.Error()}
	}
	return tpl, nil
}

// reject accumulates every reason a template cannot be converted, so one run
// reports the whole story instead of the first problem it met.
type reject struct {
	seen  map[string]bool
	order []string
}

func (r *reject) add(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[msg] {
		return
	}
	r.seen[msg] = true
	r.order = append(r.order, msg)
}

func (r *reject) any() bool         { return len(r.order) > 0 }
func (r *reject) reasons() []string { return r.order }

// service is one compose service reduced to the fields the schema can consume.
type service struct {
	name      string
	image     string
	env       []string // "KEY=VALUE" or a bare "KEY"
	volumes   []string
	dependsOn []string
	expose    []string
	keys      map[string]bool // every compose key the service actually set
	restart   string
}

type dbService struct {
	svc     service
	engine  domain.DbEngine
	version string
}

type appService struct{ svc service }

// parseCompose reads the services in document order — Go maps are unordered
// and the catalog is committed, so a stable emission order is what keeps a
// re-import from producing a spurious diff.
func parseCompose(src []byte) ([]service, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("compose file is not a mapping")
	}
	root := doc.Content[0]
	var topKeys []string
	var servicesNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		topKeys = append(topKeys, root.Content[i].Value)
		if root.Content[i].Value == "services" {
			servicesNode = root.Content[i+1]
		}
	}
	if servicesNode == nil || servicesNode.Kind != yaml.MappingNode {
		return nil, topKeys, nil
	}

	var out []service
	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		s := service{name: servicesNode.Content[i].Value, keys: map[string]bool{}}
		body := servicesNode.Content[i+1]
		if body.Kind != yaml.MappingNode {
			return nil, topKeys, fmt.Errorf("service %q is not a mapping", s.name)
		}
		for j := 0; j+1 < len(body.Content); j += 2 {
			key, val := body.Content[j].Value, body.Content[j+1]
			s.keys[key] = true
			switch key {
			case "image":
				s.image = val.Value
			case "restart":
				s.restart = val.Value
			case "environment":
				s.env = decodeEnv(val)
			case "volumes":
				s.volumes = decodeStrings(val)
			case "expose":
				s.expose = decodeStrings(val)
			case "depends_on":
				s.dependsOn = decodeKeysOrStrings(val)
			}
		}
		out = append(out, s)
	}
	return out, topKeys, nil
}

// decodeEnv normalizes both compose environment forms — a list of "K=V"
// strings and a mapping — into the list form.
func decodeEnv(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.SequenceNode:
		return decodeStrings(n)
	case yaml.MappingNode:
		var out []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value+"="+n.Content[i+1].Value)
		}
		return out
	}
	return nil
}

func decodeStrings(n *yaml.Node) []string {
	var out []string
	if n.Kind != yaml.SequenceNode {
		return nil
	}
	for _, c := range n.Content {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
		}
	}
	return out
}

// decodeKeysOrStrings reads depends_on in both its forms: a plain list of
// service names, and the mapping form whose keys are the service names.
func decodeKeysOrStrings(n *yaml.Node) []string {
	if n.Kind == yaml.MappingNode {
		var out []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out
	}
	return decodeStrings(n)
}

// appKeys lists every compose key an application service may set. Keys that
// configure the container in a way the schema cannot express are rejected;
// keys the panel owns outright (naming, restart policy, logging, the compose
// healthcheck) are ignored, because honoring them is not our job.
var appKeys = map[string]string{
	"image":           "",
	"environment":     "",
	"volumes":         "",
	"expose":          "",
	"depends_on":      "",
	"healthcheck":     "", // replaced by the schema's own probe
	"container_name":  "", // the panel names containers
	"restart":         "", // the panel owns the restart policy
	"logging":         "", // the panel owns log capture
	"labels":          "", // routing labels are generated by the proxy driver
	"exclude_from_hc": "", // Coolify-specific health-check opt-out
	"profiles":        "", // compose-only selection, no runtime effect

	"build":             "is built from source; templates install published images",
	"command":           "overrides the image command; templates configure through environment only",
	"entrypoint":        "overrides the image entrypoint; templates configure through environment only",
	"privileged":        "runs privileged",
	"cap_add":           "adds Linux capabilities",
	"cap_drop":          "drops Linux capabilities",
	"devices":           "maps host devices",
	"sysctls":           "sets kernel parameters",
	"security_opt":      "sets container security options",
	"ulimits":           "sets resource ulimits",
	"shm_size":          "sizes shared memory",
	"user":              "runs as a fixed uid",
	"hostname":          "needs a fixed hostname",
	"networks":          "joins a custom network",
	"network_mode":      "sets a custom network mode",
	"links":             "links containers by name",
	"volumes_from":      "mounts another container's volumes",
	"pid":               "shares a host or container PID namespace",
	"ports":             "publishes fixed host ports, which cannot be redeployed today (application-deploy.md §5)",
	"deploy":            "sets swarm deploy settings",
	"platform":          "pins a CPU architecture",
	"tty":               "needs a TTY",
	"stdin_open":        "needs stdin held open",
	"read_only":         "needs a read-only root filesystem",
	"working_dir":       "overrides the working directory",
	"stop_signal":       "overrides the stop signal",
	"stop_grace_period": "overrides the stop grace period",
}

func checkAppKeys(r *reject, s service) {
	keys := make([]string, 0, len(s.keys))
	for k := range s.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		why, known := appKeys[k]
		switch {
		case !known:
			r.add("service %q sets unsupported compose key %q", s.name, k)
		case why != "":
			r.add("service %q %s", s.name, why)
		}
	}
	// A service that is not meant to stay up is a migration or seed job. The
	// schema has no job resource, and installing it as an application means a
	// health gate that can never pass.
	if s.restart == "no" || s.restart == "on-failure" {
		r.add("service %q is a one-shot job (restart: %s); templates create long-running applications", s.name, s.restart)
	}
}

// engineFor recognizes the images the managed-database matrix covers. Only
// exact upstream images qualify: a fork with extensions bundled in is not the
// image the engine matrix would provision, so it stays an application.
func engineFor(image string) (domain.DbEngine, bool) {
	repo := image
	if i := strings.LastIndex(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	switch strings.ToLower(repo) {
	case "postgres", "postgresql", "library/postgres", "docker.io/postgres", "docker.io/library/postgres":
		return domain.EnginePostgreSQL, true
	case "mysql", "library/mysql", "docker.io/mysql", "docker.io/library/mysql":
		return domain.EngineMySQL, true
	case "mariadb", "library/mariadb", "docker.io/mariadb", "docker.io/library/mariadb":
		return domain.EngineMariaDB, true
	case "mongo", "library/mongo", "docker.io/mongo", "docker.io/library/mongo":
		return domain.EngineMongoDB, true
	case "redis", "library/redis", "docker.io/redis", "docker.io/library/redis":
		return domain.EngineRedis, true
	case "valkey/valkey", "docker.io/valkey/valkey":
		return domain.EngineValkey, true
	}
	return "", false
}

var engineVersionRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)`)

// engineVersion keeps the major (or major.minor) the source pinned and drops
// any variant suffix: the engine matrix builds `postgres:<version>`, so
// "15.4-alpine" would name an image the matrix never intended. An
// unrecognizable tag yields "", and the engine's own default applies.
func engineVersion(image string) string {
	tag := ""
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		tag = image[i+1:]
	}
	if i := strings.Index(tag, "@"); i >= 0 {
		tag = tag[:i]
	}
	m := engineVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}

// assignDatabaseNames gives each managed database a role name — `db`, `cache`,
// `queue` — rather than its compose service name, so installs read as
// `myapp-db` regardless of what upstream called the container.
func assignDatabaseNames(dbs []dbService, out map[string]string) {
	used := map[string]bool{}
	for _, d := range dbs {
		base := "db"
		if d.engine == domain.EngineRedis || d.engine == domain.EngineValkey {
			base = "cache"
		}
		out[d.svc.name] = uniqueName(base, used)
	}
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name]; i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	used[name] = true
	return name
}

var nonNameRe = regexp.MustCompile(`[^a-z0-9-]+`)

// resourceName sanitizes a compose service name into the schema's alphabet. A
// service named after the template itself collapses to the slug, which is what
// a single-application install is named anyway.
func resourceName(name, slug string) string {
	n := nonNameRe.ReplaceAllString(strings.ToLower(name), "-")
	n = strings.Trim(n, "-")
	if n == "" {
		n = slug
	}
	if len(n) > 31 {
		n = strings.Trim(n[:31], "-")
	}
	return n
}

// fqdnDeclaration finds the magic key that makes a service publicly routed.
// Coolify writes it as a bare environment entry: SERVICE_FQDN_<SERVICE> or
// SERVICE_URL_<SERVICE>, optionally suffixed with the container port. A key
// that names the port wins over one that does not, since only it says which
// port serves HTTP.
func fqdnDeclaration(s service) (port int, found bool) {
	for _, kv := range s.env {
		key := kv
		if i := strings.Index(kv, "="); i >= 0 {
			key = kv[:i]
		}
		m, ok := parseMagic(key)
		if !ok || (m.kind != magicFQDN && m.kind != magicURL) {
			continue
		}
		found = true
		if m.port > 0 {
			port = m.port
		}
	}
	return port, found
}

// exposedPort asks the oracle for the port of the named service's image.
func exposedPort(ports portOracle, apps []appService, svcName string) (int, bool) {
	for _, a := range apps {
		if a.svc.name == svcName {
			return lookupPort(ports, a.svc.image)
		}
	}
	return 0, false
}

func lookupPort(ports portOracle, image string) (int, bool) {
	if ports == nil {
		return 0, false
	}
	return ports(image)
}

// singleExposedPort reads a non-routed application's port from `expose`. One
// unambiguous port is usable; anything else is not a guess worth making.
func singleExposedPort(s service) (int, bool) {
	if len(s.expose) != 1 {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimSpace(s.expose[0]))
	if err != nil || p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

// namedVolume accepts `name:/container/path[:mode]` and rejects every host
// mount — a bind mount reaches outside the panel's storage, which no template
// may do (template-catalog.md §1).
func namedVolume(spec string) (name, path string, ok bool) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return "", "", false
	}
	src, dst := parts[0], parts[1]
	if src == "" || strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "$") || strings.HasPrefix(src, "~") {
		return "", "", false
	}
	if !strings.HasPrefix(dst, "/") || strings.Contains(dst, "..") || strings.Contains(dst, "$") {
		return "", "", false
	}
	name = nonNameRe.ReplaceAllString(strings.ToLower(src), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "", "", false
	}
	if len(name) > 31 {
		name = strings.Trim(name[:31], "-")
	}
	return name, dst, true
}

// dedupeVolumes drops repeat mounts of one volume name or path — compose
// tolerates them, the schema does not.
func dedupeVolumes(app *templates.TplApplication) {
	names, paths := map[string]bool{}, map[string]bool{}
	kept := app.Volumes[:0]
	for _, v := range app.Volumes {
		if names[v.Name] || paths[v.Path] {
			continue
		}
		names[v.Name], paths[v.Path] = true, true
		kept = append(kept, v)
	}
	app.Volumes = kept
}

// checkResidualHostnames catches the mapping's most dangerous failure: a value
// that still names a compose service. A database hostname should have become
// {{db.<n>.host}}; an application hostname has no placeholder at all, because
// application containers are named per revision and have no stable DNS name.
// Either way the installed app would dial a host that does not exist.
func checkResidualHostnames(r *reject, apps []templates.TplApplication, svcs []service, dbNames, appNames map[string]string) {
	for _, a := range apps {
		for _, key := range sortedKeys(a.Env) {
			// A URL scheme is not a hostname, and compose services are
			// routinely named after the protocol they speak (`redis`,
			// `postgresql`), so `redis://…` must not read as a reference to
			// the service called `redis`.
			value := schemeRe.ReplaceAllString(a.Env[key], "")
			for _, s := range svcs {
				if !hostnameRefRe(s.name).MatchString(value) {
					continue
				}
				if _, isDB := dbNames[s.name]; isDB {
					r.add("%s still names the database service %q after translation", key, s.name)
					continue
				}
				if appNames[s.name] == a.Name {
					continue // a service naming itself is not a cross-reference
				}
				r.add("%s dials the sibling service %q; application containers are named per revision and have no stable address", key, s.name)
			}
		}
	}
}

// hostnameRefRe matches a compose service name used as a host: bounded by
// characters that cannot appear in a DNS label, so "db" does not match
// "dbname" and "redis" does not match "redis-stack".
func hostnameRefRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9._-])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9._-])`)
}

// checkSecretReuse catches the one place where a faithful translation is still
// wrong: Coolify generates a value once per name and substitutes it
// everywhere, while {{secret.N}} resolves per occurrence. Two occurrences that
// upstream meant to be equal would install as two different secrets.
// checkResidualDatabaseNames refuses a template whose connection settings
// still name an application database. Upstream created that database with the
// engine container's own init variables; a managed database has only the
// engine default, so the installed application would connect to nothing.
func checkResidualDatabaseNames(r *reject, apps []templates.TplApplication, env *envResolver) {
	for _, a := range apps {
		for _, key := range sortedKeys(a.Env) {
			if literal, found := env.residualDatabaseName(a.Env[key]); found {
				r.add("%s still names the application database %q, which a managed database does not create", key, literal)
			}
		}
	}
}

// checkCredentialsReach refuses a template that never hands an application the
// database's password. Managed databases always require one — template
// installs opt every engine into password authentication — so a connection
// string upstream left open would be rejected at connect time.
func checkCredentialsReach(r *reject, apps []templates.TplApplication, dbs []dbService, dbNames map[string]string) {
	for _, d := range dbs {
		token := "{{db." + dbNames[d.svc.name] + ".password}}"
		reached := false
		for _, a := range apps {
			for _, v := range a.Env {
				if strings.Contains(v, token) {
					reached = true
				}
			}
		}
		if !reached {
			r.add("no application is given the %s database's password, which managed databases always require", dbNames[d.svc.name])
		}
	}
}

func checkSecretReuse(r *reject, uses map[string]int) {
	for _, name := range sortedCountKeys(uses) {
		if uses[name] > 1 {
			r.add("generated value SERVICE_%s is used %d times; {{secret.N}} resolves freshly at each use, so the copies would not match",
				name, uses[name])
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseHeader reads Coolify's leading `# key: value` comment block, which is
// where the catalog metadata lives (category, slogan, documented port).
func parseHeader(src []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		kv := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		i := strings.Index(kv, ":")
		if i < 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(kv[:i]))] = strings.TrimSpace(kv[i+1:])
	}
	return out
}

// categories maps Coolify's free-form category vocabulary onto the schema's
// own. Anything unrecognized lands in "other" rather than failing an import
// over a label — but the mapping is kept wide on purpose: a catalog where
// "other" is the largest bucket has a category filter that filters nothing.
var categories = map[string]string{
	"ai": "ai", "mcp": "ai",
	"analytics": "analytics", "search": "analytics",
	"automation": "automation",
	"cms":        "cms", "documentation": "cms",
	"communication": "communication", "messaging": "communication",
	"email": "communication", "mail": "communication", "helpdesk": "communication",
	"devtools": "dev-tools", "developer-tools": "dev-tools", "development": "dev-tools",
	"git": "dev-tools", "ci": "dev-tools", "api": "dev-tools", "backend": "dev-tools",
	"finance":    "finance",
	"media":      "media",
	"monitoring": "monitoring", "observability": "monitoring",
	// Reading and note-taking tools are what someone reaches for under
	// "productivity"; RSS readers and bookmark managers belong with them
	// rather than in a category of their own.
	"productivity": "productivity", "rss": "productivity",
	"security": "security", "auth": "security", "vpn": "security",
	"storage": "storage", "database": "storage", "databases": "storage",
}

func category(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		if c, ok := categories[strings.ToLower(strings.TrimSpace(part))]; ok {
			return c
		}
	}
	return "other"
}

// description trims the upstream slogan to the schema's 200 characters,
// cutting at a word boundary so the catalog never shows a severed word.
func description(slogan string) string {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(slogan), `"'`))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 200 {
		return s
	}
	// The schema's bound is 200 bytes, and the ellipsis costs three of them.
	cut := s[:197]
	if i := strings.LastIndex(cut, " "); i > 120 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-") + "…"
}

// displayNames spells the products whose casing a slug cannot carry. Anything
// absent is title-cased from the slug, which is right for the large majority.
var displayNames = map[string]string{
	"n8n": "n8n", "nocodb": "NocoDB", "postgrest": "PostgREST", "anythingllm": "AnythingLLM",
	"openwebui": "Open WebUI", "phpmyadmin": "phpMyAdmin", "rss-bridge": "RSS-Bridge",
	"freshrss": "FreshRSS", "tinymediamanager": "tinyMediaManager", "wordpress": "WordPress",
	"nextcloud": "Nextcloud", "owncloud": "ownCloud", "onlyoffice": "ONLYOFFICE",
	"minio": "MinIO", "mongodb": "MongoDB", "mysql": "MySQL", "postgresql": "PostgreSQL",
	"grafana": "Grafana", "influxdb": "InfluxDB", "clickhouse": "ClickHouse",
	"rabbitmq": "RabbitMQ", "meilisearch": "Meilisearch", "typesense": "Typesense",
	"gitea": "Gitea", "gitlab": "GitLab", "classicpress": "ClassicPress", "github": "GitHub", "jellyfin": "Jellyfin",
	"paperless-ngx": "Paperless-ngx", "vaultwarden": "Vaultwarden", "wireguard": "WireGuard",
	"nginx": "nginx", "traefik": "Traefik", "haproxy": "HAProxy", "openvpn": "OpenVPN",
	"pgadmin": "pgAdmin", "webtop": "Webtop", "youtrack": "YouTrack",
	"postgres": "PostgreSQL", "mariadb": "MariaDB", "sqlite": "SQLite",
	"s3": "S3", "webui": "WebUI", "ngx": "ngx", "ollama": "Ollama",
	"emby": "Emby", "plex": "Plex", "sonarr": "Sonarr", "radarr": "Radarr",
	"invoiceninja": "Invoice Ninja", "listmonk": "Listmonk", "umami": "Umami",
	"uptime-kuma": "Uptime Kuma", "metabase": "Metabase", "appsmith": "Appsmith",
}

var acronyms = map[string]string{
	"ai": "AI", "api": "API", "cms": "CMS", "crm": "CRM", "db": "DB", "dns": "DNS",
	"ftp": "FTP", "http": "HTTP", "id": "ID", "io": "IO", "ip": "IP", "llm": "LLM",
	"pdf": "PDF", "rss": "RSS", "sql": "SQL", "ssh": "SSH", "ui": "UI", "url": "URL",
}

// joiners stay lowercase in the middle of a name: Coolify's library is full of
// `x-with-postgresql` variants, and "Wordpress With Postgresql" reads like a
// machine wrote it — which is exactly the impression a generated catalog must
// avoid.
var joiners = map[string]bool{
	"with": true, "without": true, "and": true, "for": true, "to": true, "on": true,
}

func displayName(slug string) string {
	if n, ok := displayNames[slug]; ok {
		return n
	}
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		switch {
		case p == "":
		case displayNames[p] != "":
			// The product-name table also spells the individual words, so a
			// `-with-` variant inherits the right casing on both sides.
			parts[i] = displayNames[p]
		case acronyms[p] != "":
			parts[i] = acronyms[p]
		case i > 0 && joiners[p]:
			parts[i] = p
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

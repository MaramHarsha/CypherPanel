package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// This file owns the half of the import that is not structural: turning one
// compose environment entry into one template environment entry. Two
// vocabularies meet here — compose's `${VAR:-default}` interpolation and
// Coolify's magic `SERVICE_*` names (research/coolify.md: "template magic-envs
// are the best idea in the codebase") — and both have to land in the schema's
// much smaller placeholder grammar (template-catalog.md §2).

type magicKind int

const (
	magicNone        magicKind = iota
	magicFQDN                  // SERVICE_FQDN_<SERVICE>[_<PORT>] — the bare domain
	magicURL                   // SERVICE_URL_<SERVICE>[_<PORT>]  — https://<domain>
	magicGenerated             // SERVICE_PASSWORD_<NAME> and friends — a fresh secret
	magicUnsupported           // a generator whose output shape we cannot reproduce
)

type magic struct {
	kind magicKind
	verb string // PASSWORD, BASE64_64, USER, …
	name string // the trailing <SERVICE>/<NAME> component
	port int    // FQDN/URL only, 0 when absent
	// bytes is the {{secret.N}} length that reproduces the upstream value's
	// character count: Coolify's generators emit text, and {{secret.N}} emits
	// 2N hex characters, so N is half the intended length.
	bytes int
}

// generators maps Coolify's generator verbs to the secret length that matches
// the number of characters upstream would have produced. The REALBASE64 and
// SUPABASE* verbs are deliberately absent: the first must be valid base64 of a
// given byte count and the second is a JWT signed with another variable, and
// hex of any length is neither.
var generators = map[string]int{
	"PASSWORD":               16, // 32 characters
	"PASSWORD_64":            32,
	"PASSWORDWITHSYMBOLS":    16,
	"PASSWORDWITHSYMBOLS_64": 32,
	"BASE64":                 16, // Coolify's "BASE64" is a 32-character random string
	"BASE64_32":              16,
	"BASE64_64":              32,
	"BASE64_128":             64,
	"HEX_32":                 16,
	"HEX_64":                 32,
	"HEX_128":                64,
	"USER":                   16,
	"LOWERCASEUSER":          16,
}

// unsupportedGenerators name the verbs we recognize and refuse, so the report
// says "cannot reproduce this shape" instead of "unknown variable".
var unsupportedGenerators = map[string]string{
	"REALBASE64":         "base64-encoded random bytes",
	"REALBASE64_32":      "base64-encoded random bytes",
	"REALBASE64_64":      "base64-encoded random bytes",
	"REALBASE64_128":     "base64-encoded random bytes",
	"SUPABASEANON":       "a JWT signed with another generated value",
	"SUPABASESERVICE":    "a JWT signed with another generated value",
	"SUPABASEANONKEY":    "a JWT signed with another generated value",
	"SUPABASESERVICEKEY": "a JWT signed with another generated value",
}

// parseMagic splits a SERVICE_* name into its verb and its trailing name.
// Coolify's own parser counts underscores; counting is unreliable for verbs
// that contain one (BASE64_64) and names that contain one (NEW_API), so this
// matches the verb against the known set instead, longest first.
func parseMagic(key string) (magic, bool) {
	if !strings.HasPrefix(key, "SERVICE_") {
		return magic{}, false
	}
	rest := strings.TrimPrefix(key, "SERVICE_")

	for _, prefix := range []string{"FQDN_", "URL_"} {
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		m := magic{kind: magicFQDN, verb: strings.TrimSuffix(prefix, "_")}
		if prefix == "URL_" {
			m.kind = magicURL
		}
		m.name = strings.TrimPrefix(rest, prefix)
		// A trailing all-digit component is the container port, not part of
		// the service name: SERVICE_FQDN_UMAMI_3000.
		if i := strings.LastIndex(m.name, "_"); i >= 0 {
			if p, err := atoiStrict(m.name[i+1:]); err == nil {
				m.port, m.name = p, m.name[:i]
			}
		}
		return m, true
	}
	// Verbs are matched longest-first so BASE64_64 is not read as BASE64 with
	// a name of "64_<NAME>".
	for _, verb := range generatorVerbsLongestFirst {
		if !strings.HasPrefix(rest, verb+"_") {
			continue
		}
		return magic{kind: magicGenerated, verb: verb, name: strings.TrimPrefix(rest, verb+"_"), bytes: generators[verb]}, true
	}
	for verb, why := range unsupportedGenerators {
		if strings.HasPrefix(rest, verb+"_") {
			return magic{kind: magicUnsupported, verb: verb, name: why}, true
		}
	}
	return magic{}, false
}

// generatorVerbsLongestFirst is built once at init so parseMagic's longest-match
// rule does not depend on Go's randomized map iteration order.
var generatorVerbsLongestFirst = func() []string {
	out := make([]string, 0, len(generators))
	for v := range generators {
		out = append(out, v)
	}
	// Longest first, then lexical, so the order is total and stable.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) || (len(out[j]) == len(out[i]) && out[j] < out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}()

func atoiStrict(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// dbNameVars are the environment variables whose value *is* an application
// database name. A managed database exposes only its engine default, so every
// one of these has to resolve through {{db.<n>.database}} — leaving the
// upstream literal would point the application at a database nobody creates.
var dbNameVars = map[string]bool{
	"POSTGRES_DB": true, "POSTGRESQL_DATABASE": true, "PGDATABASE": true,
	"MYSQL_DATABASE": true, "MARIADB_DATABASE": true, "MONGO_INITDB_DATABASE": true,
}

// dbCredentialVars are the engine variables whose value is a credential. A
// magic name appearing in one of them names *this* database, however the
// template spelled it.
var dbCredentialVars = map[string]bool{
	"POSTGRES_USER": true, "POSTGRES_PASSWORD": true,
	"MYSQL_USER": true, "MYSQL_PASSWORD": true, "MYSQL_ROOT_PASSWORD": true,
	"MARIADB_USER": true, "MARIADB_PASSWORD": true, "MARIADB_ROOT_PASSWORD": true,
	"MONGO_INITDB_ROOT_USERNAME": true, "MONGO_INITDB_ROOT_PASSWORD": true,
	"REDIS_PASSWORD": true, "VALKEY_PASSWORD": true,
}

// magicRefRe finds a SERVICE_* reference inside a value, in either spelling.
var magicRefRe = regexp.MustCompile(`\$\{?(SERVICE_[A-Z0-9_]+)\}?`)

// envResolver holds everything an environment value may need to resolve
// against: compose's project-wide defaults and the databases this template
// created out of engine services.
type envResolver struct {
	defaults map[string]string // VAR -> the default some service declared for it
	byAlias  map[string]string // magic name component (upper) -> template database name
	single   string            // the sole database's name, "" when 0 or >1
	hosts    []hostRewrite     // compose service name -> {{db.<n>.host}}
	dsnPaths []*regexp.Regexp  // PostgreSQL DSN database segments to rewrite
	cacheURL []*regexp.Regexp  // password-less redis:// URLs to credential
	// literals are the application database names the engine services declared.
	// Nothing may still name one after translation: the managed database will
	// not have it, so an application pointed there would fail to connect.
	literals map[string]string // declared name -> template database name
}

type hostRewrite struct {
	re   *regexp.Regexp
	repl string
}

// engineAliases are the magic-name components that conventionally refer to an
// engine rather than to a compose service — Coolify writes
// SERVICE_PASSWORD_POSTGRES even when the service is called `teable-db`.
var engineAliases = map[domain.DbEngine][]string{
	domain.EnginePostgreSQL: {"POSTGRES", "POSTGRESQL", "PG", "PGSQL"},
	domain.EngineMySQL:      {"MYSQL", "MYSQLROOT"},
	domain.EngineMariaDB:    {"MARIADB", "MYSQL", "MYSQLROOT"},
	domain.EngineMongoDB:    {"MONGO", "MONGODB"},
	domain.EngineRedis:      {"REDIS"},
	domain.EngineValkey:     {"VALKEY", "REDIS"},
}

// collectDefaults gathers every `${VAR:-default}` the compose project
// declares. Interpolation is project-wide: a default written on one service is
// the value every other service's bare reference resolves to.
func collectDefaults(svcs []service) map[string]string {
	out := map[string]string{}
	record := func(v string) {
		for _, ref := range scanDefaults(v) {
			if _, exists := out[ref.name]; !exists {
				out[ref.name] = ref.def
			}
		}
	}
	for _, s := range svcs {
		record(s.image)
		for _, kv := range s.env {
			if i := strings.Index(kv, "="); i >= 0 {
				record(kv[i+1:])
			}
		}
	}
	return out
}

// resolveImageRef substitutes compose variables in an image reference —
// `ghcr.io/acme/web:${TAG:-latest}` is a reference the schema cannot store
// until the tag is decided. A variable with no default anywhere leaves a `$`
// behind, and the schema's reference check refuses it.
func resolveImageRef(image string, defaults map[string]string) string {
	return walk(image, func(name, def string, hasDef bool) string {
		if hasDef {
			return def
		}
		return defaults[name]
	})
}

func newEnvResolver(svcs []service, dbs []dbService, dbNames map[string]string) *envResolver {
	e := &envResolver{
		defaults: collectDefaults(svcs),
		byAlias:  map[string]string{},
		literals: map[string]string{},
	}

	ambiguous := map[string]bool{}
	claim := func(alias, dbName string) {
		if prior, ok := e.byAlias[alias]; ok && prior != dbName {
			ambiguous[alias] = true
			return
		}
		e.byAlias[alias] = dbName
	}
	for _, d := range dbs {
		name := dbNames[d.svc.name]
		claim(strings.ToUpper(nonNameRe.ReplaceAllString(strings.ToLower(d.svc.name), "")), name)
		for _, a := range engineAliases[d.engine] {
			claim(a, name)
		}
		e.hosts = append(e.hosts, hostRewrite{
			re:   hostnameRefRe(d.svc.name),
			repl: "${1}{{db." + name + ".host}}${2}",
		})
		// In a DSN for an engine with named databases the path segment *is*
		// the database, so it resolves through the placeholder whatever
		// upstream called it. Redis and Valkey are excluded deliberately:
		// their path is a database *number*, and rewriting it would point the
		// application at a name their protocol has no way to use.
		if domain.EngineDefaults(d.engine, "").DatabaseEnv != "" {
			e.dsnPaths = append(e.dsnPaths, regexp.MustCompile(
				`(\{\{db\.`+regexp.QuoteMeta(name)+`\.host\}\}(?::[0-9]+)?/)[A-Za-z0-9_.-]+`))
		}
		if d.engine == domain.EngineRedis || d.engine == domain.EngineValkey {
			// Template databases always require a password, so a URL upstream
			// wrote without credentials has to gain them or the app cannot
			// authenticate.
			e.cacheURL = append(e.cacheURL, regexp.MustCompile(
				`(rediss?://)(\{\{db\.`+regexp.QuoteMeta(name)+`\.host\}\})`))
		}
		for _, kv := range d.svc.env {
			i := strings.Index(kv, "=")
			if i < 0 {
				continue
			}
			key, value := kv[:i], kv[i+1:]
			if dbNameVars[key] {
				if declared := walk(value, func(_, def string, hasDef bool) string { return def }); declared != "" {
					e.literals[declared] = name
				}
				continue
			}
			// A magic name used as this engine's credential belongs to this
			// database, whatever it is called. Coolify's MySQL templates
			// habitually name it after the application —
			// `MYSQL_USER=$SERVICE_USER_WORDPRESS` — so without this the
			// application's own reference to that name resolves to an
			// unrelated random secret and it authenticates as nobody.
			//
			// It resolves to the *root* credentials, because a scoped
			// application user is not something a managed database creates;
			// every other converted template already connects as root, so this
			// is the same posture, not a new one.
			if !dbCredentialVars[key] {
				continue
			}
			for _, m := range magicRefRe.FindAllStringSubmatch(value, -1) {
				if parsed, ok := parseMagic(m[1]); ok && parsed.kind == magicGenerated {
					claim(strings.ToUpper(parsed.name), name)
				}
			}
		}
	}
	for a := range ambiguous {
		delete(e.byAlias, a)
	}
	if len(dbs) == 1 {
		e.single = dbNames[dbs[0].svc.name]
	}
	return e
}

type varRef struct{ name, def string }

// scanDefaults collects the `${VAR:-default}` declarations in one value.
func scanDefaults(v string) []varRef {
	var out []varRef
	walk(v, func(name, def string, hasDef bool) string {
		if hasDef {
			out = append(out, varRef{name: name, def: def})
		}
		return ""
	})
	return out
}

// walk scans a compose value left to right and calls fn for every variable
// reference, substituting whatever fn returns. `$$` is compose's escape for a
// literal dollar sign and is passed through as one.
func walk(v string, fn func(name, def string, hasDef bool) string) string {
	var b strings.Builder
	for i := 0; i < len(v); {
		if v[i] != '$' {
			b.WriteByte(v[i])
			i++
			continue
		}
		if i+1 < len(v) && v[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 < len(v) && v[i+1] == '{' {
			end := strings.IndexByte(v[i+2:], '}')
			if end < 0 {
				b.WriteByte(v[i])
				i++
				continue
			}
			body := v[i+2 : i+2+end]
			name, def, hasDef := splitBraced(body)
			b.WriteString(fn(name, def, hasDef))
			i += 2 + end + 1
			continue
		}
		j := i + 1
		for j < len(v) && (v[j] == '_' || (v[j] >= 'A' && v[j] <= 'Z') || (v[j] >= 'a' && v[j] <= 'z') || (v[j] >= '0' && v[j] <= '9')) {
			j++
		}
		if j == i+1 {
			b.WriteByte(v[i])
			i++
			continue
		}
		b.WriteString(fn(v[i+1:j], "", false))
		i = j
	}
	return b.String()
}

// splitBraced separates `NAME`, `NAME:-default`, `NAME-default`, `NAME:?msg`
// and `NAME:+alt`. Only the default forms carry a usable value; the others
// resolve to nothing, which the caller reports.
func splitBraced(body string) (name, def string, hasDef bool) {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ':':
			if i+1 < len(body) && (body[i+1] == '-' || body[i+1] == '=') {
				return body[:i], body[i+2:], true
			}
			return body[:i], "", false
		case '-', '=':
			return body[:i], body[i+1:], true
		case '?', '+':
			return body[:i], "", false
		}
	}
	return body, "", false
}

// translate converts one compose environment entry into one template entry.
// The bool reports whether anything should be emitted at all: routing
// declarations and bare pass-through keys carry no value of their own.
func translate(e *envResolver, kv string, svcName string, secretUses map[string]int, r *reject) (key, value string, emit bool) {
	i := strings.Index(kv, "=")
	if i < 0 {
		return translateBareKey(kv, svcName, secretUses, r)
	}
	key, raw := kv[:i], kv[i+1:]
	if m, ok := parseMagic(key); ok && (m.kind == magicFQDN || m.kind == magicURL) {
		// `SERVICE_FQDN_APP_3000=/path` pins a route path, which the schema's
		// single-domain routing cannot express. A value of `/` names the whole
		// domain and asks for nothing extra.
		if v := strings.TrimSpace(raw); v != "" && v != "/" {
			r.add("service %q routes on the sub-path %q; templates route a whole domain", svcName, raw)
		}
		return "", "", false
	}

	empties := 0
	out := walk(raw, func(name, def string, hasDef bool) string {
		v, ok := e.lookup(name, def, hasDef, svcName, secretUses, r)
		if !ok {
			empties++
		}
		return v
	})
	// A key whose whole value is one undefined variable is an operator-supplied
	// setting: it installs empty and is edited afterwards, exactly as it would
	// under Coolify. The same variable *embedded* in a larger string is not —
	// it would install a truncated URL or DSN that looks configured and is not.
	if empties > 0 && strings.TrimSpace(out) != "" {
		r.add("%s interpolates a variable with no default into %q; the installed value would be malformed", key, raw)
	}
	// A value that *is* an application database name has to resolve through the
	// placeholder: templates write `WORDPRESS_DB_NAME=wordpress` as a literal,
	// and the managed database is not called that. Whole-value equality only —
	// the same word inside a path or a URL is not a database reference.
	if db, ok := e.literals[strings.TrimSpace(out)]; ok {
		return key, "{{db." + db + ".database}}", true
	}
	return key, e.rewriteAddresses(out), true
}

// schemeRe matches a URL scheme introducer. Compose templates habitually name
// a service after the protocol it speaks — `redis`, `postgresql` — so the
// scheme is masked before hostname rewriting, or `redis://redis:6379` would
// have both halves replaced.
var schemeRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)

var maskRe = regexp.MustCompile("\x00([0-9]+)\x00")

// rewriteAddresses turns compose's intra-project addressing into placeholders:
// a database service's name becomes its container DNS name, a PostgreSQL DSN's
// path becomes the managed database, and a password-less cache URL gains the
// credentials the managed engine requires.
func (e *envResolver) rewriteAddresses(v string) string {
	var schemes []string
	masked := schemeRe.ReplaceAllStringFunc(v, func(m string) string {
		schemes = append(schemes, m)
		return "\x00" + fmt.Sprint(len(schemes)-1) + "\x00"
	})
	for _, h := range e.hosts {
		masked = h.re.ReplaceAllString(masked, h.repl)
	}
	out := maskRe.ReplaceAllStringFunc(masked, func(m string) string {
		i, err := atoiStrict(maskRe.FindStringSubmatch(m)[1])
		if err != nil || i >= len(schemes) {
			return m
		}
		return schemes[i]
	})

	for _, re := range e.dsnPaths {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			return re.FindStringSubmatch(m)[1] + "{{db." + dbNameIn(m) + ".database}}"
		})
	}
	for _, re := range e.cacheURL {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			s := re.FindStringSubmatch(m)
			return s[1] + ":{{db." + dbNameIn(m) + ".password}}@" + s[2]
		})
	}
	return out
}

var placeholderDbRe = regexp.MustCompile(`\{\{db\.([a-z0-9-]+)\.`)

// dbNameIn reads the database name back out of a placeholder the rewrite
// itself produced, so the follow-up token names the same database.
func dbNameIn(s string) string {
	m := placeholderDbRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// residualDatabaseName reports an application database name that survived
// translation. The managed database has only its engine default, so a value
// still naming `docmost` or `outline` would point the application at a
// database nothing creates.
func (e *envResolver) residualDatabaseName(value string) (string, bool) {
	if !strings.Contains(value, "{{db.") {
		return "", false
	}
	for literal := range e.literals {
		if hostnameRefRe(literal).MatchString(value) {
			return literal, true
		}
	}
	return "", false
}

// translateBareKey handles compose's valueless entries. Coolify uses them both
// to declare routing and to ask for a generated value under a fixed name;
// anything else is an inherit-from-the-host request with nothing to inherit.
func translateBareKey(key, svcName string, secretUses map[string]int, r *reject) (string, string, bool) {
	m, ok := parseMagic(key)
	if !ok {
		return "", "", false
	}
	switch m.kind {
	case magicFQDN, magicURL:
		return "", "", false
	case magicUnsupported:
		r.add("service %q generates %s for %s, which {{secret.N}} cannot reproduce", svcName, m.name, key)
		return "", "", false
	case magicGenerated:
		secretUses[m.verb+"_"+m.name]++
		return key, fmt.Sprintf("{{secret.%d}}", m.bytes), true
	}
	return "", "", false
}

// lookup resolves one variable reference. The bool is false when the reference
// resolves to nothing, which the caller weighs against where it appeared.
func (e *envResolver) lookup(name, def string, hasDef bool, svcName string, secretUses map[string]int, r *reject) (string, bool) {
	if m, ok := parseMagic(name); ok {
		switch m.kind {
		case magicFQDN:
			return "{{domain}}", true
		case magicURL:
			return "https://{{domain}}", true
		case magicUnsupported:
			r.add("service %q reads %s, which is %s — {{secret.N}} cannot reproduce it", svcName, name, m.name)
			return "", true
		case magicGenerated:
			if db, ok := e.byAlias[strings.ToUpper(m.name)]; ok {
				switch m.verb {
				case "USER", "LOWERCASEUSER":
					return "{{db." + db + ".user}}", true
				case "PASSWORD", "PASSWORD_64", "PASSWORDWITHSYMBOLS", "PASSWORDWITHSYMBOLS_64":
					return "{{db." + db + ".password}}", true
				}
			}
			secretUses[m.verb+"_"+m.name]++
			return fmt.Sprintf("{{secret.%d}}", m.bytes), true
		}
		r.add("service %q reads unrecognized magic variable %s", svcName, name)
		return "", true
	}
	// A variable that names an application database has to become the managed
	// database's own, whatever the upstream default called it.
	if dbNameVars[name] && e.single != "" {
		return "{{db." + e.single + ".database}}", true
	}
	if hasDef {
		return def, true
	}
	if d, ok := e.defaults[name]; ok {
		return d, true
	}
	return "", false
}

package webserver

import (
	"bytes"
	"fmt"
	"sort"
	"text/template"
)

// PoolSpec is the desired state of one account's dedicated PHP-FPM pool. The
// pool runs as the account's own user with a private socket — this is the
// per-account isolation primitive (plan.md §7).
type PoolSpec struct {
	User          string            // account system user (pool name + run user/group)
	Socket        string            // unix socket path
	WebServerUser string            // group allowed to access the socket
	MaxChildren   int               // pm.max_children (from package memory limit)
	AdminValues   map[string]string // php_admin_value overrides (memory_limit, ...)
}

var poolTmpl = template.Must(template.New("phpfpm-pool").Parse(`; Managed by CypherPanel — do not edit by hand.
[{{ .User }}]
user = {{ .User }}
group = {{ .User }}
listen = {{ .Socket }}
listen.owner = {{ .User }}
listen.group = {{ .WebServerUser }}
listen.mode = 0660
pm = ondemand
pm.max_children = {{ .MaxChildren }}
pm.process_idle_timeout = 10s
pm.max_requests = 500
chdir = /
{{- range .AdminPairs }}
php_admin_value[{{ .Key }}] = {{ .Value }}
{{- end }}
`))

type kv struct{ Key, Value string }

// RenderPHPFPMPool renders an account's PHP-FPM pool file.
func RenderPHPFPMPool(spec PoolSpec) ([]byte, error) {
	if spec.User == "" || spec.Socket == "" {
		return nil, fmt.Errorf("webserver: pool spec missing user or socket")
	}
	if spec.MaxChildren <= 0 {
		spec.MaxChildren = 5
	}
	if spec.WebServerUser == "" {
		spec.WebServerUser = "www-data"
	}

	// Deterministic ordering so output is stable (golden-file friendly).
	keys := make([]string, 0, len(spec.AdminValues))
	for k := range spec.AdminValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]kv, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, kv{Key: k, Value: spec.AdminValues[k]})
	}

	data := struct {
		PoolSpec
		AdminPairs []kv
	}{PoolSpec: spec, AdminPairs: pairs}

	var buf bytes.Buffer
	if err := poolTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("webserver: rendering php-fpm pool for %s: %w", spec.User, err)
	}
	return buf.Bytes(), nil
}

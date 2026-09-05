package proxy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Traefik is the Proxy driver (ADR-004, docs/features/routing-and-tls.md): it
// owns the per-node Traefik instance's configuration via the file provider —
// static config + per-Application dynamic fragments — and ensures Traefik is
// running (ensure.go). It implements the docker.Router seam and the proxy
// lifecycle behind one type; nothing Traefik-shaped leaks out of this package
// (project-structure rule 2).
type Traefik struct {
	cfg     Config
	appsDir string // <Dir>/apps — where dynamic fragments live

	// The panel's ACME account, as last carried in desired state
	// (agent-identity-and-tls.md §4). Guarded because the worker writes it from
	// the sync path while the reconcile loop reads it; both run on the agent's
	// own goroutines and neither may block the other.
	mu              sync.RWMutex
	desiredEmail    string
	desiredCAServer string
}

// New constructs the Traefik proxy driver. A nil Config.Engine selects
// fragment-only mode: fragment management still works while the Proxy
// lifecycle (EnsureProxy / AttachNetwork) is disabled.
func New(cfg Config) *Traefik {
	return &Traefik{cfg: cfg, appsDir: fragmentsDir(cfg.Dir)}
}

// SetACME records the panel's ACME account as carried in desired state. The
// host-local override in Config wins per field, so an operator who set
// CYPHER_ACME_EMAIL on this box keeps it (agent-identity-and-tls.md §4).
//
// Idempotent and cheap: the values are only read when a static config or a
// route fragment is rendered, and both of those already skip an unchanged
// write, so calling this on every sync converges without churn.
func (t *Traefik) SetACME(email, caServer string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.desiredEmail, t.desiredCAServer = email, caServer
}

// acme resolves the effective ACME account: the host-local override if set,
// otherwise what the panel sent.
func (t *Traefik) acme() (email, caServer string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	email, caServer = t.desiredEmail, t.desiredCAServer
	if t.cfg.ACMEEmail != "" {
		email = t.cfg.ACMEEmail
	}
	if t.cfg.ACMECAServer != "" {
		caServer = t.cfg.ACMECAServer
	}
	return email, caServer
}

// hasResolver reports whether this node's Proxy actually has a certificate
// resolver — the single question the fragment writer asks before promising
// HTTPS for a route.
func (t *Traefik) hasResolver() bool {
	email, _ := t.acme()
	return email != ""
}

// Name identifies the proxy driver ("traefik"; "caddy" is a later driver).
func (t *Traefik) Name() string { return "traefik" }

// SetRoute writes the Traefik fragment for an app atomically.
func (t *Traefik) SetRoute(ctx context.Context, appID string, route *agentv1.RouteSpec, upstream string) error {
	if route == nil {
		return fmt.Errorf("route spec is nil")
	}
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return fmt.Errorf("invalid appID")
	}
	if strings.Contains(route.Domain, "`") {
		return fmt.Errorf("invalid domain")
	}
	if strings.Contains(route.PathPrefix, "`") || strings.Contains(route.PathPrefix, "&&") || strings.Contains(route.PathPrefix, "||") {
		return fmt.Errorf("invalid path prefix")
	}

	if err := os.MkdirAll(t.appsDir, 0755); err != nil {
		return fmt.Errorf("creating traefik apps dir: %w", err)
	}

	type Service struct {
		LoadBalancer struct {
			Servers []struct {
				URL string `yaml:"url"`
			} `yaml:"servers"`
		} `yaml:"loadBalancer"`
	}
	type Router struct {
		Rule        string   `yaml:"rule"`
		EntryPoints []string `yaml:"entryPoints,omitempty"`
		Middlewares []string `yaml:"middlewares,omitempty"`
		Service     string   `yaml:"service"`
		TLS         *struct {
			CertResolver string `yaml:"certResolver,omitempty"`
		} `yaml:"tls,omitempty"`
	}
	type Middleware struct {
		RedirectScheme *struct {
			Scheme    string `yaml:"scheme"`
			Permanent bool   `yaml:"permanent"`
		} `yaml:"redirectScheme,omitempty"`
		Headers *struct {
			CustomResponseHeaders map[string]string `yaml:"customResponseHeaders"`
		} `yaml:"headers,omitempty"`
	}

	doc := struct {
		HTTP struct {
			Routers     map[string]Router     `yaml:"routers"`
			Middlewares map[string]Middleware `yaml:"middlewares,omitempty"`
			Services    map[string]Service    `yaml:"services"`
		} `yaml:"http"`
	}{}

	doc.HTTP.Routers = make(map[string]Router)
	doc.HTTP.Services = make(map[string]Service)
	doc.HTTP.Middlewares = make(map[string]Middleware)

	// Every proxied response carries a marker so "is this domain actually
	// reaching my app?" has a definitive answer rather than a guess. Without
	// it the only signal is the upstream's own Server header, which tells you
	// what answered but not whether it was us (routing-and-tls.md).
	markName := appID + "-mark"
	mark := Middleware{}
	mark.Headers = &struct {
		CustomResponseHeaders map[string]string `yaml:"customResponseHeaders"`
	}{CustomResponseHeaders: map[string]string{ServedByHeader: ServedByValue}}
	doc.HTTP.Middlewares[markName] = mark

	rule := fmt.Sprintf("Host(`%s`)", route.Domain)
	if route.PathPrefix != "" {
		rule += fmt.Sprintf(" && PathPrefix(`%s`)", route.PathPrefix)
	}

	// HTTPS is promised only when this node actually has a certificate
	// resolver. Naming `certResolver: le` when the static config defines no
	// such resolver was the original bug: Traefik fell back to its own
	// self-signed default certificate while the HTTP router permanently
	// redirected every visitor to it, so a domain whose issuance was not
	// configured served a browser warning instead of the app — and the panel
	// still called it "HTTPS · auto-renews" (agent-identity-and-tls.md §5).
	//
	// With no resolver the route is plain HTTP on `web` only: the app is
	// reachable, the deploy is unaffected, and the plane reports
	// http_only_no_resolver so the UI can say so out loud.
	if route.Https && t.hasResolver() {
		// The TLS router serves websecure only; a sibling router answers the
		// same rule on web with a permanent redirect (routing-and-tls.md §5 —
		// per-app, so HTTP-only apps on this node keep serving plain HTTP).
		// Traefik answers ACME HTTP-01 challenges ahead of routing, so the
		// redirect never blocks issuance.
		doc.HTTP.Routers[appID] = Router{
			Rule:        rule,
			EntryPoints: []string{"websecure"},
			Middlewares: []string{markName},
			Service:     appID,
			TLS: &struct {
				CertResolver string `yaml:"certResolver,omitempty"`
			}{
				CertResolver: acmeResolver, // defined in the static config (ensure.go)
			},
		}
		doc.HTTP.Routers[appID+"-http"] = Router{
			Rule:        rule,
			EntryPoints: []string{"web"},
			Middlewares: []string{appID + "-redirect"},
			Service:     appID,
		}
		mw := Middleware{}
		mw.RedirectScheme = &struct {
			Scheme    string `yaml:"scheme"`
			Permanent bool   `yaml:"permanent"`
		}{Scheme: "https", Permanent: true}
		doc.HTTP.Middlewares[appID+"-redirect"] = mw
	} else {
		// Pinned to `web`, not left to Traefik's "attach to every entrypoint"
		// default. A router with no TLS configuration has no business on the
		// TLS entrypoint: bound there it answers `https://` with Traefik's
		// self-signed default certificate, which is the browser warning this
		// feature exists to remove — and for a route the operator deliberately
		// declared HTTP-only it was never asked for either. Both HTTP-only
		// routes and https routes on a node with no resolver take this branch
		// and are served over plain HTTP only (routing-and-tls.md §7).
		doc.HTTP.Routers[appID] = Router{
			Rule:        rule,
			EntryPoints: []string{"web"},
			Middlewares: []string{markName},
			Service:     appID,
		}
	}

	srv := Service{}
	srv.LoadBalancer.Servers = []struct {
		URL string `yaml:"url"`
	}{{URL: "http://" + upstream}}

	doc.HTTP.Services[appID] = srv

	b, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling route: %w", err)
	}

	// Skip an identical write: the reconciler now calls this every cycle, and
	// rewriting the file would make Traefik reload its config on a timer for no
	// reason.
	cleanAppsDirCmp := filepath.Clean(t.appsDir)
	if existing, readErr := os.ReadFile(filepath.Clean(filepath.Join(cleanAppsDirCmp, appID+".yml"))); readErr == nil && bytes.Equal(existing, b) {
		return nil
	}

	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return fmt.Errorf("invalid route path")
	}
	tmpPath := filepath.Join(cleanAppsDir, "."+appID+".yml.tmp")

	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return fmt.Errorf("writing route tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming route file: %w", err)
	}

	return nil
}

// RemoveRoute deletes the Traefik fragment for an app.
func (t *Traefik) RemoveRoute(ctx context.Context, appID string) error {
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return fmt.Errorf("invalid appID")
	}
	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return fmt.Errorf("invalid route path")
	}
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing route file: %w", err)
	}
	return nil
}

// Route returns the currently configured upstream for an app, if any.
func (t *Traefik) Route(ctx context.Context, appID string) (upstream string, ok bool, err error) {
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return "", false, fmt.Errorf("invalid appID")
	}
	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return "", false, fmt.Errorf("invalid route path")
	}
	b, err := os.ReadFile(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading route file: %w", err)
	}

	var doc map[string]interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", false, fmt.Errorf("parsing route file: %w", err)
	}

	httpMap, ok := doc["http"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	servicesMap, ok := httpMap["services"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	appSrv, ok := servicesMap[appID].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	lb, ok := appSrv["loadBalancer"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	servers, ok := lb["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		return "", false, nil
	}
	srv, ok := servers[0].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	url, ok := srv["url"].(string)
	if !ok {
		return "", false, nil
	}

	url = strings.TrimPrefix(url, "http://")
	return url, true, nil
}

// ServedByHeader marks responses that actually passed through this panel's
// proxy. A domain can resolve to the right host and still be answered by
// something else on :80 — another web server, a control panel's default vhost
// — and that failure is invisible without a marker to look for.
const (
	ServedByHeader = "X-Served-By"
	ServedByValue  = "cypherpanel"
)

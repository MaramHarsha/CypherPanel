package proxy_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
	"github.com/MaramHarsha/cypherpanel/agent/proxy"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// fragment is the part of a written Traefik fragment the tests assert on. The
// entrypoint list is read back as a list rather than grepped for: `- web` is a
// prefix of `- websecure`, and an absent `entryPoints` key — which means "every
// entrypoint", the opposite of a restriction — greps identically to a restricted
// one.
type fragment struct {
	HTTP struct {
		Routers map[string]struct {
			Rule        string   `yaml:"rule"`
			EntryPoints []string `yaml:"entryPoints"`
			Service     string   `yaml:"service"`
			TLS         *struct {
				CertResolver string `yaml:"certResolver"`
			} `yaml:"tls"`
		} `yaml:"routers"`
	} `yaml:"http"`
}

// readFragment parses <dir>/apps/<appID>.yml, failing the test if it cannot.
func readFragment(t *testing.T, dir, appID string) fragment {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "apps", appID+".yml"))
	if err != nil {
		t.Fatalf("reading fragment for %s: %v", appID, err)
	}
	var f fragment
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("parsing fragment for %s: %v\n%s", appID, err, b)
	}
	return f
}

// wantEntryPoints asserts the exact entrypoint list of one router.
func wantEntryPoints(t *testing.T, f fragment, router string, want ...string) {
	t.Helper()
	r, ok := f.HTTP.Routers[router]
	if !ok {
		t.Fatalf("fragment has no router %q (routers: %v)", router, f.HTTP.Routers)
	}
	if len(r.EntryPoints) != len(want) {
		t.Fatalf("router %q entryPoints = %v, want %v", router, r.EntryPoints, want)
	}
	for i, w := range want {
		if r.EntryPoints[i] != w {
			t.Fatalf("router %q entryPoints = %v, want %v", router, r.EntryPoints, want)
		}
	}
}

func TestFragmentWriteObserveRemove(t *testing.T) {
	dir := t.TempDir()
	// A resolver exists on this node, so an https route is written as HTTPS.
	w := proxy.New(proxy.Config{Dir: dir, ACMEEmail: "ops@example.com"})
	ctx := context.Background()

	if _, ok, err := w.Route(ctx, "app1"); err != nil || ok {
		t.Fatalf("Route before write = ok:%v err:%v, want not-ok", ok, err)
	}

	spec := &agentv1.RouteSpec{Domain: "example.com", PathPrefix: "/api", Https: true}
	if err := w.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
		t.Fatalf("SetRoute: %v", err)
	}

	// Fragment lands under <Dir>/apps.
	b, err := os.ReadFile(filepath.Join(dir, "apps", "app1.yml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "example.com") || !strings.Contains(content, "10.0.0.1:8080") || !strings.Contains(content, "certResolver") {
		t.Fatalf("fragment missing expected content:\n%s", content)
	}
	// An HTTPS route pins its TLS router to websecure and answers the same
	// rule on web with a permanent scheme redirect (routing-and-tls.md §5).
	for _, want := range []string{"app1-http", "app1-redirect", "redirectScheme", "permanent: true"} {
		if !strings.Contains(content, want) {
			t.Fatalf("https fragment missing %q:\n%s", want, content)
		}
	}
	f1 := readFragment(t, dir, "app1")
	wantEntryPoints(t, f1, "app1", "websecure")
	wantEntryPoints(t, f1, "app1-http", "web")

	// An HTTP-only route gets neither TLS nor redirect plumbing.
	if err := w.SetRoute(ctx, "app2", &agentv1.RouteSpec{Domain: "plain.example.com"}, "10.0.0.2:8080"); err != nil {
		t.Fatalf("SetRoute http-only: %v", err)
	}
	b2, err := os.ReadFile(filepath.Join(dir, "apps", "app2.yml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if s := string(b2); strings.Contains(s, "redirectScheme") || strings.Contains(s, "certResolver") {
		t.Fatalf("http-only fragment has TLS/redirect plumbing:\n%s", s)
	}
	// …and is pinned to `web`, so it is not answered on :443 under Traefik's
	// self-signed default certificate (routing-and-tls.md §7).
	wantEntryPoints(t, readFragment(t, dir, "app2"), "app2", "web")

	up, ok, err := w.Route(ctx, "app1")
	if err != nil || !ok || up != "10.0.0.1:8080" {
		t.Fatalf("Route = %q ok:%v err:%v, want 10.0.0.1:8080", up, ok, err)
	}

	if err := w.RemoveRoute(ctx, "app1"); err != nil {
		t.Fatalf("RemoveRoute: %v", err)
	}
	if _, ok, _ := w.Route(ctx, "app1"); ok {
		t.Fatal("Route still ok after remove")
	}
}

// fakeEngine records the RunConfigs and network connects the proxy requests.
type fakeEngine struct {
	ensured   []engine.RunConfig
	connected []string // "container/network"
}

func (f *fakeEngine) EnsureContainer(_ context.Context, cfg engine.RunConfig) error {
	f.ensured = append(f.ensured, cfg)
	return nil
}

func (f *fakeEngine) ConnectNetwork(_ context.Context, container, network string) error {
	f.connected = append(f.connected, container+"/"+network)
	return nil
}

func TestEnsureProxyRunsTraefikWithStaticConfig(t *testing.T) {
	dir := t.TempDir()
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", ACMEEmail: "ops@example.com", Engine: fe})

	if err := p.EnsureProxy(context.Background()); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	// Static config written, with the file provider, both entrypoints, and the
	// ACME resolver (email set).
	static, err := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if err != nil {
		t.Fatalf("static config: %v", err)
	}
	s := string(static)
	for _, want := range []string{":80", ":443", "/etc/traefik/apps", "watch: true", "certificatesResolvers", "ops@example.com", "httpChallenge"} {
		if !strings.Contains(s, want) {
			t.Fatalf("static config missing %q:\n%s", want, s)
		}
	}
	// No Traefik API/dashboard exposure.
	if strings.Contains(s, "api:") || strings.Contains(s, "dashboard") {
		t.Fatalf("static config exposes the Traefik API/dashboard:\n%s", s)
	}
	// ACME store created 0600.
	fi, err := os.Stat(filepath.Join(dir, "acme.json"))
	if err != nil {
		t.Fatalf("acme.json: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("acme.json perm = %v, want 0600", fi.Mode().Perm())
	}

	if len(fe.ensured) != 1 {
		t.Fatalf("EnsureContainer called %d times, want 1", len(fe.ensured))
	}
	c := fe.ensured[0]
	if c.Name != "cypher-proxy" || c.Image != "traefik:v3.3" {
		t.Fatalf("container = %+v", c)
	}
	if c.Labels[driver.LabelManaged] != "proxy" {
		t.Fatalf("managed label = %q", c.Labels[driver.LabelManaged])
	}
	hostPorts := map[int]int{}
	for _, pm := range c.Ports {
		hostPorts[pm.Host] = pm.Container
	}
	if hostPorts[80] != 80 || hostPorts[443] != 443 {
		t.Fatalf("ports = %+v, want 80→80 443→443", c.Ports)
	}
	if len(c.Mounts) != 1 || c.Mounts[0].Source != dir || c.Mounts[0].Target != "/etc/traefik" {
		t.Fatalf("mounts = %+v", c.Mounts)
	}
}

// Idempotency by construction: two EnsureProxy calls produce a byte-identical
// static config and an identical RunConfig, so engine.EnsureContainer (tested
// separately) no-ops the second — the reconciler converge-twice property.
func TestEnsureProxyIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", ACMEEmail: "ops@example.com", Engine: fe})

	if err := p.EnsureProxy(context.Background()); err != nil {
		t.Fatalf("first EnsureProxy: %v", err)
	}
	firstStatic, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if err := p.EnsureProxy(context.Background()); err != nil {
		t.Fatalf("second EnsureProxy: %v", err)
	}
	secondStatic, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if string(firstStatic) != string(secondStatic) {
		t.Fatal("static config differs across converge — not idempotent")
	}
	if len(fe.ensured) != 2 {
		t.Fatalf("want 2 EnsureContainer calls, got %d", len(fe.ensured))
	}
	if fe.ensured[0].Labels["cypherpanel.static-hash"] != fe.ensured[1].Labels["cypherpanel.static-hash"] {
		t.Fatal("static-hash label changed across identical converge — engine would needlessly recreate")
	}
}

// No ACME email ⇒ HTTP-only: no cert resolver in the static config.
func TestEnsureProxyWithoutACME(t *testing.T) {
	dir := t.TempDir()
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", Engine: &fakeEngine{}})
	if err := p.EnsureProxy(context.Background()); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	s, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if strings.Contains(string(s), "certificatesResolvers") {
		t.Fatalf("cert resolver configured without an ACME email:\n%s", s)
	}
}

func TestAttachNetwork(t *testing.T) {
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: t.TempDir(), Image: "traefik:v3.3", Engine: fe})
	if err := p.AttachNetwork(context.Background(), "cypher-env1"); err != nil {
		t.Fatalf("AttachNetwork: %v", err)
	}
	if len(fe.connected) != 1 || fe.connected[0] != "cypher-proxy/cypher-env1" {
		t.Fatalf("connected = %v, want [cypher-proxy/cypher-env1]", fe.connected)
	}
}

// Fragment-only mode (no Engine) leaves the lifecycle methods as safe no-ops.
func TestFragmentOnlyModeNoOps(t *testing.T) {
	p := proxy.New(proxy.Config{Dir: t.TempDir()})
	ctx := context.Background()
	if err := p.EnsureProxy(ctx); err != nil {
		t.Fatalf("EnsureProxy no-op: %v", err)
	}
	if err := p.AttachNetwork(ctx, "cypher-env1"); err != nil {
		t.Fatalf("AttachNetwork no-op: %v", err)
	}
}

// ─── TLS honesty (agent-identity-and-tls.md §4–5) ───────────────────────────

// The whole point of the feature: with no ACME account on this node, an https
// route must NOT name a resolver the static config does not define, and must
// not permanently redirect visitors to a port serving Traefik's self-signed
// default. It serves plain HTTP instead — reachable, honest, and unaffected as
// a deploy.
func TestHTTPSRouteServesPlainHTTPWithoutAResolver(t *testing.T) {
	dir := t.TempDir()
	w := proxy.New(proxy.Config{Dir: dir})

	spec := &agentv1.RouteSpec{Domain: "app.example.com", Https: true}
	if err := w.SetRoute(context.Background(), "app1", spec, "10.0.0.1:8080"); err != nil {
		t.Fatalf("SetRoute: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "apps", "app1.yml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(b)
	for _, forbidden := range []string{"certResolver", "redirectScheme"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("fragment names %q with no resolver configured:\n%s", forbidden, got)
		}
	}
	// Restricted to `web` — asserted as a list, because an absent entryPoints
	// key would bind websecure too and serve the app under Traefik's
	// self-signed default certificate (§4).
	wantEntryPoints(t, readFragment(t, dir, "app1"), "app1", "web")
	// It is still a working route.
	for _, want := range []string{"app.example.com", "10.0.0.1:8080"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fragment missing %q:\n%s", want, got)
		}
	}
}

// The account arrives in desired state, not from the host: SetACME flips both
// the static config and every subsequently written fragment.
func TestSetACMEFromDesiredStateEnablesTLS(t *testing.T) {
	dir := t.TempDir()
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", Engine: fe})
	ctx := context.Background()

	spec := &agentv1.RouteSpec{Domain: "app.example.com", Https: true}
	if err := p.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
		t.Fatalf("SetRoute before: %v", err)
	}
	if err := p.EnsureProxy(ctx); err != nil {
		t.Fatalf("EnsureProxy before: %v", err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if strings.Contains(string(before), "certificatesResolvers") {
		t.Fatalf("resolver configured before the panel sent an account:\n%s", before)
	}

	// The panel's TLS settings land.
	p.SetACME("ops@example.com", "https://acme-staging-v02.api.letsencrypt.org/directory")

	if err := p.EnsureProxy(ctx); err != nil {
		t.Fatalf("EnsureProxy after: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	for _, want := range []string{"certificatesResolvers", "ops@example.com", "acme-staging-v02"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("static config missing %q after SetACME:\n%s", want, after)
		}
	}
	if err := p.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
		t.Fatalf("SetRoute after: %v", err)
	}
	frag, _ := os.ReadFile(filepath.Join(dir, "apps", "app1.yml"))
	for _, want := range []string{"certResolver: le", "redirectScheme"} {
		if !strings.Contains(string(frag), want) {
			t.Fatalf("fragment missing %q after SetACME:\n%s", want, frag)
		}
	}
	wantEntryPoints(t, readFragment(t, dir, "app1"), "app1", "websecure")
	// The static config changed, so the container identity must change with it
	// — Traefik reads static config only at start.
	if fe.ensured[0].Labels["cypherpanel.static-hash"] == fe.ensured[1].Labels["cypherpanel.static-hash"] {
		t.Fatal("static-hash unchanged after the resolver appeared; the Proxy would never restart into it")
	}
}

// The host-local override wins over desired state, per field — the documented
// escape hatch for a node that must use its own ACME account.
func TestHostOverrideWinsOverDesiredState(t *testing.T) {
	dir := t.TempDir()
	p := proxy.New(proxy.Config{
		Dir:       dir,
		Image:     "traefik:v3.3",
		ACMEEmail: "local@example.com",
		Engine:    &fakeEngine{},
	})
	p.SetACME("panel@example.com", "https://acme.example.com/directory")

	if err := p.EnsureProxy(context.Background()); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	s, _ := os.ReadFile(filepath.Join(dir, "traefik.yml"))
	if !strings.Contains(string(s), "local@example.com") {
		t.Fatalf("host override not honoured:\n%s", s)
	}
	if strings.Contains(string(s), "panel@example.com") {
		t.Fatalf("desired state overrode the host setting:\n%s", s)
	}
	// The CA server has no host override, so the panel's value applies.
	if !strings.Contains(string(s), "https://acme.example.com/directory") {
		t.Fatalf("panel CA server not applied:\n%s", s)
	}
}

// Converging twice equals converging once (ENGINEERING rule 13), for both TLS
// states — including the second SetRoute, which must skip the write entirely.
func TestRouteConvergenceIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email string
	}{
		{"with a resolver", "ops@example.com"},
		{"without a resolver", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := proxy.New(proxy.Config{Dir: dir, ACMEEmail: tc.email})
			ctx := context.Background()
			spec := &agentv1.RouteSpec{Domain: "app.example.com", Https: true, PathPrefix: "/api"}

			if err := p.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
				t.Fatalf("first SetRoute: %v", err)
			}
			path := filepath.Join(dir, "apps", "app1.yml")
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			firstMod := info.ModTime()

			if err := p.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
				t.Fatalf("second SetRoute: %v", err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("fragment changed across converge:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
			info, err = os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if !info.ModTime().Equal(firstMod) {
				t.Fatal("identical fragment was rewritten; Traefik would reload its config for nothing")
			}
			// And the observed route is unchanged.
			up, ok, err := p.Route(ctx, "app1")
			if err != nil || !ok || up != "10.0.0.1:8080" {
				t.Fatalf("Route = %q ok:%v err:%v", up, ok, err)
			}
		})
	}
}

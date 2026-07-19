package proxy_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
	"github.com/MaramHarsha/cypherpanel/agent/proxy"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

func TestFragmentWriteObserveRemove(t *testing.T) {
	dir := t.TempDir()
	w := proxy.New(proxy.Config{Dir: dir})
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

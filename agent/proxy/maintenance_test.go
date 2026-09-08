package proxy_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/proxy"
)

func TestEnsureMaintenanceRunsTheResponderAndReturnsItsUpstream(t *testing.T) {
	dir := t.TempDir()
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", Engine: fe})

	up, err := p.EnsureMaintenance(context.Background())
	if err != nil {
		t.Fatalf("EnsureMaintenance: %v", err)
	}
	if up != "cypher-maintenance:8080" {
		t.Fatalf("upstream = %q", up)
	}
	if !slices.Contains(fe.networks, "cypher-maintenance") {
		t.Fatalf("networks = %v, want the dedicated maintenance network", fe.networks)
	}
	// The Proxy must join that network or the upstream it was just handed is
	// unreachable — which would serve Traefik's own 502 in place of our 503.
	if !slices.Contains(fe.connected, "cypher-proxy/cypher-maintenance") {
		t.Fatalf("connected = %v, want the proxy attached", fe.connected)
	}

	var run = fe.ensured[len(fe.ensured)-1]
	if run.Name != "cypher-maintenance" {
		t.Fatalf("container name = %q", run.Name)
	}
	if run.Image != proxy.DefaultMaintenanceImage {
		t.Fatalf("image = %q, want the pinned default", run.Image)
	}
	if len(run.Ports) != 0 {
		t.Fatalf("responder published %d host ports; it is reachable only from the Proxy", len(run.Ports))
	}
	for _, m := range run.Mounts {
		if !m.ReadOnly {
			t.Fatalf("mount %s is writable; the responder only ever reads its page", m.Target)
		}
	}

	page, err := os.ReadFile(filepath.Join(dir, "maintenance", "503.html"))
	if err != nil {
		t.Fatalf("reading page: %v", err)
	}
	// The page's audience is the public internet. Naming what is behind it —
	// or loading anything from off the node — is a fingerprint we are not
	// obliged to hand out (§7).
	for _, banned := range []string{"<script", "http://", "https://", "cypher", "Cypher"} {
		if strings.Contains(string(page), banned) {
			t.Fatalf("maintenance page contains %q", banned)
		}
	}
	conf, err := os.ReadFile(filepath.Join(dir, "maintenance", "nginx.conf"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	for _, want := range []string{"return 503", "Retry-After 300", "no-store"} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("responder config is missing %q", want)
		}
	}
}

// Converging twice must not rewrite the page or move the container: the config
// hash is what makes the second EnsureContainer a daemon no-op, so it has to be
// stable across calls.
func TestEnsureMaintenanceIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: dir, Image: "traefik:v3.3", Engine: fe})

	if _, err := p.EnsureMaintenance(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := fe.ensured[len(fe.ensured)-1]
	if _, err := p.EnsureMaintenance(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	second := fe.ensured[len(fe.ensured)-1]
	if first.Labels["cypherpanel.maintenance-hash"] != second.Labels["cypherpanel.maintenance-hash"] {
		t.Fatal("the responder's content hash changed between two identical converges")
	}
}

// A node that has never raised a page still sweeps once at agent start — a
// responder left running by a previous process is invisible to this one — and
// then never asks the daemon about it again.
func TestRemoveMaintenanceSweepsOnceThenStaysQuiet(t *testing.T) {
	fe := &fakeEngine{}
	p := proxy.New(proxy.Config{Dir: t.TempDir(), Engine: fe})

	for range 3 {
		if err := p.RemoveMaintenance(context.Background()); err != nil {
			t.Fatalf("RemoveMaintenance: %v", err)
		}
	}
	if len(fe.removed) != 1 {
		t.Fatalf("removals = %v, want exactly one sweep", fe.removed)
	}

	// Raising a page arms it again, so lowering one actually removes it.
	if _, err := p.EnsureMaintenance(context.Background()); err != nil {
		t.Fatalf("EnsureMaintenance: %v", err)
	}
	if err := p.RemoveMaintenance(context.Background()); err != nil {
		t.Fatalf("RemoveMaintenance: %v", err)
	}
	if len(fe.removed) != 2 {
		t.Fatalf("removals = %v, want the responder taken down", fe.removed)
	}
}

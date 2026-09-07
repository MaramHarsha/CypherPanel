package proxy

// The maintenance responder (app-access-control.md §7).
//
// Traefik cannot serve a body of ours: it has no middleware that returns a
// fixed response, its `errors` middleware needs a responder service anyway and
// preserves the upstream's status code, and a Yaegi plugin is a network
// dependency in the one component that must come up inert (ADR-004). Pointing
// the route at a dead upstream gets Traefik's own 502, which tells a crawler
// the app is broken rather than telling it to come back.
//
// So the node runs a second managed container the way it runs the Proxy: a
// pinned image, management labels, `unless-stopped`, on a dedicated network the
// Proxy also joins — one network rather than every environment network, because
// the responder has no business reaching applications. It answers every request
// with 503, `Retry-After: 300`, `Cache-Control: no-store` and a generic page.
//
// The page names no application, no revision and no version: its whole audience
// is the public internet, and telling a stranger which product is down here is
// a fingerprint we are not obliged to hand out (§7, §11).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
)

const (
	maintenanceContainerName = "cypher-maintenance"
	maintenanceManagedValue  = "maintenance"
	// maintenanceNetwork is dedicated and shared: the responder and the Proxy
	// are its only members, ever.
	maintenanceNetwork = "cypher-maintenance"
	maintenancePort    = 8080
	// maintenanceSubdir sits beside the fragments under the agent's own
	// directory, so the page and its config survive a container recreation for
	// the same reason the certificates do.
	maintenanceSubdir       = "maintenance"
	containerMaintenanceDir = "/etc/cypher-maintenance"
	maintenanceConfName     = "nginx.conf"
	maintenancePageName     = "503.html"
	maintenanceHashLabel    = "cypherpanel.maintenance-hash"
)

// DefaultMaintenanceImage is the responder image when the agent is not told
// otherwise. Pinned to a minor, never :latest — the Proxy's rule, for the
// Proxy's reason: a surprise major is a fleet-wide change nobody asked for.
//
// This is the feature's one new external dependency: a second image pull per
// node. It is stated rather than hidden — an air-gapped node gets the IP
// allowlist and the preview password and a refusal on the third capability.
const DefaultMaintenanceImage = "nginx:1.27-alpine"

// EnsureMaintenance converges the node's maintenance responder and reports the
// upstream a route should point at while an application is in maintenance.
//
// Idempotent: the config-hash label makes a converged node a no-op, and the
// network create and the Proxy's attachment are both idempotent by
// construction. An error means the responder is NOT serving, and the caller's
// contract is to leave the route alone — never to take an application down as a
// side effect of failing to take it down politely (§8).
func (t *Traefik) EnsureMaintenance(ctx context.Context) (string, error) {
	if t.cfg.Engine == nil {
		return "", fmt.Errorf("proxy: this node does not manage a maintenance responder")
	}
	image := t.cfg.MaintenanceImage
	if image == "" {
		image = DefaultMaintenanceImage
	}

	dir := filepath.Join(t.cfg.Dir, maintenanceSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("proxy: creating maintenance dir: %w", err)
	}
	conf := maintenanceConf()
	page := []byte(maintenancePage)
	if err := writeIfChanged(filepath.Join(dir, maintenanceConfName), conf); err != nil {
		return "", fmt.Errorf("proxy: writing maintenance config: %w", err)
	}
	if err := writeIfChanged(filepath.Join(dir, maintenancePageName), page); err != nil {
		return "", fmt.Errorf("proxy: writing maintenance page: %w", err)
	}

	if err := t.cfg.Engine.EnsureNetwork(ctx, maintenanceNetwork, map[string]string{
		driver.LabelManaged: maintenanceManagedValue,
	}); err != nil {
		return "", fmt.Errorf("proxy: creating the maintenance network: %w", err)
	}

	// The config is read only at start, so a digest of it is folded into the
	// container's identity — a changed page changes the label, changes the
	// config hash, and recreates the responder once. Exactly the static
	// config's arrangement in ensure.go, for exactly its reason.
	sum := sha256.Sum256(append(conf, page...))
	cfg := engine.RunConfig{
		Name:  maintenanceContainerName,
		Image: image,
		Cmd:   []string{"nginx", "-c", containerMaintenanceDir + "/" + maintenanceConfName, "-g", "daemon off;"},
		Labels: map[string]string{
			driver.LabelManaged:  maintenanceManagedValue,
			maintenanceHashLabel: hex.EncodeToString(sum[:]),
		},
		// No published ports: the responder is reachable only from the Proxy,
		// over the shared network. A holding page on the host's :80 would be a
		// second thing listening where the Proxy already is.
		Mounts:  []engine.Mount{{Source: dir, Target: containerMaintenanceDir, ReadOnly: true}},
		Network: maintenanceNetwork,
	}
	if err := t.cfg.Engine.EnsureContainer(ctx, cfg); err != nil {
		return "", fmt.Errorf("proxy: ensuring the maintenance responder: %w", err)
	}
	if err := t.cfg.Engine.ConnectNetwork(ctx, proxyContainerName, maintenanceNetwork); err != nil {
		return "", fmt.Errorf("proxy: attaching the proxy to the maintenance network: %w", err)
	}

	t.mu.Lock()
	t.maintenanceMayExist = true
	t.mu.Unlock()

	return maintenanceContainerName + ":" + strconv.Itoa(maintenancePort), nil
}

// RemoveMaintenance takes the responder down once nothing on this node is in
// maintenance, so a node that never uses the feature pays nothing.
//
// The flag starts TRUE rather than false, which is the whole reason this costs
// one daemon call at agent start and none thereafter: a responder left running
// by a previous process is not visible in this one's memory, and sweeping once
// is cheaper than asking the daemon every cycle forever. The network is left
// behind deliberately — the Proxy is still attached to it, so removing it would
// fail, and an empty bridge network costs nothing.
func (t *Traefik) RemoveMaintenance(ctx context.Context) error {
	if t.cfg.Engine == nil {
		return nil
	}
	t.mu.Lock()
	may := t.maintenanceMayExist
	t.maintenanceMayExist = false
	t.mu.Unlock()
	if !may {
		return nil
	}
	if err := t.cfg.Engine.RemoveContainer(ctx, maintenanceContainerName); err != nil {
		t.mu.Lock()
		t.maintenanceMayExist = true
		t.mu.Unlock()
		return fmt.Errorf("proxy: removing the maintenance responder: %w", err)
	}
	return nil
}

// writeIfChanged skips a byte-identical write, so converging twice leaves the
// file's mtime alone and the responder's config hash stable.
func writeIfChanged(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) { //nolint:gosec // path is agent-owned
		return nil
	}
	return writeFileAtomic(path, data, 0o644)
}

// maintenanceConf is the responder's whole configuration. Every request, on
// every path and every method, is a 503 carrying the page — there is no route
// through it to anything else, which is what makes it safe to leave running.
func maintenanceConf() []byte {
	return fmt.Appendf(nil, `worker_processes 1;
error_log /dev/stderr warn;
pid /tmp/nginx.pid;

events { worker_connections 128; }

http {
  access_log off;
  default_type text/html;
  server_tokens off;

  server {
    listen %d default_server;
    root %s;

    error_page 503 /503.html;

    location = /503.html {
      internal;
      add_header Retry-After 300 always;
      add_header Cache-Control "no-store" always;
    }

    location / { return 503; }
  }
}
`, maintenancePort, containerMaintenanceDir)
}

// maintenancePage is deliberately generic and deliberately self-contained: no
// application name, no version, no external stylesheet and no script.
const maintenancePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Temporarily unavailable</title>
<style>
  :root { color-scheme: light dark; }
  body {
    margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
    background: #f6f7f9; color: #1f2329;
    font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  main { max-width: 32rem; padding: 2.5rem 1.5rem; text-align: center; }
  h1 { margin: 0 0 .75rem; font-size: 1.35rem; font-weight: 600; letter-spacing: -0.01em; }
  p { margin: 0; color: #5a6472; }
  @media (prefers-color-scheme: dark) {
    body { background: #14161a; color: #e6e8ec; }
    p { color: #98a1af; }
  }
</style>
</head>
<body>
  <main>
    <h1>Down for maintenance</h1>
    <p>This site is temporarily unavailable while planned work finishes. Please try again shortly.</p>
  </main>
</body>
</html>
`

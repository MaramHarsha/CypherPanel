package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
)

// Proxy layout inside the Traefik container: the agent's host directory
// (Config.Dir) is bind-mounted at containerConfigDir; fragments, static
// config, and the ACME store sit beneath it at deterministic paths.
const (
	proxyContainerName = "cypher-proxy"
	proxyManagedValue  = "proxy"
	containerConfigDir = "/etc/traefik"
	staticConfigName   = "traefik.yml"
	acmeStoreName      = "acme.json"
	fragmentsSubdir    = "apps"
	acmeResolver       = "le" // the resolver name fragments reference (traefik.go)
	staticHashLabel    = "cypherpanel.static-hash"
)

// Engine is the slice of the Docker engine the Proxy manages Traefik through
// (consumer-defined, rule 6). *engine.Client satisfies it.
type Engine interface {
	EnsureContainer(ctx context.Context, cfg engine.RunConfig) error
	ConnectNetwork(ctx context.Context, container, network string) error
}

// Config configures the Traefik Proxy driver.
type Config struct {
	// Dir is the host directory the agent owns and bind-mounts into Traefik at
	// /etc/traefik: <Dir>/apps holds fragments, <Dir>/traefik.yml the static
	// config, <Dir>/acme.json the certificates.
	Dir string
	// Image is the pinned Traefik image (e.g. "traefik:v3.3"). Never :latest —
	// a surprise major is a fleet-wide outage (routing-and-tls.md §3).
	Image string
	// ACMEEmail is a HOST-LOCAL override of the panel's ACME account email
	// (CYPHER_ACME_EMAIL). Non-empty wins over whatever desired state carries,
	// which is the escape hatch for a node that must use a different account —
	// or for a hermetic test. Empty is the normal case: the account arrives in
	// DesiredState (agent-identity-and-tls.md §4) via SetACME.
	ACMEEmail string
	// ACMECAServer is the same kind of host-local override for the ACME
	// directory URL (CYPHER_ACME_CASERVER, e.g. the Let's Encrypt staging
	// endpoint). Empty defers to desired state, and if that is empty too, to
	// Let's Encrypt production.
	ACMECAServer string
	// Engine runs and networks the Traefik container. A nil Engine selects
	// fragment-only mode: SetRoute/RemoveRoute/Route still manage fragments,
	// while EnsureProxy and AttachNetwork are no-ops (this driver does not
	// manage the node's Proxy container).
	Engine Engine
	Log    *slog.Logger
}

func fragmentsDir(dir string) string { return filepath.Join(dir, fragmentsSubdir) }

// EnsureProxy converges the node's Traefik Proxy: it (re)writes the static
// config, guarantees the ACME store exists with correct permissions, and
// ensures the cypher-proxy container runs that config. Idempotent — the
// container's config-hash and static-hash labels make a converged node a
// no-op (engine.EnsureContainer). Fragment-only mode (no Engine) is a no-op.
func (t *Traefik) EnsureProxy(ctx context.Context) error {
	if t.cfg.Engine == nil {
		return nil
	}
	if t.cfg.Image == "" {
		return fmt.Errorf("proxy: Traefik image not configured")
	}
	if err := os.MkdirAll(t.appsDir, 0o755); err != nil {
		return fmt.Errorf("proxy: creating fragments dir: %w", err)
	}

	static, err := t.renderStaticConfig()
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(t.cfg.Dir, staticConfigName), static, 0o644); err != nil {
		return fmt.Errorf("proxy: writing static config: %w", err)
	}
	// Traefik requires the ACME store to exist and be 0600 or it refuses to
	// start; create it empty if absent, never truncate an existing one.
	acmePath := filepath.Join(t.cfg.Dir, acmeStoreName)
	if f, err := os.OpenFile(acmePath, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	} else if !os.IsExist(err) {
		return fmt.Errorf("proxy: creating acme store: %w", err)
	}
	_ = os.Chmod(acmePath, 0o600)

	sum := sha256.Sum256(static)
	cfg := engine.RunConfig{
		Name:  proxyContainerName,
		Image: t.cfg.Image,
		Labels: map[string]string{
			driver.LabelManaged: proxyManagedValue,
			// Static config changes are read only at Traefik start, so fold a
			// digest of it into the container identity: a changed static config
			// changes this label, changes the config hash, and forces recreate.
			staticHashLabel: hex.EncodeToString(sum[:]),
		},
		Ports: []engine.PortMap{{Host: 80, Container: 80}, {Host: 443, Container: 443}},
		Mounts: []engine.Mount{
			// Read-write: the agent writes config + fragments, Traefik writes
			// acme.json. All under one bind so certs and routes survive a
			// container recreation (routing-and-tls.md §5).
			{Source: t.cfg.Dir, Target: containerConfigDir, ReadOnly: false},
		},
	}
	if err := t.cfg.Engine.EnsureContainer(ctx, cfg); err != nil {
		return fmt.Errorf("proxy: ensuring Traefik container: %w", err)
	}
	return nil
}

// AttachNetwork connects the Proxy to an environment network so it can reach
// that environment's Application upstreams (routing-and-tls.md §4). Idempotent.
// Fragment-only mode is a no-op.
func (t *Traefik) AttachNetwork(ctx context.Context, network string) error {
	if t.cfg.Engine == nil {
		return nil
	}
	if err := t.cfg.Engine.ConnectNetwork(ctx, proxyContainerName, network); err != nil {
		return fmt.Errorf("proxy: attaching to %s: %w", network, err)
	}
	return nil
}

// staticConfig is the Traefik v3 static configuration the agent owns. No
// `docker` provider, no API, no dashboard — config comes only from the agent's
// file fragments (ADR-004: Traefik's API is never exposed).
type staticConfig struct {
	EntryPoints   map[string]entryPoint   `yaml:"entryPoints"`
	Providers     providers               `yaml:"providers"`
	CertResolvers map[string]certResolver `yaml:"certificatesResolvers,omitempty"`
}

type entryPoint struct {
	Address string `yaml:"address"`
}

type providers struct {
	File fileProvider `yaml:"file"`
}

type fileProvider struct {
	Directory string `yaml:"directory"`
	Watch     bool   `yaml:"watch"`
}

type certResolver struct {
	ACME acmeConfig `yaml:"acme"`
}

type acmeConfig struct {
	Email         string        `yaml:"email"`
	Storage       string        `yaml:"storage"`
	CAServer      string        `yaml:"caServer,omitempty"`
	HTTPChallenge httpChallenge `yaml:"httpChallenge"`
}

type httpChallenge struct {
	EntryPoint string `yaml:"entryPoint"`
}

func (t *Traefik) renderStaticConfig() ([]byte, error) {
	c := staticConfig{
		EntryPoints: map[string]entryPoint{
			"web":       {Address: ":80"},
			"websecure": {Address: ":443"},
		},
		Providers: providers{File: fileProvider{
			Directory: containerConfigDir + "/" + fragmentsSubdir,
			Watch:     true,
		}},
	}
	// A cert resolver is only configured when an ACME account email is known —
	// from the panel's TLS settings, or from the host-local override. Without
	// one (a fresh panel, CI, a deliberately HTTP-only node) the Proxy serves
	// plain HTTP, and the fragment writer agrees with it: routes stop naming a
	// resolver that does not exist (agent-identity-and-tls.md §4).
	if email, caServer := t.acme(); email != "" {
		c.CertResolvers = map[string]certResolver{
			acmeResolver: {ACME: acmeConfig{
				Email:         email,
				Storage:       containerConfigDir + "/" + acmeStoreName,
				CAServer:      caServer,
				HTTPChallenge: httpChallenge{EntryPoint: "web"},
			}},
		}
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("proxy: marshaling static config: %w", err)
	}
	return data, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

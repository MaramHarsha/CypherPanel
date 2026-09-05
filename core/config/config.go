// Package config loads cypherd's runtime configuration from the environment.
//
// It fails closed: security-critical values (the database URL, the master key)
// are required with no silent default, because a plausible-looking default
// would be worse than a clear startup error. Everything else has a sane
// default tuned to the footprint budget in docs/vision.md.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/secret"
)

// Config is the fully-resolved control-plane configuration.
type Config struct {
	// DatabaseURL is the PostgreSQL DSN (required).
	DatabaseURL string
	// MasterKey decrypts secrets at rest, 32 bytes (required, base64 in env).
	MasterKey []byte

	// PublicHost is the hostname agents dial and the SAN placed on the plane's
	// server certificate. Must be reachable by agents (default "localhost").
	PublicHost string

	// PublicURL is the browser-facing base URL of the panel — scheme, host and
	// an optional port, nothing else (e.g. "https://panel.example.com"). Set it
	// when TLS or a reverse proxy sits in front: every link the panel writes to
	// itself (the email-change confirmation, the GitHub webhook URL, the agent
	// join command) is built from it. Empty keeps the derived
	// http://<PublicHost>:<http port> (control-plane-hardening.md §6).
	PublicURL string

	// TrustedProxies are the CIDRs whose peer address may speak for a client:
	// only from inside one of them does the panel read X-Forwarded-For or
	// X-Real-IP when it decides the client address a rate limit is keyed by
	// and whether an inbound X-Request-Id is honoured. Empty (the default)
	// means the TCP peer is always the client — the safe default for a panel
	// exposed directly (control-plane-hardening.md §§2, 5).
	TrustedProxies []netip.Prefix

	// HTTPAddr is the bind address for the REST API + web console.
	HTTPAddr string
	// EnrollAddr is the bind address for the gRPC enrollment endpoint (TLS).
	EnrollAddr string
	// NATSAddr is the bind address for the embedded NATS mTLS listener.
	NATSAddr string

	// AdminEmail / AdminPassword bootstrap the single owner account on first
	// boot. Optional: if unset and no users exist, cypherd prints a warning and
	// serves no login until an account is created.
	AdminEmail    string
	AdminPassword string

	// Durations.
	JoinTokenTTL   time.Duration // how long a join token stays valid
	AgentCertTTL   time.Duration // lifetime of issued agent certificates
	SessionTTL     time.Duration // browser/API session lifetime
	HeartbeatStale time.Duration // no heartbeat for this long ⇒ status unknown
	SweepInterval  time.Duration // how often the stale sweep runs
	ShutdownGrace  time.Duration // max time to drain on shutdown

	// MinDiskFree is the boot-time disk headroom floor in bytes (threat-model
	// §8 req 10). cypherd refuses to start with less free space than this;
	// 0 disables the check. Default 1 GiB.
	MinDiskFree uint64

	// DataDir holds cypherd's durable local state (the file-backed WORK
	// stream). Default /var/lib/cypherd.
	DataDir string

	RuntimeLogsMaxAge   time.Duration
	RuntimeLogsMaxBytes uint64

	// UpdateCheck is whether the panel polls a release feed to tell owners a
	// newer version exists. "off" disables the outbound request entirely — the
	// whole answer for an air-gapped install. The panel never updates itself
	// either way (ADR-010; control-plane-hardening.md §3).
	UpdateCheck bool
	// UpdateFeedURL is the feed to poll; empty means the package default
	// (GitHub's releases/latest for this project).
	UpdateFeedURL string
}

// Load reads and validates configuration from the process environment.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:       os.Getenv("CYPHERD_DATABASE_URL"),
		PublicHost:        envOr("CYPHERD_PUBLIC_HOST", "localhost"),
		PublicURL:         strings.TrimRight(envOr("CYPHERD_PUBLIC_URL", ""), "/"),
		HTTPAddr:          envOr("CYPHERD_HTTP_ADDR", ":8080"),
		EnrollAddr:        envOr("CYPHERD_ENROLL_ADDR", ":8443"),
		NATSAddr:          envOr("CYPHERD_NATS_ADDR", ":4222"),
		AdminEmail:        os.Getenv("CYPHERD_ADMIN_EMAIL"),
		AdminPassword:     os.Getenv("CYPHERD_ADMIN_PASSWORD"),
		JoinTokenTTL:      envDuration("CYPHERD_JOIN_TOKEN_TTL", 15*time.Minute),
		AgentCertTTL:      envDuration("CYPHERD_AGENT_CERT_TTL", 90*24*time.Hour),
		SessionTTL:        envDuration("CYPHERD_SESSION_TTL", 24*time.Hour),
		HeartbeatStale:    envDuration("CYPHERD_HEARTBEAT_STALE", 90*time.Second),
		SweepInterval:     envDuration("CYPHERD_SWEEP_INTERVAL", 30*time.Second),
		ShutdownGrace:     envDuration("CYPHERD_SHUTDOWN_GRACE", 20*time.Second),
		DataDir:           envOr("CYPHERD_DATA_DIR", "/var/lib/cypherd"),
		RuntimeLogsMaxAge: envDuration("CYPHERD_RUNTIME_LOGS_MAX_AGE", 24*time.Hour),
		UpdateCheck:       !strings.EqualFold(envOr("CYPHERD_UPDATE_CHECK", "on"), "off"),
		UpdateFeedURL:     envOr("CYPHERD_UPDATE_FEED_URL", ""),
	}

	runtimeBytes, err := envBytes("CYPHERD_RUNTIME_LOGS_MAX_BYTES", 536870912) // 512 MiB
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	c.RuntimeLogsMaxBytes = runtimeBytes

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: CYPHERD_DATABASE_URL is required")
	}

	rawKey := os.Getenv("CYPHERD_MASTER_KEY")
	if rawKey == "" {
		return Config{}, fmt.Errorf("config: CYPHERD_MASTER_KEY is required (base64 of %d random bytes)", secret.KeySize)
	}
	key, err := secret.DecodeMasterKey(rawKey)
	if err != nil {
		return Config{}, fmt.Errorf("config: CYPHERD_MASTER_KEY invalid: %w", err)
	}
	c.MasterKey = key

	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		return Config{}, fmt.Errorf("config: CYPHERD_ADMIN_EMAIL and CYPHERD_ADMIN_PASSWORD must be set together")
	}

	if err := validatePublicURL(c.PublicURL); err != nil {
		return Config{}, fmt.Errorf("config: CYPHERD_PUBLIC_URL invalid: %w", err)
	}

	proxies, err := parseCIDRList(os.Getenv("CYPHERD_TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, fmt.Errorf("config: CYPHERD_TRUSTED_PROXIES invalid: %w", err)
	}
	c.TrustedProxies = proxies

	minFree, err := envBytes("CYPHERD_MIN_DISK_FREE", 1<<30)
	if err != nil {
		return Config{}, fmt.Errorf("config: CYPHERD_MIN_DISK_FREE invalid: %w", err)
	}
	c.MinDiskFree = minFree

	return c, nil
}

// AdvertisedNATSURL is the URL agents are told to dial for the data plane. It
// combines the public host with the embedded NATS listener's port.
func (c Config) AdvertisedNATSURL() string {
	return "tls://" + net.JoinHostPort(c.PublicHost, portOf(c.NATSAddr, "4222"))
}

// AdvertisedEnrollAddr is the host:port an agent dials for gRPC enrollment.
func (c Config) AdvertisedEnrollAddr() string {
	return net.JoinHostPort(c.PublicHost, portOf(c.EnrollAddr, "8443"))
}

// AdvertisedConsoleURL is the base URL the panel writes into every link to
// itself: the installer and CA a joining server fetches, the GitHub webhook
// URL, the email-change confirmation link. CYPHERD_PUBLIC_URL wins when set —
// that is the only value that can carry https when TLS terminates in front.
// Without it the derived plain-http URL stands: TLS for the operator-facing
// plane (TB1) is the deployment's reverse proxy, and the CA fingerprint in the
// install command is what protects enrollment on this channel.
func (c Config) AdvertisedConsoleURL() string {
	if c.PublicURL != "" {
		return c.PublicURL
	}
	return "http://" + net.JoinHostPort(c.PublicHost, portOf(c.HTTPAddr, "8080"))
}

// validatePublicURL accepts an absolute http(s) origin and nothing more: a
// path, a query, a fragment or credentials would silently corrupt every link
// built by concatenation, so they are refused at boot rather than mailed out.
func validatePublicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q must be scheme://host[:port] only — no path, query, fragment or credentials", raw)
	}
	return nil
}

// parseCIDRList parses a comma-separated CIDR list. A bare address is accepted
// as its own single-address prefix, since "10.0.0.7" is what an operator
// naturally writes for one proxy.
func parseCIDRList(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%q is neither a CIDR nor an address", part)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// portOf extracts the port from a bind address like ":4222" or "0.0.0.0:4222",
// falling back to def.
func portOf(addr, def string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return def
	}
	return port
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envBytes parses a byte count from the environment (plain integer, 0 allowed
// to disable). Unlike the tuning knobs above, a malformed value is an error,
// not a silent fallback: this knob guards a security property (§5.9).
func envBytes(key string, def uint64) (uint64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %q as bytes: %w", v, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

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
	"os"
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
}

// Load reads and validates configuration from the process environment.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:    os.Getenv("CYPHERD_DATABASE_URL"),
		PublicHost:     envOr("CYPHERD_PUBLIC_HOST", "localhost"),
		HTTPAddr:       envOr("CYPHERD_HTTP_ADDR", ":8080"),
		EnrollAddr:     envOr("CYPHERD_ENROLL_ADDR", ":8443"),
		NATSAddr:       envOr("CYPHERD_NATS_ADDR", ":4222"),
		AdminEmail:     os.Getenv("CYPHERD_ADMIN_EMAIL"),
		AdminPassword:  os.Getenv("CYPHERD_ADMIN_PASSWORD"),
		JoinTokenTTL:   envDuration("CYPHERD_JOIN_TOKEN_TTL", 15*time.Minute),
		AgentCertTTL:   envDuration("CYPHERD_AGENT_CERT_TTL", 90*24*time.Hour),
		SessionTTL:     envDuration("CYPHERD_SESSION_TTL", 24*time.Hour),
		HeartbeatStale: envDuration("CYPHERD_HEARTBEAT_STALE", 90*time.Second),
		SweepInterval:  envDuration("CYPHERD_SWEEP_INTERVAL", 30*time.Second),
		ShutdownGrace:  envDuration("CYPHERD_SHUTDOWN_GRACE", 20*time.Second),
	}

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

// Package config is the single source of runtime configuration for all
// CypherPanel binaries. Every value is environment-driven with a documented
// default; nothing (paths, endpoints, secrets) may be compiled into feature code.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Core holds CypherCore (control-plane API) configuration.
type Core struct {
	Env         string
	HTTPAddr    string
	GRPCAddr    string
	DatabaseURL string
	RedisURL    string
	NATSURL     string
	// NATSCreds is an optional NATS credentials file. The URL may also carry
	// user:pass; either way, production NATS must not run open (task.md).
	NATSCreds       string
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// DefaultPHPVersion is the PHP branch new accounts are provisioned on when
	// none is chosen. Not a hardcoded constant — set it to a version actually
	// installed on your servers (see plan.md Version Policy).
	DefaultPHPVersion string
	// PHPVersions is the set of PHP branches accounts may switch between,
	// offered in the UI. Set it to the branches actually installed across your
	// fleet (plan.md Version Policy: resolve against php.net's supported list,
	// don't ship an EOL set). Default is a current-branch starting point.
	PHPVersions []string
	// AdminerURL is where an Adminer instance is served (per-account DB admin
	// handoff). Empty → the "Open in Adminer" action is unavailable.
	AdminerURL string
	// PowerDNS authoritative API for the DNS zone editor. Empty PDNSAPIURL →
	// DNS management is unavailable. DNSNameservers seed each zone's NS records.
	PDNSAPIURL     string
	PDNSAPIKey     string
	DNSNameservers []string
	// SSL auto-renewal: scan every SSLRenewInterval and renew certs expiring
	// within SSLRenewThreshold. The threshold matches the agent's own >30-day
	// re-issue skip guard so a due cert is actually renewed, not skipped.
	SSLRenewInterval  time.Duration
	SSLRenewThreshold time.Duration
	// AuditRetentionDays prunes audit rows older than this (0 = keep forever).
	AuditRetentionDays int
	// mTLS material for the agent gRPC listener. Required in production;
	// in development an empty set means plaintext gRPC (local only).
	GRPCTLSCert     string
	GRPCTLSKey      string
	GRPCTLSClientCA string
	// DBEncryptionKey is the 32-byte AES key used to encrypt stored secrets
	// (hosted-account DB passwords). Required in production; in development it
	// is derived from the JWT secret so local setups work without extra config.
	DBEncryptionKey []byte
}

// Agent holds CypherAgent (per-server daemon) configuration.
type Agent struct {
	Env       string
	CoreAddr  string
	NATSURL   string
	NATSCreds string // optional NATS credentials file
	// mTLS material locations are configuration, never constants.
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
	// ACMEDirectory is the ACME v2 directory URL for SSL issuance (default
	// Let's Encrypt production). Point at a staging/test directory for testing.
	ACMEDirectory string
	// MariaDBDSN is the admin connection (go-sql-driver/mysql DSN) the agent
	// uses to provision hosted-account databases on this server, e.g.
	// "root:pw@tcp(127.0.0.1:3306)/". Empty → DB provisioning is unavailable.
	MariaDBDSN string
	// PowerDNS API for DNS-01 challenge solving (wildcard SSL). Empty → wildcard
	// certificates are unavailable and requests fail with a clear error.
	PDNSAPIURL string
	PDNSAPIKey string
}

// insecureDevSecret is only ever used when CYPHER_ENV=development and no
// secret is set; production startup fails instead of falling back.
const insecureDevSecret = "cypherpanel-dev-secret-do-not-use-in-production"

// LoadCore reads CypherCore configuration from the environment.
func LoadCore() (Core, error) {
	c := Core{
		Env:               envOr("CYPHER_ENV", EnvDevelopment),
		HTTPAddr:          envOr("CYPHER_HTTP_ADDR", ":8080"),
		GRPCAddr:          envOr("CYPHER_GRPC_ADDR", ":9090"),
		DatabaseURL:       envOr("CYPHER_DATABASE_URL", "postgres://cypher:cypher-dev-only@localhost:5432/cypherpanel?sslmode=disable"),
		RedisURL:          envOr("CYPHER_REDIS_URL", "redis://localhost:6379/0"),
		NATSURL:           envOr("CYPHER_NATS_URL", "nats://localhost:4222"),
		NATSCreds:         os.Getenv("CYPHER_NATS_CREDS"),
		JWTSecret:         os.Getenv("CYPHER_JWT_SECRET"),
		DefaultPHPVersion: envOr("CYPHER_DEFAULT_PHP_VERSION", "8.3"),
		PHPVersions:       splitList(envOr("CYPHER_PHP_VERSIONS", "8.2,8.3,8.4")),
		AdminerURL:        os.Getenv("CYPHER_ADMINER_URL"),
		PDNSAPIURL:        os.Getenv("CYPHER_PDNS_API_URL"),
		PDNSAPIKey:        os.Getenv("CYPHER_PDNS_API_KEY"),
		DNSNameservers:    splitList(envOr("CYPHER_DNS_NAMESERVERS", "ns1.cypherpanel.local,ns2.cypherpanel.local")),
		GRPCTLSCert:       os.Getenv("CYPHER_GRPC_TLS_CERT"),
		GRPCTLSKey:        os.Getenv("CYPHER_GRPC_TLS_KEY"),
		GRPCTLSClientCA:   os.Getenv("CYPHER_GRPC_TLS_CLIENT_CA"),
	}

	var err error
	if c.AccessTokenTTL, err = durationOr("CYPHER_ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return Core{}, err
	}
	if c.RefreshTokenTTL, err = durationOr("CYPHER_REFRESH_TOKEN_TTL", 30*24*time.Hour); err != nil {
		return Core{}, err
	}
	if c.SSLRenewInterval, err = durationOr("CYPHER_SSL_RENEW_INTERVAL", 12*time.Hour); err != nil {
		return Core{}, err
	}
	if c.SSLRenewThreshold, err = durationOr("CYPHER_SSL_RENEW_THRESHOLD", 30*24*time.Hour); err != nil {
		return Core{}, err
	}
	if c.AuditRetentionDays, err = intOr("CYPHER_AUDIT_RETENTION_DAYS", 90); err != nil {
		return Core{}, err
	}

	if c.JWTSecret == "" {
		if c.Env == EnvProduction {
			return Core{}, fmt.Errorf("CYPHER_JWT_SECRET is required when CYPHER_ENV=production")
		}
		c.JWTSecret = insecureDevSecret
	}
	if c.Env == EnvProduction && (c.GRPCTLSCert == "" || c.GRPCTLSKey == "" || c.GRPCTLSClientCA == "") {
		return Core{}, fmt.Errorf("CYPHER_GRPC_TLS_CERT, CYPHER_GRPC_TLS_KEY and CYPHER_GRPC_TLS_CLIENT_CA are required when CYPHER_ENV=production")
	}

	if c.DBEncryptionKey, err = loadEncryptionKey(os.Getenv("CYPHER_DB_ENCRYPTION_KEY"), c.Env, c.JWTSecret); err != nil {
		return Core{}, err
	}
	return c, nil
}

// loadEncryptionKey resolves the 32-byte secrets key. In production it must be
// an explicit 64-char hex value; in development it is derived from the JWT
// secret so no extra setup is needed.
func loadEncryptionKey(hexKey, env, jwtSecret string) ([]byte, error) {
	if hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("CYPHER_DB_ENCRYPTION_KEY: invalid hex: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("CYPHER_DB_ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key))
		}
		return key, nil
	}
	if env == EnvProduction {
		return nil, fmt.Errorf("CYPHER_DB_ENCRYPTION_KEY (64 hex chars) is required when CYPHER_ENV=production")
	}
	sum := sha256.Sum256([]byte("cypher-db-key:" + jwtSecret))
	return sum[:], nil
}

// LoadAgent reads CypherAgent configuration from the environment.
func LoadAgent() (Agent, error) {
	a := Agent{
		Env:           envOr("CYPHER_ENV", EnvDevelopment),
		CoreAddr:      envOr("CYPHER_AGENT_CORE_ADDR", "localhost:9090"),
		NATSURL:       envOr("CYPHER_AGENT_NATS_URL", "nats://localhost:4222"),
		NATSCreds:     os.Getenv("CYPHER_AGENT_NATS_CREDS"),
		ACMEDirectory: envOr("CYPHER_ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"),
		MariaDBDSN:    os.Getenv("CYPHER_AGENT_MARIADB_DSN"),
		PDNSAPIURL:    os.Getenv("CYPHER_AGENT_PDNS_API_URL"),
		PDNSAPIKey:    os.Getenv("CYPHER_AGENT_PDNS_API_KEY"),
		TLSCertFile:   os.Getenv("CYPHER_AGENT_TLS_CERT"),
		TLSKeyFile:    os.Getenv("CYPHER_AGENT_TLS_KEY"),
		TLSCAFile:     os.Getenv("CYPHER_AGENT_TLS_CA"),
	}
	if a.Env == EnvProduction {
		if a.TLSCertFile == "" || a.TLSKeyFile == "" || a.TLSCAFile == "" {
			return Agent{}, fmt.Errorf("CYPHER_AGENT_TLS_CERT, CYPHER_AGENT_TLS_KEY and CYPHER_AGENT_TLS_CA are required when CYPHER_ENV=production")
		}
	}
	return a, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList parses a comma-separated env value into trimmed, non-empty items.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intOr(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, v, err)
	}
	return n, nil
}

func durationOr(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, v, err)
	}
	return d, nil
}

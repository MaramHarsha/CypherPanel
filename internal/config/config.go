// Package config is the single source of runtime configuration for all
// CypherPanel binaries. Every value is environment-driven with a documented
// default; nothing (paths, endpoints, secrets) may be compiled into feature code.
package config

import (
	"fmt"
	"os"
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
	// DefaultPHPVersion is the PHP branch new accounts are provisioned on until
	// per-account selection lands. Not a hardcoded constant — set it to a
	// version actually installed on your servers (see plan.md Version Policy).
	DefaultPHPVersion string
	// mTLS material for the agent gRPC listener. Required in production;
	// in development an empty set means plaintext gRPC (local only).
	GRPCTLSCert     string
	GRPCTLSKey      string
	GRPCTLSClientCA string
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
}

// insecureDevSecret is only ever used when CYPHER_ENV=development and no
// secret is set; production startup fails instead of falling back.
const insecureDevSecret = "cypherpanel-dev-secret-do-not-use-in-production"

// LoadCore reads CypherCore configuration from the environment.
func LoadCore() (Core, error) {
	c := Core{
		Env:             envOr("CYPHER_ENV", EnvDevelopment),
		HTTPAddr:        envOr("CYPHER_HTTP_ADDR", ":8080"),
		GRPCAddr:        envOr("CYPHER_GRPC_ADDR", ":9090"),
		DatabaseURL:     envOr("CYPHER_DATABASE_URL", "postgres://cypher:cypher-dev-only@localhost:5432/cypherpanel?sslmode=disable"),
		RedisURL:        envOr("CYPHER_REDIS_URL", "redis://localhost:6379/0"),
		NATSURL:         envOr("CYPHER_NATS_URL", "nats://localhost:4222"),
		NATSCreds:         os.Getenv("CYPHER_NATS_CREDS"),
		JWTSecret:         os.Getenv("CYPHER_JWT_SECRET"),
		DefaultPHPVersion: envOr("CYPHER_DEFAULT_PHP_VERSION", "8.3"),
		GRPCTLSCert:       os.Getenv("CYPHER_GRPC_TLS_CERT"),
		GRPCTLSKey:      os.Getenv("CYPHER_GRPC_TLS_KEY"),
		GRPCTLSClientCA: os.Getenv("CYPHER_GRPC_TLS_CLIENT_CA"),
	}

	var err error
	if c.AccessTokenTTL, err = durationOr("CYPHER_ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return Core{}, err
	}
	if c.RefreshTokenTTL, err = durationOr("CYPHER_REFRESH_TOKEN_TTL", 30*24*time.Hour); err != nil {
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
	return c, nil
}

// LoadAgent reads CypherAgent configuration from the environment.
func LoadAgent() (Agent, error) {
	a := Agent{
		Env:         envOr("CYPHER_ENV", EnvDevelopment),
		CoreAddr:    envOr("CYPHER_AGENT_CORE_ADDR", "localhost:9090"),
		NATSURL:     envOr("CYPHER_AGENT_NATS_URL", "nats://localhost:4222"),
		NATSCreds:   os.Getenv("CYPHER_AGENT_NATS_CREDS"),
		TLSCertFile: os.Getenv("CYPHER_AGENT_TLS_CERT"),
		TLSKeyFile:  os.Getenv("CYPHER_AGENT_TLS_KEY"),
		TLSCAFile:   os.Getenv("CYPHER_AGENT_TLS_CA"),
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

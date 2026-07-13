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
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	RedisURL        string
	NATSURL         string
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// Agent holds CypherAgent (per-server daemon) configuration.
type Agent struct {
	Env      string
	CoreAddr string
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
		Env:         envOr("CYPHER_ENV", EnvDevelopment),
		HTTPAddr:    envOr("CYPHER_HTTP_ADDR", ":8080"),
		DatabaseURL: envOr("CYPHER_DATABASE_URL", "postgres://cypher:cypher-dev-only@localhost:5432/cypherpanel?sslmode=disable"),
		RedisURL:    envOr("CYPHER_REDIS_URL", "redis://localhost:6379/0"),
		NATSURL:     envOr("CYPHER_NATS_URL", "nats://localhost:4222"),
		JWTSecret:   os.Getenv("CYPHER_JWT_SECRET"),
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
	return c, nil
}

// LoadAgent reads CypherAgent configuration from the environment.
func LoadAgent() (Agent, error) {
	a := Agent{
		Env:         envOr("CYPHER_ENV", EnvDevelopment),
		CoreAddr:    envOr("CYPHER_AGENT_CORE_ADDR", "localhost:9090"),
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

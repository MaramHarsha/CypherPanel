package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// validKey is a well-formed master key for tests (32 zero bytes, base64).
var validKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

// setBaseEnv sets the minimum valid environment; individual tests override.
func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CYPHERD_DATABASE_URL", "postgres://localhost/cypher")
	t.Setenv("CYPHERD_MASTER_KEY", validKey)
	// Neutralize anything inherited from the host environment.
	for _, k := range []string{
		"CYPHERD_PUBLIC_HOST", "CYPHERD_HTTP_ADDR", "CYPHERD_ENROLL_ADDR",
		"CYPHERD_NATS_ADDR", "CYPHERD_ADMIN_EMAIL", "CYPHERD_ADMIN_PASSWORD",
		"CYPHERD_MIN_DISK_FREE", "CYPHERD_JOIN_TOKEN_TTL",
	} {
		t.Setenv(k, "")
	}
}

// TestLoadFailsClosed: the security-critical values have no defaults
// (ENGINEERING: fail closed; package doc).
func TestLoadFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T)
		wantErr string
	}{
		{"missing database url", func(t *testing.T) { t.Setenv("CYPHERD_DATABASE_URL", "") }, "CYPHERD_DATABASE_URL"},
		{"missing master key", func(t *testing.T) { t.Setenv("CYPHERD_MASTER_KEY", "") }, "CYPHERD_MASTER_KEY"},
		{"malformed master key", func(t *testing.T) { t.Setenv("CYPHERD_MASTER_KEY", "not-base64!") }, "CYPHERD_MASTER_KEY"},
		{"short master key", func(t *testing.T) {
			t.Setenv("CYPHERD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16)))
		}, "CYPHERD_MASTER_KEY"},
		{"admin email without password", func(t *testing.T) { t.Setenv("CYPHERD_ADMIN_EMAIL", "a@b.c") }, "must be set together"},
		{"admin password without email", func(t *testing.T) { t.Setenv("CYPHERD_ADMIN_PASSWORD", "pw") }, "must be set together"},
		{"malformed min disk free", func(t *testing.T) { t.Setenv("CYPHERD_MIN_DISK_FREE", "lots") }, "CYPHERD_MIN_DISK_FREE"},
		{"negative min disk free", func(t *testing.T) { t.Setenv("CYPHERD_MIN_DISK_FREE", "-1") }, "CYPHERD_MIN_DISK_FREE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBaseEnv(t)
			tc.mutate(t)
			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; want error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	setBaseEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PublicHost != "localhost" {
		t.Errorf("PublicHost = %q, want localhost", c.PublicHost)
	}
	if c.MinDiskFree != 1<<30 {
		t.Errorf("MinDiskFree = %d, want 1 GiB", c.MinDiskFree)
	}
	if c.JoinTokenTTL != 15*time.Minute {
		t.Errorf("JoinTokenTTL = %v, want 15m", c.JoinTokenTTL)
	}
	if got := c.AdvertisedNATSURL(); got != "tls://localhost:4222" {
		t.Errorf("AdvertisedNATSURL = %q", got)
	}
	if got := c.AdvertisedEnrollAddr(); got != "localhost:8443" {
		t.Errorf("AdvertisedEnrollAddr = %q", got)
	}
}

func TestLoadOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("CYPHERD_PUBLIC_HOST", "panel.example.com")
	t.Setenv("CYPHERD_NATS_ADDR", "0.0.0.0:14222")
	t.Setenv("CYPHERD_MIN_DISK_FREE", "0")
	t.Setenv("CYPHERD_JOIN_TOKEN_TTL", "5m")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MinDiskFree != 0 {
		t.Errorf("MinDiskFree = %d, want 0 (explicitly disabled)", c.MinDiskFree)
	}
	if c.JoinTokenTTL != 5*time.Minute {
		t.Errorf("JoinTokenTTL = %v, want 5m", c.JoinTokenTTL)
	}
	if got := c.AdvertisedNATSURL(); got != "tls://panel.example.com:14222" {
		t.Errorf("AdvertisedNATSURL = %q", got)
	}
}

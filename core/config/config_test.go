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
		"CYPHERD_PUBLIC_URL", "CYPHERD_TRUSTED_PROXIES",
		"CYPHERD_UPDATE_CHECK", "CYPHERD_UPDATE_FEED_URL",
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
	if got := c.AdvertisedConsoleURL(); got != "http://localhost:8080" {
		t.Errorf("AdvertisedConsoleURL = %q", got)
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

// TestPublicURLOverridesTheConsoleURL: CYPHERD_PUBLIC_URL is the one value that
// can carry https and a proxy's hostname into every link the panel writes to
// itself (control-plane-hardening.md §6). Anything that is not a bare origin is
// refused at boot rather than mailed to an operator.
func TestPublicURLOverridesTheConsoleURL(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("CYPHERD_PUBLIC_HOST", "10.0.0.5")
	t.Setenv("CYPHERD_PUBLIC_URL", "https://panel.example.com/")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The trailing slash is trimmed: every link is built by concatenation.
	if got := c.AdvertisedConsoleURL(); got != "https://panel.example.com" {
		t.Errorf("AdvertisedConsoleURL = %q, want the public URL", got)
	}
	// The data plane is unaffected — agents still dial the public host.
	if got := c.AdvertisedNATSURL(); got != "tls://10.0.0.5:4222" {
		t.Errorf("AdvertisedNATSURL = %q, want the public host", got)
	}

	for _, bad := range []string{
		"panel.example.com",                 // no scheme
		"ftp://panel.example.com",           // unknown scheme
		"https://panel.example.com/console", // a path
		"https://panel.example.com?x=1",     // a query
		"https://user:pw@panel.example.com", // credentials
		"https://",                          // no host
	} {
		t.Setenv("CYPHERD_PUBLIC_URL", bad)
		if _, err := Load(); err == nil {
			t.Errorf("CYPHERD_PUBLIC_URL=%q was accepted", bad)
		} else if !strings.Contains(err.Error(), "CYPHERD_PUBLIC_URL") {
			t.Errorf("CYPHERD_PUBLIC_URL=%q: error %v does not name the variable", bad, err)
		}
	}
}

// TestTrustedProxiesParsing: a CIDR list, a bare address as its own /32 or
// /128, and a refusal for anything else (control-plane-hardening.md §5).
func TestTrustedProxiesParsing(t *testing.T) {
	setBaseEnv(t)
	if c, err := Load(); err != nil || len(c.TrustedProxies) != 0 {
		t.Fatalf("default TrustedProxies = %v, %v; want none", c.TrustedProxies, err)
	}
	t.Setenv("CYPHERD_TRUSTED_PROXIES", " 10.0.0.0/8 , 172.17.0.9 ,, ::1 ")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"10.0.0.0/8", "172.17.0.9/32", "::1/128"}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", c.TrustedProxies, want)
	}
	for i, w := range want {
		if got := c.TrustedProxies[i].String(); got != w {
			t.Errorf("TrustedProxies[%d] = %q, want %q", i, got, w)
		}
	}
	// A prefix with host bits set is masked, so containment is well-defined.
	t.Setenv("CYPHERD_TRUSTED_PROXIES", "10.1.2.3/8")
	if c, err := Load(); err != nil || c.TrustedProxies[0].String() != "10.0.0.0/8" {
		t.Fatalf("unmasked prefix = %v, %v", c.TrustedProxies, err)
	}
	t.Setenv("CYPHERD_TRUSTED_PROXIES", "not-an-address")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CYPHERD_TRUSTED_PROXIES") {
		t.Fatalf("err = %v, want one naming CYPHERD_TRUSTED_PROXIES", err)
	}
}

// TestUpdateCheckIsOnUnlessTurnedOff: the check is opt-out, and "off" — in any
// casing — is the one value that stops it (control-plane-hardening.md §3).
func TestUpdateCheckIsOnUnlessTurnedOff(t *testing.T) {
	setBaseEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.UpdateCheck || c.UpdateFeedURL != "" {
		t.Fatalf("defaults = %v, %q; want on with the package default feed", c.UpdateCheck, c.UpdateFeedURL)
	}
	for _, off := range []string{"off", "OFF", " Off "} {
		t.Setenv("CYPHERD_UPDATE_CHECK", off)
		if c, err := Load(); err != nil || c.UpdateCheck {
			t.Errorf("CYPHERD_UPDATE_CHECK=%q left the check on (%v)", off, err)
		}
	}
	// Anything else leaves it on: a typo must not silently disable a security
	// notification channel.
	for _, on := range []string{"on", "true", "yes", "disabled"} {
		t.Setenv("CYPHERD_UPDATE_CHECK", on)
		if c, err := Load(); err != nil || !c.UpdateCheck {
			t.Errorf("CYPHERD_UPDATE_CHECK=%q turned the check off (%v)", on, err)
		}
	}
	t.Setenv("CYPHERD_UPDATE_CHECK", "on")
	t.Setenv("CYPHERD_UPDATE_FEED_URL", "https://feed.example.test/latest")
	if c, err := Load(); err != nil || c.UpdateFeedURL != "https://feed.example.test/latest" {
		t.Fatalf("feed url = %q, %v", c.UpdateFeedURL, err)
	}
}

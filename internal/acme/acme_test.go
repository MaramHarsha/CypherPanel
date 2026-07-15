package acme

import (
	"errors"
	"testing"
)

func TestIsWildcard(t *testing.T) {
	cases := map[string]bool{
		"*.example.com":     true,
		"example.com":       false,
		"www.example.com":   false,
		"*.sub.example.com": true,
	}
	for domain, want := range cases {
		if got := IsWildcard(domain); got != want {
			t.Errorf("IsWildcard(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestObtain_WildcardWithoutDNSProviderFailsFast(t *testing.T) {
	// No DNS provider configured: a wildcard must fail immediately with the
	// sentinel error (and never attempt network I/O against the directory).
	i := NewIssuer("http://127.0.0.1:1/dir", t.TempDir())
	_, err := i.Obtain("*.example.com", "admin@example.com", t.TempDir())
	if !errors.Is(err, ErrWildcardNeedsDNS) {
		t.Fatalf("want ErrWildcardNeedsDNS, got %v", err)
	}
}

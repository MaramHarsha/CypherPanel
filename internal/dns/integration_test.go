package dns

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestACMEProvider_LiveDNS01 exercises the DNS-01 solver against a real
// PowerDNS. Gated on CYPHER_PDNS_TEST_URL so it only runs in the E2E harness.
//
//	CYPHER_PDNS_TEST_URL=http://localhost:8081 CYPHER_PDNS_TEST_KEY=... \
//	  CYPHER_PDNS_TEST_ZONE=dbuser.example.com go test ./internal/dns/ -run LiveDNS01 -v
func TestACMEProvider_LiveDNS01(t *testing.T) {
	url := os.Getenv("CYPHER_PDNS_TEST_URL")
	if url == "" {
		t.Skip("set CYPHER_PDNS_TEST_URL to run the live DNS-01 test")
	}
	zone := os.Getenv("CYPHER_PDNS_TEST_ZONE")
	pdns := NewPowerDNS(url, os.Getenv("CYPHER_PDNS_TEST_KEY"))
	provider := NewACMEProvider(pdns)

	// Present writes the _acme-challenge TXT; verify it lands in the zone.
	if err := provider.Present(zone, "tok", "some-key-authorization-value"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	records, err := pdns.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range records {
		if r.Type == "TXT" && strings.HasPrefix(r.Name, "_acme-challenge.") {
			found = true
		}
	}
	if !found {
		t.Fatal("Present did not create an _acme-challenge TXT record")
	}

	// CleanUp removes it.
	if err := provider.CleanUp(zone, "tok", "some-key-authorization-value"); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	records, _ = pdns.ListRecords(context.Background(), zone)
	for _, r := range records {
		if r.Type == "TXT" && strings.HasPrefix(r.Name, "_acme-challenge.") {
			t.Fatal("CleanUp did not remove the _acme-challenge TXT record")
		}
	}
}

package dns

import "testing"

func TestValidateRecord(t *testing.T) {
	valid := []Record{
		{Name: "example.com.", Type: "A", TTL: 3600, Contents: []string{"1.2.3.4"}},
		{Name: "example.com.", Type: "AAAA", TTL: 3600, Contents: []string{"2606:4700:4700::1111"}},
		{Name: "www.example.com.", Type: "CNAME", TTL: 3600, Contents: []string{"example.com."}},
		{Name: "example.com.", Type: "MX", TTL: 3600, Contents: []string{"10 mail.example.com."}},
		{Name: "example.com.", Type: "TXT", TTL: 3600, Contents: []string{"v=spf1 -all"}},
		{Name: "_sip._tcp.example.com.", Type: "SRV", TTL: 3600, Contents: []string{"10 60 5060 sip.example.com."}},
		{Name: "example.com.", Type: "CAA", TTL: 3600, Contents: []string{"0 issue \"letsencrypt.org\""}},
	}
	for _, r := range valid {
		if err := ValidateRecord(r); err != nil {
			t.Errorf("valid %s record rejected: %v", r.Type, err)
		}
	}

	invalid := []Record{
		{Name: "x", Type: "A", TTL: 1, Contents: []string{"not-an-ip"}},
		{Name: "x", Type: "A", TTL: 1, Contents: []string{"2606:4700::1"}}, // v6 in A
		{Name: "x", Type: "AAAA", TTL: 1, Contents: []string{"1.2.3.4"}},   // v4 in AAAA
		{Name: "x", Type: "CNAME", TTL: 1, Contents: []string{"a.com.", "b.com."}}, // multi CNAME
		{Name: "x", Type: "MX", TTL: 1, Contents: []string{"mail.example.com."}},   // no priority
		{Name: "x", Type: "SRV", TTL: 1, Contents: []string{"10 60 sip.example.com."}},
		{Name: "x", Type: "CAA", TTL: 1, Contents: []string{"0 bogus \"x\""}},
		{Name: "x", Type: "WKS", TTL: 1, Contents: []string{"x"}}, // unsupported type
		{Name: "x", Type: "A", TTL: -1, Contents: []string{"1.2.3.4"}},
		{Name: "x", Type: "A", TTL: 1, Contents: nil},
	}
	for i, r := range invalid {
		if err := ValidateRecord(r); err == nil {
			t.Errorf("invalid record #%d (%s) accepted", i, r.Type)
		}
	}
}

func TestValidateAgainstZone_CNAMECoexistence(t *testing.T) {
	existing := []Record{
		{Name: "www.example.com.", Type: "A", Contents: []string{"1.2.3.4"}},
	}
	// A CNAME at a name that already has an A must be rejected.
	cname := Record{Name: "www.example.com.", Type: "CNAME", Contents: []string{"example.com."}}
	if err := ValidateAgainstZone(cname, existing); err == nil {
		t.Fatal("CNAME coexisting with A should be rejected")
	}
	// Adding an A where a CNAME exists must be rejected.
	existingCNAME := []Record{{Name: "blog.example.com.", Type: "CNAME", Contents: []string{"example.com."}}}
	a := Record{Name: "blog.example.com.", Type: "A", Contents: []string{"1.2.3.4"}}
	if err := ValidateAgainstZone(a, existingCNAME); err == nil {
		t.Fatal("A coexisting with CNAME should be rejected")
	}
	// A second A at the same name is fine (same type).
	a2 := Record{Name: "www.example.com.", Type: "A", Contents: []string{"5.6.7.8"}}
	if err := ValidateAgainstZone(a2, existing); err != nil {
		t.Fatalf("same-type coexistence should be allowed: %v", err)
	}
}

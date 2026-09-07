package mailhost

import "testing"

// The local part is deliberately NARROWER than RFC 5321 allows. The RFC permits
// quoted strings with spaces and almost any byte; a panel that accepted them
// would be generating addresses that half the internet mishandles, and the
// operator would find out which half from a customer.
func TestLocalPartIsNarrowerThanTheRFC(t *testing.T) {
	valid := []string{"hello", "a.b", "first.last", "no-reply", "team+billing", "x_y", "a1"}
	for _, v := range valid {
		if !ValidLocalPart(v) {
			t.Errorf("ValidLocalPart(%q) = false, want true", v)
		}
	}
	rejected := []string{
		"", ".leading", "trailing.", "double..dot",
		"Upper",     // addresses are normalised to lowercase before this
		"has space", // legal quoted, and universally mishandled
		`"quoted"`,  // ditto
		"semi;colon", "a@b", "slash/es", "back\\slash",
	}
	for _, v := range rejected {
		if ValidLocalPart(v) {
			t.Errorf("ValidLocalPart(%q) = true — this address would work here and break elsewhere", v)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if ValidLocalPart(string(long)) {
		t.Error("a 65-character local part was accepted; the limit is 64")
	}
}

func TestSplitAddress(t *testing.T) {
	local, dom, err := SplitAddress("  Hello@Example.COM ")
	if err != nil {
		t.Fatalf("SplitAddress: %v", err)
	}
	if local != "hello" || dom != "example.com" {
		t.Errorf("got %q @ %q, want hello @ example.com", local, dom)
	}
	for _, bad := range []string{"", "no-at-sign", "@nolocal", "nodomain@"} {
		if _, _, err := SplitAddress(bad); err == nil {
			t.Errorf("SplitAddress(%q) accepted", bad)
		}
	}
}

// An operator reading four TXT records needs the fully-qualified name, not a
// bare @.
func TestRecordNamesAreFullyQualified(t *testing.T) {
	cases := []struct {
		in   Record
		want string
	}{
		{Record{Name: "@"}, "acme.com"},
		{Record{Name: ""}, "acme.com"},
		{Record{Name: "_dmarc"}, "_dmarc.acme.com"},
		{Record{Name: "key1._domainkey"}, "key1._domainkey.acme.com"},
		{Record{Name: "already.acme.com"}, "already.acme.com"},
	}
	for _, c := range cases {
		if got := RecordName(c.in, "acme.com"); got != c.want {
			t.Errorf("RecordName(%q) = %q, want %q", c.in.Name, got, c.want)
		}
	}
}

// Every record carries a PURPOSE, because a record with no explanation is a
// record an operator deletes during a cleanup.
func TestEveryStandardRecordSaysWhatItIsFor(t *testing.T) {
	records := migaduStandardRecords("acme.com", "key1", "MIIBIjAN")
	if len(records) < 5 {
		t.Fatalf("got %d records; MX, MX, SPF, DMARC and DKIM are all needed", len(records))
	}
	for _, r := range records {
		if r.Purpose == "" {
			t.Errorf("%s %s carries no purpose", r.Type, r.Name)
		}
	}
	// The DKIM record must carry the PROVIDER's public key: the panel never
	// generates or holds the private half, which is the whole reason mail
	// living at a provider is safer than mail living here.
	var dkim string
	for _, r := range records {
		if r.Name == "key1._domainkey" {
			dkim = r.Content
		}
	}
	if dkim == "" {
		t.Fatal("no DKIM record was produced for a domain whose provider gave a selector and key")
	}
	if dkim != "v=DKIM1; k=rsa; p=MIIBIjAN" {
		t.Errorf("DKIM record = %q", dkim)
	}
}

// A provider that reports no DKIM selector yet must not produce a DKIM record
// with an empty key — a published record with no key is worse than no record,
// because receivers treat it as a signing failure.
func TestNoDKIMRecordWithoutAKey(t *testing.T) {
	for _, r := range migaduStandardRecords("acme.com", "", "") {
		if r.Name != "" && r.Name != "@" && r.Name != "_dmarc" {
			t.Errorf("an unexpected record was produced with no DKIM key: %+v", r)
		}
	}
}

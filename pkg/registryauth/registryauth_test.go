package registryauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeProducesTheHeaderTheDaemonReads(t *testing.T) {
	got, err := Encode("ghcr.io", "acme", "ghp_s3cret")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decoding %q: %v", got, err)
	}
	var obj map[string]string
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshalling %s: %v", raw, err)
	}
	// The daemon's own field names: password, not token; serveraddress, not url.
	if obj["username"] != "acme" || obj["password"] != "ghp_s3cret" || obj["serveraddress"] != "ghcr.io" {
		t.Fatalf("auth object = %v", obj)
	}
}

// URL encoding, not standard: the value rides in a header, where '+' and '/'
// are needlessly risky.
func TestEncodeUsesURLSafeAlphabet(t *testing.T) {
	// A token whose bytes push the encoder into the differing characters.
	got, err := Encode("ghcr.io", "u", strings.Repeat("\xff\xfe", 8))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.ContainsAny(got, "+/") {
		t.Fatalf("encoded = %q, want no + or / in a header value", got)
	}
}

// Nothing to authenticate with means no header at all — an anonymous pull,
// which is what every public image does.
func TestEncodeIsEmptyWithoutACredential(t *testing.T) {
	for _, tc := range [][2]string{{"", "tok"}, {"ghcr.io", ""}, {"", ""}} {
		got, err := Encode(tc[0], "u", tc[1])
		if err != nil || got != "" {
			t.Fatalf("Encode(%q, u, %q) = %q, %v; want an empty header", tc[0], tc[1], got, err)
		}
	}
}

// /build takes a MAP from host to credential, because one build may pull from
// several registries.
func TestEncodeConfigIsKeyedByHost(t *testing.T) {
	got, err := EncodeConfig("registry.example.com", "acme", "s3cret")
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decoding %q: %v", got, err)
	}
	var obj map[string]map[string]string
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshalling %s: %v", raw, err)
	}
	entry, ok := obj["registry.example.com"]
	if !ok {
		t.Fatalf("config = %v, want it keyed by the host", obj)
	}
	if entry["password"] != "s3cret" {
		t.Fatalf("entry = %v", entry)
	}
}

func TestEncodeConfigIsEmptyWithoutACredential(t *testing.T) {
	got, err := EncodeConfig("", "u", "")
	if err != nil || got != "" {
		t.Fatalf("got %q, %v; want an empty header", got, err)
	}
}

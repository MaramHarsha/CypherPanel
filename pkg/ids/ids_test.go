package ids

import (
	"strings"
	"testing"
)

func TestNewHasPrefixAndIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := New(PrefixServer)
		if !strings.HasPrefix(id, "srv_") {
			t.Fatalf("id %q missing prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewIsLowercaseAndURLSafe(t *testing.T) {
	id := New(PrefixJoinToken)
	body, ok := strings.CutPrefix(id, "jt_")
	if !ok {
		t.Fatalf("unexpected format: %q", id)
	}
	if body != strings.ToLower(body) {
		t.Errorf("id body not lowercase: %q", body)
	}
	for _, r := range body {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			t.Errorf("id body has non-URL-safe rune %q in %q", r, id)
		}
	}
}

func TestSecretIsHighEntropyAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		s := Secret()
		if len(s) < 20 {
			t.Fatalf("secret unexpectedly short (%d chars): %q", len(s), s)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate secret generated: %q", s)
		}
		seen[s] = struct{}{}
	}
}

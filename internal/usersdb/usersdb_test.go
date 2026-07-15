package usersdb

import (
	"strings"
	"testing"
)

func TestSpecValidate_RejectsInjection(t *testing.T) {
	bad := []Spec{
		{Database: "db`; DROP", User: "u", Host: "localhost"},
		{Database: "db", User: "u'--", Host: "localhost"},
		{Database: "db", User: "u", Host: "local host"},
		{Database: "", User: "u", Host: "localhost"},
		{Database: "UPPER", User: "u", Host: "localhost"}, // identifiers are lowercased by Core
		{Database: strings.Repeat("a", 65), User: "u", Host: "localhost"},
	}
	for _, s := range bad {
		if err := s.validate(); err == nil {
			t.Errorf("expected %+v to be rejected", s)
		}
	}
}

func TestSpecValidate_AcceptsNamespaced(t *testing.T) {
	s := Spec{Database: "cyph_tls1_blog", User: "cyph_tls1_blog", Host: "localhost"}
	if err := s.validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestGeneratePassword_SafeAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) < 24 {
			t.Fatalf("password too short: %q", pw)
		}
		// Must be safe inside a single-quoted SQL string literal.
		if strings.ContainsAny(pw, "'\"\\`") {
			t.Fatalf("password has unsafe chars: %q", pw)
		}
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}

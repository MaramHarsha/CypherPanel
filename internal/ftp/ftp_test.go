package ftp

import (
	"strings"
	"testing"
)

func TestSpecValidate(t *testing.T) {
	ok := Spec{Username: "cyph_tls1_deploy", SystemUser: "cyph_tls1", HomeDir: "/home/cyph_tls1"}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	bad := []Spec{
		{Username: "bad name", SystemUser: "cyph_tls1", HomeDir: "/home/x"},
		{Username: "u", SystemUser: "cyph_tls1", HomeDir: "relative/path"},
		{Username: "u", SystemUser: "cyph_tls1", HomeDir: "/home/../etc"},
		{Username: "u'--", SystemUser: "cyph_tls1", HomeDir: "/home/x"},
		{Username: "1bad", SystemUser: "cyph_tls1", HomeDir: "/home/x"},
	}
	for _, s := range bad {
		if err := s.validate(); err == nil {
			t.Errorf("expected %+v to be rejected", s)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 24 || strings.ContainsAny(pw, "'\"\\ \n") {
		t.Fatalf("weak/unsafe password: %q", pw)
	}
}

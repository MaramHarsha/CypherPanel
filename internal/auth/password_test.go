package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash not in PHC argon2id format: %s", hash)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestHashesAreSalted(t *testing.T) {
	h1, _ := HashPassword("same input")
	h2, _ := HashPassword("same input")
	if h1 == h2 {
		t.Fatal("two hashes of the same password are identical — salt is not random")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$argon2id$broken", "$md5$x$y$z$w"} {
		if _, err := VerifyPassword("x", bad); err == nil {
			t.Errorf("malformed hash %q accepted without error", bad)
		}
	}
}

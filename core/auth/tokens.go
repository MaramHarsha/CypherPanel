package auth

import (
	"crypto/sha256"
	"crypto/subtle"
)

// HashToken returns the SHA-256 of an opaque bearer token. We store only this
// hash (for sessions and join tokens), never the raw token, so a database read
// does not yield usable credentials (threat-model §5.3, §5.8).
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ConstantTimeEqual compares two byte slices without leaking their contents
// through timing (ENGINEERING rule 21).
func ConstantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

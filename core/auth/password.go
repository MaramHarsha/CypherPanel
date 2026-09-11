// Package auth handles control-plane account authentication: password hashing,
// opaque session tokens, and login rate limiting (threat-model §5.8). Account
// takeover here is fleet *command* (bounded by §5.1), so these paths are
// treated as security-critical.
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash suitable for storage. bcrypt considers
// only the first 72 bytes of the password; that is acceptable for our use.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hashing password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches the stored bcrypt hash. It is
// constant-time with respect to the password by construction of bcrypt.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

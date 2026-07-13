package auth

import (
	"testing"
	"time"
)

func newTestTokenService(ttl time.Duration) *TokenService {
	// Redis-backed refresh flows are covered by integration tests against the
	// dev stack; these tests exercise the stateless JWT paths.
	return NewTokenService("test-secret", ttl, time.Hour, nil)
}

func TestAccessTokenRoundTrip(t *testing.T) {
	ts := newTestTokenService(time.Minute)

	token, err := ts.IssueAccess("user-123", RoleReseller, "reseller-9", "")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}

	claims, err := ts.ParseAccess(token)
	if err != nil {
		t.Fatalf("ParseAccess: %v", err)
	}
	if claims.Subject != "user-123" || claims.Role != RoleReseller || claims.ResellerID != "reseller-9" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	ts := newTestTokenService(-time.Minute) // already expired at issue time
	token, err := ts.IssueAccess("user-123", RoleEndUser, "", "acct-1")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if _, err := ts.ParseAccess(token); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	ts := newTestTokenService(time.Minute)
	token, _ := ts.IssueAccess("user-123", RoleEndUser, "", "")

	other := NewTokenService("different-secret", time.Minute, time.Hour, nil)
	if _, err := other.ParseAccess(token); err == nil {
		t.Fatal("token signed with another secret accepted")
	}
}

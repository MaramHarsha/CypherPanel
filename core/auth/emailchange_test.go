package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// request mints a pending change for a seeded user and returns the wire token.
func request(t *testing.T, a *Authenticator, fs *fakeStore) (token, userID string) {
	t.Helper()
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}
	token, old, err := a.RequestEmailChange(context.Background(), "usr_1", "new@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("RequestEmailChange: %v", err)
	}
	if old != "old@example.com" {
		t.Fatalf("old address = %q, want the address being moved away from", old)
	}
	if !strings.Contains(token, ".") {
		t.Fatalf("token %q is not the id.secret wire form", token)
	}
	return token, "usr_1"
}

func TestEmailChangeRequiresCurrentPassword(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}

	if _, _, err := a.RequestEmailChange(context.Background(), "usr_1", "new@example.com", "wrong"); err == nil {
		t.Fatal("a wrong current password started a change; a session alone must never move an address")
	}
}

func TestEmailChangeAppliesAndIsSingleUse(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, userID := request(t, a, fs)

	user, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw-session")
	if err != nil {
		t.Fatalf("ConfirmEmailChange: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Fatalf("email = %q, want the new address", user.Email)
	}
	// Replaying the same link must not work — the spend is what makes it once.
	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw-session"); err == nil {
		t.Fatal("the same confirmation link worked twice")
	}
}

// The ordering that matters: a wrong guess against a real change id must not
// spend it, or an attacker could burn every pending change by guessing.
func TestWrongSecretDoesNotBurnTheChange(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, userID := request(t, a, fs)
	id, _, _ := strings.Cut(token, ".")

	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, id+".wrong-secret", "raw"); err == nil {
		t.Fatal("a wrong secret was accepted")
	}
	if ec := fs.emailChanges[id]; ec.ConsumedAt != nil {
		t.Fatal("a wrong guess consumed the pending change")
	}
	// The real token still works afterwards.
	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw"); err != nil {
		t.Fatalf("the valid token stopped working after a wrong guess: %v", err)
	}
}

func TestEmailChangeRejectsOtherUsersToken(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, _ := request(t, a, fs)

	if _, _, err := a.ConfirmEmailChange(context.Background(), "usr_someone_else", token, "raw"); err == nil {
		t.Fatal("one account confirmed another account's change")
	}
}

func TestExpiredEmailChangeIsRefused(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, userID := request(t, a, fs)
	id, _, _ := strings.Cut(token, ".")
	ec := fs.emailChanges[id]
	ec.ExpiresAt = time.Now().Add(-time.Minute)
	fs.emailChanges[id] = ec

	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw"); err == nil {
		t.Fatal("an expired confirmation link was accepted")
	}
}

func TestEmailChangeRefusesAddressInUse(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}
	fs.users["taken@example.com"] = domain.User{ID: "usr_2", Email: "taken@example.com"}

	if _, _, err := a.RequestEmailChange(context.Background(), "usr_1", "taken@example.com", "correct-horse"); err == nil {
		t.Fatal("a change to an address already in use was allowed")
	}
}

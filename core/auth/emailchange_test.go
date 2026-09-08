package auth

import (
	"context"
	"errors"
	"strconv"
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
	change, err := a.RequestEmailChange(context.Background(), "usr_1", "new@example.com", "correct-horse", "ip1")
	if err != nil {
		t.Fatalf("RequestEmailChange: %v", err)
	}
	if change.OldEmail != "old@example.com" {
		t.Fatalf("old address = %q, want the address being moved away from", change.OldEmail)
	}
	if !strings.Contains(change.Token, ".") {
		t.Fatalf("token %q is not the id.secret wire form", change.Token)
	}
	return change.Token, "usr_1"
}

func TestEmailChangeRequiresCurrentPassword(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}

	if _, err := a.RequestEmailChange(context.Background(), "usr_1", "new@example.com", "wrong", "ip1"); err == nil {
		t.Fatal("a wrong current password started a change; a session alone must never move an address")
	}
}

func TestEmailChangeAppliesAndIsSingleUse(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, userID := request(t, a, fs)

	user, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw-session", "ip1")
	if err != nil {
		t.Fatalf("ConfirmEmailChange: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Fatalf("email = %q, want the new address", user.Email)
	}
	// Replaying the same link must not work — the spend is what makes it once.
	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw-session", "ip1"); err == nil {
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

	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, id+".wrong-secret", "raw", "ip1"); err == nil {
		t.Fatal("a wrong secret was accepted")
	}
	if ec := fs.emailChanges[id]; ec.ConsumedAt != nil {
		t.Fatal("a wrong guess consumed the pending change")
	}
	// The real token still works afterwards.
	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw", "ip1"); err != nil {
		t.Fatalf("the valid token stopped working after a wrong guess: %v", err)
	}
}

func TestEmailChangeRejectsOtherUsersToken(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	token, _ := request(t, a, fs)

	if _, _, err := a.ConfirmEmailChange(context.Background(), "usr_someone_else", token, "raw", "ip1"); err == nil {
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

	if _, _, err := a.ConfirmEmailChange(context.Background(), userID, token, "raw", "ip1"); err == nil {
		t.Fatal("an expired confirmation link was accepted")
	}
}

func TestEmailChangeRefusesAddressInUse(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}
	fs.users["taken@example.com"] = domain.User{ID: "usr_2", Email: "taken@example.com"}

	if _, err := a.RequestEmailChange(context.Background(), "usr_1", "taken@example.com", "correct-horse", "ip1"); err == nil {
		t.Fatal("a change to an address already in use was allowed")
	}
}

// A person can type a display-name form, and ParseAddress accepts it — but only
// the bare address may reach a recipient list or a message body. Returning the
// raw string is how untrusted input becomes a header (CWE-640).
func TestRequestReturnsTheParsedAddressNotTheRawInput(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, nil, time.Hour)
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}

	change, err := a.RequestEmailChange(context.Background(), "usr_1", `"Sai H" <new@example.com>`, "correct-horse", "ip1")
	if err != nil {
		t.Fatalf("RequestEmailChange: %v", err)
	}
	if change.NewEmail != "new@example.com" {
		t.Fatalf("NewEmail = %q, want the bare parsed address", change.NewEmail)
	}
}

// Both email-change routes are brute-force surfaces and are throttled like
// Login (panel-mail.md §5; threat-model §5.10): a wrong current password or a
// wrong confirmation secret spends the budget, and the throttled answer says
// how long to wait.
func TestEmailChangeRequestIsThrottled(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("correct-horse")
	fs.users["old@example.com"] = domain.User{ID: "usr_1", Email: "old@example.com", PasswordHash: hash}
	a := NewAuthenticator(fs, nil, NewLimiter(2, time.Minute), time.Hour)
	ctx := context.Background()

	for range 2 {
		if _, err := a.RequestEmailChange(ctx, "usr_1", "new@example.com", "wrong", "ip1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	}
	_, err := a.RequestEmailChange(ctx, "usr_1", "new@example.com", "correct-horse", "ip1")
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfterSeconds() < 1 {
		t.Fatalf("err = %v, want a *RateLimitedError with a positive wait", err)
	}
	// A different address is still bounded by the account's own budget.
	for i := range 2 {
		_, _ = a.RequestEmailChange(ctx, "usr_1", "new@example.com", "wrong", "other"+strconv.Itoa(i))
	}
	if _, err := a.RequestEmailChange(ctx, "usr_1", "new@example.com", "correct-horse", "fresh"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after four failures across addresses: err = %v, want ErrRateLimited (account budget)", err)
	}
}

func TestEmailChangeConfirmIsThrottled(t *testing.T) {
	fs := newFakeStore()
	a := NewAuthenticator(fs, nil, NewLimiter(2, time.Minute), time.Hour)
	token, userID := request(t, a, fs)
	ctx := context.Background()

	for range 2 {
		if _, _, err := a.ConfirmEmailChange(ctx, userID, "ec_guess.secret", "raw", "ip1"); !errors.Is(err, ErrInvalidEmailChange) {
			t.Fatalf("err = %v, want ErrInvalidEmailChange", err)
		}
	}
	if _, _, err := a.ConfirmEmailChange(ctx, userID, token, "raw", "ip1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the valid token got through a throttled address: %v", err)
	}
	// The throttle refused it before the token was parsed, so it is unspent.
	if _, _, err := a.ConfirmEmailChange(ctx, userID, token, "raw", "ip2"); err != nil {
		t.Fatalf("the valid token from an unthrottled address: %v", err)
	}
}

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func TestCreateTokenAuthenticatesAsOwnerAndTouches(t *testing.T) {
	a, fs := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()

	raw, tok, err := a.CreateToken(ctx, "usr_1", "ci", nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(raw, APITokenPrefix) {
		t.Fatalf("raw token missing prefix: %q", raw)
	}
	if tok.ID == "" || tok.Name != "ci" {
		t.Fatalf("bad token metadata: %+v", tok)
	}

	user, err := a.Authenticate(ctx, raw)
	if err != nil {
		t.Fatalf("Authenticate(token): %v", err)
	}
	if user.ID != "usr_1" {
		t.Fatalf("token resolved wrong user: %q", user.ID)
	}
	if fs.touched[string(HashToken(raw))] != 1 {
		t.Fatalf("expected last-used to be recorded once, got %d", fs.touched[string(HashToken(raw))])
	}
}

func TestExpiredTokenDoesNotAuthenticate(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	raw, _, err := a.CreateToken(ctx, "usr_1", "old", &past)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := a.Authenticate(ctx, raw); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired token: err = %v, want ErrInvalidSession", err)
	}
}

func TestDeleteTokenRequiresOwnership(t *testing.T) {
	a, fs := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	// A token owned by someone else.
	fs.tokens["tok_other"] = domain.APIToken{ID: "tok_other", UserID: "usr_2", Name: "theirs"}

	if err := a.DeleteToken(ctx, "usr_1", "tok_other"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("cross-user delete: err = %v, want ErrTokenNotFound", err)
	}
	if _, ok := fs.tokens["tok_other"]; !ok {
		t.Fatal("another user's token was deleted")
	}
	if err := a.DeleteToken(ctx, "usr_1", "tok_missing"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("missing delete: err = %v, want ErrTokenNotFound", err)
	}

	// The owner can delete their own.
	raw, tok, _ := a.CreateToken(ctx, "usr_1", "mine", nil)
	if err := a.DeleteToken(ctx, "usr_1", tok.ID); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	if _, err := a.Authenticate(ctx, raw); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("deleted token still authenticates: %v", err)
	}
}

// A malformed token that is only the bare prefix must not authenticate.
func TestEmptyPrefixTokenRejected(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	if _, err := a.Authenticate(context.Background(), APITokenPrefix); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("bare-prefix token: err = %v, want ErrInvalidSession", err)
	}
}

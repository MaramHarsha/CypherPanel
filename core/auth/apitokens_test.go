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

	raw, tok, err := a.CreateToken(ctx, "usr_1", "ci", domain.AllAbilities(), nil, "")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(raw, APITokenPrefix) {
		t.Fatalf("raw token missing prefix: %q", raw)
	}
	if tok.ID == "" || tok.Name != "ci" {
		t.Fatalf("bad token metadata: %+v", tok)
	}

	p, err := a.Authenticate(ctx, raw)
	if err != nil {
		t.Fatalf("Authenticate(token): %v", err)
	}
	if p.User.ID != "usr_1" {
		t.Fatalf("token resolved wrong user: %q", p.User.ID)
	}
	if p.Kind != KindAPIToken {
		t.Fatalf("principal kind = %q, want api_token", p.Kind)
	}
	if fs.touched[string(HashToken(raw))] != 1 {
		t.Fatalf("expected last-used to be recorded once, got %d", fs.touched[string(HashToken(raw))])
	}
}

func TestExpiredTokenDoesNotAuthenticate(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	raw, _, err := a.CreateToken(ctx, "usr_1", "old", domain.AllAbilities(), &past, "")
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
	raw, tok, _ := a.CreateToken(ctx, "usr_1", "mine", domain.AllAbilities(), nil, "")
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

// A token carries only the abilities it was issued with — that is what the
// middleware enforces per request.
func TestCreateTokenAbilities(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()

	raw, tok, err := a.CreateToken(ctx, "usr_1", "readonly", []domain.Ability{domain.AbilityRead}, nil, "")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if len(tok.Abilities) != 1 || tok.Abilities[0] != domain.AbilityRead {
		t.Fatalf("abilities = %v, want [read]", tok.Abilities)
	}
	p, err := a.Authenticate(ctx, raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !p.Can(domain.AbilityRead) || p.Can(domain.AbilityWrite) || p.Can(domain.AbilityDeploy) {
		t.Fatalf("principal abilities = %v, want read only", p.Abilities)
	}
}

// An empty or unknown ability set is refused rather than silently widened —
// a credential's authority is always an explicit choice.
func TestCreateTokenRejectsBadAbilities(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()

	for name, abilities := range map[string][]domain.Ability{
		"empty":   {},
		"unknown": {domain.Ability("root")},
		"mixed":   {domain.AbilityRead, domain.Ability("sudo")},
	} {
		if _, _, err := a.CreateToken(ctx, "usr_1", name, abilities, nil, ""); !errors.Is(err, ErrInvalidAbility) {
			t.Errorf("%s: err = %v, want ErrInvalidAbility", name, err)
		}
	}
}

// A session must hold every ability: interactive use is never narrowed.
func TestSessionPrincipalHoldsAllAbilities(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	token, _, err := a.Login(ctx, "sam@example.com", "pw", "", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	p, err := a.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	for _, ab := range domain.AllAbilities() {
		if !p.Can(ab) {
			t.Errorf("session missing ability %q", ab)
		}
	}
}

// Revoking "other" sessions keeps the caller signed in and drops the rest.
func TestRevokeOtherSessionsKeepsCaller(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	mine, _, err := a.Login(ctx, "sam@example.com", "pw", "", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	other, _, err := a.Login(ctx, "sam@example.com", "pw", "", "ip2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	n, err := a.RevokeOtherSessions(ctx, "usr_1", mine)
	if err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("revoked %d sessions, want 1", n)
	}
	if _, err := a.Authenticate(ctx, mine); err != nil {
		t.Fatalf("caller's own session was revoked: %v", err)
	}
	if _, err := a.Authenticate(ctx, other); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("other session survived: %v", err)
	}
}

// One account can never revoke another's session.
func TestRevokeSessionRequiresOwnership(t *testing.T) {
	a, fs := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	fs.sessions["someone-elses-hash"] = "usr_2"

	if err := a.RevokeSession(ctx, "usr_1", "sess_someone-elses-hash"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-user revoke: err = %v, want ErrSessionNotFound", err)
	}
	if fs.sessions["someone-elses-hash"] != "usr_2" {
		t.Fatal("another user's session was revoked")
	}
}

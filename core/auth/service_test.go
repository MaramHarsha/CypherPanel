package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore is an in-memory Store for authenticator unit tests. Integration
// tests exercise the real Postgres store (ENGINEERING rule 29).
type fakeStore struct {
	users    map[string]domain.User     // by email
	sessions map[string]string          // token-hash → userID
	tokens   map[string]domain.APIToken // token id → metadata
	byHash   map[string]string          // token-hash → token id
	touched  map[string]int             // token-hash → times touched
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    map[string]domain.User{},
		sessions: map[string]string{},
		tokens:   map[string]domain.APIToken{},
		byHash:   map[string]string{},
		touched:  map[string]int{},
	}
}

func (f *fakeStore) userByID(id string) (domain.User, bool) {
	for _, u := range f.users {
		if u.ID == id {
			return u, true
		}
	}
	return domain.User{}, false
}

func (f *fakeStore) CreateAPIToken(_ context.Context, id, userID, name string, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error) {
	tok := domain.APIToken{ID: id, UserID: userID, Name: name, ExpiresAt: expiresAt, CreatedAt: time.Now()}
	f.tokens[id] = tok
	f.byHash[string(tokenHash)] = id
	return tok, nil
}

func (f *fakeStore) UserForAPIToken(_ context.Context, tokenHash []byte) (domain.User, error) {
	id, ok := f.byHash[string(tokenHash)]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	tok := f.tokens[id]
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now()) {
		return domain.User{}, store.ErrNotFound
	}
	u, ok := f.userByID(tok.UserID)
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) TouchAPIToken(_ context.Context, tokenHash []byte) error {
	f.touched[string(tokenHash)]++
	return nil
}

func (f *fakeStore) ListAPITokensByUser(_ context.Context, userID string) ([]domain.APIToken, error) {
	var out []domain.APIToken
	for _, t := range f.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) GetAPIToken(_ context.Context, id string) (domain.APIToken, error) {
	t, ok := f.tokens[id]
	if !ok {
		return domain.APIToken{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) DeleteAPIToken(_ context.Context, id string) error {
	delete(f.tokens, id)
	return nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	u, ok := f.users[email]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) CreateSession(_ context.Context, _, userID string, tokenHash []byte, _ time.Time) error {
	f.sessions[string(tokenHash)] = userID
	return nil
}

func (f *fakeStore) UserForSessionToken(_ context.Context, tokenHash []byte) (domain.User, error) {
	userID, ok := f.sessions[string(tokenHash)]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	for _, u := range f.users {
		if u.ID == userID {
			return u, nil
		}
	}
	return domain.User{}, store.ErrNotFound
}

func (f *fakeStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	delete(f.sessions, string(tokenHash))
	return nil
}

func newAuthWithUser(t *testing.T, email, password string) (*Authenticator, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	fs.users[email] = domain.User{ID: "usr_1", Email: email, PasswordHash: hash, Role: "owner"}
	a := NewAuthenticator(fs, NewLimiter(5, time.Minute), time.Hour)
	return a, fs
}

func TestLoginSuccessIssuesUsableSession(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "correct horse battery")
	ctx := context.Background()

	token, user, err := a.Login(ctx, "sam@example.com", "correct horse battery", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("expected a session token")
	}
	if user.ID != "usr_1" {
		t.Fatalf("user ID = %q", user.ID)
	}
	got, err := a.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != "usr_1" {
		t.Fatalf("Authenticate resolved wrong user: %q", got.ID)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	_, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "ip1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginUnknownUserIsIndistinguishable(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	_, _, err := a.Login(context.Background(), "nobody@example.com", "whatever", "ip1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	a := NewAuthenticator(fs, NewLimiter(3, time.Minute), time.Hour)

	for range 3 {
		if _, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "ip1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected invalid credentials, got %v", err)
		}
	}
	if _, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "ip1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit after 3 failures, got %v", err)
	}
}

func TestSuccessfulLoginResetsRateLimit(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	ctx := context.Background()
	// Two failures, then a success, should clear the counter.
	_, _, _ = a.Login(ctx, "sam@example.com", "wrong", "ip1")
	_, _, _ = a.Login(ctx, "sam@example.com", "wrong", "ip1")
	if _, _, err := a.Login(ctx, "sam@example.com", "right", "ip1"); err != nil {
		t.Fatalf("Login success: %v", err)
	}
	// Counter reset; a fresh wrong attempt is invalid-credentials, not limited.
	if _, _, err := a.Login(ctx, "sam@example.com", "wrong", "ip1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials after reset, got %v", err)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	ctx := context.Background()
	token, _, err := a.Login(ctx, "sam@example.com", "right", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := a.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := a.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected ErrInvalidSession after logout, got %v", err)
	}
}

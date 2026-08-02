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
	users       map[string]domain.User       // by email
	sessions    map[string]string            // token-hash → userID
	tokens      map[string]domain.APIToken   // token id → metadata
	byHash      map[string]string            // token-hash → token id
	touched     map[string]int               // token-hash → times touched
	totpSecrets map[string]*store.TOTPSecret // userID → 2FA secret+state
	recovery    map[string][]string          // userID → unused code-hashes
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

func (f *fakeStore) CreateAPIToken(_ context.Context, id, userID, name string, abilities []domain.Ability, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error) {
	tok := domain.APIToken{ID: id, UserID: userID, Name: name, Abilities: abilities, ExpiresAt: expiresAt, CreatedAt: time.Now()}
	f.tokens[id] = tok
	f.byHash[string(tokenHash)] = id
	return tok, nil
}

func (f *fakeStore) APITokenByHash(_ context.Context, tokenHash []byte) (domain.User, string, []domain.Ability, error) {
	id, ok := f.byHash[string(tokenHash)]
	if !ok {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	tok := f.tokens[id]
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now()) {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	u, ok := f.userByID(tok.UserID)
	if !ok {
		return domain.User{}, "", nil, store.ErrNotFound
	}
	return u, tok.ID, tok.Abilities, nil
}

func (f *fakeStore) SessionIDForToken(_ context.Context, tokenHash []byte) (string, error) {
	if _, ok := f.sessions[string(tokenHash)]; !ok {
		return "", store.ErrNotFound
	}
	return "sess_" + string(tokenHash), nil
}

func (f *fakeStore) ListSessionsByUser(_ context.Context, userID string) ([]domain.Session, error) {
	var out []domain.Session
	for hash, uid := range f.sessions {
		if uid == userID {
			out = append(out, domain.Session{ID: "sess_" + hash, UserID: uid, ExpiresAt: time.Now().Add(time.Hour)})
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteSessionForUser(_ context.Context, sessionID, userID string) (bool, error) {
	for hash, uid := range f.sessions {
		if "sess_"+hash == sessionID && uid == userID {
			delete(f.sessions, hash)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) DeleteOtherSessionsForUser(_ context.Context, userID string, keepTokenHash []byte) (int64, error) {
	var n int64
	for hash, uid := range f.sessions {
		if uid == userID && hash != string(keepTokenHash) {
			delete(f.sessions, hash)
			n++
		}
	}
	return n, nil
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

// fakeBox is an identity SecretBox for tests: the sealed value is the plaintext
// (the real AES-GCM box is exercised in the secret package). It lets TOTP tests
// assert on the actual secret round-tripping without crypto in the way.
type fakeBox struct{}

func (fakeBox) Seal(pt []byte) (ct, nonce []byte, err error) { return pt, []byte("n"), nil }
func (fakeBox) Open(ct, _ []byte) ([]byte, error)            { return ct, nil }

func (f *fakeStore) totp(userID string) *store.TOTPSecret {
	if f.totpSecrets == nil {
		f.totpSecrets = map[string]*store.TOTPSecret{}
	}
	s, ok := f.totpSecrets[userID]
	if !ok {
		s = &store.TOTPSecret{}
		f.totpSecrets[userID] = s
	}
	return s
}

func (f *fakeStore) SetTOTPSecret(_ context.Context, userID string, ct, nonce []byte) error {
	s := f.totp(userID)
	s.CT, s.Nonce, s.Enabled = ct, nonce, false
	return nil
}

func (f *fakeStore) EnableTOTP(_ context.Context, userID string) error {
	f.totp(userID).Enabled = true
	return nil
}

func (f *fakeStore) DisableTOTP(_ context.Context, userID string) error {
	f.totpSecrets[userID] = &store.TOTPSecret{}
	return nil
}

func (f *fakeStore) GetTOTPSecret(_ context.Context, userID string) (store.TOTPSecret, error) {
	return *f.totp(userID), nil
}

func (f *fakeStore) AddRecoveryCode(_ context.Context, _, userID string, codeHash []byte) error {
	if f.recovery == nil {
		f.recovery = map[string][]string{}
	}
	f.recovery[userID] = append(f.recovery[userID], string(codeHash))
	return nil
}

func (f *fakeStore) ConsumeRecoveryCode(_ context.Context, userID string, codeHash []byte) (bool, error) {
	codes := f.recovery[userID]
	for i, h := range codes {
		if h == string(codeHash) {
			f.recovery[userID] = append(codes[:i], codes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) CountUnusedRecoveryCodes(_ context.Context, userID string) (int, error) {
	return len(f.recovery[userID]), nil
}

func (f *fakeStore) DeleteRecoveryCodes(_ context.Context, userID string) error {
	if f.recovery != nil {
		delete(f.recovery, userID)
	}
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
	a := NewAuthenticator(fs, fakeBox{}, NewLimiter(5, time.Minute), time.Hour)
	return a, fs
}

func TestLoginSuccessIssuesUsableSession(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "correct horse battery")
	ctx := context.Background()

	token, user, err := a.Login(ctx, "sam@example.com", "correct horse battery", "", "ip1")
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
	if got.User.ID != "usr_1" {
		t.Fatalf("Authenticate resolved wrong user: %q", got.User.ID)
	}
	if got.Kind != KindSession {
		t.Fatalf("principal kind = %q, want session", got.Kind)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	_, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "", "ip1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginUnknownUserIsIndistinguishable(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	_, _, err := a.Login(context.Background(), "nobody@example.com", "whatever", "", "ip1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	a := NewAuthenticator(fs, fakeBox{}, NewLimiter(3, time.Minute), time.Hour)

	for range 3 {
		if _, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "", "ip1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected invalid credentials, got %v", err)
		}
	}
	if _, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "", "ip1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit after 3 failures, got %v", err)
	}
}

func TestSuccessfulLoginResetsRateLimit(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	ctx := context.Background()
	// Two failures, then a success, should clear the counter.
	_, _, _ = a.Login(ctx, "sam@example.com", "wrong", "", "ip1")
	_, _, _ = a.Login(ctx, "sam@example.com", "wrong", "", "ip1")
	if _, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip1"); err != nil {
		t.Fatalf("Login success: %v", err)
	}
	// Counter reset; a fresh wrong attempt is invalid-credentials, not limited.
	if _, _, err := a.Login(ctx, "sam@example.com", "wrong", "", "ip1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials after reset, got %v", err)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "right")
	ctx := context.Background()
	token, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip1")
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

package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore is an in-memory Store for authenticator unit tests. Integration
// tests exercise the real Postgres store (ENGINEERING rule 29).
type fakeStore struct {
	users             map[string]domain.User        // by email
	sessions          map[string]string             // token-hash → userID
	tokens            map[string]domain.APIToken    // token id → metadata
	byHash            map[string]string             // token-hash → token id
	touched           map[string]int                // token-hash → times touched
	totpSecrets       map[string]*store.TOTPSecret  // userID → 2FA secret+state
	recovery          map[string][]string           // userID → unused code-hashes
	avatars           map[string]domain.Avatar      // userID → profile photo
	emailChanges      map[string]domain.EmailChange // pending address moves
	emailChangeHashes map[string][]byte             // change id → token hash
	expiries          map[string]time.Time          // token-hash → session expiry
	purgeErr          error                         // DeleteExpiredSessions failure, when set
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

func (f *fakeStore) SessionForToken(_ context.Context, tokenHash []byte) (domain.User, string, error) {
	userID, ok := f.sessions[string(tokenHash)]
	if !ok {
		return domain.User{}, "", store.ErrNotFound
	}
	u, ok := f.userByID(userID)
	if !ok {
		return domain.User{}, "", store.ErrNotFound
	}
	return u, "sess_" + string(tokenHash), nil
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

func (f *fakeStore) SetUserAvatar(_ context.Context, userID, contentType string, data []byte, etag string) error {
	if f.avatars == nil {
		f.avatars = map[string]domain.Avatar{}
	}
	f.avatars[userID] = domain.Avatar{ContentType: contentType, Bytes: data, ETag: etag}
	return nil
}

func (f *fakeStore) GetUserAvatar(_ context.Context, userID string) (domain.Avatar, error) {
	av, ok := f.avatars[userID]
	if !ok {
		return domain.Avatar{}, store.ErrNotFound
	}
	return av, nil
}

func (f *fakeStore) DeleteUserAvatar(_ context.Context, userID string) error {
	delete(f.avatars, userID)
	return nil
}

func (f *fakeStore) UpdateUserEmail(_ context.Context, userID, email string) (domain.User, error) {
	for e, u := range f.users {
		if u.ID == userID {
			delete(f.users, e)
			u.Email = email
			f.users[email] = u
			return u, nil
		}
	}
	return domain.User{}, store.ErrNotFound
}

func (f *fakeStore) CreateEmailChange(_ context.Context, id, userID, newEmail string, tokenHash []byte, expiresAt time.Time) (domain.EmailChange, error) {
	if f.emailChanges == nil {
		f.emailChanges = map[string]domain.EmailChange{}
	}
	if f.emailChangeHashes == nil {
		f.emailChangeHashes = map[string][]byte{}
	}
	ec := domain.EmailChange{ID: id, UserID: userID, NewEmail: newEmail, ExpiresAt: expiresAt}
	f.emailChanges[id] = ec
	f.emailChangeHashes[id] = tokenHash
	return ec, nil
}

func (f *fakeStore) EmailChangeTokenHash(_ context.Context, id string) (domain.EmailChange, []byte, error) {
	ec, ok := f.emailChanges[id]
	if !ok {
		return domain.EmailChange{}, nil, store.ErrNotFound
	}
	return ec, f.emailChangeHashes[id], nil
}

func (f *fakeStore) ConsumeEmailChange(_ context.Context, id string) (domain.EmailChange, error) {
	ec, ok := f.emailChanges[id]
	if !ok || ec.ConsumedAt != nil {
		return domain.EmailChange{}, store.ErrNotFound
	}
	now := time.Now()
	ec.ConsumedAt = &now
	f.emailChanges[id] = ec
	return ec, nil
}

func (f *fakeStore) GetUserByID(_ context.Context, id string) (domain.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return domain.User{}, store.ErrNotFound
}

func (f *fakeStore) UpdateUserProfile(_ context.Context, userID, displayName, timezone string) (domain.User, error) {
	for email, u := range f.users {
		if u.ID == userID {
			u.DisplayName, u.Timezone = displayName, timezone
			f.users[email] = u
			return u, nil
		}
	}
	return domain.User{}, store.ErrNotFound
}

func (f *fakeStore) UpdateUserPassword(_ context.Context, userID, passwordHash string) error {
	for email, u := range f.users {
		if u.ID == userID {
			u.PasswordHash = passwordHash
			f.users[email] = u
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) CreateSession(_ context.Context, _, userID string, tokenHash []byte, expiresAt time.Time) error {
	f.sessions[string(tokenHash)] = userID
	if f.expiries == nil {
		f.expiries = map[string]time.Time{}
	}
	f.expiries[string(tokenHash)] = expiresAt
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

// DeleteExpiredSessions drops the sessions whose recorded expiry is at or
// before the cutoff (expiries maps token-hash → expiry; unrecorded = never).
func (f *fakeStore) DeleteExpiredSessions(_ context.Context, before time.Time) (int64, error) {
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	var n int64
	for hash, exp := range f.expiries {
		if !exp.After(before) {
			delete(f.sessions, hash)
			delete(f.expiries, hash)
			n++
		}
	}
	return n, nil
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

// TestLoginThrottleCarriesRetryAfter: the throttled answer says how long, and
// still matches the sentinel callers already map to 429.
func TestLoginThrottleCarriesRetryAfter(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	now := time.Now()
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	a := NewAuthenticator(fs, fakeBox{}, l, time.Hour)

	for range 2 {
		_, _, _ = a.Login(context.Background(), "sam@example.com", "wrong", "", "ip1")
	}
	_, _, err := a.Login(context.Background(), "sam@example.com", "wrong", "", "ip1")
	var rl *RateLimitedError
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a *RateLimitedError matching ErrRateLimited", err)
	}
	if rl.RetryAfter != time.Minute || rl.RetryAfterSeconds() != 60 {
		t.Fatalf("RetryAfter = %v (%ds), want the full window from the oldest failure", rl.RetryAfter, rl.RetryAfterSeconds())
	}
}

// TestLoginThrottlesPerAccountAcrossAddresses: a guess spread over many
// addresses is bounded by the account's own budget (twice the address one by
// default), while another account — and the same account from a fresh address
// once its budget resets — is untouched. This is what stops a botnet from
// enjoying five free guesses per address (control-plane-hardening.md §5).
func TestLoginThrottlesPerAccountAcrossAddresses(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	fs.users["ann@example.com"] = domain.User{ID: "usr_2", Email: "ann@example.com", PasswordHash: hash}
	a := NewAuthenticator(fs, fakeBox{}, NewLimiter(3, time.Minute), time.Hour)
	ctx := context.Background()

	// Six failures from six addresses: each address is within its own budget
	// of three, yet the account's budget of six is spent. Case-folded, so a
	// seventh guess with different capitalisation shares the same budget.
	for i := range 6 {
		if _, _, err := a.Login(ctx, "Sam@Example.com", "wrong", "", "ip"+strconv.Itoa(i)); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: err = %v, want ErrInvalidCredentials", i, err)
		}
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip-fresh"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("seventh attempt from a fresh address: err = %v, want ErrRateLimited (account budget spent)", err)
	}
	// Another account behind the same proxy address is not locked out.
	if _, _, err := a.Login(ctx, "ann@example.com", "right", "", "ip0"); err != nil {
		t.Fatalf("another account from a used address: %v, want success", err)
	}
}

// TestLoginPerAddressThrottleSparesOtherAccounts: exhausting one address does
// not reach an account signing in from elsewhere.
func TestLoginPerAddressThrottleSparesOtherAccounts(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	fs.users["ann@example.com"] = domain.User{ID: "usr_2", Email: "ann@example.com", PasswordHash: hash}
	a := NewAuthenticator(fs, fakeBox{}, NewLimiter(2, time.Minute), time.Hour)
	ctx := context.Background()
	for range 2 {
		_, _, _ = a.Login(ctx, "nobody@example.com", "wrong", "", "attacker")
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "right", "", "attacker"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("from the exhausted address: err = %v, want ErrRateLimited", err)
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "right", "", "home"); err != nil {
		t.Fatalf("same account from another address: %v, want success", err)
	}
	if _, _, err := a.Login(ctx, "ann@example.com", "right", "", "home"); err != nil {
		t.Fatalf("another account from another address: %v, want success", err)
	}
}

// TestPurgeExpiredSessionsUsesTheInjectedClock: rows at or before the clock go,
// later ones stay, and the purge reports the count (control-plane-hardening.md §7).
func TestPurgeExpiredSessionsUsesTheInjectedClock(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	a := NewAuthenticator(fs, fakeBox{}, nil, time.Hour, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	early, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	now = now.Add(30 * time.Minute)
	late, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	now = now.Add(31 * time.Minute) // the first session expired a minute ago
	n, err := a.PurgeExpiredSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions = %d, %v; want 1, nil", n, err)
	}
	if _, err := a.Authenticate(ctx, early); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session still authenticates: %v", err)
	}
	if _, err := a.Authenticate(ctx, late); err != nil {
		t.Fatalf("the live session was purged: %v", err)
	}
	fs.purgeErr = errors.New("db down")
	if _, err := a.PurgeExpiredSessions(ctx); err == nil {
		t.Fatal("a store failure was swallowed")
	}
}

// TestRunSessionPurgeStopsWithItsContext: the sweep goroutine ticks and returns
// on cancellation (ENGINEERING rule 7: every goroutine has an owner).
func TestRunSessionPurgeStopsWithItsContext(t *testing.T) {
	fs := newFakeStore()
	hash, _ := HashPassword("right")
	fs.users["sam@example.com"] = domain.User{ID: "usr_1", Email: "sam@example.com", PasswordHash: hash}
	now := time.Now()
	a := NewAuthenticator(fs, fakeBox{}, nil, time.Hour, WithClock(func() time.Time { return now }))
	ctx, cancel := context.WithCancel(context.Background())
	token, _, err := a.Login(ctx, "sam@example.com", "right", "", "ip1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	now = now.Add(2 * time.Hour)

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.RunSessionPurge(ctx, 5*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := a.Authenticate(context.Background(), token); errors.Is(err, ErrInvalidSession) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the purge never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunSessionPurge did not return after cancellation")
	}
}

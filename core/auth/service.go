package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Sentinel errors returned by the Authenticator. Callers match with errors.Is
// and map to HTTP status codes; the messages are deliberately non-specific so
// they never reveal whether an email exists (threat-model §5.8).
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrRateLimited        = errors.New("auth: too many attempts, try again later")
	ErrInvalidSession     = errors.New("auth: invalid or expired session")
	// ErrTOTPRequired means the password was correct but the account has
	// two-factor enabled and no (or a wrong) second factor was supplied. The
	// caller should re-submit with a code; the password need not be re-sent.
	ErrTOTPRequired = errors.New("auth: two-factor code required")
)

// SecretBox seals and opens the TOTP secret at rest (consumer-defined;
// *secret.Box satisfies it — the same AES-256-GCM box that protects the CA key).
type SecretBox interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// Store is the persistence the authenticator depends on (consumer-defined,
// ENGINEERING rule 6). The concrete *store.Store satisfies it.
type Store interface {
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	CreateSession(ctx context.Context, id, userID string, tokenHash []byte, expiresAt time.Time) error
	UserForSessionToken(ctx context.Context, tokenHash []byte) (domain.User, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error)

	SessionForToken(ctx context.Context, tokenHash []byte) (domain.User, string, error)
	ListSessionsByUser(ctx context.Context, userID string) ([]domain.Session, error)
	DeleteSessionForUser(ctx context.Context, sessionID, userID string) (bool, error)
	DeleteOtherSessionsForUser(ctx context.Context, userID string, keepTokenHash []byte) (int64, error)

	CreateAPIToken(ctx context.Context, id, userID, name string, abilities []domain.Ability, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error)
	APITokenByHash(ctx context.Context, tokenHash []byte) (domain.User, string, []domain.Ability, error)
	TouchAPIToken(ctx context.Context, tokenHash []byte) error
	ListAPITokensByUser(ctx context.Context, userID string) ([]domain.APIToken, error)
	GetAPIToken(ctx context.Context, id string) (domain.APIToken, error)
	DeleteAPIToken(ctx context.Context, id string) error

	GetUserByID(ctx context.Context, id string) (domain.User, error)
	SetUserAvatar(ctx context.Context, userID, contentType string, data []byte, etag string) error
	GetUserAvatar(ctx context.Context, userID string) (domain.Avatar, error)
	DeleteUserAvatar(ctx context.Context, userID string) error
	UpdateUserProfile(ctx context.Context, userID, displayName, timezone string) (domain.User, error)
	UpdateUserEmail(ctx context.Context, userID, email string) (domain.User, error)
	CreateEmailChange(ctx context.Context, id, userID, newEmail string, tokenHash []byte, expiresAt time.Time) (domain.EmailChange, error)
	EmailChangeTokenHash(ctx context.Context, id string) (domain.EmailChange, []byte, error)
	ConsumeEmailChange(ctx context.Context, id string) (domain.EmailChange, error)
	PendingEmailChange(ctx context.Context, userID string) (domain.EmailChange, error)
	CancelPendingEmailChanges(ctx context.Context, userID string) (int64, error)
	UpdateUserPassword(ctx context.Context, userID, passwordHash string) error

	SetTOTPSecret(ctx context.Context, userID string, ct, nonce []byte) error
	EnableTOTP(ctx context.Context, userID string) error
	DisableTOTP(ctx context.Context, userID string) error
	GetTOTPSecret(ctx context.Context, userID string) (store.TOTPSecret, error)
	AddRecoveryCode(ctx context.Context, id, userID string, codeHash []byte) error
	ConsumeRecoveryCode(ctx context.Context, userID string, codeHash []byte) (bool, error)
	CountUnusedRecoveryCodes(ctx context.Context, userID string) (int, error)
	DeleteRecoveryCodes(ctx context.Context, userID string) error
}

// dummyHash is a valid bcrypt hash compared against when a login names a
// nonexistent user, so response timing does not reveal which emails exist.
var dummyHash string

func init() {
	h, err := HashPassword("cypherpanel-user-enumeration-defense")
	if err != nil {
		panic(fmt.Sprintf("auth: initializing dummy hash: %v", err)) // init invariant (rule 8)
	}
	dummyHash = h
}

// Authenticator verifies credentials and manages sessions.
type Authenticator struct {
	store Store
	box   SecretBox
	// limiter throttles by client address; accounts throttles by the account
	// being attacked, so one attacker behind a shared proxy cannot lock
	// everyone out and a distributed guess at one account is still bounded
	// (control-plane-hardening.md §5). Either may be nil (never throttles).
	limiter    *Limiter
	accounts   *Limiter
	sessionTTL time.Duration
	now        func() time.Time
}

// Option tunes an Authenticator at construction (ENGINEERING rule 5: no
// setters after the fact).
type Option func(*Authenticator)

// WithAccountLimiter sets the per-account limiter. Without it, one is derived
// from the address limiter with twice its failure budget over the same
// window: an account throttle that trips as easily as the address one would
// let a stranger lock an account with five wrong guesses.
func WithAccountLimiter(l *Limiter) Option { return func(a *Authenticator) { a.accounts = l } }

// WithClock injects the clock session expiry and the purge read (rule 9).
func WithClock(now func() time.Time) Option { return func(a *Authenticator) { a.now = now } }

// NewAuthenticator wires the authenticator. limiter throttles failed logins by
// client address; sessionTTL is how long an issued session stays valid; box
// seals the TOTP secret at rest.
func NewAuthenticator(s Store, box SecretBox, limiter *Limiter, sessionTTL time.Duration, opts ...Option) *Authenticator {
	a := &Authenticator{store: s, box: box, limiter: limiter, sessionTTL: sessionTTL, now: time.Now}
	for _, o := range opts {
		o(a)
	}
	if a.accounts == nil && limiter != nil {
		a.accounts = NewLimiter(limiter.max*2, limiter.window)
	}
	return a
}

// accountKey is the per-account throttle key for a sign-in: the address as
// typed, folded so "Sam@Example.com" and "sam@example.com" share one budget.
func accountKey(email string) string { return "login:" + strings.ToLower(strings.TrimSpace(email)) }

// Login verifies credentials and, on success, creates a session and returns its
// raw bearer token (shown to the client once, never stored). throttleKey scopes
// rate limiting by client — the client address as the API resolved it (behind a
// trusted proxy, the forwarded one). The account is throttled on its own key
// besides. totpCode is the optional second factor: for a 2FA-enabled account it
// must be a valid authenticator code or an unused recovery code, otherwise
// Login returns ErrTOTPRequired (or, for a wrong code, ErrInvalidCredentials).
// A throttled attempt returns a *RateLimitedError (errors.Is ErrRateLimited).
func (a *Authenticator) Login(ctx context.Context, email, password, totpCode, throttleKey string) (rawToken string, user domain.User, err error) {
	limiters := []*Limiter{a.limiter, a.accounts}
	keys := []string{throttleKey, accountKey(email)}
	if err := throttle(limiters, keys); err != nil {
		return "", domain.User{}, err
	}
	fail := func() {
		for i, l := range limiters {
			l.Fail(keys[i])
		}
	}

	user, err = a.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			CheckPassword(dummyHash, password) // spend comparable time; ignore result
			fail()
			return "", domain.User{}, ErrInvalidCredentials
		}
		return "", domain.User{}, fmt.Errorf("auth: looking up user: %w", err)
	}

	if !CheckPassword(user.PasswordHash, password) {
		fail()
		return "", domain.User{}, ErrInvalidCredentials
	}

	// Second factor. The password was correct, so a missing code is a benign
	// "need the code" (no limiter penalty); a wrong code is a failed attempt.
	if user.TOTPEnabled {
		if totpCode == "" {
			return "", domain.User{}, ErrTOTPRequired
		}
		ok, err := a.checkSecondFactor(ctx, user.ID, totpCode)
		if err != nil {
			return "", domain.User{}, err
		}
		if !ok {
			fail()
			return "", domain.User{}, ErrInvalidCredentials
		}
	}
	for i, l := range limiters {
		l.Reset(keys[i])
	}

	token, err := a.StartSession(ctx, user.ID)
	if err != nil {
		return "", domain.User{}, err
	}
	return token, user, nil
}

// StartSession mints a fresh session for an already-authenticated user and
// returns its raw bearer token (shown once, never stored). It is the
// session-creation tail of Login, exposed so flows that establish identity
// without a password check — first-run setup (onboarding) — can auto-sign-in
// the account they just created. Callers own the authorization decision;
// StartSession itself performs no credential check.
func (a *Authenticator) StartSession(ctx context.Context, userID string) (rawToken string, err error) {
	rawToken = ids.Secret()
	expires := a.now().Add(a.sessionTTL)
	if err := a.store.CreateSession(ctx, ids.New(ids.PrefixSession), userID, HashToken(rawToken), expires); err != nil {
		return "", fmt.Errorf("auth: creating session: %w", err)
	}
	return rawToken, nil
}

// PrincipalKind distinguishes how a caller proved who they are. It matters for
// authorization: a personal access token must never be able to escalate itself
// by minting more tokens, revoking sessions, or turning off two-factor auth —
// only an interactive session can manage credentials (threat-model §5.8).
type PrincipalKind string

const (
	KindSession  PrincipalKind = "session"
	KindAPIToken PrincipalKind = "api_token"
)

// Principal is an authenticated caller: the user, how they authenticated, and
// what that credential is allowed to do. A session carries the full ability
// set; a token carries only the abilities it was issued with.
type Principal struct {
	User      domain.User
	Kind      PrincipalKind
	Abilities []domain.Ability
	// SessionID is set for session principals so a session list can mark the
	// caller's own entry. Empty for tokens.
	SessionID string
	// TokenID is set for API-token principals (audit/debug). Empty for sessions.
	TokenID string
}

// Can reports whether this principal's credential carries an ability.
func (p Principal) Can(a domain.Ability) bool { return domain.HasAbility(p.Abilities, a) }

// Authenticate resolves a raw bearer token to its principal, or
// ErrInvalidSession. A token carrying the API-token prefix is resolved as a
// personal access token (authenticating as its owning user, narrowed by its
// abilities); anything else is a session. Session secrets are uppercase base32
// (ids.Secret) and never carry the prefix, so the two spaces cannot collide.
func (a *Authenticator) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if strings.HasPrefix(rawToken, APITokenPrefix) {
		return a.authenticateAPIToken(ctx, rawToken)
	}
	// One query resolves both the user and the session id: the join already
	// selects both rows, and authentication runs on every request, so looking
	// them up separately would double its cost for nothing.
	user, sessionID, err := a.store.SessionForToken(ctx, HashToken(rawToken))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrInvalidSession
		}
		return Principal{}, fmt.Errorf("auth: resolving session: %w", err)
	}
	return Principal{
		User:      user,
		Kind:      KindSession,
		Abilities: domain.AllAbilities(),
		SessionID: sessionID,
	}, nil
}

// Logout revokes the session behind a raw bearer token. Revoking an unknown
// token is not an error (idempotent logout).
func (a *Authenticator) Logout(ctx context.Context, rawToken string) error {
	if err := a.store.DeleteSession(ctx, HashToken(rawToken)); err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

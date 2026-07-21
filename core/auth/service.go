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

	CreateAPIToken(ctx context.Context, id, userID, name string, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error)
	UserForAPIToken(ctx context.Context, tokenHash []byte) (domain.User, error)
	TouchAPIToken(ctx context.Context, tokenHash []byte) error
	ListAPITokensByUser(ctx context.Context, userID string) ([]domain.APIToken, error)
	GetAPIToken(ctx context.Context, id string) (domain.APIToken, error)
	DeleteAPIToken(ctx context.Context, id string) error

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
	store      Store
	box        SecretBox
	limiter    *Limiter
	sessionTTL time.Duration
	now        func() time.Time
}

// NewAuthenticator wires the authenticator. limiter throttles failed logins;
// sessionTTL is how long an issued session stays valid; box seals the TOTP
// secret at rest.
func NewAuthenticator(s Store, box SecretBox, limiter *Limiter, sessionTTL time.Duration) *Authenticator {
	return &Authenticator{store: s, box: box, limiter: limiter, sessionTTL: sessionTTL, now: time.Now}
}

// Login verifies credentials and, on success, creates a session and returns its
// raw bearer token (shown to the client once, never stored). throttleKey scopes
// rate limiting — typically the client IP. totpCode is the optional second
// factor: for a 2FA-enabled account it must be a valid authenticator code or an
// unused recovery code, otherwise Login returns ErrTOTPRequired (or, for a wrong
// code, ErrInvalidCredentials).
func (a *Authenticator) Login(ctx context.Context, email, password, totpCode, throttleKey string) (rawToken string, user domain.User, err error) {
	if !a.limiter.Allow(throttleKey) {
		return "", domain.User{}, ErrRateLimited
	}

	user, err = a.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			CheckPassword(dummyHash, password) // spend comparable time; ignore result
			a.limiter.Fail(throttleKey)
			return "", domain.User{}, ErrInvalidCredentials
		}
		return "", domain.User{}, fmt.Errorf("auth: looking up user: %w", err)
	}

	if !CheckPassword(user.PasswordHash, password) {
		a.limiter.Fail(throttleKey)
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
			a.limiter.Fail(throttleKey)
			return "", domain.User{}, ErrInvalidCredentials
		}
	}
	a.limiter.Reset(throttleKey)

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

// Authenticate resolves a raw bearer token to its user, or ErrInvalidSession.
// A token carrying the API-token prefix is resolved as a personal access token
// (authenticating as its owning user); anything else is a session. Session
// secrets are uppercase base32 (ids.Secret) and never carry the prefix, so the
// two spaces cannot collide.
func (a *Authenticator) Authenticate(ctx context.Context, rawToken string) (domain.User, error) {
	if strings.HasPrefix(rawToken, APITokenPrefix) {
		return a.authenticateAPIToken(ctx, rawToken)
	}
	user, err := a.store.UserForSessionToken(ctx, HashToken(rawToken))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, ErrInvalidSession
		}
		return domain.User{}, fmt.Errorf("auth: resolving session: %w", err)
	}
	return user, nil
}

// Logout revokes the session behind a raw bearer token. Revoking an unknown
// token is not an error (idempotent logout).
func (a *Authenticator) Logout(ctx context.Context, rawToken string) error {
	if err := a.store.DeleteSession(ctx, HashToken(rawToken)); err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

package auth

import (
	"context"
	"errors"
	"fmt"
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
)

// Store is the persistence the authenticator depends on (consumer-defined,
// ENGINEERING rule 6). The concrete *store.Store satisfies it.
type Store interface {
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	CreateSession(ctx context.Context, id, userID string, tokenHash []byte, expiresAt time.Time) error
	UserForSessionToken(ctx context.Context, tokenHash []byte) (domain.User, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
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
	limiter    *Limiter
	sessionTTL time.Duration
	now        func() time.Time
}

// NewAuthenticator wires the authenticator. limiter throttles failed logins;
// sessionTTL is how long an issued session stays valid.
func NewAuthenticator(s Store, limiter *Limiter, sessionTTL time.Duration) *Authenticator {
	return &Authenticator{store: s, limiter: limiter, sessionTTL: sessionTTL, now: time.Now}
}

// Login verifies credentials and, on success, creates a session and returns its
// raw bearer token (shown to the client once, never stored). throttleKey scopes
// rate limiting — typically the client IP.
func (a *Authenticator) Login(ctx context.Context, email, password, throttleKey string) (rawToken string, user domain.User, err error) {
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
	a.limiter.Reset(throttleKey)

	rawToken = ids.Secret()
	expires := a.now().Add(a.sessionTTL)
	if err := a.store.CreateSession(ctx, ids.New(ids.PrefixSession), user.ID, HashToken(rawToken), expires); err != nil {
		return "", domain.User{}, fmt.Errorf("auth: creating session: %w", err)
	}
	return rawToken, user, nil
}

// Authenticate resolves a raw bearer token to its user, or ErrInvalidSession.
func (a *Authenticator) Authenticate(ctx context.Context, rawToken string) (domain.User, error) {
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

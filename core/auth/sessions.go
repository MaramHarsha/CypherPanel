package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ErrSessionNotFound is returned when a session id does not exist or belongs to
// someone else — the two are indistinguishable to the caller by design, so a
// session list cannot be probed across accounts.
var ErrSessionNotFound = errors.New("auth: session not found")

// ListSessions returns a user's live sessions, newest first. Nothing replayable
// is included: the domain type carries no token material.
func (a *Authenticator) ListSessions(ctx context.Context, userID string) ([]domain.Session, error) {
	sessions, err := a.store.ListSessionsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: listing sessions: %w", err)
	}
	return sessions, nil
}

// RevokeSession signs one session out, but only if it belongs to userID.
// Revoking the caller's own session is allowed — it is simply a logout from
// this device, and the client discovers it on the next request.
func (a *Authenticator) RevokeSession(ctx context.Context, userID, sessionID string) error {
	deleted, err := a.store.DeleteSessionForUser(ctx, sessionID, userID)
	if err != nil {
		return fmt.Errorf("auth: revoking session: %w", err)
	}
	if !deleted {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeOtherSessions signs the user out everywhere except the device making
// the request — the standard response to "I think someone has my password",
// paired with a password change. The surviving session is identified by the
// presented token's hash, never by an id the client supplies, so this cannot be
// turned into "keep the attacker's session and drop mine".
func (a *Authenticator) RevokeOtherSessions(ctx context.Context, userID, rawToken string) (int64, error) {
	n, err := a.store.DeleteOtherSessionsForUser(ctx, userID, HashToken(rawToken))
	if err != nil {
		return 0, fmt.Errorf("auth: revoking other sessions: %w", err)
	}
	return n, nil
}

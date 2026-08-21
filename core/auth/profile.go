// Profile: the fields a person sets about themselves, and the one credential
// they can change without an administrator (canvas 7a/13i).
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ValidationError marks input the caller can fix; the REST layer maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrSamePassword is returned when the new password equals the current one.
// Accepting it would be a lie: the operator asked for a rotation and got none,
// and any session they revoked on the strength of it is still guarded by the
// same secret as before.
var ErrSamePassword = errors.New("auth: new password matches the current one")

const (
	maxDisplayName = 64
	maxTimezone    = 64
	// The same floor the first-run screen enforces (onboarding.minPasswordLen);
	// a rotation must not be able to weaken an account below what creating it
	// would have allowed.
	minPasswordLen = 8
)

// UpdateProfile stores a display name and a timezone. Either may be cleared by
// passing an empty string — the panel then falls back to the address and to
// UTC, which is what it did before the fields existed.
func (a *Authenticator) UpdateProfile(ctx context.Context, userID, displayName, timezone string) (domain.User, error) {
	displayName = strings.TrimSpace(displayName)
	timezone = strings.TrimSpace(timezone)

	if utf8.RuneCountInString(displayName) > maxDisplayName {
		return domain.User{}, invalid(fmt.Sprintf("a display name is at most %d characters", maxDisplayName))
	}
	if strings.ContainsAny(displayName, "\r\n") {
		return domain.User{}, invalid("a display name is a single line")
	}
	// Validated against the machine's own zone database rather than a pattern:
	// the only useful definition of a valid zone is one Go can actually load,
	// and storing a name it cannot would break every timestamp it touches.
	if timezone != "" {
		if utf8.RuneCountInString(timezone) > maxTimezone {
			return domain.User{}, invalid("that is not an IANA timezone name")
		}
		if _, err := time.LoadLocation(timezone); err != nil {
			return domain.User{}, invalid(fmt.Sprintf("%q is not an IANA timezone name — try Europe/Berlin", timezone))
		}
	}
	return a.store.UpdateUserProfile(ctx, userID, displayName, timezone)
}

// ChangePassword rotates a password after proving the current one, and reports
// how many other sessions it revoked.
//
// Whether to revoke is the caller's to decide, not this function's: the canvas
// asks the question inside the dialog (9i), because a rotation after a scare
// wants every other session gone and a routine one does not — and guessing
// either way is worse than asking.
func (a *Authenticator) ChangePassword(ctx context.Context, userID, current, next, rawToken string, revokeOthers bool) (int64, error) {
	user, err := a.store.GetUserByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	// The same error as a failed sign-in, deliberately: this is a credential
	// check, and it should tell an attacker who has stolen a session exactly as
	// little as the login form does.
	if !CheckPassword(user.PasswordHash, current) {
		return 0, ErrInvalidCredentials
	}
	if len(next) < minPasswordLen {
		return 0, invalid(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
	}
	if current == next {
		return 0, ErrSamePassword
	}
	hash, err := HashPassword(next)
	if err != nil {
		return 0, err
	}
	if err := a.store.UpdateUserPassword(ctx, userID, hash); err != nil {
		return 0, err
	}
	if !revokeOthers {
		return 0, nil
	}
	return a.RevokeOtherSessions(ctx, userID, rawToken)
}

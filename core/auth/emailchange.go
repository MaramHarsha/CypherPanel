// Moving an account to a new sign-in address (docs/features/panel-mail.md §3,
// threat-model §5.10).
//
// This is join_tokens' shape, because it is join_tokens' problem: a short-lived,
// single-use permission to do one thing once. The wire token is `id.secret`, so
// the lookup is by a public id and only a hash of the secret is stored; the
// secret is compared in constant time *before* the change is spent, so a wrong
// guess against a real id cannot burn a valid one; and the spend itself is a
// single atomic UPDATE, which is the only race-free way to enforce "once".
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

var (
	// ErrInvalidEmailChange covers wrong, expired, already-spent and unknown —
	// one undifferentiated answer, so failures leak nothing about which changes
	// exist. The same discipline as enroll.ErrInvalidToken.
	ErrInvalidEmailChange = errors.New("auth: that confirmation link is not valid")
	// ErrEmailInUse is deliberately distinguishable. See panel-mail.md §5: the
	// caller is authenticated, rate limited, and asking about their own account,
	// and silence would be a worse failure than the disclosure is a risk.
	ErrEmailInUse = errors.New("auth: that address is already in use")
	// ErrSameEmail — nothing to do, and saying so is kinder than a link that
	// changes nothing.
	ErrSameEmail = errors.New("auth: that is already your address")
)

// emailChangeTTL is long enough to walk to another device and short enough that
// a forwarded link goes stale.
const emailChangeTTL = 30 * time.Minute

// PendingChange is what a caller needs to tell people about a requested move:
// the wire token for the new address, the addresses themselves, and nothing
// from the request that produced it.
//
// NewEmail is the address as this function parsed it, not as it arrived. The
// caller must use this and never the raw request field: what a person typed is
// untrusted input, and it ends up in a recipient list and an email body.
type PendingChange struct {
	Token    string
	NewEmail string
	OldEmail string
}

// RequestEmailChange proves the caller owns the account, records a pending
// change, and returns what is needed to mail both addresses — the new one its
// confirmation, the old one its warning (threat-model §5.10).
func (a *Authenticator) RequestEmailChange(ctx context.Context, userID, newEmail, currentPassword string) (PendingChange, error) {
	addr, parseErr := mail.ParseAddress(strings.TrimSpace(newEmail))
	if parseErr != nil {
		return PendingChange{}, invalid(fmt.Sprintf("%q is not a valid email address", newEmail))
	}
	// From here on only the parsed address is used. ParseAddress accepts
	// "Name <a@b.com>", so the raw string and the address are not the same
	// value — and only one of them belongs in a header.
	newEmail = addr.Address

	user, err := a.store.GetUserByID(ctx, userID)
	if err != nil {
		return PendingChange{}, err
	}
	// Possession of a session never weakens a credential on its own.
	if !CheckPassword(user.PasswordHash, currentPassword) {
		return PendingChange{}, ErrInvalidCredentials
	}
	if strings.EqualFold(newEmail, user.Email) {
		return PendingChange{}, ErrSameEmail
	}
	if _, err := a.store.GetUserByEmail(ctx, newEmail); err == nil {
		return PendingChange{}, ErrEmailInUse
	} else if !errors.Is(err, store.ErrNotFound) {
		return PendingChange{}, err
	}

	secret := ids.Secret()
	id := ids.New(ids.PrefixEmailChange)
	change, err := a.store.CreateEmailChange(ctx, id, userID, newEmail, HashToken(secret), time.Now().Add(emailChangeTTL))
	if err != nil {
		return PendingChange{}, err
	}
	// The address is read back from the row that was just written, so what goes
	// into the mail is what the panel stored rather than what the request said.
	return PendingChange{Token: id + "." + secret, NewEmail: change.NewEmail, OldEmail: user.Email}, nil
}

// ConfirmEmailChange spends a pending change and moves the address. It also
// reports how many other sessions it revoked: the address that can recover this
// account has moved, so every other sign-in is now suspect.
func (a *Authenticator) ConfirmEmailChange(ctx context.Context, userID, token, rawSessionToken string) (domain.User, int64, error) {
	id, secret, ok := splitEmailChangeToken(token)
	if !ok {
		return domain.User{}, 0, ErrInvalidEmailChange
	}
	pending, hash, err := a.store.EmailChangeTokenHash(ctx, id)
	if err != nil {
		return domain.User{}, 0, ErrInvalidEmailChange
	}
	// Compared before anything is spent, so a wrong guess against a real id
	// cannot consume a valid change.
	if !ConstantTimeEqual(hash, HashToken(secret)) {
		return domain.User{}, 0, ErrInvalidEmailChange
	}
	// A change belongs to the account that asked for it. Without this, a link
	// mailed to one operator could be confirmed from another's session.
	if pending.UserID != userID {
		return domain.User{}, 0, ErrInvalidEmailChange
	}
	if pending.ConsumedAt != nil || !time.Now().Before(pending.ExpiresAt) {
		return domain.User{}, 0, ErrInvalidEmailChange
	}

	// The authoritative guard: no row back means someone else spent it first.
	spent, err := a.store.ConsumeEmailChange(ctx, id)
	if err != nil {
		return domain.User{}, 0, ErrInvalidEmailChange
	}

	user, err := a.store.UpdateUserEmail(ctx, userID, spent.NewEmail)
	if err != nil {
		// The address was taken between the request and the confirmation; the
		// unique constraint is the last word.
		return domain.User{}, 0, ErrEmailInUse
	}
	revoked, err := a.RevokeOtherSessions(ctx, userID, rawSessionToken)
	if err != nil {
		// The address did move; failing the whole call here would tell the
		// operator the opposite of the truth.
		revoked = 0
	}
	return user, revoked, nil
}

func splitEmailChangeToken(t string) (id, secret string, ok bool) {
	i := strings.IndexByte(t, '.')
	if i <= 0 || i == len(t)-1 {
		return "", "", false
	}
	return t[:i], t[i+1:], true
}

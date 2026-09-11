// Package access is how a person gets into a team
// (docs/features/invitations-and-access-requests.md): an **Invitation** — a
// signed, expiring link that lets someone who is not in the panel join one team
// at one role — and an **Access Request** — a member of a team asking its
// owners for a higher rank.
//
// They are two halves of one question and share one package because they share
// one rule set: the grant rank is checked when the permission is CREATED, never
// when it is spent (domain.CanGrantRole, the same comparison core/teams uses);
// a refusal that is not the caller's fault is one undifferentiated answer; and
// nothing here re-implements membership — a granted request goes through the
// existing member-role path, so the last-owner guard keeps holding.
//
// Neither half adds desired state, a NATS subject, a work item or an agent
// path: they are authorization records in Postgres.
package access

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/mail"
	"github.com/MaramHarsha/cypherpanel/core/notify"
)

// Sentinel errors the REST layer maps to statuses.
var (
	// ErrForbidden marks an actor of insufficient rank to hand out the role
	// they asked for (403).
	ErrForbidden = errors.New("access: insufficient role")
	// ErrInvalidInvite covers unknown, wrong-secret, expired, revoked and
	// already-accepted — ONE undifferentiated answer (404), so a guess against
	// the public routes learns nothing about which invitations exist. The same
	// discipline auth.ErrInvalidEmailChange uses.
	ErrInvalidInvite = errors.New("access: that invitation link is not valid")
	// ErrAlreadyMember refuses an invitation for someone the team already has
	// (409). Re-ranking an existing member belongs to the member-role route,
	// which has the last-owner guard.
	ErrAlreadyMember = errors.New("access: already a member of this team")
	// ErrRequestOpen refuses a second open request from one person (409): the
	// owners' inbox must not fill with the same ask.
	ErrRequestOpen = errors.New("access: a request from you is already open on this team")
	// ErrNotMember refuses granting a request whose subject has since left
	// (409). Leaving is a decision, and an old ask must not undo it.
	ErrNotMember = errors.New("access: the requester is no longer a member of this team")
	// ErrDecided refuses a second decision on one request (409).
	ErrDecided = errors.New("access: that request has already been decided")
)

// ValidationError is a client-caused input error; handlers map it to 400. Its
// message never contains a password or a token (ENGINEERING rule 20).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "access: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Mailer is the panel's own transport (consumer-defined; *mail.Service
// satisfies it). nil — or a panel with nothing configured — means the mail is
// simply not sent: an invitation still exists and its accept URL was returned
// once to the operator who created it (spec §6).
type Mailer interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

// Limiter throttles the two public routes by client address (consumer-defined;
// *auth.Limiter satisfies it). A nil Limiter never throttles, which is what
// unit tests of unrelated paths rely on.
type Limiter interface {
	Allow(key string) bool
	Fail(key string)
	Reset(key string)
	Refuse(key string) bool
	RetryAfter(key string) time.Duration
}

// noopLimiter stands in for an absent throttle so call sites stay branch-free.
// A nil *auth.Limiter is itself nil-safe, but a nil INTERFACE is not callable,
// and "the throttle is optional" must not become "a nil dereference on the one
// unauthenticated route".
type noopLimiter struct{}

func (noopLimiter) Allow(string) bool               { return true }
func (noopLimiter) Fail(string)                     {}
func (noopLimiter) Reset(string)                    {}
func (noopLimiter) Refuse(string) bool              { return false }
func (noopLimiter) RetryAfter(string) time.Duration { return 0 }

// send delivers one message best-effort. A panel with no transport is the
// common self-hosted case and is not an error here; a delivery failure is
// logged and swallowed, because the record the mail describes already exists
// and failing the request would say the opposite of the truth (spec §6).
//
// It reports whether the message actually went out, which is what the create
// response's `mail_sent` tells the operator.
func send(ctx context.Context, m Mailer, log *slog.Logger, to []string, subject, body string, attrs ...any) bool {
	if m == nil || len(to) == 0 {
		return false
	}
	// The subject is a header: a team name is operator-supplied text and a line
	// break in one would split the message (CWE-93).
	if err := m.Send(ctx, to, notify.SanitizeHeader(subject), body); err != nil {
		if !errors.Is(err, mail.ErrNotConfigured) {
			log.Error("sending access mail", append(attrs, "error", err)...)
		}
		return false
	}
	return true
}

// bounded trims and length-checks a free-text field a person typed — a
// request's message, a denial's reason. Runes, not bytes: the cap is a
// sentence, not a byte budget.
func bounded(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > max {
		return "", invalid(field + " is at most " + strconv.Itoa(max) + " characters")
	}
	return v, nil
}

// roleOrDefault applies the wire default (member) and validates the closed set.
func roleOrDefault(role string) (string, error) {
	if role == "" {
		role = domain.RoleMember
	}
	if !domain.ValidRole(role) {
		return "", invalid("role must be member, admin, or owner")
	}
	return role, nil
}

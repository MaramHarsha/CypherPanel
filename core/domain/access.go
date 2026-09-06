package domain

// Team invitations and access requests
// (docs/features/invitations-and-access-requests.md §2): the two ways a person
// gets into a team from outside the "an admin adds an account that already
// exists" path.
//
// An Invitation is a BEARER permission — whoever holds the link may take it —
// so it is short-lived, single-use, revocable, and only the hash of its secret
// is ever stored. An Access Request is the mirror image: it carries no secret
// and grants nothing on its own; it is a durable ask that a team owner answers.

import "time"

// InviteTTL is how long an invitation stays acceptable. Long enough to survive
// a weekend and a forwarded mail, short enough that a link found in an old
// mailbox is already dead (spec §8).
const InviteTTL = 7 * 24 * time.Hour

// Invite states, DERIVED from the three timestamps rather than stored: a
// stored "expired" would need a sweeper to stay true, and a row whose state
// disagrees with its own expires_at is worse than one that has to be computed.
const (
	InviteStatePending  = "pending"
	InviteStateAccepted = "accepted"
	InviteStateRevoked  = "revoked"
	InviteStateExpired  = "expired"
)

// Access-request states. Closed set; `pending` is the only one the partial
// unique index treats as open (spec §2).
const (
	AccessRequestPending = "pending"
	AccessRequestGranted = "granted"
	AccessRequestDenied  = "denied"
)

// AccessMessageMax bounds a request's message and a denial's reason. Both are
// a sentence to a colleague, not a document.
const AccessMessageMax = 500

// TeamInvite is one outstanding (or spent) invitation to join one team at one
// role.
//
// TokenHash is the SHA-256 of the wire secret and never leaves the store layer;
// no field here can carry the secret itself, which is what keeps "the token is
// never stored in clear" a property of the type rather than of a call site.
type TeamInvite struct {
	ID     string
	TeamID string
	// Email is the address as the panel parsed and lower-cased it, never as it
	// was typed: it reaches a recipient list and an email body (CWE-640).
	Email string
	Role  string
	// TokenHash is sha256(secret). Populated only on the write path and on the
	// lookup that verifies a presented token; the API never sees it.
	TokenHash []byte
	// InvitedBy is nil once that account is deleted; InvitedByLabel is the
	// snapshot the accept landing prints either way (spec §2).
	InvitedBy      *string
	InvitedByLabel string
	ExpiresAt      time.Time
	AcceptedAt     *time.Time
	RevokedAt      *time.Time
	CreatedAt      time.Time
}

// State derives the invite's state at the given instant. The clock is passed
// in rather than read here so a caller's answer and the SQL predicate that
// spends the row are compared against one time source (ENGINEERING rule 9).
func (i TeamInvite) State(now time.Time) string {
	switch {
	case i.AcceptedAt != nil:
		return InviteStateAccepted
	case i.RevokedAt != nil:
		return InviteStateRevoked
	case !now.Before(i.ExpiresAt):
		return InviteStateExpired
	default:
		return InviteStatePending
	}
}

// Acceptable reports whether this invite could still be spent at now. It is
// advisory: the authoritative check is the single atomic UPDATE in the store,
// which is the only race-free answer (spec §4).
func (i TeamInvite) Acceptable(now time.Time) bool {
	return i.State(now) == InviteStatePending
}

// AccessRequest is one member's ask for a higher rank in one team.
//
// UserEmail and CurrentRole are derived at read time — the requester's address
// and the membership as it stands NOW — so the owner's card can say
// "member → admin" without a second call, and says it about the current
// membership rather than the one that existed when the ask was made.
type AccessRequest struct {
	ID            string
	TeamID        string
	UserID        string
	UserEmail     string
	CurrentRole   string
	RequestedRole string
	Message       string
	State         string
	// DecidedBy is nil while pending, and nil again once that account is
	// deleted; DecidedByLabel is the snapshot (spec §2).
	DecidedBy      *string
	DecidedByLabel string
	DecisionReason string
	DecidedAt      *time.Time
	CreatedAt      time.Time
}

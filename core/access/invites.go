package access

// Team invitations (invitations-and-access-requests.md §2, §4, §6).
//
// The wire token is `<invite id>.<secret>`, so a lookup is an indexed
// primary-key read and the secret is compared in CONSTANT TIME before anything
// is spent — a wrong guess against a real id can therefore never burn a valid
// invitation. Only sha256(secret) is stored, and no field of any type this file
// returns can carry the secret: the accept URL is composed once, handed to the
// creating operator, and never reconstructible afterwards.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// inviteThrottleKey scopes the public routes' throttle to the client address,
// like sign-in. Prefixed so an invite guess and a login guess do not share one
// budget: they are different surfaces with different costs.
func inviteThrottleKey(clientIP string) string { return "invite:" + clientIP }

// InviteStore is the persistence invitations need (consumer-defined;
// *store.Store satisfies it).
type InviteStore interface {
	GetTeam(ctx context.Context, id string) (domain.Team, error)
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	CreateUser(ctx context.Context, id, email, passwordHash, role string) (domain.User, error)
	GetTeamMember(ctx context.Context, teamID, userID string) (domain.TeamMember, error)
	UpsertTeamMember(ctx context.Context, teamID, userID, role string) (domain.TeamMember, error)

	CreateTeamInvite(ctx context.Context, inv domain.TeamInvite) (domain.TeamInvite, error)
	GetTeamInvite(ctx context.Context, id string) (domain.TeamInvite, error)
	ListTeamInvites(ctx context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error)
	RevokeTeamInvite(ctx context.Context, teamID, id string) (domain.TeamInvite, error)
	RevokeLiveTeamInvitesFor(ctx context.Context, teamID, email string) (int64, error)
	AcceptTeamInvite(ctx context.Context, id string) (domain.TeamInvite, error)
}

// Sessions is the authentication this needs (consumer-defined;
// *auth.Authenticator satisfies it).
//
// Login is deliberately the WHOLE sign-in path rather than a password check:
// accepting an invitation for an address that already has an account inherits
// its throttling, its constant-time comparison and its second-factor
// requirement, so an invitation can never be a way around 2FA (spec §4).
type Sessions interface {
	Login(ctx context.Context, email, password, totpCode, throttleKey string) (string, domain.User, error)
	StartSession(ctx context.Context, userID string) (string, error)
	UpdateProfile(ctx context.Context, userID, displayName, timezone string) (domain.User, error)
}

// InviteAnnouncer tells the inviter their link was used (consumer-defined;
// *inbox.Service satisfies it). nil records nothing.
type InviteAnnouncer interface {
	RecordInviteAccepted(ctx context.Context, n inbox.InviteNotice) error
}

// Invites is the invitation surface. Construct with NewInvites.
type Invites struct {
	store   InviteStore
	auth    Sessions
	mailer  Mailer
	inbox   InviteAnnouncer
	limiter Limiter
	log     *slog.Logger
	// baseURL is the panel's own advertised base URL — the accept link's host
	// comes from the panel's configuration, never from a request header
	// (panel-mail.md §5: an attacker-chosen host in a link the panel signs is
	// how a trusted sender becomes someone else's phishing relay).
	baseURL string
	// now is injected so expiry copy is deterministic in tests; the
	// AUTHORITATIVE expiry check is the SQL predicate that spends the row
	// (ENGINEERING rule 9, spec §8).
	now func() time.Time
}

// NewInvites wires the invitation service. mailer, announcer and limiter may
// each be nil: no mail, no inbox item, no throttle — none of which changes
// whether an invitation is valid.
func NewInvites(st InviteStore, sessions Sessions, mailer Mailer, announcer InviteAnnouncer, limiter Limiter, baseURL string, log *slog.Logger) *Invites {
	if limiter == nil {
		limiter = noopLimiter{}
	}
	return &Invites{
		store: st, auth: sessions, mailer: mailer, inbox: announcer,
		limiter: limiter, baseURL: strings.TrimRight(baseURL, "/"),
		log: log, now: time.Now,
	}
}

// CreateInput is an invitation request. Role empty means member.
type CreateInput struct {
	Email string
	Role  string
}

// Created is a fresh invitation plus the two things that exist only at this
// moment: the accept URL — the ONLY time the wire token is readable — and
// whether the panel managed to mail it.
type Created struct {
	Invite    domain.TeamInvite
	AcceptURL string
	MailSent  bool
}

// Create issues an invitation. actorRole is the caller's effective role in the
// team: the invited role is checked against it here and never again, because
// the person accepting has no rank of their own to check (spec §1).
func (s *Invites) Create(ctx context.Context, teamID string, in CreateInput, actor domain.User, actorRole string) (Created, error) {
	role, err := roleOrDefault(in.Role)
	if err != nil {
		return Created{}, err
	}
	if !domain.CanGrantRole(actorRole, role) {
		return Created{}, ErrForbidden
	}
	addr, parseErr := mail.ParseAddress(strings.TrimSpace(in.Email))
	if parseErr != nil {
		return Created{}, invalid(fmt.Sprintf("%q is not a valid email address", in.Email))
	}
	// Only the parsed, folded address is used from here on. ParseAddress
	// accepts "Name <a@b.com>", so the raw string and the address are not the
	// same value — and only one of them belongs in a recipient list.
	email := strings.ToLower(addr.Address)

	team, err := s.store.GetTeam(ctx, teamID)
	if err != nil {
		return Created{}, err
	}
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		if _, err := s.store.GetTeamMember(ctx, teamID, u.ID); err == nil {
			return Created{}, ErrAlreadyMember
		} else if !errors.Is(err, store.ErrNotFound) {
			return Created{}, fmt.Errorf("access: checking membership: %w", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Created{}, fmt.Errorf("access: looking up %s: %w", email, err)
	}

	// Supersede rather than conflict: re-inviting an address is how an operator
	// fixes a link that went to the wrong place, and a 409 would leave them
	// stuck behind an invitation they cannot see (spec §2).
	if _, err := s.store.RevokeLiveTeamInvitesFor(ctx, teamID, email); err != nil {
		return Created{}, err
	}

	secret := ids.Secret()
	id := ids.New(ids.PrefixTeamInvite)
	inviter := actor.ID
	inv, err := s.store.CreateTeamInvite(ctx, domain.TeamInvite{
		ID:             id,
		TeamID:         teamID,
		Email:          email,
		Role:           role,
		TokenHash:      auth.HashToken(secret),
		InvitedBy:      &inviter,
		InvitedByLabel: actor.Email,
		ExpiresAt:      s.now().Add(domain.InviteTTL),
	})
	if err != nil {
		return Created{}, err
	}
	// The address is read back from the row that was just written, so what goes
	// into the mail is what the panel stored rather than what the request said.
	acceptURL := s.acceptURL(id + "." + secret)
	sent := send(ctx, s.mailer, s.log, []string{inv.Email},
		"You've been invited to "+team.Name+" on CypherPanel",
		inviteBody(team.Name, inv, acceptURL),
		"team_id", teamID, "invite_id", inv.ID)
	s.log.Info("team invitation issued",
		"team_id", teamID, "invite_id", inv.ID, "role", inv.Role, "mail_sent", sent)
	return Created{Invite: inv, AcceptURL: acceptURL, MailSent: sent}, nil
}

// List returns a team's invitations. includeDecided false is the operator's
// "outstanding" view.
func (s *Invites) List(ctx context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error) {
	return s.store.ListTeamInvites(ctx, teamID, includeDecided)
}

// Revoke kills one live invitation of a team. store.ErrNotFound covers already
// revoked, already accepted, and never belonged here.
func (s *Invites) Revoke(ctx context.Context, teamID, id string) (domain.TeamInvite, error) {
	return s.store.RevokeTeamInvite(ctx, teamID, id)
}

// Preview is what the unauthenticated landing screen needs: who invited you,
// to what, as what, and until when.
//
// AccountExists is what lets the screen say "Choose a password" or "Enter your
// password" honestly. It is a disclosure to whoever holds a valid, unexpired
// invitation for THAT address — strictly less than the team membership the
// same token already grants (spec §4).
type Preview struct {
	TeamName      string
	InviterLabel  string
	Email         string
	Role          string
	ExpiresAt     time.Time
	AccountExists bool
}

// Preview resolves a wire token for the landing screen. Every failure of the
// TOKEN is ErrInvalidInvite and costs the client address one strike against the
// throttle; a failure of the panel's own — a database that is down — is neither
// a 404 nor a strike.
func (s *Invites) Preview(ctx context.Context, token, clientIP string) (Preview, error) {
	key := inviteThrottleKey(clientIP)
	if err := s.throttle(key); err != nil {
		return Preview{}, err
	}
	inv, err := s.resolve(ctx, token)
	if err != nil {
		// A guess costs the address a strike; a failure of ours does not.
		if errors.Is(err, ErrInvalidInvite) {
			s.limiter.Fail(key)
		}
		return Preview{}, err
	}
	s.limiter.Reset(key)

	team, err := s.store.GetTeam(ctx, inv.TeamID)
	if err != nil {
		// The team is gone (the invite's FK cascades, so this is a race with a
		// team delete). The link is not usable; say the one thing we ever say.
		if errors.Is(err, store.ErrNotFound) {
			return Preview{}, ErrInvalidInvite
		}
		return Preview{}, err
	}
	exists := true
	if _, err := s.store.GetUserByEmail(ctx, inv.Email); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return Preview{}, fmt.Errorf("access: looking up %s: %w", inv.Email, err)
		}
		exists = false
	}
	return Preview{
		TeamName:      team.Name,
		InviterLabel:  inv.InvitedByLabel,
		Email:         inv.Email,
		Role:          inv.Role,
		ExpiresAt:     inv.ExpiresAt,
		AccountExists: exists,
	}, nil
}

// AcceptInput is what the landing form submits. Password is the invitee's
// CHOSEN password for an address the panel does not know, and their CURRENT
// password for one it does — never a reset of the latter (spec §4).
type AcceptInput struct {
	Password    string
	DisplayName string
	TOTPCode    string
}

// Accepted is a spent invitation: the session the invitee is now signed in
// with, the account it belongs to, and which team they joined.
type Accepted struct {
	Token    string
	User     domain.User
	Invite   domain.TeamInvite
	TeamName string
	// Created reports whether the account was made by this call, so the API can
	// answer 201 for a new account and 200 for a sign-in.
	Created bool
}

// Accept spends an invitation and returns a session.
//
// The order is load-bearing: authenticate (or validate) FIRST, spend SECOND,
// grant THIRD. Spending before the credential check would burn a valid
// invitation on a typo; granting before the spend would let a double-submit
// grant twice. The spend itself is one atomic UPDATE, so "used once" is the
// database's answer and not a read-then-write (spec §4).
func (s *Invites) Accept(ctx context.Context, token string, in AcceptInput, clientIP string) (Accepted, error) {
	key := inviteThrottleKey(clientIP)
	if err := s.throttle(key); err != nil {
		return Accepted{}, err
	}
	inv, err := s.resolve(ctx, token)
	if err != nil {
		// A guess costs the address a strike; a failure of ours does not.
		if errors.Is(err, ErrInvalidInvite) {
			s.limiter.Fail(key)
		}
		return Accepted{}, err
	}
	s.limiter.Reset(key)

	team, err := s.store.GetTeam(ctx, inv.TeamID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Accepted{}, ErrInvalidInvite
		}
		return Accepted{}, err
	}

	existing, err := s.store.GetUserByEmail(ctx, inv.Email)
	switch {
	case err == nil:
		return s.acceptAsExistingUser(ctx, inv, team, existing, in, clientIP)
	case errors.Is(err, store.ErrNotFound):
		return s.acceptAsNewUser(ctx, inv, team, in)
	default:
		return Accepted{}, fmt.Errorf("access: looking up %s: %w", inv.Email, err)
	}
}

// acceptAsExistingUser signs the invitee in with the account they already have
// and adds the membership. It NEVER changes that account's password: making an
// invitation able to reset one would turn "invite an address" into an
// account-takeover primitive for anyone who can invite (spec §1).
func (s *Invites) acceptAsExistingUser(ctx context.Context, inv domain.TeamInvite, team domain.Team, user domain.User, in AcceptInput, clientIP string) (Accepted, error) {
	token, user, err := s.auth.Login(ctx, inv.Email, in.Password, in.TOTPCode, clientIP)
	if err != nil {
		// auth's own errors travel unwrapped-by-value to the handler:
		// ErrTOTPRequired, ErrInvalidCredentials and a *RateLimitedError each
		// have a distinct answer on the sign-in screen, and this form is that
		// screen wearing a different title.
		return Accepted{}, err
	}
	// Between issuing and accepting, an admin may have added them by hand.
	// Refuse rather than silently re-rank — a demotion or promotion belongs to
	// the member-role route, which has the last-owner guard — and kill the
	// now-moot link so it cannot sit around for a week (spec §8).
	if _, err := s.store.GetTeamMember(ctx, inv.TeamID, user.ID); err == nil {
		if _, err := s.store.RevokeTeamInvite(ctx, inv.TeamID, inv.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Error("revoking moot invitation", "team_id", inv.TeamID, "invite_id", inv.ID, "error", err)
		}
		return Accepted{}, ErrAlreadyMember
	} else if !errors.Is(err, store.ErrNotFound) {
		return Accepted{}, fmt.Errorf("access: checking membership: %w", err)
	}

	spent, err := s.spend(ctx, inv.ID)
	if err != nil {
		return Accepted{}, err
	}
	if err := s.grant(ctx, spent, user); err != nil {
		return Accepted{}, err
	}
	return Accepted{Token: token, User: user, Invite: spent, TeamName: team.Name}, nil
}

// acceptAsNewUser creates the account the invitation was addressed to. The
// password is the invitee's own choice, held to the same floor the first-run
// screen enforces; the panel role is member, because an invitation grants a
// place in one TEAM and nothing panel-wide.
func (s *Invites) acceptAsNewUser(ctx context.Context, inv domain.TeamInvite, team domain.Team, in AcceptInput) (Accepted, error) {
	if len(in.Password) < minPasswordLen {
		return Accepted{}, invalid(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Accepted{}, fmt.Errorf("access: hashing password: %w", err)
	}
	spent, err := s.spend(ctx, inv.ID)
	if err != nil {
		return Accepted{}, err
	}
	user, err := s.store.CreateUser(ctx, ids.New(ids.PrefixUser), inv.Email, hash, domain.RoleMember)
	if err != nil {
		return Accepted{}, fmt.Errorf("access: creating account for %s: %w", inv.Email, err)
	}
	if name := strings.TrimSpace(in.DisplayName); name != "" {
		// Validated by the profile path rather than here, so one rule governs
		// what a display name may be. A rejected name must not cost the
		// account that already exists, so it is a 400 the invitee can fix from
		// their profile — never a failure of the join.
		updated, err := s.auth.UpdateProfile(ctx, user.ID, name, "")
		if err != nil {
			s.log.Warn("invitation display name rejected", "user_id", user.ID, "error", err)
		} else {
			user = updated
		}
	}
	if err := s.grant(ctx, spent, user); err != nil {
		return Accepted{}, err
	}
	token, err := s.auth.StartSession(ctx, user.ID)
	if err != nil {
		return Accepted{}, fmt.Errorf("access: starting session: %w", err)
	}
	return Accepted{Token: token, User: user, Invite: spent, TeamName: team.Name, Created: true}, nil
}

// spend is the atomic single-use guard. store.ErrNotFound here means somebody
// else got there first — the same answer an unknown token gets, so a race and a
// guess are indistinguishable from outside.
func (s *Invites) spend(ctx context.Context, id string) (domain.TeamInvite, error) {
	spent, err := s.store.AcceptTeamInvite(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.TeamInvite{}, ErrInvalidInvite
		}
		return domain.TeamInvite{}, err
	}
	return spent, nil
}

// grant adds the membership the invitation described and tells the inviter.
// The upsert is idempotent, so a retry after a partial failure converges rather
// than duplicating (ENGINEERING rule 12).
func (s *Invites) grant(ctx context.Context, inv domain.TeamInvite, user domain.User) error {
	if _, err := s.store.UpsertTeamMember(ctx, inv.TeamID, user.ID, inv.Role); err != nil {
		return fmt.Errorf("access: adding %s to team %s: %w", user.ID, inv.TeamID, err)
	}
	s.log.Info("invitation accepted",
		"team_id", inv.TeamID, "invite_id", inv.ID, "user_id", user.ID, "role", inv.Role)
	s.announceAccepted(ctx, inv, user)
	return nil
}

// announceAccepted is best-effort: the membership is real whether or not the
// inviter's bell learns about it, and failing the join over a notification
// would be the wrong trade.
func (s *Invites) announceAccepted(ctx context.Context, inv domain.TeamInvite, user domain.User) {
	if s.inbox == nil || inv.InvitedBy == nil {
		return
	}
	team, err := s.store.GetTeam(ctx, inv.TeamID)
	if err != nil {
		s.log.Error("naming team for invitation notice", "team_id", inv.TeamID, "error", err)
		return
	}
	if err := s.inbox.RecordInviteAccepted(ctx, inbox.InviteNotice{
		TeamID:    inv.TeamID,
		TeamName:  team.Name,
		InviteID:  inv.ID,
		InviterID: *inv.InvitedBy,
		Email:     user.Email,
		Role:      inv.Role,
	}); err != nil {
		s.log.Error("recording invitation-accepted item",
			"team_id", inv.TeamID, "invite_id", inv.ID, "error", err)
	}
}

// resolve turns a wire token into the invitation it names, or
// ErrInvalidInvite. The secret is compared in constant time BEFORE any state is
// read from the row's usability, so a wrong guess against a real id is
// indistinguishable from a guess at an id that does not exist.
func (s *Invites) resolve(ctx context.Context, token string) (domain.TeamInvite, error) {
	id, secret, ok := splitToken(token)
	if !ok {
		return domain.TeamInvite{}, ErrInvalidInvite
	}
	inv, err := s.store.GetTeamInvite(ctx, id)
	if err != nil {
		// Only "no such row" is the caller's problem. Collapsing a dead pool
		// into ErrInvalidInvite would tell a legitimate invitee their link is
		// spent, charge their address a strike for the panel's own outage, and
		// log nothing — so the one failure class that is ours is the one that
		// disappears. Wrap it instead and let the handler log it and answer 500
		// (ENGINEERING rule 3).
		if errors.Is(err, store.ErrNotFound) {
			return domain.TeamInvite{}, ErrInvalidInvite
		}
		// %q, not %s: the id half of the token is caller-supplied and this
		// string reaches the panel's own log, which `GET /panel/logs` hands to
		// a team owner — a quoted string cannot forge a log line.
		return domain.TeamInvite{}, fmt.Errorf("access: reading invitation %q: %w", id, err)
	}
	if !auth.ConstantTimeEqual(inv.TokenHash, auth.HashToken(secret)) {
		return domain.TeamInvite{}, ErrInvalidInvite
	}
	if !inv.Acceptable(s.now()) {
		return domain.TeamInvite{}, ErrInvalidInvite
	}
	return inv, nil
}

// throttle refuses a public call that is over budget, carrying the wait the
// sign-in screen already knows how to count down against.
func (s *Invites) throttle(key string) error {
	if s.limiter.Allow(key) {
		return nil
	}
	return &auth.RateLimitedError{
		RetryAfter:   s.limiter.RetryAfter(key),
		FirstRefusal: s.limiter.Refuse(key),
	}
}

// acceptURL is the link that goes in the mail and comes back once from the
// create response. The path is the unauthenticated landing route.
func (s *Invites) acceptURL(token string) string {
	return s.baseURL + "/invite/" + token
}

// inviteBody is the mail an invitee reads. Composed here, beside the token that
// authorises it, so a CLI would send the same words (CLAUDE.md rule 4).
func inviteBody(teamName string, inv domain.TeamInvite, acceptURL string) string {
	who := inv.InvitedByLabel
	if who == "" {
		who = "Someone"
	}
	return who + " invited you to join " + teamName + " on CypherPanel as " + inv.Role + ".\n\n" +
		"Open this link to accept:\n\n" + acceptURL + "\n\n" +
		"The link works once and expires in 7 days. If you were not expecting this, ignore it — nothing happens until you open it."
}

// splitToken splits `<id>.<secret>` — the same shape an email-change token
// uses, for the same reason: the id is public and indexable, the secret is not.
func splitToken(t string) (id, secret string, ok bool) {
	i := strings.LastIndexByte(t, '.')
	if i <= 0 || i == len(t)-1 {
		return "", "", false
	}
	return t[:i], t[i+1:], true
}

// minPasswordLen is the floor an invitee's chosen password must clear — the
// same one core/onboarding sets for the first owner and core/auth enforces on a
// rotation, so no path into the panel is weaker than another.
const minPasswordLen = 8

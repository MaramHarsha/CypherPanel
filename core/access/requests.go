package access

// Access requests (invitations-and-access-requests.md §2, §5, §6).
//
// The mirror image of an invitation: no secret, no bearer token, no expiry —
// the row IS the ask, and it grants nothing until a team owner answers it. The
// grant does not re-implement membership; it goes through the existing
// member-role path, so the last-owner guard and the grant-rank rule keep
// holding without being restated here.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// RequestStore is the persistence access requests need (consumer-defined;
// *store.Store satisfies it).
type RequestStore interface {
	GetTeam(ctx context.Context, id string) (domain.Team, error)
	CreateAccessRequest(ctx context.Context, r domain.AccessRequest) (domain.AccessRequest, error)
	GetAccessRequest(ctx context.Context, id string) (domain.AccessRequest, error)
	ListAccessRequests(ctx context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error)
	DecideAccessRequest(ctx context.Context, id, state, decidedBy, decidedByLabel, reason string) (domain.AccessRequest, error)
	ListTeamOwnerEmails(ctx context.Context, teamID string) ([]string, error)
}

// RoleChanger applies a granted request through the membership path that
// already owns the invariants (consumer-defined; *teams.Service satisfies it —
// spec §5).
type RoleChanger interface {
	ChangeMemberRole(ctx context.Context, teamID, userID, role, actorRole string) (domain.TeamMember, error)
}

// RequestAnnouncer is the inbox (consumer-defined; *inbox.Service satisfies
// it). nil records nothing, which is exactly what a panel with no inbox wired
// should do — the decision itself is unaffected.
type RequestAnnouncer interface {
	RecordAccessRequested(ctx context.Context, n inbox.AccessNotice) error
	RecordAccessGranted(ctx context.Context, n inbox.AccessNotice) error
	RecordAccessDenied(ctx context.Context, n inbox.AccessNotice) error
}

// Requests is the access-request surface. Construct with NewRequests.
type Requests struct {
	store  RequestStore
	roles  RoleChanger
	mailer Mailer
	inbox  RequestAnnouncer
	log    *slog.Logger
}

// NewRequests wires the service. mailer and announcer may be nil.
func NewRequests(st RequestStore, roles RoleChanger, mailer Mailer, announcer RequestAnnouncer, log *slog.Logger) *Requests {
	return &Requests{store: st, roles: roles, mailer: mailer, inbox: announcer, log: log}
}

// RequestInput is what a member submits from the 403 screen.
type RequestInput struct {
	RequestedRole string
	Message       string
}

// Create opens one request. actorRole is the caller's effective role in the
// team, already resolved by the route: a request must ask for something
// strictly above it, because "please give me the role I already have" is not a
// question an owner should have to answer.
func (s *Requests) Create(ctx context.Context, teamID string, actor domain.User, actorRole string, in RequestInput) (domain.AccessRequest, error) {
	role, err := roleOrDefault(in.RequestedRole)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	if domain.RoleRank(role) <= domain.RoleRank(actorRole) {
		return domain.AccessRequest{}, invalid("you already hold that role or higher on this team")
	}
	message, err := bounded("a message", in.Message, domain.AccessMessageMax)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	team, err := s.store.GetTeam(ctx, teamID)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	req, err := s.store.CreateAccessRequest(ctx, domain.AccessRequest{
		ID:            ids.New(ids.PrefixAccessRequest),
		TeamID:        teamID,
		UserID:        actor.ID,
		RequestedRole: role,
		Message:       message,
	})
	if err != nil {
		// The partial unique index is the authority on "one open request per
		// person per team", so the conflict comes from the database rather than
		// from a check that could race it.
		if errors.Is(err, store.ErrConflict) {
			return domain.AccessRequest{}, ErrRequestOpen
		}
		return domain.AccessRequest{}, err
	}
	s.log.Info("access requested",
		"team_id", teamID, "request_id", req.ID, "user_id", actor.ID, "requested_role", role)
	s.announce(ctx, req, team.Name, "", func(a RequestAnnouncer, n inbox.AccessNotice) error {
		return a.RecordAccessRequested(ctx, n)
	})
	s.mailOwners(ctx, req, team.Name)
	return req, nil
}

// List returns a team's requests; includeDecided false is the owners' queue.
func (s *Requests) List(ctx context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error) {
	return s.store.ListAccessRequests(ctx, teamID, includeDecided)
}

// Get returns one request. It is also the authorization resolver's entry point
// — the route needs the team before it can decide who may see this — so it
// stays a plain lookup.
func (s *Requests) Get(ctx context.Context, id string) (domain.AccessRequest, error) {
	return s.store.GetAccessRequest(ctx, id)
}

// Grant applies the request. actorRole is the caller's effective role in the
// team; the route already requires owner, and ChangeMemberRole checks it again
// against the rank being handed out — one rule, enforced where it lives.
//
// The role change goes FIRST: a request marked granted whose membership did not
// move would be a lie an owner has no way to spot.
func (s *Requests) Grant(ctx context.Context, id string, actor domain.User, actorRole string) (domain.AccessRequest, error) {
	req, err := s.pending(ctx, id)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	if req.CurrentRole == "" {
		return domain.AccessRequest{}, ErrNotMember
	}
	if _, err := s.roles.ChangeMemberRole(ctx, req.TeamID, req.UserID, req.RequestedRole, actorRole); err != nil {
		return domain.AccessRequest{}, err
	}
	decided, err := s.decide(ctx, req, domain.AccessRequestGranted, actor, "")
	if err != nil {
		// The membership DID move; the operator sees the error and can retry
		// the decision, and the request stays visibly pending meanwhile —
		// which is the honest state, not a silent success.
		return domain.AccessRequest{}, err
	}
	return decided, nil
}

// Deny refuses the request with an optional reason, and changes no membership.
func (s *Requests) Deny(ctx context.Context, id, reason string, actor domain.User) (domain.AccessRequest, error) {
	reason, err := bounded("a reason", reason, domain.AccessMessageMax)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	req, err := s.pending(ctx, id)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	return s.decide(ctx, req, domain.AccessRequestDenied, actor, reason)
}

// pending loads a request that may still be decided.
func (s *Requests) pending(ctx context.Context, id string) (domain.AccessRequest, error) {
	req, err := s.store.GetAccessRequest(ctx, id)
	if err != nil {
		return domain.AccessRequest{}, err
	}
	if req.State != domain.AccessRequestPending {
		return domain.AccessRequest{}, ErrDecided
	}
	return req, nil
}

// decide records the one decision a request may receive and tells the person
// who asked. store.ErrNotFound from the statement means another owner decided
// it in between — reported as ErrDecided, the same answer the pre-check gives.
func (s *Requests) decide(ctx context.Context, req domain.AccessRequest, state string, actor domain.User, reason string) (domain.AccessRequest, error) {
	decided, err := s.store.DecideAccessRequest(ctx, req.ID, state, actor.ID, actor.Email, reason)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.AccessRequest{}, ErrDecided
		}
		return domain.AccessRequest{}, err
	}
	s.log.Info("access request decided",
		"team_id", decided.TeamID, "request_id", decided.ID,
		"user_id", decided.UserID, "state", state, "decided_by", actor.ID)

	team, err := s.store.GetTeam(ctx, decided.TeamID)
	teamName := ""
	if err != nil {
		s.log.Error("naming team for access decision", "team_id", decided.TeamID, "error", err)
	} else {
		teamName = team.Name
	}
	s.announce(ctx, decided, teamName, actor.Email, func(a RequestAnnouncer, n inbox.AccessNotice) error {
		if state == domain.AccessRequestGranted {
			return a.RecordAccessGranted(ctx, n)
		}
		return a.RecordAccessDenied(ctx, n)
	})
	return decided, nil
}

// announce writes the inbox item for one request or decision. Best-effort: the
// decision is real whether or not a bell learns about it, and failing the call
// over a notification would be the wrong trade (deploy-protection.md §9 makes
// the same one).
func (s *Requests) announce(ctx context.Context, req domain.AccessRequest, teamName, actorEmail string, record func(RequestAnnouncer, inbox.AccessNotice) error) {
	if s.inbox == nil {
		return
	}
	if err := record(s.inbox, inbox.AccessNotice{
		TeamID:         req.TeamID,
		TeamName:       teamName,
		RequestID:      req.ID,
		RequesterID:    req.UserID,
		RequesterEmail: req.UserEmail,
		CurrentRole:    req.CurrentRole,
		RequestedRole:  req.RequestedRole,
		Message:        req.Message,
		ActorEmail:     actorEmail,
		Reason:         req.DecisionReason,
	}); err != nil {
		s.log.Error("recording access inbox item",
			"team_id", req.TeamID, "request_id", req.ID, "error", err)
	}
}

// mailOwners tells the people who can decide, through Panel Mail when one is
// configured. The recipients come from the membership join — a panel owner's
// implicit team-owner rank is an authorization escape hatch, never an opinion
// about whose mailbox should receive a team's business (spec §6).
func (s *Requests) mailOwners(ctx context.Context, req domain.AccessRequest, teamName string) {
	if s.mailer == nil {
		return
	}
	owners, err := s.store.ListTeamOwnerEmails(ctx, req.TeamID)
	if err != nil {
		s.log.Error("listing team owners for access mail", "team_id", req.TeamID, "error", err)
		return
	}
	send(ctx, s.mailer, s.log, owners,
		"Access request for "+teamName,
		requestBody(req, teamName),
		"team_id", req.TeamID, "request_id", req.ID)
}

// requestBody is the mail an owner reads: who asked, for what, in their own
// words, and where to answer it.
func requestBody(req domain.AccessRequest, teamName string) string {
	from := req.CurrentRole
	if from == "" {
		from = "no role"
	}
	body := fmt.Sprintf("%s asks to move from %s to %s on %s.",
		req.UserEmail, from, req.RequestedRole, teamName)
	if req.Message != "" {
		body += "\n\n" + req.Message
	}
	return body + "\n\nAnswer it in the panel under Settings → Teams."
}

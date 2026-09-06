package store

// Team invitations and access requests
// (invitations-and-access-requests.md §2). Domain types in, domain types out;
// pgx/pgtype stays inside this package.
//
// The two spend paths — accepting an invitation and deciding a request — are
// each ONE atomic statement, and both report "nothing to spend" as ErrNotFound.
// That is deliberate: a read-then-write would grant the same invitation twice
// under a double-submit, and would let two owners decide one request.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── Invitations ────────────────────────────────────────────────────────────

// CreateTeamInvite stores one invitation. tokenHash is sha256 of the wire
// secret; the secret itself never reaches this package.
func (s *Store) CreateTeamInvite(ctx context.Context, inv domain.TeamInvite) (domain.TeamInvite, error) {
	invitedBy := pgtype.Text{}
	if inv.InvitedBy != nil {
		invitedBy = pgtype.Text{String: *inv.InvitedBy, Valid: true}
	}
	row, err := s.q.CreateTeamInvite(ctx, db.CreateTeamInviteParams{
		ID:             inv.ID,
		TeamID:         inv.TeamID,
		Email:          inv.Email,
		Role:           inv.Role,
		TokenHash:      inv.TokenHash,
		InvitedBy:      invitedBy,
		InvitedByLabel: inv.InvitedByLabel,
		ExpiresAt:      pgtype.Timestamptz{Time: inv.ExpiresAt, Valid: true},
	})
	if err != nil {
		return domain.TeamInvite{}, wrapCreate("creating team invite", err)
	}
	return inviteFromRow(row), nil
}

// GetTeamInvite reads one invitation by id — the public half of its wire token.
// The returned TokenHash is what the caller compares, in constant time, against
// the hash of the secret it was presented (spec §2).
func (s *Store) GetTeamInvite(ctx context.Context, id string) (domain.TeamInvite, error) {
	row, err := s.q.GetTeamInvite(ctx, id)
	if err != nil {
		return domain.TeamInvite{}, wrap("getting team invite", err)
	}
	return inviteFromRow(row), nil
}

// ListTeamInvites returns a team's invitations, newest first. includeDecided
// false is the operator's "outstanding" view: only invites that can still be
// accepted, filtered in SQL so the page and any count of it agree.
func (s *Store) ListTeamInvites(ctx context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error) {
	rows, err := s.q.ListTeamInvites(ctx, db.ListTeamInvitesParams{TeamID: teamID, IncludeDecided: includeDecided})
	if err != nil {
		return nil, fmt.Errorf("store: listing team invites: %w", err)
	}
	out := make([]domain.TeamInvite, 0, len(rows))
	for _, r := range rows {
		out = append(out, inviteFromRow(r))
	}
	return out, nil
}

// RevokeTeamInvite revokes one live invitation of a team. ErrNotFound means it
// was already revoked, already accepted, or never belonged to this team — one
// undifferentiated answer, the same discipline the accept path uses.
func (s *Store) RevokeTeamInvite(ctx context.Context, teamID, id string) (domain.TeamInvite, error) {
	row, err := s.q.RevokeTeamInvite(ctx, db.RevokeTeamInviteParams{ID: id, TeamID: teamID})
	if err != nil {
		return domain.TeamInvite{}, wrap("revoking team invite", err)
	}
	return inviteFromRow(row), nil
}

// RevokeLiveTeamInvitesFor supersedes every outstanding invitation for one
// address on one team, and reports how many died. It is what makes re-inviting
// an address replace the link rather than collide with it (spec §2).
func (s *Store) RevokeLiveTeamInvitesFor(ctx context.Context, teamID, email string) (int64, error) {
	n, err := s.q.RevokeLiveTeamInvitesFor(ctx, db.RevokeLiveTeamInvitesForParams{TeamID: teamID, Email: email})
	if err != nil {
		return 0, fmt.Errorf("store: superseding team invites: %w", err)
	}
	return n, nil
}

// RevokeLiveTeamInvitesBy revokes the invitations one member issued for one
// team — called when that member is removed, so a removed admin keeps no
// 7-day back door in an envelope (spec §8).
func (s *Store) RevokeLiveTeamInvitesBy(ctx context.Context, teamID, userID string) (int64, error) {
	n, err := s.q.RevokeLiveTeamInvitesBy(ctx, db.RevokeLiveTeamInvitesByParams{
		TeamID:    teamID,
		InvitedBy: pgtype.Text{String: userID, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("store: revoking team invites by member: %w", err)
	}
	return n, nil
}

// AcceptTeamInvite spends an invitation. ErrNotFound is the authoritative
// answer for expired, revoked, and already-accepted alike: the predicate lives
// in the statement, so nothing can race between the check and the write.
func (s *Store) AcceptTeamInvite(ctx context.Context, id string) (domain.TeamInvite, error) {
	row, err := s.q.AcceptTeamInvite(ctx, id)
	if err != nil {
		return domain.TeamInvite{}, wrap("accepting team invite", err)
	}
	return inviteFromRow(row), nil
}

// ─── Access requests ────────────────────────────────────────────────────────

// CreateAccessRequest opens one request. A second open request for the same
// (team, user) violates the partial unique index and comes back as ErrConflict.
func (s *Store) CreateAccessRequest(ctx context.Context, r domain.AccessRequest) (domain.AccessRequest, error) {
	row, err := s.q.CreateAccessRequest(ctx, db.CreateAccessRequestParams{
		ID:            r.ID,
		TeamID:        r.TeamID,
		UserID:        r.UserID,
		RequestedRole: r.RequestedRole,
		Message:       r.Message,
	})
	if err != nil {
		return domain.AccessRequest{}, wrapCreate("creating access request", err)
	}
	// The insert returns the bare row; the two derived fields (the requester's
	// address and their current rank) come from the read model.
	return s.GetAccessRequest(ctx, row.ID)
}

// GetAccessRequest reads one request with its derived fields.
func (s *Store) GetAccessRequest(ctx context.Context, id string) (domain.AccessRequest, error) {
	row, err := s.q.GetAccessRequest(ctx, id)
	if err != nil {
		return domain.AccessRequest{}, wrap("getting access request", err)
	}
	return domain.AccessRequest{
		ID: row.ID, TeamID: row.TeamID, UserID: row.UserID,
		UserEmail: row.UserEmail, CurrentRole: row.CurrentRole,
		RequestedRole: row.RequestedRole, Message: row.Message, State: row.State,
		DecidedBy: ptrText(row.DecidedBy), DecidedByLabel: row.DecidedByLabel,
		DecisionReason: row.DecisionReason,
		DecidedAt:      ptrTime(row.DecidedAt), CreatedAt: row.CreatedAt.Time,
	}, nil
}

// ListAccessRequests returns a team's requests, newest first; includeDecided
// false is the owners' queue.
func (s *Store) ListAccessRequests(ctx context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error) {
	rows, err := s.q.ListAccessRequests(ctx, db.ListAccessRequestsParams{TeamID: teamID, IncludeDecided: includeDecided})
	if err != nil {
		return nil, fmt.Errorf("store: listing access requests: %w", err)
	}
	out := make([]domain.AccessRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.AccessRequest{
			ID: r.ID, TeamID: r.TeamID, UserID: r.UserID,
			UserEmail: r.UserEmail, CurrentRole: r.CurrentRole,
			RequestedRole: r.RequestedRole, Message: r.Message, State: r.State,
			DecidedBy: ptrText(r.DecidedBy), DecidedByLabel: r.DecidedByLabel,
			DecisionReason: r.DecisionReason,
			DecidedAt:      ptrTime(r.DecidedAt), CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

// DecideAccessRequest records the one decision a pending request may receive.
// ErrNotFound means it was already decided — which is what keeps two owners
// clicking at once from producing two outcomes.
func (s *Store) DecideAccessRequest(ctx context.Context, id, state, decidedBy, decidedByLabel, reason string) (domain.AccessRequest, error) {
	row, err := s.q.DecideAccessRequest(ctx, db.DecideAccessRequestParams{
		ID:             id,
		State:          state,
		DecidedBy:      pgtype.Text{String: decidedBy, Valid: decidedBy != ""},
		DecidedByLabel: decidedByLabel,
		DecisionReason: reason,
	})
	if err != nil {
		return domain.AccessRequest{}, wrap("deciding access request", err)
	}
	return s.GetAccessRequest(ctx, row.ID)
}

// ListTeamOwnerEmails is who a pending request is mailed to (spec §6).
func (s *Store) ListTeamOwnerEmails(ctx context.Context, teamID string) ([]string, error) {
	out, err := s.q.ListTeamOwnerEmails(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("store: listing team owner emails: %w", err)
	}
	return out, nil
}

// ─── mapping ────────────────────────────────────────────────────────────────

func inviteFromRow(r db.TeamInvite) domain.TeamInvite {
	return domain.TeamInvite{
		ID:             r.ID,
		TeamID:         r.TeamID,
		Email:          r.Email,
		Role:           r.Role,
		TokenHash:      r.TokenHash,
		InvitedBy:      ptrText(r.InvitedBy),
		InvitedByLabel: r.InvitedByLabel,
		ExpiresAt:      r.ExpiresAt.Time,
		AcceptedAt:     ptrTime(r.AcceptedAt),
		RevokedAt:      ptrTime(r.RevokedAt),
		CreatedAt:      r.CreatedAt.Time,
	}
}

package store

// Real-Postgres tests for invitations and access requests
// (invitations-and-access-requests.md §2). They prove the four things the
// SCHEMA — not the service — is responsible for:
//
//   - one LIVE invitation per (team, address), enforced by a partial unique
//     index rather than by a check that could race it;
//   - the single-use spend and the one-decision-per-request update, each a
//     single atomic statement whose predicate is the whole guard;
//   - the derived read model on an access request (the requester's address and
//     their CURRENT rank), so an owner's card cannot show a stale role;
//   - the team-scoped inbox rows and what leaving a team costs them.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// seedAccessTeam creates a team with an owner, an admin and a member.
func seedAccessTeam(t *testing.T, s *Store) (team domain.Team, owner, admin, member domain.User) {
	t.Helper()
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "acc-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	mk := func(label, role string) domain.User {
		u, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), label+"-"+ids.Secret()[:8]+"@example.test", "h", domain.RoleMember)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", label, err)
		}
		if _, err := s.UpsertTeamMember(ctx, team.ID, u.ID, role); err != nil {
			t.Fatalf("UpsertTeamMember(%s): %v", label, err)
		}
		return u
	}
	return team, mk("own", domain.RoleOwner), mk("adm", domain.RoleAdmin), mk("mem", domain.RoleMember)
}

func newInvite(teamID, email, role, inviterID, inviterLabel string, expires time.Time) domain.TeamInvite {
	return domain.TeamInvite{
		ID: ids.New(ids.PrefixTeamInvite), TeamID: teamID, Email: email, Role: role,
		TokenHash: []byte("hash-" + ids.Secret()[:8]),
		InvitedBy: &inviterID, InvitedByLabel: inviterLabel,
		ExpiresAt: expires,
	}
}

func TestStoreTeamInviteRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, _ := seedAccessTeam(t, s)
	email := "invitee-" + ids.Secret()[:8] + "@example.test"

	inv, err := s.CreateTeamInvite(ctx, newInvite(team.ID, email, domain.RoleAdmin, owner.ID, owner.Email, time.Now().Add(domain.InviteTTL)))
	if err != nil {
		t.Fatalf("CreateTeamInvite: %v", err)
	}
	got, err := s.GetTeamInvite(ctx, inv.ID)
	if err != nil {
		t.Fatalf("GetTeamInvite: %v", err)
	}
	if got.Email != email || got.Role != domain.RoleAdmin || got.InvitedByLabel != owner.Email {
		t.Fatalf("invite = %+v, want the address, role and inviter snapshot", got)
	}
	if got.InvitedBy == nil || *got.InvitedBy != owner.ID {
		t.Errorf("invited_by = %v, want %q", got.InvitedBy, owner.ID)
	}
	if !got.Acceptable(time.Now()) {
		t.Errorf("a fresh invitation is not acceptable: state %q", got.State(time.Now()))
	}

	// Pending-only is the default view; `all` adds everything.
	pending, err := s.ListTeamInvites(ctx, team.ID, false)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListTeamInvites(pending) = %d rows, %v", len(pending), err)
	}

	// The spend is single-use, enforced by the statement.
	spent, err := s.AcceptTeamInvite(ctx, inv.ID)
	if err != nil {
		t.Fatalf("AcceptTeamInvite: %v", err)
	}
	if spent.AcceptedAt == nil {
		t.Fatal("accepted_at not set by the spend")
	}
	if _, err := s.AcceptTeamInvite(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second spend: err = %v, want ErrNotFound", err)
	}
	if pending, err = s.ListTeamInvites(ctx, team.ID, false); err != nil || len(pending) != 0 {
		t.Fatalf("ListTeamInvites(pending) after acceptance = %d rows, %v", len(pending), err)
	}
	all, err := s.ListTeamInvites(ctx, team.ID, true)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListTeamInvites(all) = %d rows, %v", len(all), err)
	}
	if all[0].State(time.Now()) != domain.InviteStateAccepted {
		t.Errorf("state = %q, want accepted", all[0].State(time.Now()))
	}
}

// The partial unique index is the authority on "one live invitation per
// address": a second one collides, and superseding the first makes room.
func TestStoreOneLiveInvitePerAddress(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, _ := seedAccessTeam(t, s)
	email := "invitee-" + ids.Secret()[:8] + "@example.test"
	expires := time.Now().Add(domain.InviteTTL)

	first, err := s.CreateTeamInvite(ctx, newInvite(team.ID, email, domain.RoleMember, owner.ID, owner.Email, expires))
	if err != nil {
		t.Fatalf("first CreateTeamInvite: %v", err)
	}
	if _, err := s.CreateTeamInvite(ctx, newInvite(team.ID, email, domain.RoleMember, owner.ID, owner.Email, expires)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second live invitation: err = %v, want ErrConflict", err)
	}

	n, err := s.RevokeLiveTeamInvitesFor(ctx, team.ID, email)
	if err != nil || n != 1 {
		t.Fatalf("RevokeLiveTeamInvitesFor = %d, %v", n, err)
	}
	second, err := s.CreateTeamInvite(ctx, newInvite(team.ID, email, domain.RoleAdmin, owner.ID, owner.Email, expires))
	if err != nil {
		t.Fatalf("re-invite after superseding: %v", err)
	}
	// The superseded link is dead and cannot be spent.
	if _, err := s.AcceptTeamInvite(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("spending a superseded invitation: err = %v, want ErrNotFound", err)
	}
	if _, err := s.AcceptTeamInvite(ctx, second.ID); err != nil {
		t.Fatalf("spending the fresh invitation: %v", err)
	}
}

// Expiry lives in the statement, so a stale row cannot be spent even by a
// caller whose own clock disagrees.
func TestStoreExpiredInviteCannotBeSpent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, _ := seedAccessTeam(t, s)

	inv, err := s.CreateTeamInvite(ctx, newInvite(team.ID,
		"stale-"+ids.Secret()[:8]+"@example.test", domain.RoleMember, owner.ID, owner.Email,
		time.Now().Add(-time.Minute)))
	if err != nil {
		t.Fatalf("CreateTeamInvite: %v", err)
	}
	if _, err := s.AcceptTeamInvite(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("spending an expired invitation: err = %v, want ErrNotFound", err)
	}
	if got, _ := s.GetTeamInvite(ctx, inv.ID); got.State(time.Now()) != domain.InviteStateExpired {
		t.Errorf("state = %q, want expired", got.State(time.Now()))
	}
	// It is not "pending" to an operator either.
	pending, err := s.ListTeamInvites(ctx, team.ID, false)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ListTeamInvites(pending) = %d rows, %v — an expired invitation is not outstanding", len(pending), err)
	}
}

func TestStoreRevokeTeamInvite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, _ := seedAccessTeam(t, s)
	other, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "other-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	inv, err := s.CreateTeamInvite(ctx, newInvite(team.ID,
		"rev-"+ids.Secret()[:8]+"@example.test", domain.RoleMember, owner.ID, owner.Email,
		time.Now().Add(domain.InviteTTL)))
	if err != nil {
		t.Fatalf("CreateTeamInvite: %v", err)
	}
	// Another team cannot revoke it: the team id is part of the predicate, so
	// an invitation is not addressable across tenants.
	if _, err := s.RevokeTeamInvite(ctx, other.ID, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-team revoke: err = %v, want ErrNotFound", err)
	}
	if _, err := s.RevokeTeamInvite(ctx, team.ID, inv.ID); err != nil {
		t.Fatalf("RevokeTeamInvite: %v", err)
	}
	if _, err := s.RevokeTeamInvite(ctx, team.ID, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: err = %v, want ErrNotFound", err)
	}
	if _, err := s.AcceptTeamInvite(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("spending a revoked invitation: err = %v, want ErrNotFound", err)
	}
}

// Removing a member takes their outstanding invitations with them: an
// invitation outlives its issuer's session, but not their membership (§8).
func TestStoreRevokeInvitesByIssuer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, admin, _ := seedAccessTeam(t, s)
	expires := time.Now().Add(domain.InviteTTL)

	byAdmin, err := s.CreateTeamInvite(ctx, newInvite(team.ID,
		"a-"+ids.Secret()[:8]+"@example.test", domain.RoleMember, admin.ID, admin.Email, expires))
	if err != nil {
		t.Fatalf("CreateTeamInvite(admin): %v", err)
	}
	byOwner, err := s.CreateTeamInvite(ctx, newInvite(team.ID,
		"o-"+ids.Secret()[:8]+"@example.test", domain.RoleMember, owner.ID, owner.Email, expires))
	if err != nil {
		t.Fatalf("CreateTeamInvite(owner): %v", err)
	}

	n, err := s.RevokeLiveTeamInvitesBy(ctx, team.ID, admin.ID)
	if err != nil || n != 1 {
		t.Fatalf("RevokeLiveTeamInvitesBy = %d, %v, want exactly the admin's one", n, err)
	}
	if got, _ := s.GetTeamInvite(ctx, byAdmin.ID); got.RevokedAt == nil {
		t.Error("the removed admin's invitation is still live")
	}
	if got, _ := s.GetTeamInvite(ctx, byOwner.ID); got.RevokedAt != nil {
		t.Error("someone else's invitation was revoked too")
	}
}

// Deleting a team takes its invitations and requests with it — they are live
// state about that team, not evidence that has to outlive it (unlike an audit
// row, which deliberately carries no foreign key).
func TestStoreDeletingATeamCascades(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, member := seedAccessTeam(t, s)

	inv, err := s.CreateTeamInvite(ctx, newInvite(team.ID,
		"c-"+ids.Secret()[:8]+"@example.test", domain.RoleMember, owner.ID, owner.Email,
		time.Now().Add(domain.InviteTTL)))
	if err != nil {
		t.Fatalf("CreateTeamInvite: %v", err)
	}
	req, err := s.CreateAccessRequest(ctx, domain.AccessRequest{
		ID: ids.New(ids.PrefixAccessRequest), TeamID: team.ID, UserID: member.ID,
		RequestedRole: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	if err := s.DeleteTeam(ctx, team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := s.GetTeamInvite(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("invitation survived the team: %v", err)
	}
	if _, err := s.GetAccessRequest(ctx, req.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("access request survived the team: %v", err)
	}
}

func TestStoreAccessRequestRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, member := seedAccessTeam(t, s)

	req, err := s.CreateAccessRequest(ctx, domain.AccessRequest{
		ID: ids.New(ids.PrefixAccessRequest), TeamID: team.ID, UserID: member.ID,
		RequestedRole: domain.RoleAdmin, Message: "Need to deploy the import fix.",
	})
	if err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	// The two derived fields come from the join, not from the insert.
	if req.UserEmail != member.Email || req.CurrentRole != domain.RoleMember {
		t.Fatalf("request = %+v, want the requester's address and current rank", req)
	}
	if req.State != domain.AccessRequestPending {
		t.Errorf("state = %q, want pending", req.State)
	}

	// One OPEN request per (team, user).
	if _, err := s.CreateAccessRequest(ctx, domain.AccessRequest{
		ID: ids.New(ids.PrefixAccessRequest), TeamID: team.ID, UserID: member.ID,
		RequestedRole: domain.RoleOwner,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second open request: err = %v, want ErrConflict", err)
	}

	// One decision, whoever gets there first.
	decided, err := s.DecideAccessRequest(ctx, req.ID, domain.AccessRequestGranted, owner.ID, owner.Email, "")
	if err != nil {
		t.Fatalf("DecideAccessRequest: %v", err)
	}
	if decided.State != domain.AccessRequestGranted || decided.DecidedByLabel != owner.Email || decided.DecidedAt == nil {
		t.Fatalf("decided = %+v, want granted with the decider's snapshot", decided)
	}
	if _, err := s.DecideAccessRequest(ctx, req.ID, domain.AccessRequestDenied, owner.ID, owner.Email, "no"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second decision: err = %v, want ErrNotFound", err)
	}

	// The queue is pending-only; history needs `all`.
	open, err := s.ListAccessRequests(ctx, team.ID, false)
	if err != nil || len(open) != 0 {
		t.Fatalf("ListAccessRequests(pending) = %d rows, %v", len(open), err)
	}
	all, err := s.ListAccessRequests(ctx, team.ID, true)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAccessRequests(all) = %d rows, %v", len(all), err)
	}
	// Once decided, a new ask is allowed.
	if _, err := s.CreateAccessRequest(ctx, domain.AccessRequest{
		ID: ids.New(ids.PrefixAccessRequest), TeamID: team.ID, UserID: member.ID,
		RequestedRole: domain.RoleOwner,
	}); err != nil {
		t.Fatalf("asking again after a decision: %v", err)
	}
}

// current_role is read at DECISION time, not at ask time: a requester who has
// left reads as no role, which is what makes the service's 409 possible.
func TestStoreAccessRequestCurrentRoleIsLive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, _, _, member := seedAccessTeam(t, s)

	req, err := s.CreateAccessRequest(ctx, domain.AccessRequest{
		ID: ids.New(ids.PrefixAccessRequest), TeamID: team.ID, UserID: member.ID,
		RequestedRole: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	if err := s.DeleteTeamMember(ctx, team.ID, member.ID); err != nil {
		t.Fatalf("DeleteTeamMember: %v", err)
	}
	after, err := s.GetAccessRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("GetAccessRequest: %v", err)
	}
	if after.CurrentRole != "" {
		t.Fatalf("current_role = %q for someone who left, want empty", after.CurrentRole)
	}
}

func TestStoreListTeamOwnerEmails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, _, _ := seedAccessTeam(t, s)

	got, err := s.ListTeamOwnerEmails(ctx, team.ID)
	if err != nil {
		t.Fatalf("ListTeamOwnerEmails: %v", err)
	}
	if len(got) != 1 || got[0] != owner.Email {
		t.Fatalf("owner emails = %v, want exactly [%s] — membership is the join, not users.role", got, owner.Email)
	}
}

// The inbox's third scope: team items reach the ranks that can act, carry no
// project, and go when the member does.
func TestStoreTeamScopedInboxItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, owner, admin, member := seedAccessTeam(t, s)

	owners, err := s.ListTeamInboxRecipients(ctx, team.ID, domain.InboxKindAccessRequested, domain.RoleOwner)
	if err != nil {
		t.Fatalf("ListTeamInboxRecipients: %v", err)
	}
	if len(owners) != 1 || owners[0] != owner.ID {
		t.Fatalf("owner-rank recipients = %v, want [%s]", owners, owner.ID)
	}
	everyone, err := s.ListTeamInboxRecipients(ctx, team.ID, domain.InboxKindAccessRequested, domain.RoleMember)
	if err != nil || len(everyone) != 3 {
		t.Fatalf("member-rank recipients = %v (%v), want all three", everyone, err)
	}

	// A mute is honoured on the team scope like everywhere else.
	if _, err := s.SetInboxPreferences(ctx, admin.ID, []string{domain.InboxKindAccessRequested}); err != nil {
		t.Fatalf("SetInboxPreferences: %v", err)
	}
	everyone, err = s.ListTeamInboxRecipients(ctx, team.ID, domain.InboxKindAccessRequested, domain.RoleMember)
	if err != nil || len(everyone) != 2 {
		t.Fatalf("recipients after a mute = %v (%v), want two", everyone, err)
	}

	one, err := s.ListTeamInboxRecipientIfMember(ctx, team.ID, domain.InboxKindAccessGranted, member.ID)
	if err != nil || len(one) != 1 || one[0] != member.ID {
		t.Fatalf("single recipient = %v (%v), want [%s]", one, err, member.ID)
	}

	dedupe := domain.InboxKindAccessRequested + ":acr_test"
	fanout := InboxFanout{
		IDs: []string{ids.New(ids.PrefixInboxItem)}, UserIDs: []string{owner.ID},
		TeamID: team.ID, Kind: domain.InboxKindAccessRequested, Severity: "info",
		Title: "Access requested", Body: "b", Link: "/settings/teams",
		LinkLabel: "Open team settings", DedupeKey: dedupe,
	}
	if err := s.InsertTeamInboxItems(ctx, fanout); err != nil {
		t.Fatalf("InsertTeamInboxItems: %v", err)
	}
	// Redelivery is a no-op (ENGINEERING rule 12).
	fanout.IDs = []string{ids.New(ids.PrefixInboxItem)}
	if err := s.InsertTeamInboxItems(ctx, fanout); err != nil {
		t.Fatalf("InsertTeamInboxItems (again): %v", err)
	}
	items, err := s.ListInboxItems(ctx, owner.ID, false, 10)
	if err != nil {
		t.Fatalf("ListInboxItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("owner holds %d items, want 1", len(items))
	}
	if items[0].TeamID != team.ID || items[0].ProjectID != "" {
		t.Fatalf("item scope = team %q project %q, want the team and no project", items[0].TeamID, items[0].ProjectID)
	}

	// Leaving the team empties it: "never hold an item for a team you do not
	// belong to" holds for the team scope too.
	if err := s.DeleteInboxItemsForTeamMember(ctx, team.ID, owner.ID); err != nil {
		t.Fatalf("DeleteInboxItemsForTeamMember: %v", err)
	}
	if items, err = s.ListInboxItems(ctx, owner.ID, false, 10); err != nil || len(items) != 0 {
		t.Fatalf("items after leaving = %d (%v), want none", len(items), err)
	}
}

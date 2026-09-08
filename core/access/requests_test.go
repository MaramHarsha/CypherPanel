package access

// Access requests (invitations-and-access-requests.md §9, acceptance 8–9).
//
// The properties under test: an ask must be for something you do not have, one
// open ask per person per team, a grant goes through the member-role path
// rather than around it, and a decision is made exactly once.

import (
	"errors"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/teams"
)

func newRequests(t *testing.T) (*Requests, *fakeStore, *fakeRoles, *fakeMailer, *fakeAnnouncer) {
	t.Helper()
	st := newFakeStore()
	roles := &fakeRoles{store: st}
	mailer := &fakeMailer{}
	announcer := &fakeAnnouncer{}
	return NewRequests(st, roles, mailer, announcer, quietLog()), st, roles, mailer, announcer
}

// seedRequesters puts an owner and a member in tm_1 and returns them.
func seedRequesters(st *fakeStore) (owner, member domain.User) {
	owner = st.addUser("usr_sam", "sam@meridian.dev", domain.RoleMember)
	member = st.addUser("usr_priya", "priya@meridian.dev", domain.RoleMember)
	st.addMember("tm_1", owner.ID, domain.RoleOwner)
	st.addMember("tm_1", member.ID, domain.RoleMember)
	return owner, member
}

func TestCreateRequestTellsTheOwners(t *testing.T) {
	svc, st, _, mailer, announcer := newRequests(t)
	_, priya := seedRequesters(st)

	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{
		RequestedRole: domain.RoleAdmin,
		Message:       "Need to deploy the import fix to production.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if req.State != domain.AccessRequestPending {
		t.Errorf("state = %q, want pending", req.State)
	}
	if req.CurrentRole != domain.RoleMember || req.RequestedRole != domain.RoleAdmin {
		t.Errorf("request = %s → %s, want member → admin", req.CurrentRole, req.RequestedRole)
	}
	if req.UserEmail != "priya@meridian.dev" {
		t.Errorf("user_email = %q, derived at read time from the account", req.UserEmail)
	}
	if len(announcer.requested) != 1 || announcer.requested[0].RequestID != req.ID {
		t.Fatalf("inbox notices = %+v, want one for this request", announcer.requested)
	}
	// The mail goes to the OWNERS — the only rank that can decide it.
	if len(mailer.sent) != 1 || len(mailer.sent[0].To) != 1 || mailer.sent[0].To[0] != "sam@meridian.dev" {
		t.Fatalf("mail = %+v, want one message to the team's owner", mailer.sent)
	}
	if !strings.Contains(mailer.sent[0].Body, "import fix") {
		t.Errorf("mail body %q does not carry the requester's message", mailer.sent[0].Body)
	}
}

// You may only ask for something above what you hold: "give me the role I
// already have" is not a question an owner should have to answer.
func TestCreateRequestMustAskAboveYourRank(t *testing.T) {
	svc, st, _, _, _ := newRequests(t)
	_, priya := seedRequesters(st)

	var ve *ValidationError
	for _, role := range []string{domain.RoleMember, ""} {
		if _, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: role}); !errors.As(err, &ve) {
			t.Fatalf("asking for %q as a member: err = %v, want a ValidationError", role, err)
		}
	}
	if _, err := svc.Create(ctx(), "tm_1", priya, domain.RoleAdmin, RequestInput{RequestedRole: domain.RoleAdmin}); !errors.As(err, &ve) {
		t.Fatalf("an admin asking for admin: err = %v, want a ValidationError", err)
	}
}

func TestCreateRequestBoundsTheMessage(t *testing.T) {
	svc, st, _, _, _ := newRequests(t)
	_, priya := seedRequesters(st)

	var ve *ValidationError
	long := strings.Repeat("x", domain.AccessMessageMax+1)
	if _, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{
		RequestedRole: domain.RoleAdmin, Message: long,
	}); !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
}

// Acceptance 8: a second ask while one is open is a 409, so the owners' inbox
// does not fill with the same request.
func TestOneOpenRequestPerPersonPerTeam(t *testing.T) {
	svc, st, roles, _, _ := newRequests(t)
	sam, priya := seedRequesters(st)

	first, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin}); !errors.Is(err, ErrRequestOpen) {
		t.Fatalf("second Create: err = %v, want ErrRequestOpen", err)
	}
	// Once decided it is history, and asking again is allowed.
	if _, err := svc.Deny(ctx(), first.ID, "not yet", sam); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if _, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin}); err != nil {
		t.Fatalf("asking again after a decision: %v", err)
	}
	if len(roles.calls) != 0 {
		t.Errorf("a denial changed a membership: %v", roles.calls)
	}
}

// Acceptance 9: granting moves the membership THROUGH the member-role path, so
// the last-owner guard and the grant-rank rule apply without being restated.
func TestGrantGoesThroughTheMemberRolePath(t *testing.T) {
	svc, st, roles, _, announcer := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	granted, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if granted.State != domain.AccessRequestGranted {
		t.Errorf("state = %q, want granted", granted.State)
	}
	if granted.DecidedByLabel != "sam@meridian.dev" || granted.DecidedAt == nil {
		t.Errorf("decision = %q at %v, want the decider's snapshot and a timestamp", granted.DecidedByLabel, granted.DecidedAt)
	}
	want := [4]string{"tm_1", priya.ID, domain.RoleAdmin, domain.RoleOwner}
	if len(roles.calls) != 1 || roles.calls[0] != want {
		t.Fatalf("member-role calls = %v, want exactly one %v", roles.calls, want)
	}
	if got := st.role("tm_1", priya.ID); got != domain.RoleAdmin {
		t.Errorf("membership = %q, want admin", got)
	}
	if len(announcer.granted) != 1 || announcer.granted[0].ActorEmail != "sam@meridian.dev" {
		t.Fatalf("granted notices = %+v, want one naming the decider", announcer.granted)
	}
}

// A refused role change must leave the request pending: a request marked
// granted whose membership did not move would be a lie an owner cannot spot.
func TestGrantThatCannotMoveTheMembershipStaysPending(t *testing.T) {
	svc, st, roles, _, announcer := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	roles.err = teams.ErrLastOwner

	if _, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner); !errors.Is(err, teams.ErrLastOwner) {
		t.Fatalf("err = %v, want the membership path's error", err)
	}
	after, err := svc.Get(ctx(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.State != domain.AccessRequestPending {
		t.Errorf("state = %q, want it still pending", after.State)
	}
	if len(announcer.granted) != 0 {
		t.Errorf("the requester was told about a grant that did not happen: %+v", announcer.granted)
	}
}

// A requester who has left the team is not silently re-added: leaving is a
// decision, and an old ask must not undo it.
func TestGrantRefusesWhenTheRequesterHasLeft(t *testing.T) {
	svc, st, _, _, _ := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st.mu.Lock()
	delete(st.members, "tm_1/"+priya.ID)
	st.mu.Unlock()

	if _, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner); !errors.Is(err, ErrNotMember) {
		t.Fatalf("err = %v, want ErrNotMember", err)
	}
}

// One decision per request, whichever verb gets there first.
func TestARequestIsDecidedOnce(t *testing.T) {
	svc, st, _, _, announcer := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"grant again", func() error { _, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner); return err }},
		{"deny after granting", func() error { _, err := svc.Deny(ctx(), req.ID, "", sam); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrDecided) {
				t.Fatalf("err = %v, want ErrDecided", err)
			}
		})
	}
	if len(announcer.granted) != 1 {
		t.Errorf("granted notices = %d, want exactly one", len(announcer.granted))
	}
}

func TestDenyCarriesTheReasonAndChangesNoMembership(t *testing.T) {
	svc, st, roles, _, announcer := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	denied, err := svc.Deny(ctx(), req.ID, "  Ask again after the audit.  ", sam)
	if err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if denied.State != domain.AccessRequestDenied {
		t.Errorf("state = %q, want denied", denied.State)
	}
	if denied.DecisionReason != "Ask again after the audit." {
		t.Errorf("reason = %q, want it trimmed and verbatim", denied.DecisionReason)
	}
	if got := st.role("tm_1", priya.ID); got != domain.RoleMember {
		t.Errorf("membership = %q, want it unchanged", got)
	}
	if len(roles.calls) != 0 {
		t.Errorf("a denial called the member-role path: %v", roles.calls)
	}
	if len(announcer.denied) != 1 || announcer.denied[0].Reason != "Ask again after the audit." {
		t.Fatalf("denied notices = %+v, want one carrying the reason", announcer.denied)
	}
}

func TestDenyBoundsTheReason(t *testing.T) {
	svc, st, _, _, _ := newRequests(t)
	sam, priya := seedRequesters(st)
	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var ve *ValidationError
	if _, err := svc.Deny(ctx(), req.ID, strings.Repeat("x", domain.AccessMessageMax+1), sam); !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
}

// A panel with no inbox and no mail still decides requests: notification is a
// courtesy, not a precondition.
func TestRequestsWorkWithNoInboxOrMail(t *testing.T) {
	st := newFakeStore()
	roles := &fakeRoles{store: st}
	svc := NewRequests(st, roles, nil, nil, quietLog())
	sam, priya := seedRequesters(st)

	req, err := svc.Create(ctx(), "tm_1", priya, domain.RoleMember, RequestInput{RequestedRole: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Grant(ctx(), req.ID, sam, domain.RoleOwner); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if got := st.role("tm_1", priya.ID); got != domain.RoleAdmin {
		t.Errorf("membership = %q, want admin", got)
	}
}

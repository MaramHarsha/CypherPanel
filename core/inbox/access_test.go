package inbox

// The team-access audiences (invitations-and-access-requests.md §6). Three
// rules are the point, and none of them is something the fake can fake away:
//
//   - a request reaches only the team's OWNERS, because they are the only rank
//     that may decide it — an item you cannot act on is noise;
//   - a decision reaches only the person who ASKED;
//   - an accepted invitation reaches only the member who SENT it, and nobody at
//     all when that account is gone.

import (
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// seedAccessTeam puts three members of one team at three ranks.
func seedAccessTeam(st *fakeStore) {
	st.teamMembers["tm_1"] = []string{"usr_owner", "usr_admin", "usr_priya"}
	st.roles["usr_owner"] = domain.RoleOwner
	st.roles["usr_admin"] = domain.RoleAdmin
	st.roles["usr_priya"] = domain.RoleMember
}

func accessNotice() AccessNotice {
	return AccessNotice{
		TeamID:         "tm_1",
		TeamName:       "meridian studio",
		RequestID:      "acr_9f2abcd",
		RequesterID:    "usr_priya",
		RequesterEmail: "priya@meridian.dev",
		CurrentRole:    domain.RoleMember,
		RequestedRole:  domain.RoleAdmin,
		Message:        "Need to deploy the import fix to production.",
	}
}

func TestAccessRequestReachesOnlyOwners(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	svc := New(st, quietLog())

	if err := svc.RecordAccessRequested(context.Background(), accessNotice()); err != nil {
		t.Fatalf("RecordAccessRequested: %v", err)
	}
	if got := len(st.items["usr_owner"]); got != 1 {
		t.Fatalf("owner items = %d, want 1", got)
	}
	for _, uid := range []string{"usr_admin", "usr_priya"} {
		if got := len(st.items[uid]); got != 0 {
			t.Errorf("%s items = %d, want 0 — only owners decide a request", uid, got)
		}
	}
	it := st.items["usr_owner"][0]
	if it.TeamID != "tm_1" || it.ProjectID != "" {
		t.Errorf("item scope = team %q project %q, want team tm_1 and no project", it.TeamID, it.ProjectID)
	}
	if it.Kind != domain.InboxKindAccessRequested {
		t.Errorf("kind = %q, want %q", it.Kind, domain.InboxKindAccessRequested)
	}
	// The body must let an owner decide without opening anything: who, the
	// move being asked for, and their own words.
	for _, want := range []string{"priya@meridian.dev", "member → admin", "meridian studio", "import fix"} {
		if !strings.Contains(it.Body, want) {
			t.Errorf("body %q missing %q", it.Body, want)
		}
	}
	if it.Link != "/settings/teams" {
		t.Errorf("link = %q, want the in-panel team settings path", it.Link)
	}
}

func TestAccessDecisionsReachOnlyTheRequester(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record func(*Service, AccessNotice) error
		kind   string
		want   string
	}{
		{"granted", func(s *Service, n AccessNotice) error {
			return s.RecordAccessGranted(context.Background(), n)
		}, domain.InboxKindAccessGranted, "Granted admin by sam@meridian.dev"},
		{"denied", func(s *Service, n AccessNotice) error {
			return s.RecordAccessDenied(context.Background(), n)
		}, domain.InboxKindAccessDenied, "Denied by sam@meridian.dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			seedAccessTeam(st)
			svc := New(st, quietLog())
			n := accessNotice()
			n.ActorEmail = "sam@meridian.dev"
			n.Reason = "Ask again after the audit."

			if err := tc.record(svc, n); err != nil {
				t.Fatalf("record: %v", err)
			}
			if got := len(st.items["usr_priya"]); got != 1 {
				t.Fatalf("requester items = %d, want 1", got)
			}
			for _, uid := range []string{"usr_owner", "usr_admin"} {
				if got := len(st.items[uid]); got != 0 {
					t.Errorf("%s items = %d, want 0 — a decision is the requester's news", uid, got)
				}
			}
			it := st.items["usr_priya"][0]
			if it.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", it.Kind, tc.kind)
			}
			if !strings.Contains(it.Body, tc.want) {
				t.Errorf("body %q missing %q", it.Body, tc.want)
			}
		})
	}
}

// A denial's reason is carried verbatim; a grant has none to carry.
func TestDeniedItemCarriesTheReason(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	svc := New(st, quietLog())
	n := accessNotice()
	n.ActorEmail = "sam@meridian.dev"
	n.Reason = "Ask again after the audit."

	if err := svc.RecordAccessDenied(context.Background(), n); err != nil {
		t.Fatalf("RecordAccessDenied: %v", err)
	}
	if body := st.items["usr_priya"][0].Body; !strings.Contains(body, "Reason: Ask again after the audit.") {
		t.Fatalf("body %q does not carry the reason", body)
	}
}

// Redelivery is a no-op: the dedupe key is (kind, request), so granting the
// same request twice leaves one item (ENGINEERING rule 12).
func TestAccessItemsAreIdempotent(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	svc := New(st, quietLog())

	for range 3 {
		if err := svc.RecordAccessRequested(context.Background(), accessNotice()); err != nil {
			t.Fatalf("RecordAccessRequested: %v", err)
		}
	}
	if got := len(st.items["usr_owner"]); got != 1 {
		t.Fatalf("owner items after three identical records = %d, want 1", got)
	}
}

// A member who muted the kind is not a recipient — the same rule every other
// fan-out obeys.
func TestMutedAccessKindIsNotDelivered(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	st.prefs["usr_owner"] = []string{domain.InboxKindAccessRequested}
	svc := New(st, quietLog())

	if err := svc.RecordAccessRequested(context.Background(), accessNotice()); err != nil {
		t.Fatalf("RecordAccessRequested: %v", err)
	}
	if got := len(st.items["usr_owner"]); got != 0 {
		t.Fatalf("muted owner items = %d, want 0", got)
	}
}

func TestInviteAcceptedReachesOnlyTheInviter(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	svc := New(st, quietLog())

	n := InviteNotice{
		TeamID: "tm_1", TeamName: "meridian studio", InviteID: "inv_1",
		InviterID: "usr_admin", Email: "new@meridian.dev", Role: domain.RoleMember,
	}
	if err := svc.RecordInviteAccepted(context.Background(), n); err != nil {
		t.Fatalf("RecordInviteAccepted: %v", err)
	}
	if got := len(st.items["usr_admin"]); got != 1 {
		t.Fatalf("inviter items = %d, want 1", got)
	}
	for _, uid := range []string{"usr_owner", "usr_priya"} {
		if got := len(st.items[uid]); got != 0 {
			t.Errorf("%s items = %d, want 0", uid, got)
		}
	}
	if body := st.items["usr_admin"][0].Body; !strings.Contains(body, "new@meridian.dev joined meridian studio as member") {
		t.Fatalf("body %q does not say who joined as what", body)
	}
}

// An invitation issued by an account that has since been deleted has nobody to
// tell, and must not fan out to the team instead.
func TestInviteAcceptedWithNoInviterTellsNobody(t *testing.T) {
	st := newFakeStore()
	seedAccessTeam(st)
	svc := New(st, quietLog())

	if err := svc.RecordInviteAccepted(context.Background(), InviteNotice{
		TeamID: "tm_1", TeamName: "meridian studio", InviteID: "inv_1", Email: "new@meridian.dev",
	}); err != nil {
		t.Fatalf("RecordInviteAccepted: %v", err)
	}
	for uid, rows := range st.items {
		if len(rows) != 0 {
			t.Fatalf("%s received %d items, want none", uid, len(rows))
		}
	}
}

// Every team-access kind is in the taxonomy a preference list may name —
// otherwise muting one would be a 400 and the item could never be turned off.
func TestAccessKindsAreMutable(t *testing.T) {
	for _, k := range []string{
		domain.InboxKindAccessRequested,
		domain.InboxKindAccessGranted,
		domain.InboxKindAccessDenied,
		domain.InboxKindInviteAccepted,
	} {
		if !domain.ValidInboxKind(k) {
			t.Errorf("%q is not a valid inbox kind", k)
		}
		found := false
		for _, listed := range AvailableKinds() {
			if listed == k {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is missing from the served taxonomy", k)
		}
	}
}

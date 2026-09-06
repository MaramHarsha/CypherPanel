package inbox

// The deploy-protection audiences (deploy-protection.md §9). Two rules are the
// point, and neither is something the fake can fake away:
//
//   - the awaiting-approval item reaches only the members who could ACT on it
//     (rank >= required_role), because an item you cannot act on in front of
//     you is noise;
//   - the decision items reach only the person who ASKED, and a webhook deploy
//     — which nobody asked for — produces none at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func protectionNotice() DeployNotice {
	return DeployNotice{
		ProjectID:       "prj_1",
		ApplicationID:   "app_web",
		ApplicationName: "web",
		Commit:          "c99d2e1a1b2c3d4e5f60718293a4b5c6d7e8f900",
		DeploymentID:    "dep_9f2abcd",
		RequiredRole:    domain.RoleOwner,
		RequestedBy:     "usr_alex",
		RequesterEmail:  "alex@acme.com",
	}
}

// seedTeam puts three members of one team at three ranks.
func seedTeam(st *fakeStore) {
	st.members["prj_1"] = []string{"usr_owner", "usr_admin", "usr_alex"}
	st.roles["usr_owner"] = domain.RoleOwner
	st.roles["usr_admin"] = domain.RoleAdmin
	st.roles["usr_alex"] = domain.RoleMember
}

func TestRecordDeployAwaitingApprovalReachesOnlyApprovers(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	seedTeam(st)

	if err := s.RecordDeployAwaitingApproval(ctx, protectionNotice()); err != nil {
		t.Fatalf("RecordDeployAwaitingApproval: %v", err)
	}
	if got := len(st.items["usr_owner"]); got != 1 {
		t.Fatalf("owner items = %d, want 1", got)
	}
	if got := len(st.items["usr_admin"]); got != 0 {
		t.Fatalf("an admin was told about an owner-only approval (%d items)", got)
	}
	if got := len(st.items["usr_alex"]); got != 0 {
		t.Fatalf("a member was told about an owner-only approval (%d items)", got)
	}

	it := st.items["usr_owner"][0]
	if it.Kind != domain.InboxKindDeployAwaitingApproval {
		t.Fatalf("kind = %q", it.Kind)
	}
	// Severity info, not error: a deploy waiting for a person is the control
	// working, not a fault.
	if it.Severity != domain.NotifyInfo {
		t.Fatalf("severity = %q, want info", it.Severity)
	}
	if it.Digest {
		t.Fatal("an awaiting-approval item was digested; it must be immediate")
	}
	if !strings.Contains(it.Title, "dep_9f2") {
		t.Fatalf("title does not name the deployment: %q", it.Title)
	}
	// The body reads without a lookup: which app, which commit, who asked.
	for _, want := range []string{"web", "c99d2e1", "requested by alex@acme.com"} {
		if !strings.Contains(it.Body, want) {
			t.Fatalf("body %q is missing %q", it.Body, want)
		}
	}
	if it.Link != "/projects/prj_1/applications/app_web/deployments?dep=dep_9f2abcd" {
		t.Fatalf("link = %q", it.Link)
	}

	// Redelivery is a no-op (ENGINEERING rule 12).
	if err := s.RecordDeployAwaitingApproval(ctx, protectionNotice()); err != nil {
		t.Fatalf("second record: %v", err)
	}
	if got := len(st.items["usr_owner"]); got != 1 {
		t.Fatalf("owner items after redelivery = %d, want 1", got)
	}
}

// A lower required role widens the audience — the rank is a floor, not a match.
func TestAwaitingApprovalAudienceFollowsTheRequiredRole(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	seedTeam(st)

	n := protectionNotice()
	n.RequiredRole = domain.RoleAdmin
	if err := s.RecordDeployAwaitingApproval(ctx, n); err != nil {
		t.Fatalf("RecordDeployAwaitingApproval: %v", err)
	}
	if len(st.items["usr_owner"]) != 1 || len(st.items["usr_admin"]) != 1 {
		t.Fatalf("admins-and-up audience = owner %d, admin %d",
			len(st.items["usr_owner"]), len(st.items["usr_admin"]))
	}
	if len(st.items["usr_alex"]) != 0 {
		t.Fatal("a plain member was told about an admin-or-above approval")
	}
}

// A muted kind is still a mute: preferences apply to these items like any
// other.
func TestAwaitingApprovalRespectsMutes(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	seedTeam(st)
	st.prefs["usr_owner"] = []string{domain.InboxKindDeployAwaitingApproval}

	if err := s.RecordDeployAwaitingApproval(ctx, protectionNotice()); err != nil {
		t.Fatalf("RecordDeployAwaitingApproval: %v", err)
	}
	if got := len(st.items["usr_owner"]); got != 0 {
		t.Fatalf("a muted owner received %d items", got)
	}
}

// An unknown required role must never WIDEN the audience: it falls back to the
// narrowest rank in the set.
func TestAwaitingApprovalFallsBackToTheNarrowestRole(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	seedTeam(st)

	n := protectionNotice()
	n.RequiredRole = "root"
	if err := s.RecordDeployAwaitingApproval(ctx, n); err != nil {
		t.Fatalf("RecordDeployAwaitingApproval: %v", err)
	}
	if len(st.items["usr_owner"]) != 1 {
		t.Fatalf("owner items = %d, want 1", len(st.items["usr_owner"]))
	}
	if len(st.items["usr_admin"]) != 0 || len(st.items["usr_alex"]) != 0 {
		t.Fatal("a corrupt required role widened the audience")
	}
}

// The decision goes to the requester and nobody else, and it names the decider
// — and, for a rejection, the reason.
func TestRecordDeployDecisionReachesTheRequesterOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("approved", func(t *testing.T) {
		s, st := newService(t)
		seedTeam(st)
		n := protectionNotice()
		n.ActorEmail = "sam@acme.com"
		if err := s.RecordDeployApproved(ctx, n); err != nil {
			t.Fatalf("RecordDeployApproved: %v", err)
		}
		if len(st.items["usr_alex"]) != 1 {
			t.Fatalf("requester items = %d, want 1", len(st.items["usr_alex"]))
		}
		if len(st.items["usr_owner"]) != 0 || len(st.items["usr_admin"]) != 0 {
			t.Fatal("a decision reached someone who did not ask for the deploy")
		}
		it := st.items["usr_alex"][0]
		if it.Kind != domain.InboxKindDeployApproved {
			t.Fatalf("kind = %q", it.Kind)
		}
		if !strings.Contains(it.Body, "Approved by sam@acme.com") {
			t.Fatalf("body does not name the decider: %q", it.Body)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		s, st := newService(t)
		seedTeam(st)
		n := protectionNotice()
		n.ActorEmail = "sam@acme.com"
		n.Reason = "shipping Monday"
		if err := s.RecordDeployRejected(ctx, n); err != nil {
			t.Fatalf("RecordDeployRejected: %v", err)
		}
		it := st.items["usr_alex"][0]
		if it.Kind != domain.InboxKindDeployRejected {
			t.Fatalf("kind = %q", it.Kind)
		}
		for _, want := range []string{"Rejected by sam@acme.com", "shipping Monday"} {
			if !strings.Contains(it.Body, want) {
				t.Fatalf("body %q is missing %q", it.Body, want)
			}
		}
	})

	// A webhook deploy has no requester: there is nobody to tell, and the
	// write is a silent no-op rather than a fan-out to the whole team.
	t.Run("webhook deploy tells nobody", func(t *testing.T) {
		s, st := newService(t)
		seedTeam(st)
		n := protectionNotice()
		n.RequestedBy, n.RequesterEmail = "", ""
		if err := s.RecordDeployApproved(ctx, n); err != nil {
			t.Fatalf("RecordDeployApproved: %v", err)
		}
		for _, u := range []string{"usr_owner", "usr_admin", "usr_alex"} {
			if len(st.items[u]) != 0 {
				t.Fatalf("%s received an item for a deploy nobody asked for", u)
			}
		}
	})

	// A requester who has left the team holds no item for it — the rule the
	// whole inbox obeys.
	t.Run("ex-member is not told", func(t *testing.T) {
		s, st := newService(t)
		st.members["prj_1"] = []string{"usr_owner"}
		st.roles["usr_owner"] = domain.RoleOwner
		if err := s.RecordDeployApproved(ctx, protectionNotice()); err != nil {
			t.Fatalf("RecordDeployApproved: %v", err)
		}
		if len(st.items["usr_alex"]) != 0 {
			t.Fatal("an ex-member received an item for their old team's project")
		}
	})
}

// A notice with nothing to address is a silent no-op, not an error and not a
// half-written row.
func TestDeployNoticesIgnoreIncompleteInput(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	seedTeam(st)

	for _, tc := range []struct {
		name string
		n    DeployNotice
	}{
		{"no project", DeployNotice{DeploymentID: "dep_1", RequiredRole: domain.RoleOwner}},
		{"no deployment", DeployNotice{ProjectID: "prj_1", RequiredRole: domain.RoleOwner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.RecordDeployAwaitingApproval(ctx, tc.n); err != nil {
				t.Fatalf("RecordDeployAwaitingApproval: %v", err)
			}
			if len(st.items["usr_owner"]) != 0 {
				t.Fatalf("an incomplete notice wrote %d items", len(st.items["usr_owner"]))
			}
		})
	}
}

// The rendered line degrades honestly when a piece is missing: an image-source
// app has no commit, and a notice with neither name nor commit falls back to
// the deployment's short id.
func TestDeployNoticeBodyDegrades(t *testing.T) {
	if got := describeDeploy(DeployNotice{ApplicationName: "web", DeploymentID: "dep_abc1234"}); got != "web" {
		t.Fatalf("no commit = %q, want \"web\"", got)
	}
	if got := describeDeploy(DeployNotice{DeploymentID: "dep_abc1234xyz"}); got != "dep_abc1234" {
		t.Fatalf("bare deployment = %q, want the short id", got)
	}
	// A ref that is not a SHA is left alone rather than truncated to gibberish.
	n := DeployNotice{ApplicationName: "web", Commit: "ghcr.io/acme/web:1.4.0"}
	if got := describeDeploy(n); !strings.HasSuffix(got, "ghcr.io/acme/web:1.4.0") {
		t.Fatalf("image reference = %q, want it intact", got)
	}
}

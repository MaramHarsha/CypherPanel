package store

// Real-Postgres tests for deploy-protection persistence (deploy-protection.md
// §2). They prove the four things the schema — not the service — is responsible
// for: wholesale replacement of the protection document, the one-decision-per-
// deployment key, the rank-narrowed approver count, and grant expiry.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// seedProtectedEnv creates a team, a project and an environment, plus a member
// at each rank, and returns what the protection tests address.
func seedProtectedEnv(t *testing.T, s *Store) (team domain.Team, proj domain.Project, env domain.Environment, owner, admin, member domain.User) {
	t.Helper()
	ctx := context.Background()

	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "prot-"+ids.Secret()[:8])
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
	owner, admin, member = mk("own", domain.RoleOwner), mk("adm", domain.RoleAdmin), mk("mem", domain.RoleMember)

	proj, env, err = s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject),
		"prot-proj-"+ids.Secret()[:8], team.ID, projSlug("prot"), ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}
	return team, proj, env, owner, admin, member
}

func window(envID string, startDOW time.Weekday, startMin int, endDOW time.Weekday, endMin int, tz string) domain.FreezeWindow {
	return domain.FreezeWindow{
		ID: ids.New(ids.PrefixFreezeWindow), EnvironmentID: envID,
		StartDOW: startDOW, StartMinute: startMin,
		EndDOW: endDOW, EndMinute: endMin, Timezone: tz,
	}
}

// The document is desired state: a PUT replaces the flags AND the whole window
// list, so a window dropped from the request is gone from the database.
func TestStoreEnvironmentProtectionIsWholesale(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, env, _, _, _ := seedProtectedEnv(t, s)

	// An environment that has never been protected has no row: the service
	// turns that into the default document rather than a 404.
	if _, err := s.GetEnvironmentProtection(ctx, env.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unprotected environment = %v, want ErrNotFound", err)
	}

	first, err := s.SetEnvironmentProtection(ctx, domain.EnvironmentProtection{
		EnvironmentID:   env.ID,
		RequireApproval: true,
		MinApproverRole: domain.RoleOwner,
		FreezeEnabled:   true,
		Windows: []domain.FreezeWindow{
			window(env.ID, time.Friday, 18*60, time.Monday, 8*60, "Europe/Berlin"),
			window(env.ID, time.Wednesday, 0, time.Wednesday, 60, "UTC"),
		},
	})
	if err != nil {
		t.Fatalf("SetEnvironmentProtection: %v", err)
	}
	if len(first.Windows) != 2 || !first.RequireApproval || !first.FreezeEnabled {
		t.Fatalf("first write = %+v", first)
	}

	// Replace with one window and approval off. The dropped window must not
	// survive: a partial write here is exactly the state an operator cannot
	// reason about.
	second, err := s.SetEnvironmentProtection(ctx, domain.EnvironmentProtection{
		EnvironmentID:   env.ID,
		RequireApproval: false,
		MinApproverRole: domain.RoleAdmin,
		FreezeEnabled:   true,
		Windows:         []domain.FreezeWindow{window(env.ID, time.Saturday, 0, time.Sunday, 0, "UTC")},
	})
	if err != nil {
		t.Fatalf("SetEnvironmentProtection(replace): %v", err)
	}
	if len(second.Windows) != 1 || second.Windows[0].StartDOW != time.Saturday {
		t.Fatalf("replace left %+v", second.Windows)
	}
	if second.RequireApproval || second.MinApproverRole != domain.RoleAdmin {
		t.Fatalf("flags not replaced: %+v", second)
	}

	read, err := s.GetEnvironmentProtection(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetEnvironmentProtection: %v", err)
	}
	if len(read.Windows) != 1 || read.Windows[0].Timezone != "UTC" {
		t.Fatalf("read back %+v", read)
	}

	// Clearing the calendar is a first-class request, not a delete.
	cleared, err := s.SetEnvironmentProtection(ctx, domain.EnvironmentProtection{
		EnvironmentID: env.ID, MinApproverRole: domain.RoleOwner,
	})
	if err != nil {
		t.Fatalf("SetEnvironmentProtection(clear): %v", err)
	}
	if len(cleared.Windows) != 0 {
		t.Fatalf("cleared windows = %+v", cleared.Windows)
	}
}

// A window that starts and ends at the same moment is either empty or the whole
// week and no reader can tell which, so the column refuses it.
func TestStoreFreezeWindowRejectsDegenerate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, env, _, _, _ := seedProtectedEnv(t, s)

	_, err := s.SetEnvironmentProtection(ctx, domain.EnvironmentProtection{
		EnvironmentID:   env.ID,
		MinApproverRole: domain.RoleOwner,
		FreezeEnabled:   true,
		Windows:         []domain.FreezeWindow{window(env.ID, time.Friday, 18*60, time.Friday, 18*60, "UTC")},
	})
	if err == nil {
		t.Fatal("a zero-length window was accepted")
	}
}

// One gate decision per deployment, and it is once-only: the second decision
// finds no pending row and comes back ErrConflict, which is the 409.
func TestStoreDeployApprovalDecidesOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, owner, _, member := seedProtectedEnv(t, s)
	srv, _, env, app := seedApp(t, s)
	_ = srv

	rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "abc", []byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	dep, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "manual")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	ap, err := s.CreateDeployApproval(ctx, dep.ID, env.ID, member.ID, domain.RoleOwner)
	if err != nil {
		t.Fatalf("CreateDeployApproval: %v", err)
	}
	if ap.State != domain.ApprovalPending || ap.RequiredRole != domain.RoleOwner {
		t.Fatalf("new approval = %+v", ap)
	}
	// A second approval on the same deployment is impossible by key.
	if _, err := s.CreateDeployApproval(ctx, dep.ID, env.ID, member.ID, domain.RoleOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("second approval = %v, want ErrConflict", err)
	}

	got, err := s.GetDeployApproval(ctx, dep.ID)
	if err != nil {
		t.Fatalf("GetDeployApproval: %v", err)
	}
	if got.RequestedByEmail != member.Email {
		t.Fatalf("requested_by_email = %q, want %q", got.RequestedByEmail, member.Email)
	}
	if got.DecidedAt != nil {
		t.Fatalf("pending approval carries decided_at %v", got.DecidedAt)
	}

	decided, err := s.DecideDeployApproval(ctx, dep.ID, domain.ApprovalRejected, owner.ID, "shipping Monday")
	if err != nil {
		t.Fatalf("DecideDeployApproval: %v", err)
	}
	if decided.State != domain.ApprovalRejected || decided.DecidedByEmail != owner.Email ||
		decided.Reason != "shipping Monday" || decided.DecidedAt == nil {
		t.Fatalf("decided = %+v", decided)
	}

	// Decisions are once-only: the update matches no pending row.
	if _, err := s.DecideDeployApproval(ctx, dep.ID, domain.ApprovalApproved, owner.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second decision = %v, want ErrConflict", err)
	}
	// A decision on a deployment that was never gated is a not-found, not a
	// conflict — the two answer different status codes.
	if _, err := s.DecideDeployApproval(ctx, "dep_nope", domain.ApprovalApproved, owner.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decision on an ungated deployment = %v, want ErrNotFound", err)
	}

	// The per-application batch read is what the Deployments tab uses, and it
	// answers for the page it is handed: an id that is not on the page is not
	// in the answer, which is what keeps the read model bounded by the response
	// rather than by the application's whole deploy history.
	byDep, err := s.ListDeployApprovalsByApplication(ctx, app.ID, []string{dep.ID})
	if err != nil {
		t.Fatalf("ListDeployApprovalsByApplication: %v", err)
	}
	if got := byDep[dep.ID]; got.State != domain.ApprovalRejected {
		t.Fatalf("batch read = %+v", byDep)
	}
	off, err := s.ListDeployApprovalsByApplication(ctx, app.ID, []string{"dep_not_on_this_page"})
	if err != nil || len(off) != 0 {
		t.Fatalf("off-page read = %+v, %v; want empty", off, err)
	}
	if none, err := s.ListDeployApprovalsByApplication(ctx, app.ID, nil); err != nil || len(none) != 0 {
		t.Fatalf("empty page = %+v, %v; want empty", none, err)
	}

	// The environment queue filters by state, and "" means every state.
	pending, err := s.ListDeployApprovalsByEnvironment(ctx, env.ID, domain.ApprovalPending, 100)
	if err != nil {
		t.Fatalf("ListDeployApprovalsByEnvironment(pending): %v", err)
	}
	for _, a := range pending {
		if a.DeploymentID == dep.ID {
			t.Fatal("a rejected approval showed up in the pending queue")
		}
	}
	all, err := s.ListDeployApprovalsByEnvironment(ctx, env.ID, "", 100)
	if err != nil {
		t.Fatalf("ListDeployApprovalsByEnvironment(all): %v", err)
	}
	// The listing is bounded: `state=all` on a long-lived environment is a
	// growing history, and the limit is what keeps it a response.
	if bounded, err := s.ListDeployApprovalsByEnvironment(ctx, env.ID, "", 0); err != nil || len(bounded) != 0 {
		t.Fatalf("limit 0 returned %d rows (%v) — the bound is not applied", len(bounded), err)
	}
	found := false
	for _, a := range all {
		if a.DeploymentID == dep.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unfiltered queue omitted the decided approval: %+v", all)
	}
}

// A webhook deploy has no panel user behind it, so requested_by is NULL — and
// deleting a requester must not delete the record of what they asked for.
func TestStoreDeployApprovalSurvivesItsRequester(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, _, _, member := seedProtectedEnv(t, s)
	_, _, env, app := seedApp(t, s)

	mkApproval := func(requestedBy string) domain.DeployApproval {
		t.Helper()
		rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "abc", []byte(`{}`))
		if err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
		dep, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "webhook")
		if err != nil {
			t.Fatalf("CreateDeployment: %v", err)
		}
		ap, err := s.CreateDeployApproval(ctx, dep.ID, env.ID, requestedBy, domain.RoleOwner)
		if err != nil {
			t.Fatalf("CreateDeployApproval: %v", err)
		}
		return ap
	}

	pushed := mkApproval("")
	if got, err := s.GetDeployApproval(ctx, pushed.DeploymentID); err != nil || got.RequestedBy != "" {
		t.Fatalf("webhook approval requested_by = %q, %v; want empty", got.RequestedBy, err)
	}

	asked := mkApproval(member.ID)
	if err := s.DeleteUser(ctx, member.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	got, err := s.GetDeployApproval(ctx, asked.DeploymentID)
	if err != nil {
		t.Fatalf("approval vanished with its requester: %v", err)
	}
	if got.RequestedBy != "" || got.RequestedByEmail != "" {
		t.Fatalf("deleted requester still named: %+v", got)
	}
}

// The approver count is what lifts the no-self-approval rule for a solo
// operator: it counts team members at or above a rank, excluding one user.
func TestStoreCountQualifiedApprovers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, _, owner, admin, member := seedProtectedEnv(t, s)

	for _, tc := range []struct {
		name    string
		minRole string
		exclude string
		want    int64
	}{
		{"owners besides the owner", domain.RoleOwner, owner.ID, 0},
		{"owners besides a member", domain.RoleOwner, member.ID, 1},
		{"admins-and-up besides the admin", domain.RoleAdmin, admin.ID, 1},
		{"admins-and-up besides a member", domain.RoleAdmin, member.ID, 2},
		{"everyone besides the owner", domain.RoleMember, owner.ID, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := s.CountQualifiedApprovers(ctx, proj.ID, tc.minRole, tc.exclude)
			if err != nil {
				t.Fatalf("CountQualifiedApprovers: %v", err)
			}
			if n != tc.want {
				t.Fatalf("count = %d, want %d", n, tc.want)
			}
		})
	}
}

// A grant suspends the freeze until it lapses, and lapses on its own — the
// query answers against the caller's clock, not the database's.
func TestStoreBreakGlassGrantExpires(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, env, owner, _, _ := seedProtectedEnv(t, s)

	now := time.Now()
	g, err := s.CreateBreakGlassGrant(ctx, domain.BreakGlassGrant{
		ID:            ids.New(ids.PrefixBreakGlass),
		EnvironmentID: env.ID,
		OpenedBy:      owner.ID,
		Reason:        "checkout returning 500s",
		ExpiresAt:     now.Add(domain.BreakGlassTTL),
	})
	if err != nil {
		t.Fatalf("CreateBreakGlassGrant: %v", err)
	}

	open, err := s.BreakGlassOpen(ctx, env.ID, now)
	if err != nil || !open {
		t.Fatalf("BreakGlassOpen(now) = %v, %v; want true", open, err)
	}
	// One second past its lifetime it is closed: nothing revokes it, it lapses.
	open, err = s.BreakGlassOpen(ctx, env.ID, now.Add(domain.BreakGlassTTL+time.Second))
	if err != nil || open {
		t.Fatalf("BreakGlassOpen(after TTL) = %v, %v; want false", open, err)
	}

	list, err := s.ListBreakGlassGrants(ctx, env.ID, 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListBreakGlassGrants = %+v, %v", list, err)
	}
	if list[0].ID != g.ID || list[0].OpenedByEmail != owner.Email ||
		list[0].Reason != "checkout returning 500s" {
		t.Fatalf("grant listing = %+v", list[0])
	}
	// An empty reason cannot be stored: an unrecorded override is not a
	// recorded one.
	if _, err := s.CreateBreakGlassGrant(ctx, domain.BreakGlassGrant{
		ID: ids.New(ids.PrefixBreakGlass), EnvironmentID: env.ID,
		OpenedBy: owner.ID, ExpiresAt: now.Add(time.Minute),
	}); err == nil {
		t.Fatal("a grant with no reason was accepted")
	}

	// The grant is append-only: what it says happened, happened. Deleting the
	// person who opened it must not erase the record of the override — the same
	// choice deploy_approvals makes for its requester, and the shape the read
	// query already assumes (LEFT JOIN users, address COALESCEd to '').
	if err := s.DeleteUser(ctx, owner.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	list, err = s.ListBreakGlassGrants(ctx, env.ID, 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("grant vanished with its opener: %+v, %v", list, err)
	}
	if list[0].ID != g.ID || list[0].OpenedBy != "" || list[0].OpenedByEmail != "" {
		t.Fatalf("deleted opener still named: %+v", list[0])
	}
	if list[0].Reason != "checkout returning 500s" {
		t.Fatalf("the override lost its reason: %+v", list[0])
	}
	// And it still suspends the freeze: the override outlives the account.
	if open, err := s.BreakGlassOpen(ctx, env.ID, now); err != nil || !open {
		t.Fatalf("BreakGlassOpen after deleting the opener = %v, %v; want true", open, err)
	}
}

// A parked deployment holds no pipeline slot: it is excluded from both queue
// queries, so it can neither block a later deploy nor be resumed by Recover.
func TestStoreParkedDeploymentLeavesTheQueue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	rev, err := s.CreateRevision(ctx, ids.New(ids.PrefixRevision), app.ID, "abc", []byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	dep, err := s.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), app.ID, rev.ID, "manual")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	// Queued, it is in the queue.
	active, err := s.ListActiveDeploymentsByApplication(ctx, app.ID)
	if err != nil || len(active) != 1 {
		t.Fatalf("queued deployment not active: %+v %v", active, err)
	}

	parked, err := s.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployAwaitingApproval, "waiting for approval from an owner")
	if err != nil {
		t.Fatalf("UpdateDeploymentStatus: %v", err)
	}
	if parked.Status != domain.DeployAwaitingApproval || parked.Status.Terminal() {
		t.Fatalf("parked = %+v; awaiting_approval must not be terminal", parked)
	}
	if parked.FinishedAt != nil {
		t.Fatalf("parking stamped finished_at %v — a parked deploy has not finished", parked.FinishedAt)
	}

	if active, err = s.ListActiveDeploymentsByApplication(ctx, app.ID); err != nil || len(active) != 0 {
		t.Fatalf("parked deployment still holds a queue slot: %+v %v", active, err)
	}
	all, err := s.ListActiveDeployments(ctx)
	if err != nil {
		t.Fatalf("ListActiveDeployments: %v", err)
	}
	for _, d := range all {
		if d.ID == dep.ID {
			t.Fatal("Recover would have tried to resume a parked deployment")
		}
	}

	// Approving puts it back in the ordinary queue.
	if _, err := s.UpdateDeploymentStatus(ctx, dep.ID, domain.DeployQueued, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if active, err = s.ListActiveDeploymentsByApplication(ctx, app.ID); err != nil || len(active) != 1 {
		t.Fatalf("approved deployment did not re-enter the queue: %+v %v", active, err)
	}
}

// The inbox audience for a parked deploy is rank-narrowed: an item nobody in
// front of it can act on is noise, and "never hold an item for a team you do
// not belong to" still has to hold, so membership stays the join. Both queries
// carry the rank ladder and the mute filter against a LEFT JOIN that is NULL
// for a user with no preferences row, which is the part worth proving against
// real Postgres rather than against a fake that agrees with itself.
func TestStoreApprovalInboxAudience(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, _, owner, admin, member := seedProtectedEnv(t, s)
	kind := domain.InboxKindDeployAwaitingApproval

	// Someone with an account but no membership of this project's team must
	// never be resolved, at any rank.
	stranger, err := s.CreateUser(ctx, ids.New(ids.PrefixUser),
		"stranger-"+ids.Secret()[:8]+"@example.test", "h", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(stranger): %v", err)
	}

	for _, tc := range []struct {
		name    string
		minRole string
		want    []string
	}{
		{"owners only", domain.RoleOwner, []string{owner.ID}},
		{"admins and up", domain.RoleAdmin, []string{owner.ID, admin.ID}},
		{"everyone", domain.RoleMember, []string{owner.ID, admin.ID, member.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListApprovalInboxRecipients(ctx, proj.ID, kind, tc.minRole)
			if err != nil {
				t.Fatalf("ListApprovalInboxRecipients: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("audience = %v, want %v", got, tc.want)
			}
			for _, id := range tc.want {
				if !containsString(got, id) {
					t.Fatalf("audience = %v, missing %s", got, id)
				}
			}
			if containsString(got, stranger.ID) {
				t.Fatalf("a non-member was addressed: %v", got)
			}
		})
	}

	// A muted kind drops exactly the muter, and nobody else: the mute lives on
	// a LEFT JOIN, so a member with no preferences row must survive it.
	if _, err := s.SetInboxPreferences(ctx, admin.ID, []string{kind}); err != nil {
		t.Fatalf("SetInboxPreferences: %v", err)
	}
	got, err := s.ListApprovalInboxRecipients(ctx, proj.ID, kind, domain.RoleAdmin)
	if err != nil {
		t.Fatalf("ListApprovalInboxRecipients (muted): %v", err)
	}
	if len(got) != 1 || got[0] != owner.ID {
		t.Fatalf("audience with the admin muted = %v, want only the owner", got)
	}
	// The mute is per KIND, not per user: another protection kind still reaches
	// the same person.
	if got, err = s.ListApprovalInboxRecipients(ctx, proj.ID, domain.InboxKindDeployApproved, domain.RoleAdmin); err != nil ||
		!containsString(got, admin.ID) {
		t.Fatalf("muting one kind silenced another: %v, %v", got, err)
	}

	// A decision is news for the requester alone — and only while they are
	// still a member and have not muted it.
	one, err := s.ListInboxRecipientIfMember(ctx, proj.ID, domain.InboxKindDeployApproved, member.ID)
	if err != nil || len(one) != 1 || one[0] != member.ID {
		t.Fatalf("requester lookup = %v, %v; want just the requester", one, err)
	}
	if none, err := s.ListInboxRecipientIfMember(ctx, proj.ID, domain.InboxKindDeployApproved, stranger.ID); err != nil || len(none) != 0 {
		t.Fatalf("a non-member requester resolved to %v, %v; want nobody", none, err)
	}
	if _, err := s.SetInboxPreferences(ctx, member.ID, []string{domain.InboxKindDeployApproved}); err != nil {
		t.Fatalf("SetInboxPreferences(member): %v", err)
	}
	if none, err := s.ListInboxRecipientIfMember(ctx, proj.ID, domain.InboxKindDeployApproved, member.ID); err != nil || len(none) != 0 {
		t.Fatalf("a muted requester resolved to %v, %v; want nobody", none, err)
	}
	// A webhook deploy has no requester at all, and an empty id must resolve to
	// nobody rather than to everybody.
	if none, err := s.ListInboxRecipientIfMember(ctx, proj.ID, domain.InboxKindDeployApproved, ""); err != nil || len(none) != 0 {
		t.Fatalf("an empty requester resolved to %v, %v; want nobody", none, err)
	}
}

package teams

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore is an in-memory Store: teams, memberships (by team+user), and
// users, enough to exercise every invariant without Postgres.
type fakeStore struct {
	teams    map[string]domain.Team
	members  map[string]map[string]domain.TeamMember // teamID -> userID -> member
	users    map[string]domain.User                  // by id
	byEmail  map[string]domain.User
	projects map[string]int // teamID -> project count
	// clearedInboxes is "<teamID>/<userID>" per inbox sweep, in call order.
	clearedInboxes []string
	revokedInvites []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		teams:    map[string]domain.Team{"tm_1": {ID: "tm_1", Name: "acme"}},
		members:  map[string]map[string]domain.TeamMember{"tm_1": {}},
		users:    map[string]domain.User{},
		byEmail:  map[string]domain.User{},
		projects: map[string]int{},
	}
}

func (f *fakeStore) addUser(id, email, role string) domain.User {
	u := domain.User{ID: id, Email: email, Role: role}
	f.users[id] = u
	f.byEmail[email] = u
	return u
}

func (f *fakeStore) addMember(teamID, userID, role string) {
	if f.members[teamID] == nil {
		f.members[teamID] = map[string]domain.TeamMember{}
	}
	f.members[teamID][userID] = domain.TeamMember{TeamID: teamID, UserID: userID, Role: role}
}

func (f *fakeStore) CreateTeam(_ context.Context, id, name string) (domain.Team, error) {
	t := domain.Team{ID: id, Name: name}
	f.teams[id] = t
	f.members[id] = map[string]domain.TeamMember{}
	return t, nil
}
func (f *fakeStore) GetTeam(_ context.Context, id string) (domain.Team, error) {
	t, ok := f.teams[id]
	if !ok {
		return domain.Team{}, store.ErrNotFound
	}
	return t, nil
}
func (f *fakeStore) ListTeams(context.Context) ([]domain.Team, error) {
	var out []domain.Team
	for _, t := range f.teams {
		out = append(out, t)
	}
	return out, nil
}
func (f *fakeStore) RenameTeam(_ context.Context, id, name string) (domain.Team, error) {
	t := f.teams[id]
	t.Name = name
	f.teams[id] = t
	return t, nil
}
func (f *fakeStore) DeleteTeam(_ context.Context, id string) error { delete(f.teams, id); return nil }
func (f *fakeStore) CountTeamProjects(_ context.Context, teamID string) (int64, error) {
	return int64(f.projects[teamID]), nil
}
func (f *fakeStore) UpsertTeamMember(_ context.Context, teamID, userID, role string) (domain.TeamMember, error) {
	f.addMember(teamID, userID, role)
	return f.members[teamID][userID], nil
}
func (f *fakeStore) GetTeamMember(_ context.Context, teamID, userID string) (domain.TeamMember, error) {
	m, ok := f.members[teamID][userID]
	if !ok {
		return domain.TeamMember{}, store.ErrNotFound
	}
	return m, nil
}
func (f *fakeStore) ListTeamMembers(_ context.Context, teamID string) ([]domain.TeamMember, error) {
	var out []domain.TeamMember
	for _, m := range f.members[teamID] {
		out = append(out, m)
	}
	return out, nil
}
func (f *fakeStore) DeleteTeamMember(_ context.Context, teamID, userID string) error {
	delete(f.members[teamID], userID)
	return nil
}

// clearedInboxes records the (team, user) pairs RemoveMember swept, so the test
// can assert that leaving a team empties that team's items from the ex-member's
// inbox (notification-inbox.md §4).
func (f *fakeStore) DeleteInboxItemsForTeamMember(_ context.Context, teamID, userID string) error {
	f.clearedInboxes = append(f.clearedInboxes, teamID+"/"+userID)
	return nil
}

// revokedInvites records the (team, issuer) pairs RemoveMember swept: an
// invitation outlives its issuer's session, but not their membership
// (invitations-and-access-requests.md §8).
func (f *fakeStore) RevokeLiveTeamInvitesBy(_ context.Context, teamID, userID string) (int64, error) {
	f.revokedInvites = append(f.revokedInvites, teamID+"/"+userID)
	return 1, nil
}
func (f *fakeStore) CountTeamOwners(_ context.Context, teamID string) (int64, error) {
	var n int64
	for _, m := range f.members[teamID] {
		if m.Role == domain.RoleOwner {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) ListTeamsByUser(_ context.Context, userID string) ([]domain.TeamWithRole, error) {
	var out []domain.TeamWithRole
	for tid, ms := range f.members {
		if m, ok := ms[userID]; ok {
			out = append(out, domain.TeamWithRole{Team: f.teams[tid], Role: m.Role})
		}
	}
	return out, nil
}
func (f *fakeStore) GetTeamRoleForProject(context.Context, string, string) (string, error) {
	return "", store.ErrNotFound
}
func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	u, ok := f.byEmail[email]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}
func (f *fakeStore) CreateUser(_ context.Context, id, email, _, role string) (domain.User, error) {
	return f.addUser(id, email, role), nil
}
func (f *fakeStore) ListUsers(context.Context) ([]domain.User, error) {
	var out []domain.User
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, nil
}
func (f *fakeStore) UpdateUserRole(_ context.Context, id, role string) (domain.User, error) {
	u := f.users[id]
	u.Role = role
	f.users[id] = u
	return u, nil
}
func (f *fakeStore) DeleteUser(_ context.Context, id string) error { delete(f.users, id); return nil }

func ctx() context.Context { return context.Background() }

// ─── Role resolution + panel-owner bypass ───────────────────────────────────

func TestPanelOwnerBypassesTeamMembership(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	owner := domain.User{ID: "usr_po", Role: domain.RoleOwner} // panel owner, no membership row
	role, err := svc.RoleInTeam(ctx(), owner, "tm_1")
	if err != nil || role != domain.RoleOwner {
		t.Fatalf("panel owner role = %q, %v; want owner", role, err)
	}
}

func TestNonMemberHasNoRole(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	stranger := domain.User{ID: "usr_x", Role: domain.RoleMember}
	role, err := svc.RoleInTeam(ctx(), stranger, "tm_1")
	if err != nil || role != "" {
		t.Fatalf("stranger role = %q, %v; want empty", role, err)
	}
}

// ─── Grant rules (no self-service escalation) ───────────────────────────────

func TestAdminCannotGrantOwner(t *testing.T) {
	fs := newFakeStore()
	fs.addUser("usr_b", "b@acme.com", domain.RoleMember)
	svc := NewService(fs)
	_, err := svc.AddMember(ctx(), "tm_1", "b@acme.com", domain.RoleOwner, domain.RoleAdmin)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin granting owner: err = %v, want ErrForbidden", err)
	}
}

func TestAdminGrantsMember(t *testing.T) {
	fs := newFakeStore()
	fs.addUser("usr_b", "b@acme.com", domain.RoleMember)
	svc := NewService(fs)
	m, err := svc.AddMember(ctx(), "tm_1", "b@acme.com", domain.RoleMember, domain.RoleAdmin)
	if err != nil || m.Role != domain.RoleMember {
		t.Fatalf("admin granting member: %+v, %v", m, err)
	}
}

func TestAdminCannotRemoveOwner(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	fs.addMember("tm_1", "usr_owner2", domain.RoleOwner) // so the last-owner guard is not what trips
	svc := NewService(fs)
	err := svc.RemoveMember(ctx(), "tm_1", "usr_owner", domain.RoleAdmin)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin removing owner: err = %v, want ErrForbidden", err)
	}
}

// Acceptance 7 of notification-inbox.md §8: leaving a team empties that team's
// items from the ex-member's inbox. The rule is "never hold an item for a team
// you do not belong to", and a stale title naming a project you were just
// removed from breaks it as surely as a live delivery would.
func TestRemoveMemberClearsTheirInboxForThatTeam(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	fs.addMember("tm_1", "usr_member", domain.RoleMember)
	svc := NewService(fs)

	if err := svc.RemoveMember(ctx(), "tm_1", "usr_member", domain.RoleAdmin); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if len(fs.clearedInboxes) != 1 || fs.clearedInboxes[0] != "tm_1/usr_member" {
		t.Fatalf("inbox sweeps = %v, want exactly [tm_1/usr_member]", fs.clearedInboxes)
	}
	// …and the invitations they issued for that team die with the membership
	// (invitations-and-access-requests.md §8): otherwise a removed admin keeps
	// up to seven days of live links in other people's mailboxes.
	if len(fs.revokedInvites) != 1 || fs.revokedInvites[0] != "tm_1/usr_member" {
		t.Fatalf("invitation sweeps = %v, want exactly [tm_1/usr_member]", fs.revokedInvites)
	}
}

// A removal that is refused must not touch anyone's inbox — the guard runs
// first, so nothing has changed to clean up.
func TestRefusedRemovalLeavesTheInboxAlone(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	svc := NewService(fs)

	if err := svc.RemoveMember(ctx(), "tm_1", "usr_owner", domain.RoleOwner); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing last owner: err = %v, want ErrLastOwner", err)
	}
	if len(fs.clearedInboxes) != 0 {
		t.Fatalf("inbox swept on a refused removal: %v", fs.clearedInboxes)
	}
	if len(fs.revokedInvites) != 0 {
		t.Fatalf("invitations revoked on a refused removal: %v", fs.revokedInvites)
	}
}

// ─── Last-owner guard ───────────────────────────────────────────────────────

func TestCannotDemoteLastOwner(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	svc := NewService(fs)
	_, err := svc.ChangeMemberRole(ctx(), "tm_1", "usr_owner", domain.RoleAdmin, domain.RoleOwner)
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting last owner: err = %v, want ErrLastOwner", err)
	}
}

func TestCanDemoteOwnerWhenAnotherRemains(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	fs.addMember("tm_1", "usr_owner2", domain.RoleOwner)
	svc := NewService(fs)
	if _, err := svc.ChangeMemberRole(ctx(), "tm_1", "usr_owner", domain.RoleAdmin, domain.RoleOwner); err != nil {
		t.Fatalf("demote with second owner present: %v", err)
	}
}

func TestCannotRemoveLastOwner(t *testing.T) {
	fs := newFakeStore()
	fs.addMember("tm_1", "usr_owner", domain.RoleOwner)
	svc := NewService(fs)
	if err := svc.RemoveMember(ctx(), "tm_1", "usr_owner", domain.RoleOwner); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing last owner: err = %v, want ErrLastOwner", err)
	}
}

// ─── Team delete guard ──────────────────────────────────────────────────────

func TestDeleteTeamRefusedWithProjects(t *testing.T) {
	fs := newFakeStore()
	fs.projects["tm_1"] = 2
	svc := NewService(fs)
	if err := svc.Delete(ctx(), "tm_1"); !errors.Is(err, ErrTeamNotEmpty) {
		t.Fatalf("delete team with projects: err = %v, want ErrTeamNotEmpty", err)
	}
	fs.projects["tm_1"] = 0
	if err := svc.Delete(ctx(), "tm_1"); err != nil {
		t.Fatalf("delete empty team: %v", err)
	}
}

// ─── User management (panel role grants) ────────────────────────────────────

func TestPanelAdminCannotCreateAdmin(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.CreateUser(ctx(), "new@acme.com", "a-strong-password", domain.RoleAdmin, domain.RoleAdmin)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("panel admin creating admin: err = %v, want ErrForbidden", err)
	}
}

func TestPanelAdminCreatesMember(t *testing.T) {
	svc := NewService(newFakeStore())
	u, err := svc.CreateUser(ctx(), "new@acme.com", "a-strong-password", domain.RoleMember, domain.RoleAdmin)
	if err != nil || u.Role != domain.RoleMember {
		t.Fatalf("panel admin creating member: %+v, %v", u, err)
	}
}

func TestCreateUserRejectsWeakInput(t *testing.T) {
	svc := NewService(newFakeStore())
	for _, tc := range []struct{ email, pw string }{
		{"noat", "a-strong-password"},
		{"ok@acme.com", "short"},
	} {
		if _, err := svc.CreateUser(ctx(), tc.email, tc.pw, domain.RoleMember, domain.RoleOwner); err == nil {
			t.Fatalf("weak input %+v accepted", tc)
		}
	}
}

func TestDeleteOwnAccountRefused(t *testing.T) {
	fs := newFakeStore()
	actor := fs.addUser("usr_me", "me@acme.com", domain.RoleOwner)
	svc := NewService(fs)
	if err := svc.DeleteUser(ctx(), "usr_me", actor); err == nil {
		t.Fatal("deleting own account should be refused")
	}
}

func TestCannotDemoteLastPanelOwner(t *testing.T) {
	fs := newFakeStore()
	actor := fs.addUser("usr_po", "po@acme.com", domain.RoleOwner)
	svc := NewService(fs)
	if _, err := svc.SetUserRole(ctx(), "usr_po", domain.RoleAdmin, actor); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("self-demote of last panel owner: err = %v, want ErrLastOwner", err)
	}
	fs.addUser("usr_po2", "po2@acme.com", domain.RoleOwner)
	if _, err := svc.SetUserRole(ctx(), "usr_po", domain.RoleAdmin, actor); err != nil {
		t.Fatalf("self-demote with second panel owner present: %v", err)
	}
}

package access

// In-memory doubles for the seams this package defines. They behave like the
// real store where the behaviour is the point — the single-use spend, the
// "one live invitation per address" supersede, the membership join — and stay
// deliberately simple everywhere else: a fake that reimplements Postgres proves
// nothing about Postgres, which is what the real-database tests in core/store
// are for (ENGINEERING rule 29).

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

func ctx() context.Context { return context.Background() }

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore satisfies both InviteStore and RequestStore: the two halves of the
// feature share a database, and a test that grants an access request to someone
// who joined by invitation should not need two fakes to say so.
type fakeStore struct {
	mu       sync.Mutex
	teams    map[string]domain.Team
	users    map[string]domain.User // by lower-cased email
	byID     map[string]domain.User
	members  map[string]string // "team/user" -> role
	invites  map[string]domain.TeamInvite
	requests map[string]domain.AccessRequest

	// now is the clock the spend predicates read, so a test can age an
	// invitation without sleeping.
	now func() time.Time
	// failCreate makes CreateTeamInvite fail, for the error path.
	failCreate bool
	// failGetInvite makes GetTeamInvite fail with something that is NOT
	// store.ErrNotFound, so the "an outage is not a bad link" path is testable.
	failGetInvite error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		teams:    map[string]domain.Team{"tm_1": {ID: "tm_1", Name: "meridian studio"}},
		users:    map[string]domain.User{},
		byID:     map[string]domain.User{},
		members:  map[string]string{},
		invites:  map[string]domain.TeamInvite{},
		requests: map[string]domain.AccessRequest{},
		now:      time.Now,
	}
}

func (f *fakeStore) addUser(id, email, role string) domain.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := domain.User{ID: id, Email: email, Role: role}
	f.users[email] = u
	f.byID[id] = u
	return u
}

func (f *fakeStore) addMember(teamID, userID, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[teamID+"/"+userID] = role
}

func (f *fakeStore) role(teamID, userID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[teamID+"/"+userID]
}

func (f *fakeStore) GetTeam(_ context.Context, id string) (domain.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.teams[id]
	if !ok {
		return domain.Team{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[email]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) CreateUser(_ context.Context, id, email, passwordHash, role string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, taken := f.users[email]; taken {
		return domain.User{}, store.ErrConflict
	}
	u := domain.User{ID: id, Email: email, PasswordHash: passwordHash, Role: role}
	f.users[email] = u
	f.byID[id] = u
	return u, nil
}

func (f *fakeStore) GetTeamMember(_ context.Context, teamID, userID string) (domain.TeamMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.members[teamID+"/"+userID]
	if !ok {
		return domain.TeamMember{}, store.ErrNotFound
	}
	return domain.TeamMember{TeamID: teamID, UserID: userID, Role: role}, nil
}

func (f *fakeStore) UpsertTeamMember(_ context.Context, teamID, userID, role string) (domain.TeamMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[teamID+"/"+userID] = role
	return domain.TeamMember{TeamID: teamID, UserID: userID, Role: role}, nil
}

func (f *fakeStore) CreateTeamInvite(_ context.Context, inv domain.TeamInvite) (domain.TeamInvite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		return domain.TeamInvite{}, errors.New("boom")
	}
	// The partial unique index, in miniature: one LIVE invitation per
	// (team, address).
	for _, cur := range f.invites {
		if cur.TeamID == inv.TeamID && cur.Email == inv.Email &&
			cur.AcceptedAt == nil && cur.RevokedAt == nil {
			return domain.TeamInvite{}, store.ErrConflict
		}
	}
	inv.CreatedAt = f.now()
	f.invites[inv.ID] = inv
	return inv, nil
}

func (f *fakeStore) GetTeamInvite(_ context.Context, id string) (domain.TeamInvite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGetInvite != nil {
		return domain.TeamInvite{}, f.failGetInvite
	}
	inv, ok := f.invites[id]
	if !ok {
		return domain.TeamInvite{}, store.ErrNotFound
	}
	return inv, nil
}

func (f *fakeStore) ListTeamInvites(_ context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.TeamInvite{}
	for _, inv := range f.invites {
		if inv.TeamID != teamID {
			continue
		}
		if !includeDecided && !inv.Acceptable(f.now()) {
			continue
		}
		out = append(out, inv)
	}
	return out, nil
}

func (f *fakeStore) RevokeTeamInvite(_ context.Context, teamID, id string) (domain.TeamInvite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.invites[id]
	if !ok || inv.TeamID != teamID || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return domain.TeamInvite{}, store.ErrNotFound
	}
	at := f.now()
	inv.RevokedAt = &at
	f.invites[id] = inv
	return inv, nil
}

func (f *fakeStore) RevokeLiveTeamInvitesFor(_ context.Context, teamID, email string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, inv := range f.invites {
		if inv.TeamID == teamID && inv.Email == email && inv.AcceptedAt == nil && inv.RevokedAt == nil {
			at := f.now()
			inv.RevokedAt = &at
			f.invites[id] = inv
			n++
		}
	}
	return n, nil
}

// AcceptTeamInvite is the single-use spend: the predicate lives in the
// statement, so a second call finds nothing.
func (f *fakeStore) AcceptTeamInvite(_ context.Context, id string) (domain.TeamInvite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.invites[id]
	if !ok || !inv.Acceptable(f.now()) {
		return domain.TeamInvite{}, store.ErrNotFound
	}
	at := f.now()
	inv.AcceptedAt = &at
	f.invites[id] = inv
	return inv, nil
}

func (f *fakeStore) CreateAccessRequest(_ context.Context, r domain.AccessRequest) (domain.AccessRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cur := range f.requests {
		if cur.TeamID == r.TeamID && cur.UserID == r.UserID && cur.State == domain.AccessRequestPending {
			return domain.AccessRequest{}, store.ErrConflict
		}
	}
	r.State = domain.AccessRequestPending
	r.CreatedAt = f.now()
	r.UserEmail = f.byID[r.UserID].Email
	r.CurrentRole = f.members[r.TeamID+"/"+r.UserID]
	f.requests[r.ID] = r
	return r, nil
}

func (f *fakeStore) GetAccessRequest(_ context.Context, id string) (domain.AccessRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	if !ok {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	// current_role is derived at read time, as the SQL does it.
	r.CurrentRole = f.members[r.TeamID+"/"+r.UserID]
	r.UserEmail = f.byID[r.UserID].Email
	return r, nil
}

func (f *fakeStore) ListAccessRequests(_ context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.AccessRequest{}
	for _, r := range f.requests {
		if r.TeamID != teamID {
			continue
		}
		if !includeDecided && r.State != domain.AccessRequestPending {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) DecideAccessRequest(_ context.Context, id, state, decidedBy, decidedByLabel, reason string) (domain.AccessRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	if !ok || r.State != domain.AccessRequestPending {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	at := f.now()
	by := decidedBy
	r.State, r.DecidedBy, r.DecidedByLabel, r.DecisionReason, r.DecidedAt = state, &by, decidedByLabel, reason, &at
	r.CurrentRole = f.members[r.TeamID+"/"+r.UserID]
	f.requests[id] = r
	return r, nil
}

func (f *fakeStore) ListTeamOwnerEmails(_ context.Context, teamID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for key, role := range f.members {
		if role != domain.RoleOwner || len(key) <= len(teamID)+1 || key[:len(teamID)] != teamID {
			continue
		}
		out = append(out, f.byID[key[len(teamID)+1:]].Email)
	}
	return out, nil
}

// fakeSessions is the authentication seam. loginErr is what an existing
// account's sign-in returns, so a test can model a wrong password or a demanded
// second factor without a real password hash.
type fakeSessions struct {
	loginErr error
	// logins records the (email, password, code) triples Login was asked for,
	// proving the accept path really goes through sign-in.
	logins   [][3]string
	profiles map[string]string
	users    map[string]domain.User
	started  []string
	// store lets UpdateProfile answer with the whole account, as the real
	// authenticator does — reading it back from the row it just wrote.
	store *fakeStore
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{profiles: map[string]string{}, users: map[string]domain.User{}}
}

func (f *fakeSessions) Login(_ context.Context, email, password, totpCode, _ string) (string, domain.User, error) {
	f.logins = append(f.logins, [3]string{email, password, totpCode})
	if f.loginErr != nil {
		return "", domain.User{}, f.loginErr
	}
	u, ok := f.users[email]
	if !ok {
		return "", domain.User{}, auth.ErrInvalidCredentials
	}
	return "session-for-" + u.ID, u, nil
}

func (f *fakeSessions) StartSession(_ context.Context, userID string) (string, error) {
	f.started = append(f.started, userID)
	return "session-for-" + userID, nil
}

func (f *fakeSessions) UpdateProfile(_ context.Context, userID, displayName, _ string) (domain.User, error) {
	f.profiles[userID] = displayName
	var u domain.User
	if f.store != nil {
		f.store.mu.Lock()
		u = f.store.byID[userID]
		f.store.mu.Unlock()
	}
	u.ID, u.DisplayName = userID, displayName
	return u, nil
}

// fakeMailer records what the panel would have sent.
type fakeMailer struct {
	sent []sentMail
	err  error
}

type sentMail struct {
	To      []string
	Subject string
	Body    string
}

func (f *fakeMailer) Send(_ context.Context, to []string, subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentMail{To: to, Subject: subject, Body: body})
	return nil
}

// fakeAnnouncer records the inbox items the services asked for.
type fakeAnnouncer struct {
	requested []inbox.AccessNotice
	granted   []inbox.AccessNotice
	denied    []inbox.AccessNotice
	accepted  []inbox.InviteNotice
}

func (f *fakeAnnouncer) RecordAccessRequested(_ context.Context, n inbox.AccessNotice) error {
	f.requested = append(f.requested, n)
	return nil
}

func (f *fakeAnnouncer) RecordAccessGranted(_ context.Context, n inbox.AccessNotice) error {
	f.granted = append(f.granted, n)
	return nil
}

func (f *fakeAnnouncer) RecordAccessDenied(_ context.Context, n inbox.AccessNotice) error {
	f.denied = append(f.denied, n)
	return nil
}

func (f *fakeAnnouncer) RecordInviteAccepted(_ context.Context, n inbox.InviteNotice) error {
	f.accepted = append(f.accepted, n)
	return nil
}

// fakeRoles is the membership seam a granted request goes through. It records
// the calls so a test can prove the grant used the member-role path rather than
// writing a membership itself.
type fakeRoles struct {
	store *fakeStore
	err   error
	calls [][4]string
}

func (f *fakeRoles) ChangeMemberRole(_ context.Context, teamID, userID, role, actorRole string) (domain.TeamMember, error) {
	f.calls = append(f.calls, [4]string{teamID, userID, role, actorRole})
	if f.err != nil {
		return domain.TeamMember{}, f.err
	}
	f.store.addMember(teamID, userID, role)
	return domain.TeamMember{TeamID: teamID, UserID: userID, Role: role}, nil
}

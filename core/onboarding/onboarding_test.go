package onboarding

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore is an in-memory onboarding.Store.
type fakeStore struct {
	mu      sync.Mutex
	users   map[string]domain.User
	teams   map[string]domain.Team
	members map[string]string // "team:user" -> role
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]domain.User{}, teams: map[string]domain.Team{}, members: map[string]string{}}
}

func (f *fakeStore) CountUsers(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.users)), nil
}

func (f *fakeStore) CreateUser(_ context.Context, id, email, hash, role string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := domain.User{ID: id, Email: email, PasswordHash: hash, Role: role}
	f.users[id] = u
	return u, nil
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

func (f *fakeStore) CreateTeam(_ context.Context, id, name string) (domain.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := domain.Team{ID: id, Name: name}
	f.teams[id] = t
	return t, nil
}

func (f *fakeStore) UpsertTeamMember(_ context.Context, teamID, userID, role string) (domain.TeamMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[teamID+":"+userID] = role
	return domain.TeamMember{UserID: userID, Role: role}, nil
}

func TestNeedsSetupThenNot(t *testing.T) {
	fs := newFakeStore()
	svc := New(fs)

	needs, err := svc.NeedsSetup(context.Background())
	if err != nil || !needs {
		t.Fatalf("NeedsSetup on empty panel = %v, %v; want true", needs, err)
	}
	if _, err := svc.CreateFirstOwner(context.Background(), "owner@acme.com", "hunter2!!"); err != nil {
		t.Fatalf("CreateFirstOwner: %v", err)
	}
	needs, err = svc.NeedsSetup(context.Background())
	if err != nil || needs {
		t.Fatalf("NeedsSetup after setup = %v, %v; want false", needs, err)
	}
}

func TestCreateFirstOwnerShape(t *testing.T) {
	fs := newFakeStore()
	svc := New(fs)

	owner, err := svc.CreateFirstOwner(context.Background(), "  Owner@Acme.com ", "hunter2!!")
	if err != nil {
		t.Fatalf("CreateFirstOwner: %v", err)
	}
	if owner.Role != domain.RoleOwner {
		t.Errorf("role = %q, want owner", owner.Role)
	}
	if owner.Email != "owner@acme.com" {
		t.Errorf("email = %q, want normalized owner@acme.com", owner.Email)
	}
	if _, ok := fs.teams[defaultTeamID]; !ok {
		t.Error("default team was not created")
	}
	if fs.members[defaultTeamID+":"+owner.ID] != domain.RoleOwner {
		t.Error("owner was not enrolled into the default team as owner")
	}
}

// The critical invariant: once an account exists, setup is closed.
func TestSecondSetupRefused(t *testing.T) {
	fs := newFakeStore()
	svc := New(fs)

	if _, err := svc.CreateFirstOwner(context.Background(), "first@acme.com", "hunter2!!"); err != nil {
		t.Fatalf("first CreateFirstOwner: %v", err)
	}
	_, err := svc.CreateFirstOwner(context.Background(), "second@acme.com", "hunter2!!")
	if !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("second CreateFirstOwner err = %v, want ErrAlreadySetUp", err)
	}
	if n := len(fs.users); n != 1 {
		t.Fatalf("users = %d, want exactly 1", n)
	}
}

func TestSetupValidation(t *testing.T) {
	svc := New(newFakeStore())
	for name, tc := range map[string]struct{ email, password string }{
		"no at sign":     {"notanemail", "hunter2!!"},
		"empty email":    {"", "hunter2!!"},
		"short password": {"owner@acme.com", "short"},
	} {
		_, err := svc.CreateFirstOwner(context.Background(), tc.email, tc.password)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
	// A validation failure must not have created anything.
	if n, _ := svc.store.CountUsers(context.Background()); n != 0 {
		t.Fatalf("users after invalid setups = %d, want 0", n)
	}
}

func TestExistingDefaultTeamReused(t *testing.T) {
	fs := newFakeStore()
	// Simulate a migrated panel where the default team already exists.
	fs.teams[defaultTeamID] = domain.Team{ID: defaultTeamID, Name: "default"}
	svc := New(fs)

	owner, err := svc.CreateFirstOwner(context.Background(), "owner@acme.com", "hunter2!!")
	if err != nil {
		t.Fatalf("CreateFirstOwner: %v", err)
	}
	if fs.members[defaultTeamID+":"+owner.ID] != domain.RoleOwner {
		t.Error("owner not enrolled into the pre-existing default team")
	}
	if !strings.HasPrefix(owner.ID, "usr_") {
		t.Errorf("owner id = %q, want usr_ prefix", owner.ID)
	}
}

// Package onboarding owns first-run setup: creating the very first owner
// account through the API when a panel has no users yet
// (docs/features/first-run-setup.md). It is the single home for "make the
// first owner" — the env-var bootstrap (cypherd main) and the in-browser setup
// screen both go through CreateFirstOwner, so a fresh boot and a migrated panel
// converge to the same shape (one owner in the default team).
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// defaultTeamID is the team every panel has from first boot; the first owner is
// enrolled into it (teams-and-roles.md §2).
const defaultTeamID = "tm_default"

// minPasswordLen is the floor for the first owner's password. Deliberately
// modest — this is a self-hosted panel, not a consumer service; the operator
// owns the box either way.
const minPasswordLen = 8

// ErrAlreadySetUp means the panel already has at least one account, so the
// public setup path is closed. It is the invariant that keeps setup one-time:
// once an owner exists, nobody can register a second account without auth.
var ErrAlreadySetUp = errors.New("onboarding: panel already has an account")

// ValidationError marks bad setup input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Store is the persistence onboarding needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	CountUsers(ctx context.Context) (int64, error)
	CreateUser(ctx context.Context, id, email, passwordHash, role string) (domain.User, error)
	GetTeam(ctx context.Context, id string) (domain.Team, error)
	CreateTeam(ctx context.Context, id, name string) (domain.Team, error)
	UpsertTeamMember(ctx context.Context, teamID, userID, role string) (domain.TeamMember, error)
}

// Service creates the first owner. Construct with New.
type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

// NeedsSetup reports whether the panel has no accounts yet — the gate the UI
// checks to decide between the setup screen and login.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("onboarding: counting users: %w", err)
	}
	return n == 0, nil
}

// CreateFirstOwner creates the panel's first account (an owner), the default
// team, and the owner's membership in it. It refuses with ErrAlreadySetUp once
// any account exists, so the public setup endpoint can never mint a second
// account. Returns the created owner.
func (s *Service) CreateFirstOwner(ctx context.Context, email, password string) (domain.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return domain.User{}, invalid("a valid email is required")
	}
	if len(password) < minPasswordLen {
		return domain.User{}, invalid(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
	}

	// Gate: setup is one-time. A tiny race (two setups seeing zero at once) is
	// benign — both would create valid owners; the far more important property
	// is that an already-set-up panel refuses, which this guarantees.
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("onboarding: counting users: %w", err)
	}
	if n > 0 {
		return domain.User{}, ErrAlreadySetUp
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return domain.User{}, fmt.Errorf("onboarding: hashing password: %w", err)
	}
	owner, err := s.store.CreateUser(ctx, ids.New(ids.PrefixUser), email, hash, domain.RoleOwner)
	if err != nil {
		return domain.User{}, fmt.Errorf("onboarding: creating owner: %w", err)
	}

	// The default team always exists with the owner as a member (idempotent so
	// a re-run or the env-var bootstrap converges to the same shape).
	if _, err := s.store.GetTeam(ctx, defaultTeamID); errors.Is(err, store.ErrNotFound) {
		if _, err := s.store.CreateTeam(ctx, defaultTeamID, "default"); err != nil {
			return domain.User{}, fmt.Errorf("onboarding: creating default team: %w", err)
		}
	} else if err != nil {
		return domain.User{}, fmt.Errorf("onboarding: checking default team: %w", err)
	}
	if _, err := s.store.UpsertTeamMember(ctx, defaultTeamID, owner.ID, domain.RoleOwner); err != nil {
		return domain.User{}, fmt.Errorf("onboarding: enrolling owner in default team: %w", err)
	}
	return owner, nil
}

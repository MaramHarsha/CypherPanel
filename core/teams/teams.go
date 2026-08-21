// Package teams is the tenancy layer (teams-and-roles.md): Teams own projects,
// users belong to teams with a ranked role (member < admin < owner), and this
// service owns the invariants — grant rules, the last-owner guard, the
// delete-with-projects guard, and the panel-owner bypass. Per-route minimum
// ranks live in the REST layer; the rules that must hold regardless of route
// live here.
package teams

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

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Sentinel errors the REST layer maps to specific statuses.
var (
	// ErrForbidden marks an actor of insufficient rank for the operation (403).
	ErrForbidden = errors.New("teams: insufficient role")
	// ErrLastOwner guards the final owner of a team from demotion/removal (409).
	ErrLastOwner = errors.New("teams: a team must keep at least one owner")
	// ErrTeamNotEmpty refuses deleting a team that still owns projects (409).
	ErrTeamNotEmpty = errors.New("teams: team still owns projects")
	// ErrUserNotFound marks an unknown user email/id (404).
	ErrUserNotFound = errors.New("teams: user not found")
)

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	CreateTeam(ctx context.Context, id, name string) (domain.Team, error)
	GetTeam(ctx context.Context, id string) (domain.Team, error)
	ListTeams(ctx context.Context) ([]domain.Team, error)
	RenameTeam(ctx context.Context, id, name string) (domain.Team, error)
	DeleteTeam(ctx context.Context, id string) error
	CountTeamProjects(ctx context.Context, teamID string) (int64, error)

	UpsertTeamMember(ctx context.Context, teamID, userID, role string) (domain.TeamMember, error)
	GetTeamMember(ctx context.Context, teamID, userID string) (domain.TeamMember, error)
	ListTeamMembers(ctx context.Context, teamID string) ([]domain.TeamMember, error)
	DeleteTeamMember(ctx context.Context, teamID, userID string) error
	// DeleteInboxItemsForTeamMember drops the ex-member's notification-inbox
	// items for this team's projects (notification-inbox.md §4).
	DeleteInboxItemsForTeamMember(ctx context.Context, teamID, userID string) error
	CountTeamOwners(ctx context.Context, teamID string) (int64, error)
	ListTeamsByUser(ctx context.Context, userID string) ([]domain.TeamWithRole, error)
	GetTeamRoleForProject(ctx context.Context, projectID, userID string) (string, error)

	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	CreateUser(ctx context.Context, id, email, passwordHash, role string) (domain.User, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	UpdateUserRole(ctx context.Context, id, role string) (domain.User, error)
	DeleteUser(ctx context.Context, id string) error
}

// Service owns team/membership/user management. Construct with NewService.
type Service struct {
	store Store
}

// NewService wires the service.
func NewService(st Store) *Service { return &Service{store: st} }

// ─── Role resolution (spec §1, §3) ──────────────────────────────────────────

// RoleInTeam returns actor's effective role in the team: their membership row,
// or implicit owner for a panel owner (the superadmin bypass). "" means no
// access.
func (s *Service) RoleInTeam(ctx context.Context, actor domain.User, teamID string) (string, error) {
	if actor.Role == domain.RoleOwner {
		return domain.RoleOwner, nil
	}
	m, err := s.store.GetTeamMember(ctx, teamID, actor.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return m.Role, nil
}

// RoleForProject returns actor's effective role in the team that owns
// projectID, with the same bypass. "" means no access (the caller surfaces 404
// — a project you cannot see does not exist, spec §3).
func (s *Service) RoleForProject(ctx context.Context, actor domain.User, projectID string) (string, error) {
	if actor.Role == domain.RoleOwner {
		return domain.RoleOwner, nil
	}
	role, err := s.store.GetTeamRoleForProject(ctx, projectID, actor.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// ─── Teams ──────────────────────────────────────────────────────────────────

// Create creates a team with the creator as its first owner.
func (s *Service) Create(ctx context.Context, name string, creator domain.User) (domain.Team, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.Team{}, invalid("name must be 1–100 characters")
	}
	t, err := s.store.CreateTeam(ctx, ids.New(ids.PrefixTeam), name)
	if err != nil {
		return domain.Team{}, err
	}
	if _, err := s.store.UpsertTeamMember(ctx, t.ID, creator.ID, domain.RoleOwner); err != nil {
		// Roll the team back rather than leave one nobody owns.
		_ = s.store.DeleteTeam(ctx, t.ID)
		return domain.Team{}, fmt.Errorf("teams: enrolling creator: %w", err)
	}
	return t, nil
}

// Get returns one team.
func (s *Service) Get(ctx context.Context, id string) (domain.Team, error) {
	return s.store.GetTeam(ctx, id)
}

// ListFor returns the actor's teams with roles; a panel owner sees every team
// (role owner on each, per the bypass).
func (s *Service) ListFor(ctx context.Context, actor domain.User) ([]domain.TeamWithRole, error) {
	if actor.Role != domain.RoleOwner {
		return s.store.ListTeamsByUser(ctx, actor.ID)
	}
	all, err := s.store.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.TeamWithRole, 0, len(all))
	for _, t := range all {
		out = append(out, domain.TeamWithRole{Team: t, Role: domain.RoleOwner})
	}
	return out, nil
}

// Rename renames a team.
func (s *Service) Rename(ctx context.Context, id, name string) (domain.Team, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.Team{}, invalid("name must be 1–100 characters")
	}
	return s.store.RenameTeam(ctx, id, name)
}

// Delete removes a team. Refused while the team still owns projects —
// destroying a team must never silently cascade workloads (spec §4).
func (s *Service) Delete(ctx context.Context, id string) error {
	n, err := s.store.CountTeamProjects(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrTeamNotEmpty
	}
	return s.store.DeleteTeam(ctx, id)
}

// ─── Membership (grant rules per spec §4–5) ─────────────────────────────────

// Members lists a team's members (with emails).
func (s *Service) Members(ctx context.Context, teamID string) ([]domain.TeamMember, error) {
	return s.store.ListTeamMembers(ctx, teamID)
}

// AddMember adds the user with email to the team at role. actorRole is the
// caller's effective role in this team: granting owner requires owner;
// granting member/admin requires admin (no self-service escalation).
func (s *Service) AddMember(ctx context.Context, teamID, email, role, actorRole string) (domain.TeamMember, error) {
	if !domain.ValidRole(role) {
		return domain.TeamMember{}, invalid("role must be member, admin, or owner")
	}
	if err := requireGrantRank(actorRole, role); err != nil {
		return domain.TeamMember{}, err
	}
	u, err := s.store.GetUserByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, store.ErrNotFound) {
		return domain.TeamMember{}, ErrUserNotFound
	}
	if err != nil {
		return domain.TeamMember{}, err
	}
	if _, err := s.store.GetTeamMember(ctx, teamID, u.ID); err == nil {
		return domain.TeamMember{}, invalid("user is already a member of this team")
	}
	m, err := s.store.UpsertTeamMember(ctx, teamID, u.ID, role)
	if err != nil {
		return domain.TeamMember{}, err
	}
	m.Email = u.Email
	return m, nil
}

// ChangeMemberRole changes an existing member's role, enforcing the grant
// rules in both directions (touching an owner requires owner) and the
// last-owner guard.
func (s *Service) ChangeMemberRole(ctx context.Context, teamID, userID, role, actorRole string) (domain.TeamMember, error) {
	if !domain.ValidRole(role) {
		return domain.TeamMember{}, invalid("role must be member, admin, or owner")
	}
	cur, err := s.store.GetTeamMember(ctx, teamID, userID)
	if err != nil {
		return domain.TeamMember{}, err
	}
	if err := requireGrantRank(actorRole, role); err != nil {
		return domain.TeamMember{}, err
	}
	if err := requireGrantRank(actorRole, cur.Role); err != nil {
		return domain.TeamMember{}, err // demoting an owner also takes owner
	}
	if cur.Role == domain.RoleOwner && role != domain.RoleOwner {
		if err := s.guardLastOwner(ctx, teamID); err != nil {
			return domain.TeamMember{}, err
		}
	}
	return s.store.UpsertTeamMember(ctx, teamID, userID, role)
}

// RemoveMember removes a member, enforcing grant rules (removing an owner
// requires owner) and the last-owner guard.
//
// It also empties this team's notification-inbox items from the ex-member's
// inbox (notification-inbox.md §4). The rule there is "never hold an item for a
// team you do not belong to", and a stale title naming a project you were just
// removed from breaks it as surely as a live delivery would. The membership row
// goes first: if the cleanup then fails, the caller sees the error and retries,
// and in the meantime the person has already lost access.
func (s *Service) RemoveMember(ctx context.Context, teamID, userID, actorRole string) error {
	cur, err := s.store.GetTeamMember(ctx, teamID, userID)
	if err != nil {
		return err
	}
	if err := requireGrantRank(actorRole, cur.Role); err != nil {
		return err
	}
	if cur.Role == domain.RoleOwner {
		if err := s.guardLastOwner(ctx, teamID); err != nil {
			return err
		}
	}
	if err := s.store.DeleteTeamMember(ctx, teamID, userID); err != nil {
		return err
	}
	if err := s.store.DeleteInboxItemsForTeamMember(ctx, teamID, userID); err != nil {
		return fmt.Errorf("teams: clearing inbox items for removed member %s: %w", userID, err)
	}
	return nil
}

// requireGrantRank: acting on the owner rank requires owner; anything else
// requires admin.
func requireGrantRank(actorRole, subjectRole string) error {
	need := domain.RoleAdmin
	if subjectRole == domain.RoleOwner {
		need = domain.RoleOwner
	}
	if domain.RoleRank(actorRole) < domain.RoleRank(need) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) guardLastOwner(ctx context.Context, teamID string) error {
	n, err := s.store.CountTeamOwners(ctx, teamID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastOwner
	}
	return nil
}

// ─── Users (panel-level management, spec §4) ────────────────────────────────

// CreateUser creates a user with a panel role. actorRole is the caller's panel
// role: creating above member requires panel owner; creating a member requires
// panel admin.
func (s *Service) CreateUser(ctx context.Context, email, password, role, actorRole string) (domain.User, error) {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return domain.User{}, invalid("a valid email is required")
	}
	if len(password) < 12 {
		return domain.User{}, invalid("password must be at least 12 characters")
	}
	if role == "" {
		role = domain.RoleMember
	}
	if !domain.ValidRole(role) {
		return domain.User{}, invalid("role must be member, admin, or owner")
	}
	need := domain.RoleAdmin
	if role != domain.RoleMember {
		need = domain.RoleOwner
	}
	if domain.RoleRank(actorRole) < domain.RoleRank(need) {
		return domain.User{}, ErrForbidden
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return domain.User{}, fmt.Errorf("teams: hashing password: %w", err)
	}
	return s.store.CreateUser(ctx, ids.New(ids.PrefixUser), email, hash, role)
}

// ListUsers returns every user (panel admin+ surface; secrets never included —
// the DTO layer strips the hash).
func (s *Service) ListUsers(ctx context.Context) ([]domain.User, error) {
	return s.store.ListUsers(ctx)
}

// SetUserRole changes a user's panel role (panel owner only — enforced at the
// route). A panel owner demoting themself is allowed only if another panel
// owner remains, so the panel always has one.
func (s *Service) SetUserRole(ctx context.Context, userID, role string, actor domain.User) (domain.User, error) {
	if !domain.ValidRole(role) {
		return domain.User{}, invalid("role must be member, admin, or owner")
	}
	if userID == actor.ID && role != domain.RoleOwner {
		if err := s.guardLastPanelOwner(ctx); err != nil {
			return domain.User{}, err
		}
	}
	return s.store.UpdateUserRole(ctx, userID, role)
}

// DeleteUser removes a user (never the caller themself — locking yourself out
// is not a supported operation; team membership rows cascade).
func (s *Service) DeleteUser(ctx context.Context, userID string, actor domain.User) error {
	if userID == actor.ID {
		return invalid("you cannot delete your own account")
	}
	return s.store.DeleteUser(ctx, userID)
}

func (s *Service) guardLastPanelOwner(ctx context.Context) error {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	owners := 0
	for _, u := range users {
		if u.Role == domain.RoleOwner {
			owners++
		}
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

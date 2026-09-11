package store

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── Teams (teams-and-roles.md §2) ──────────────────────────────────────────

func (s *Store) CreateTeam(ctx context.Context, id, name string) (domain.Team, error) {
	row, err := s.q.CreateTeam(ctx, db.CreateTeamParams{ID: id, Name: name})
	if err != nil {
		return domain.Team{}, wrapCreate("creating team", err)
	}
	return teamFromRow(row), nil
}

func (s *Store) GetTeam(ctx context.Context, id string) (domain.Team, error) {
	row, err := s.q.GetTeam(ctx, id)
	if err != nil {
		return domain.Team{}, wrap("getting team", err)
	}
	return teamFromRow(row), nil
}

// ListTeams returns every team (the panel-owner view).
func (s *Store) ListTeams(ctx context.Context) ([]domain.Team, error) {
	rows, err := s.q.ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing teams: %w", err)
	}
	out := make([]domain.Team, 0, len(rows))
	for _, r := range rows {
		out = append(out, teamFromRow(r))
	}
	return out, nil
}

func (s *Store) RenameTeam(ctx context.Context, id, name string) (domain.Team, error) {
	row, err := s.q.RenameTeam(ctx, db.RenameTeamParams{ID: id, Name: name})
	if err != nil {
		return domain.Team{}, wrapUpdate("renaming team", err)
	}
	return teamFromRow(row), nil
}

func (s *Store) DeleteTeam(ctx context.Context, id string) error {
	if err := s.q.DeleteTeam(ctx, id); err != nil {
		return wrapDelete("deleting team", err)
	}
	return nil
}

// CountTeamProjects backs the delete guard: a team with projects is not
// deletable (spec §4).
func (s *Store) CountTeamProjects(ctx context.Context, teamID string) (int64, error) {
	n, err := s.q.CountTeamProjects(ctx, teamID)
	if err != nil {
		return 0, fmt.Errorf("store: counting team projects: %w", err)
	}
	return n, nil
}

// ─── Membership ─────────────────────────────────────────────────────────────

// UpsertTeamMember adds a user to a team or changes their existing role.
func (s *Store) UpsertTeamMember(ctx context.Context, teamID, userID, role string) (domain.TeamMember, error) {
	row, err := s.q.UpsertTeamMember(ctx, db.UpsertTeamMemberParams{TeamID: teamID, UserID: userID, Role: role})
	if err != nil {
		return domain.TeamMember{}, wrapCreate("upserting team member", err)
	}
	return domain.TeamMember{TeamID: row.TeamID, UserID: row.UserID, Role: row.Role, CreatedAt: row.CreatedAt.Time}, nil
}

func (s *Store) GetTeamMember(ctx context.Context, teamID, userID string) (domain.TeamMember, error) {
	row, err := s.q.GetTeamMember(ctx, db.GetTeamMemberParams{TeamID: teamID, UserID: userID})
	if err != nil {
		return domain.TeamMember{}, wrap("getting team member", err)
	}
	return domain.TeamMember{TeamID: row.TeamID, UserID: row.UserID, Role: row.Role, CreatedAt: row.CreatedAt.Time}, nil
}

func (s *Store) ListTeamMembers(ctx context.Context, teamID string) ([]domain.TeamMember, error) {
	rows, err := s.q.ListTeamMembers(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("store: listing team members: %w", err)
	}
	out := make([]domain.TeamMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.TeamMember{TeamID: r.TeamID, UserID: r.UserID, Email: r.Email, Role: r.Role, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}

func (s *Store) DeleteTeamMember(ctx context.Context, teamID, userID string) error {
	if err := s.q.DeleteTeamMember(ctx, db.DeleteTeamMemberParams{TeamID: teamID, UserID: userID}); err != nil {
		return wrapDelete("deleting team member", err)
	}
	return nil
}

// CountTeamOwners backs the last-owner guard (spec §4).
func (s *Store) CountTeamOwners(ctx context.Context, teamID string) (int64, error) {
	n, err := s.q.CountTeamOwners(ctx, teamID)
	if err != nil {
		return 0, fmt.Errorf("store: counting team owners: %w", err)
	}
	return n, nil
}

// ListTeamsByUser returns the caller's teams with their role in each.
func (s *Store) ListTeamsByUser(ctx context.Context, userID string) ([]domain.TeamWithRole, error) {
	rows, err := s.q.ListTeamsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("store: listing user teams: %w", err)
	}
	out := make([]domain.TeamWithRole, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.TeamWithRole{
			Team: domain.Team{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time},
			Role: r.Role,
		})
	}
	return out, nil
}

// GetTeamRoleForProject resolves the caller's role in the team that owns
// projectID — the single authorization query (spec §3). ErrNotFound means "no
// membership" (or no such project), which callers surface as 404.
func (s *Store) GetTeamRoleForProject(ctx context.Context, projectID, userID string) (string, error) {
	role, err := s.q.GetTeamRoleForProject(ctx, db.GetTeamRoleForProjectParams{ID: projectID, UserID: userID})
	if err != nil {
		return "", wrap("resolving project role", err)
	}
	return role, nil
}

// ListProjectsByUser returns the projects of the caller's teams.
func (s *Store) ListProjectsByUser(ctx context.Context, userID string) ([]domain.Project, error) {
	rows, err := s.q.ListProjectsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("store: listing user projects: %w", err)
	}
	out := make([]domain.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

// ─── Users (management surface, spec §4) ────────────────────────────────────

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing users: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateUserRole(ctx context.Context, id, role string) (domain.User, error) {
	row, err := s.q.UpdateUserRole(ctx, db.UpdateUserRoleParams{ID: id, Role: role})
	if err != nil {
		return domain.User{}, wrapUpdate("updating user role", err)
	}
	return userFromRow(row), nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	if err := s.q.DeleteUser(ctx, id); err != nil {
		return wrapDelete("deleting user", err)
	}
	return nil
}

func teamFromRow(r db.Team) domain.Team {
	return domain.Team{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
}

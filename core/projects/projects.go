// Package projects is the operator-facing lifecycle for the organizational
// spine: projects and their environments (docs/features/application-deploy.md
// §1). A new project always gets a default "production" environment so there is
// somewhere to put resources from the start.
package projects

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// DefaultEnvironment is the environment created with every new project.
const DefaultEnvironment = "production"

// ErrInvalidName is returned for an empty or over-long name.
var ErrInvalidName = errors.New("projects: name must be 1–100 characters")

var (
	// ErrEnvironmentNotInProject guards the default-environment field: a
	// project cannot point at an environment belonging to someone else.
	ErrEnvironmentNotInProject = errors.New("projects: that environment is not in this project")
	// ErrPreviewEnvironment marks an attempt to rename or delete an
	// environment the PR lifecycle owns. Previews come and go on their own;
	// editing one by hand desynchronises it from the pull request that made it.
	ErrPreviewEnvironment = errors.New("projects: a preview environment is managed by its pull request")
	// ErrLastEnvironment refuses to remove the environment a project would be
	// left without.
	ErrLastEnvironment = errors.New("projects: a project keeps at least one environment")
)

// slugAlphabet is everything a slug may contain after normalisation.
var slugAlphabet = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify renders a project name as a URL handle. Exported because the same
// rule ran in the backfill migration, and a second implementation that drifted
// would give old and new projects different-looking slugs.
func Slugify(name string) string {
	s := slugAlphabet.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		// A name made entirely of punctuation or non-Latin script still needs a
		// handle. The disambiguating suffix does the work from here.
		return "project"
	}
	const maxSlug = 60
	if len(s) > maxSlug {
		s = strings.Trim(s[:maxSlug], "-")
	}
	return s
}

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	CreateProjectWithEnvironment(ctx context.Context, projectID, name, teamID, slug, envID, envName string) (domain.Project, domain.Environment, error)
	UpdateProject(ctx context.Context, id string, f store.UpdateProjectFields) (domain.Project, error)
	SlugTakenInTeam(ctx context.Context, teamID, slug string) (bool, error)
	ProjectRollups(ctx context.Context) (map[string]domain.ProjectRollup, error)
	RenameEnvironment(ctx context.Context, id, name string) (domain.Environment, error)
	DeleteEnvironment(ctx context.Context, id string) error
	GetProject(ctx context.Context, id string) (domain.Project, error)
	ListProjects(ctx context.Context) ([]domain.Project, error)
	ListProjectsByUser(ctx context.Context, userID string) ([]domain.Project, error)
	DeleteProject(ctx context.Context, id string) error
	CreateEnvironment(ctx context.Context, id, projectID, name string) (domain.Environment, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	ListEnvironmentsByProject(ctx context.Context, projectID string) ([]domain.Environment, error)
}

// Service manages projects and environments.
type Service struct {
	store Store
}

// NewService wires the service.
func NewService(s Store) *Service {
	return &Service{store: s}
}

// Create registers a project under a team, with its default production
// environment (teams-and-roles.md §2 — every project belongs to a team).
func (s *Service) Create(ctx context.Context, name, teamID string) (domain.Project, domain.Environment, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return domain.Project{}, domain.Environment{}, ErrInvalidName
	}
	slug, err := s.freeSlug(ctx, teamID, Slugify(name))
	if err != nil {
		return domain.Project{}, domain.Environment{}, err
	}
	proj, env, err := s.store.CreateProjectWithEnvironment(
		ctx,
		ids.New(ids.PrefixProject), name, teamID, slug,
		ids.New(ids.PrefixEnvironment), DefaultEnvironment,
	)
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("projects: creating project: %w", err)
	}
	return proj, env, nil
}

// freeSlug finds an unused slug in the team, appending -2, -3 … as the backfill
// migration did. Bounded: after a hundred collisions the name is the problem,
// not the suffix, and looping forever would turn a naming mistake into a hang.
func (s *Service) freeSlug(ctx context.Context, teamID, base string) (string, error) {
	for n := 1; n <= 100; n++ {
		candidate := base
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n)
		}
		taken, err := s.store.SlugTakenInTeam(ctx, teamID, candidate)
		if err != nil {
			return "", fmt.Errorf("projects: checking slug: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("projects: too many projects named like %q in this team", base)
}

// UpdateInput is a partial edit of a project. Slug is deliberately absent: it
// is chosen once and never changes, because URLs and scripts depend on it.
type UpdateInput struct {
	Name                 *string
	TeamID               *string
	DefaultEnvironmentID *string
}

// Update applies a partial edit.
//
// A transfer keeps the slug only if it is free in the destination team; where
// it collides the project is given the next free one rather than the transfer
// being refused, because a name clash between two teams is not the operator's
// mistake to fix.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (domain.Project, error) {
	proj, err := s.store.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, fmt.Errorf("projects: getting project: %w", err)
	}

	f := store.UpdateProjectFields{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if !validName(name) {
			return domain.Project{}, ErrInvalidName
		}
		f.Name = &name
	}
	if in.TeamID != nil && *in.TeamID != proj.TeamID {
		f.TeamID = in.TeamID
		slug, err := s.freeSlug(ctx, *in.TeamID, proj.Slug)
		if err != nil {
			return domain.Project{}, err
		}
		if slug != proj.Slug {
			f.Slug = &slug
		}
	}
	if in.DefaultEnvironmentID != nil {
		if *in.DefaultEnvironmentID == "" {
			f.ClearDefaultEnvironment = true
		} else {
			env, err := s.store.GetEnvironment(ctx, *in.DefaultEnvironmentID)
			if err != nil {
				return domain.Project{}, fmt.Errorf("projects: getting environment: %w", err)
			}
			if env.ProjectID != id {
				return domain.Project{}, ErrEnvironmentNotInProject
			}
			f.DefaultEnvironmentID = in.DefaultEnvironmentID
		}
	}

	updated, err := s.store.UpdateProject(ctx, id, f)
	if err != nil {
		return domain.Project{}, fmt.Errorf("projects: updating project: %w", err)
	}
	return updated, nil
}

// Rollups returns per-project resource counts and worst status for the list.
func (s *Service) Rollups(ctx context.Context) (map[string]domain.ProjectRollup, error) {
	r, err := s.store.ProjectRollups(ctx)
	if err != nil {
		return nil, fmt.Errorf("projects: rolling up: %w", err)
	}
	return r, nil
}

// RenameEnvironment renames a standing environment. A preview belongs to its
// pull request and is refused.
func (s *Service) RenameEnvironment(ctx context.Context, id, name string) (domain.Environment, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return domain.Environment{}, ErrInvalidName
	}
	env, err := s.store.GetEnvironment(ctx, id)
	if err != nil {
		return domain.Environment{}, fmt.Errorf("projects: getting environment: %w", err)
	}
	if env.Kind == domain.EnvPreview {
		return domain.Environment{}, ErrPreviewEnvironment
	}
	renamed, err := s.store.RenameEnvironment(ctx, id, name)
	if err != nil {
		return domain.Environment{}, fmt.Errorf("projects: renaming environment: %w", err)
	}
	return renamed, nil
}

// DeleteEnvironment removes a standing environment. A preview is refused (its
// pull request owns it), and so is the last one a project has — a project with
// nowhere to put resources is not a state the UI can recover from.
//
// Resources still inside are refused by the store's foreign keys, which is the
// same protection deleting a project gets.
func (s *Service) DeleteEnvironment(ctx context.Context, id string) error {
	env, err := s.store.GetEnvironment(ctx, id)
	if err != nil {
		return fmt.Errorf("projects: getting environment: %w", err)
	}
	if env.Kind == domain.EnvPreview {
		return ErrPreviewEnvironment
	}
	siblings, err := s.store.ListEnvironmentsByProject(ctx, env.ProjectID)
	if err != nil {
		return fmt.Errorf("projects: listing environments: %w", err)
	}
	standing := 0
	for _, e := range siblings {
		if e.Kind != domain.EnvPreview {
			standing++
		}
	}
	if standing <= 1 {
		return ErrLastEnvironment
	}
	if err := s.store.DeleteEnvironment(ctx, id); err != nil {
		return fmt.Errorf("projects: deleting environment: %w", err)
	}
	return nil
}

// List returns all projects, newest first (the panel-owner view).
func (s *Service) List(ctx context.Context) ([]domain.Project, error) {
	list, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("projects: listing: %w", err)
	}
	return list, nil
}

// ListForUser returns the projects of the user's teams (spec §3: listings
// filter rather than fail).
func (s *Service) ListForUser(ctx context.Context, userID string) ([]domain.Project, error) {
	list, err := s.store.ListProjectsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("projects: listing for user: %w", err)
	}
	return list, nil
}

// Get returns one project; the error wraps store.ErrNotFound when absent.
func (s *Service) Get(ctx context.Context, id string) (domain.Project, error) {
	proj, err := s.store.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, fmt.Errorf("projects: getting project: %w", err)
	}
	return proj, nil
}

// Delete removes a project and (by cascade) its environments and resources.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.store.DeleteProject(ctx, id); err != nil {
		return fmt.Errorf("projects: deleting project: %w", err)
	}
	return nil
}

// ListEnvironments returns a project's environments. It verifies the project
// exists first so a missing project is a clean not-found, not an empty list.
func (s *Service) ListEnvironments(ctx context.Context, projectID string) ([]domain.Environment, error) {
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		return nil, fmt.Errorf("projects: getting project: %w", err)
	}
	list, err := s.store.ListEnvironmentsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("projects: listing environments: %w", err)
	}
	return list, nil
}

// CreateEnvironment adds an environment to an existing project.
func (s *Service) CreateEnvironment(ctx context.Context, projectID, name string) (domain.Environment, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return domain.Environment{}, ErrInvalidName
	}
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		return domain.Environment{}, fmt.Errorf("projects: getting project: %w", err)
	}
	env, err := s.store.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), projectID, name)
	if err != nil {
		return domain.Environment{}, fmt.Errorf("projects: creating environment: %w", err)
	}
	return env, nil
}

func validName(name string) bool {
	return name != "" && len(name) <= 100
}

// GetEnvironment returns one environment — the authz layer's resolution step
// from an environment-scoped route to its project (teams-and-roles.md §3).
func (s *Service) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	return s.store.GetEnvironment(ctx, id)
}

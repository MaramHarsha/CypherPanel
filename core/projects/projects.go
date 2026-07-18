// Package projects is the operator-facing lifecycle for the organizational
// spine: projects and their environments (docs/features/application-deploy.md
// §1). A new project always gets a default "production" environment so there is
// somewhere to put resources from the start.
package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// DefaultEnvironment is the environment created with every new project.
const DefaultEnvironment = "production"

// ErrInvalidName is returned for an empty or over-long name.
var ErrInvalidName = errors.New("projects: name must be 1–100 characters")

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	CreateProjectWithEnvironment(ctx context.Context, projectID, name, envID, envName string) (domain.Project, domain.Environment, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	ListProjects(ctx context.Context) ([]domain.Project, error)
	DeleteProject(ctx context.Context, id string) error
	CreateEnvironment(ctx context.Context, id, projectID, name string) (domain.Environment, error)
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

// Create registers a project and its default production environment.
func (s *Service) Create(ctx context.Context, name string) (domain.Project, domain.Environment, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return domain.Project{}, domain.Environment{}, ErrInvalidName
	}
	proj, env, err := s.store.CreateProjectWithEnvironment(
		ctx,
		ids.New(ids.PrefixProject), name,
		ids.New(ids.PrefixEnvironment), DefaultEnvironment,
	)
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("projects: creating project: %w", err)
	}
	return proj, env, nil
}

// List returns all projects, newest first.
func (s *Service) List(ctx context.Context) ([]domain.Project, error) {
	list, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("projects: listing: %w", err)
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

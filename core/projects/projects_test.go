package projects

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	projects map[string]domain.Project
	envs     map[string][]domain.Environment
}

func newFakeStore() *fakeStore {
	return &fakeStore{projects: map[string]domain.Project{}, envs: map[string][]domain.Environment{}}
}

func (f *fakeStore) CreateProjectWithEnvironment(_ context.Context, pid, name, teamID, eid, ename string) (domain.Project, domain.Environment, error) {
	_ = teamID
	p := domain.Project{ID: pid, Name: name, CreatedAt: time.Now()}
	e := domain.Environment{ID: eid, ProjectID: pid, Name: ename, CreatedAt: time.Now()}
	f.projects[pid] = p
	f.envs[pid] = append(f.envs[pid], e)
	return p, e, nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListProjects(context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) DeleteProject(_ context.Context, id string) error {
	delete(f.projects, id)
	return nil
}

func (f *fakeStore) ListProjectsByUser(context.Context, string) ([]domain.Project, error) {
	return nil, nil
}

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	for _, envs := range f.envs {
		for _, e := range envs {
			if e.ID == id {
				return e, nil
			}
		}
	}
	return domain.Environment{}, store.ErrNotFound
}

func (f *fakeStore) CreateEnvironment(_ context.Context, id, pid, name string) (domain.Environment, error) {
	e := domain.Environment{ID: id, ProjectID: pid, Name: name, CreatedAt: time.Now()}
	f.envs[pid] = append(f.envs[pid], e)
	return e, nil
}

func (f *fakeStore) ListEnvironmentsByProject(_ context.Context, pid string) ([]domain.Environment, error) {
	return f.envs[pid], nil
}

func TestCreateAddsDefaultEnvironment(t *testing.T) {
	s := NewService(newFakeStore())
	proj, env, err := s.Create(context.Background(), "  acme  ", "tm_default")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if proj.Name != "acme" { // trimmed
		t.Errorf("name = %q, want trimmed 'acme'", proj.Name)
	}
	if env.Name != DefaultEnvironment {
		t.Errorf("default env = %q, want %q", env.Name, DefaultEnvironment)
	}
	if env.ProjectID != proj.ID {
		t.Errorf("env project = %q, want %q", env.ProjectID, proj.ID)
	}
}

func TestCreateRejectsBadName(t *testing.T) {
	s := NewService(newFakeStore())
	for _, name := range []string{"", "   ", string(make([]byte, 101))} {
		if _, _, err := s.Create(context.Background(), name, "tm_default"); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: err = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestCreateEnvironmentRequiresProject(t *testing.T) {
	s := NewService(newFakeStore())
	if _, err := s.CreateEnvironment(context.Background(), "prj_missing", "staging"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func TestListEnvironmentsRequiresProject(t *testing.T) {
	s := NewService(newFakeStore())
	if _, err := s.ListEnvironments(context.Background(), "prj_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func TestGetMissingProject(t *testing.T) {
	s := NewService(newFakeStore())
	if _, err := s.Get(context.Background(), "prj_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

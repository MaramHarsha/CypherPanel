package projects

import (
	"context"
	"errors"
	"strings"
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

func (f *fakeStore) CreateProjectWithEnvironment(_ context.Context, pid, name, teamID, slug, eid, ename string) (domain.Project, domain.Environment, error) {
	p := domain.Project{
		ID: pid, Name: name, TeamID: teamID, Slug: slug,
		DefaultEnvironmentID: eid, LastActivityAt: time.Now(), CreatedAt: time.Now(),
	}
	e := domain.Environment{ID: eid, ProjectID: pid, Name: ename, Kind: domain.EnvProduction, CreatedAt: time.Now()}
	f.projects[pid] = p
	f.envs[pid] = append(f.envs[pid], e)
	return p, e, nil
}

func (f *fakeStore) UpdateProject(_ context.Context, id string, fields store.UpdateProjectFields) (domain.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	if fields.Name != nil {
		p.Name = *fields.Name
	}
	if fields.TeamID != nil {
		p.TeamID = *fields.TeamID
	}
	if fields.Slug != nil {
		p.Slug = *fields.Slug
	}
	switch {
	case fields.ClearDefaultEnvironment:
		p.DefaultEnvironmentID = ""
	case fields.DefaultEnvironmentID != nil:
		p.DefaultEnvironmentID = *fields.DefaultEnvironmentID
	}
	f.projects[id] = p
	return p, nil
}

func (f *fakeStore) SlugTakenInTeam(_ context.Context, teamID, slug string) (bool, error) {
	for _, p := range f.projects {
		if p.TeamID == teamID && p.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ProjectRollups(context.Context) (map[string]domain.ProjectRollup, error) {
	return map[string]domain.ProjectRollup{}, nil
}

func (f *fakeStore) RenameEnvironment(_ context.Context, id, name string) (domain.Environment, error) {
	for pid, list := range f.envs {
		for i, e := range list {
			if e.ID == id {
				f.envs[pid][i].Name = name
				return f.envs[pid][i], nil
			}
		}
	}
	return domain.Environment{}, store.ErrNotFound
}

func (f *fakeStore) DeleteEnvironment(_ context.Context, id string) error {
	for pid, list := range f.envs {
		for i, e := range list {
			if e.ID == id {
				f.envs[pid] = append(list[:i], list[i+1:]...)
				return nil
			}
		}
	}
	return store.ErrNotFound
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

// ─── slug, transfer and environment lifecycle ───────────────────────────────

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Atlas CRM":        "atlas-crm",
		"atlas-crm":        "atlas-crm",
		"  Spaced  Out  ":  "spaced-out",
		"Ünïcodé Nàme":     "n-cod-n-me", // ASCII survives; the rest is separators
		"!!!":              "project",    // nothing usable is still a handle
		"":                 "project",
		"Trailing---":      "trailing",
		"UPPER_case_Under": "upper-case-under",
		"a.b.c":            "a-b-c",
		"Ship It 2026!":    "ship-it-2026",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	// Long names are cut without leaving a dangling separator.
	long := Slugify(strings.Repeat("verylongword ", 20))
	if len(long) > 60 {
		t.Fatalf("slug is %d chars, want at most 60", len(long))
	}
	if strings.HasSuffix(long, "-") {
		t.Fatalf("slug ends with a separator: %q", long)
	}
}

// A second project with the same name gets the next free slug rather than
// failing, and the suffix rule matches the one the backfill migration used.
func TestCreateDisambiguatesSlugsWithinATeam(t *testing.T) {
	svc := NewService(newFakeStore())
	ctx := context.Background()

	first, _, err := svc.Create(ctx, "Atlas CRM", "tm_1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, _, err := svc.Create(ctx, "Atlas CRM", "tm_1")
	if err != nil {
		t.Fatalf("Create (second): %v", err)
	}
	if first.Slug != "atlas-crm" || second.Slug != "atlas-crm-2" {
		t.Fatalf("slugs = %q, %q; want atlas-crm and atlas-crm-2", first.Slug, second.Slug)
	}

	// A different team is a different namespace: the bare slug is free again.
	other, _, err := svc.Create(ctx, "Atlas CRM", "tm_2")
	if err != nil {
		t.Fatalf("Create (other team): %v", err)
	}
	if other.Slug != "atlas-crm" {
		t.Fatalf("slug in another team = %q, want atlas-crm", other.Slug)
	}
}

// The first environment is the default, so a fresh project always has somewhere
// for "open this project" to land.
func TestCreateSetsTheFirstEnvironmentAsDefault(t *testing.T) {
	svc := NewService(newFakeStore())
	proj, env, err := svc.Create(context.Background(), "atlas", "tm_1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if proj.DefaultEnvironmentID != env.ID {
		t.Fatalf("default environment = %q, want the created one %q", proj.DefaultEnvironmentID, env.ID)
	}
}

func TestUpdateRenamesWithoutChangingTheSlug(t *testing.T) {
	svc := NewService(newFakeStore())
	ctx := context.Background()
	proj, _, _ := svc.Create(ctx, "Atlas CRM", "tm_1")

	newName := "Atlas Sales"
	got, err := svc.Update(ctx, proj.ID, UpdateInput{Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != newName {
		t.Fatalf("name = %q, want %q", got.Name, newName)
	}
	// The slug is the contract URLs and scripts depend on; a rename must not
	// move it.
	if got.Slug != proj.Slug {
		t.Fatalf("slug changed on rename: %q → %q", proj.Slug, got.Slug)
	}
}

func TestUpdateRejectsAnEmptyName(t *testing.T) {
	svc := NewService(newFakeStore())
	ctx := context.Background()
	proj, _, _ := svc.Create(ctx, "atlas", "tm_1")

	empty := "   "
	if _, err := svc.Update(ctx, proj.ID, UpdateInput{Name: &empty}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("err = %v, want ErrInvalidName", err)
	}
}

// A transfer keeps the slug when the destination has it free, and takes the
// next one when it does not — a clash between two teams is not the operator's
// mistake to fix, so it must not refuse the transfer.
func TestUpdateTransferReassignsAClashingSlug(t *testing.T) {
	st := newFakeStore()
	svc := NewService(st)
	ctx := context.Background()

	moving, _, _ := svc.Create(ctx, "Atlas CRM", "tm_1")
	if _, _, err := svc.Create(ctx, "Atlas CRM", "tm_2"); err != nil {
		t.Fatalf("Create in destination: %v", err)
	}

	dest := "tm_2"
	got, err := svc.Update(ctx, moving.ID, UpdateInput{TeamID: &dest})
	if err != nil {
		t.Fatalf("Update (transfer): %v", err)
	}
	if got.TeamID != dest {
		t.Fatalf("team = %q, want %q", got.TeamID, dest)
	}
	if got.Slug != "atlas-crm-2" {
		t.Fatalf("slug after a clashing transfer = %q, want atlas-crm-2", got.Slug)
	}

	// Into an empty team the slug survives.
	empty := "tm_3"
	got2, err := svc.Update(ctx, got.ID, UpdateInput{TeamID: &empty})
	if err != nil {
		t.Fatalf("Update (second transfer): %v", err)
	}
	if got2.Slug != "atlas-crm-2" {
		t.Fatalf("slug changed on a transfer with no clash: %q", got2.Slug)
	}
}

// A project must not be able to point at somebody else's environment.
func TestUpdateRejectsAForeignDefaultEnvironment(t *testing.T) {
	svc := NewService(newFakeStore())
	ctx := context.Background()
	mine, _, _ := svc.Create(ctx, "mine", "tm_1")
	_, theirEnv, _ := svc.Create(ctx, "theirs", "tm_1")

	if _, err := svc.Update(ctx, mine.ID, UpdateInput{DefaultEnvironmentID: &theirEnv.ID}); !errors.Is(err, ErrEnvironmentNotInProject) {
		t.Fatalf("err = %v, want ErrEnvironmentNotInProject", err)
	}
}

// Previews belong to their pull request: renaming or deleting one by hand
// desynchronises it from the PR that made it.
func TestPreviewEnvironmentsAreNotEditableByHand(t *testing.T) {
	st := newFakeStore()
	svc := NewService(st)
	ctx := context.Background()
	proj, _, _ := svc.Create(ctx, "atlas", "tm_1")

	preview := domain.Environment{ID: "env_pr", ProjectID: proj.ID, Name: "pr-214", Kind: domain.EnvPreview}
	st.envs[proj.ID] = append(st.envs[proj.ID], preview)

	if _, err := svc.RenameEnvironment(ctx, preview.ID, "renamed"); !errors.Is(err, ErrPreviewEnvironment) {
		t.Fatalf("rename err = %v, want ErrPreviewEnvironment", err)
	}
	if err := svc.DeleteEnvironment(ctx, preview.ID); !errors.Is(err, ErrPreviewEnvironment) {
		t.Fatalf("delete err = %v, want ErrPreviewEnvironment", err)
	}
}

// A project with nowhere to put resources is not a state the UI can recover
// from, so the last standing environment cannot be removed. Previews do not
// count towards that floor.
func TestDeleteEnvironmentKeepsTheLastStandingOne(t *testing.T) {
	st := newFakeStore()
	svc := NewService(st)
	ctx := context.Background()
	proj, prod, _ := svc.Create(ctx, "atlas", "tm_1")

	if err := svc.DeleteEnvironment(ctx, prod.ID); !errors.Is(err, ErrLastEnvironment) {
		t.Fatalf("err = %v, want ErrLastEnvironment", err)
	}

	// A preview alongside it does not make production removable.
	st.envs[proj.ID] = append(st.envs[proj.ID], domain.Environment{
		ID: "env_pr", ProjectID: proj.ID, Name: "pr-1", Kind: domain.EnvPreview,
	})
	if err := svc.DeleteEnvironment(ctx, prod.ID); !errors.Is(err, ErrLastEnvironment) {
		t.Fatalf("with only a preview alongside: err = %v, want ErrLastEnvironment", err)
	}

	// A second standing environment does.
	staging, err := svc.CreateEnvironment(ctx, proj.ID, "staging")
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if err := svc.DeleteEnvironment(ctx, staging.ID); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
}

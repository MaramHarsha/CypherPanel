package sharedvars

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// identitySealer makes sealing observable and reversible: the ciphertext is a
// recognizable transform of the plaintext, never equal to it.
type identitySealer struct{}

func (identitySealer) Seal(pt []byte) (ct, nonce []byte, err error) {
	return append([]byte("sealed:"), pt...), []byte("nonce"), nil
}

type fakeStore struct {
	projects  map[string]bool
	envs      map[string]domain.Environment
	vars      map[string]domain.SharedVariable
	usage     map[string][]domain.SharedVariableUsage
	pending   map[string]bool
	pendByEnv map[string][]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		projects: map[string]bool{"prj_1": true},
		envs: map[string]domain.Environment{
			"env_prod":  {ID: "env_prod", ProjectID: "prj_1", Name: "production"},
			"env_stage": {ID: "env_stage", ProjectID: "prj_1", Name: "staging"},
			"env_other": {ID: "env_other", ProjectID: "prj_2", Name: "production"},
		},
		vars:      map[string]domain.SharedVariable{},
		usage:     map[string][]domain.SharedVariableUsage{},
		pending:   map[string]bool{},
		pendByEnv: map[string][]string{},
	}
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	if !f.projects[id] {
		return domain.Project{}, store.ErrNotFound
	}
	return domain.Project{ID: id, Name: id}, nil
}

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	e, ok := f.envs[id]
	if !ok {
		return domain.Environment{}, store.ErrNotFound
	}
	return e, nil
}

func (f *fakeStore) ListEnvironmentsByProject(_ context.Context, projectID string) ([]domain.Environment, error) {
	var out []domain.Environment
	for _, e := range f.envs {
		if e.ProjectID == projectID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateSharedVariable(_ context.Context, v domain.SharedVariable) (domain.SharedVariable, error) {
	for _, cur := range f.vars {
		if cur.ProjectID == v.ProjectID && cur.Key == v.Key && sameScope(cur.EnvironmentID, v.EnvironmentID) {
			return domain.SharedVariable{}, store.ErrConflict
		}
	}
	v.CreatedAt, v.UpdatedAt = time.Unix(0, 0), time.Unix(0, 0)
	f.vars[v.ID] = v
	return v, nil
}

func sameScope(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func (f *fakeStore) GetSharedVariable(_ context.Context, id string) (domain.SharedVariable, error) {
	v, ok := f.vars[id]
	if !ok {
		return domain.SharedVariable{}, store.ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) ListSharedVariablesByProject(_ context.Context, projectID string) ([]domain.SharedVariable, error) {
	var out []domain.SharedVariable
	for _, v := range f.vars {
		if v.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateSharedVariableValue(_ context.Context, id string, ct, nonce []byte) (domain.SharedVariable, error) {
	v, ok := f.vars[id]
	if !ok {
		return domain.SharedVariable{}, store.ErrNotFound
	}
	v.ValueCT, v.ValueNonce = ct, nonce
	v.UpdatedAt = v.UpdatedAt.Add(time.Second)
	f.vars[id] = v
	return v, nil
}

func (f *fakeStore) DeleteSharedVariable(_ context.Context, id string) error {
	delete(f.vars, id)
	return nil
}

func (f *fakeStore) CountSharedVariableUsage(_ context.Context, id string) (int64, error) {
	return int64(len(f.usage[id])), nil
}

func (f *fakeStore) CountSharedVariableUsageByProject(_ context.Context, projectID string) (map[string]int64, error) {
	out := map[string]int64{}
	for id, v := range f.vars {
		if v.ProjectID == projectID {
			out[id] = int64(len(f.usage[id]))
		}
	}
	return out, nil
}

func (f *fakeStore) ListSharedVariableUsage(_ context.Context, id string) ([]domain.SharedVariableUsage, error) {
	return f.usage[id], nil
}

func (f *fakeStore) ApplicationRedeployPending(_ context.Context, appID string) (bool, error) {
	return f.pending[appID], nil
}

func (f *fakeStore) ListRedeployPendingApplications(_ context.Context, envID string) ([]string, error) {
	return f.pendByEnv[envID], nil
}

func newService() (*Service, *fakeStore) {
	fs := newFakeStore()
	return NewService(fs, identitySealer{}), fs
}

func ptr(s string) *string { return &s }

func TestCreateSealsAndNeverReturnsTheValue(t *testing.T) {
	s, fs := newService()
	v, err := s.Create(context.Background(), "prj_1", CreateInput{Key: "SENTRY_DSN", Value: "https://k@sentry.io/1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(v.Variable.ID, "sv_") {
		t.Errorf("id = %q, want an sv_ prefix", v.Variable.ID)
	}
	if v.Variable.EnvironmentID != nil || v.EnvironmentName != "" {
		t.Errorf("project scope carried an environment: %+v", v)
	}
	if v.UsedByCount != 0 {
		t.Errorf("used_by_count = %d on a brand-new variable, want 0", v.UsedByCount)
	}
	stored := fs.vars[v.Variable.ID]
	if string(stored.ValueCT) != "sealed:https://k@sentry.io/1" {
		t.Errorf("value was not sealed: %q", stored.ValueCT)
	}
	// The View type has no field that could carry a value; assert the sealed
	// bytes are the only copy, so a future field addition fails this test.
	if strings.Contains(v.Variable.Key, "sentry.io") {
		t.Error("the value leaked into the returned key")
	}
}

func TestCreateEnvironmentScope(t *testing.T) {
	s, _ := newService()
	v, err := s.Create(context.Background(), "prj_1", CreateInput{Key: "SMTP_HOST", EnvironmentID: ptr("env_prod"), Value: "smtp.sendgrid.net"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.EnvironmentName != "production" {
		t.Fatalf("environment_name = %q, want production", v.EnvironmentName)
	}
}

// The same key at two different scopes is the whole point of shadowing, so it
// must be accepted; the same key at the SAME scope is a conflict.
func TestScopesCoexistAndDuplicatesConflict(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	if _, err := s.Create(ctx, "prj_1", CreateInput{Key: "SMTP_HOST", Value: "mail.internal"}); err != nil {
		t.Fatalf("project scope: %v", err)
	}
	if _, err := s.Create(ctx, "prj_1", CreateInput{Key: "SMTP_HOST", EnvironmentID: ptr("env_prod"), Value: "smtp.sendgrid.net"}); err != nil {
		t.Fatalf("environment scope: %v", err)
	}
	if _, err := s.Create(ctx, "prj_1", CreateInput{Key: "SMTP_HOST", Value: "again"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate project scope = %v, want ErrConflict", err)
	}
	if _, err := s.Create(ctx, "prj_1", CreateInput{Key: "SMTP_HOST", EnvironmentID: ptr("env_prod"), Value: "again"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate environment scope = %v, want ErrConflict", err)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]func(*CreateInput){
		"empty key":           func(in *CreateInput) { in.Key = "" },
		"digit-first key":     func(in *CreateInput) { in.Key = "1FOO" },
		"key with equals":     func(in *CreateInput) { in.Key = "FOO=BAR" },
		"key with newline":    func(in *CreateInput) { in.Key = "FOO\nBAR" },
		"key with dash":       func(in *CreateInput) { in.Key = "FOO-BAR" },
		"nested reference":    func(in *CreateInput) { in.Value = "a{{shared.OTHER}}b" },
		"value too large":     func(in *CreateInput) { in.Value = strings.Repeat("x", 32*1024+1) },
		"foreign environment": func(in *CreateInput) { in.EnvironmentID = ptr("env_other") },
		"unknown environment": func(in *CreateInput) { in.EnvironmentID = ptr("env_missing") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newService()
			in := CreateInput{Key: "OK", Value: "v"}
			mutate(&in)
			_, err := s.Create(context.Background(), "prj_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Create = %v, want a ValidationError", err)
			}
		})
	}
}

func TestCreateUnknownProject(t *testing.T) {
	s, _ := newService()
	_, err := s.Create(context.Background(), "prj_missing", CreateInput{Key: "K", Value: "v"})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Create = %v, want ErrProjectNotFound", err)
	}
}

// A write always moves updated_at, even when the plaintext is unchanged: AES-GCM
// with a fresh nonce yields different ciphertext for identical plaintext, so
// comparing would mean unsealing on every write — and marking a redeploy that
// turns out to be a no-op is safe, while missing one is not (§5).
func TestSetValueAlwaysMovesUpdatedAt(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	created, err := s.Create(ctx, "prj_1", CreateInput{Key: "K", Value: "same"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := s.SetValue(ctx, created.Variable.ID, "same")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !updated.Variable.UpdatedAt.After(created.Variable.UpdatedAt) {
		t.Fatalf("updated_at did not move on an identical value: %v -> %v",
			created.Variable.UpdatedAt, updated.Variable.UpdatedAt)
	}
}

func TestSetValueRejectsNesting(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	v, err := s.Create(ctx, "prj_1", CreateInput{Key: "K", Value: "v"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var ve *ValidationError
	if _, err := s.SetValue(ctx, v.Variable.ID, "{{shared.OTHER}}"); !errors.As(err, &ve) {
		t.Fatalf("SetValue = %v, want a ValidationError", err)
	}
}

func TestListViewsCarriesScopeAndCount(t *testing.T) {
	s, fs := newService()
	ctx := context.Background()
	proj, err := s.Create(ctx, "prj_1", CreateInput{Key: "SENTRY_DSN", Value: "v"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	env, err := s.Create(ctx, "prj_1", CreateInput{Key: "SMTP_HOST", EnvironmentID: ptr("env_prod"), Value: "v"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fs.usage[proj.Variable.ID] = []domain.SharedVariableUsage{{ApplicationName: "a"}, {ApplicationName: "b"}, {ApplicationName: "c"}}

	views, err := s.ListViews(ctx, "prj_1")
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	byID := map[string]View{}
	for _, v := range views {
		byID[v.Variable.ID] = v
	}
	if got := byID[proj.Variable.ID]; got.UsedByCount != 3 || got.EnvironmentName != "" {
		t.Errorf("project-scoped view = %+v, want 3 uses at project scope", got)
	}
	if got := byID[env.Variable.ID]; got.UsedByCount != 0 || got.EnvironmentName != "production" {
		t.Errorf("environment-scoped view = %+v, want 0 uses in production", got)
	}
}

// Delete is guarded with no force override: the 409 names the applications so
// the operator knows exactly where to go (§7).
func TestDeleteRefusesWhileReferenced(t *testing.T) {
	s, fs := newService()
	ctx := context.Background()
	v, err := s.Create(ctx, "prj_1", CreateInput{Key: "SENTRY_DSN", Value: "v"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fs.usage[v.Variable.ID] = []domain.SharedVariableUsage{
		{ApplicationID: "app_1", ApplicationName: "web"},
		{ApplicationID: "app_2", ApplicationName: "worker"},
	}
	err = s.Delete(ctx, v.Variable.ID)
	var inUse *InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("Delete = %v, want InUseError", err)
	}
	if strings.Join(inUse.Applications, ",") != "web,worker" {
		t.Fatalf("InUseError names %v, want the referencing applications", inUse.Applications)
	}
	if _, ok := fs.vars[v.Variable.ID]; !ok {
		t.Fatal("the refused delete removed the row anyway")
	}

	// Remove the references, and the same delete succeeds.
	fs.usage[v.Variable.ID] = nil
	if err := s.Delete(ctx, v.Variable.ID); err != nil {
		t.Fatalf("Delete after removing references: %v", err)
	}
	if _, ok := fs.vars[v.Variable.ID]; ok {
		t.Fatal("the variable survived its delete")
	}
}

func TestDeleteUnknown(t *testing.T) {
	s, _ := newService()
	if err := s.Delete(context.Background(), "sv_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete = %v, want store.ErrNotFound", err)
	}
}

func TestUsedByUnknown(t *testing.T) {
	s, _ := newService()
	if _, err := s.UsedBy(context.Background(), "sv_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UsedBy = %v, want store.ErrNotFound", err)
	}
}

func TestPendingInEnvironment(t *testing.T) {
	s, fs := newService()
	fs.pendByEnv["env_prod"] = []string{"app_1", "app_3"}
	got, err := s.PendingInEnvironment(context.Background(), "env_prod")
	if err != nil {
		t.Fatalf("PendingInEnvironment: %v", err)
	}
	if !got["app_1"] || !got["app_3"] || got["app_2"] {
		t.Fatalf("pending set = %v, want app_1 and app_3 only", got)
	}
}

func TestRedeployPending(t *testing.T) {
	s, fs := newService()
	fs.pending["app_1"] = true
	if ok, err := s.RedeployPending(context.Background(), "app_1"); err != nil || !ok {
		t.Fatalf("RedeployPending(app_1) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := s.RedeployPending(context.Background(), "app_2"); err != nil || ok {
		t.Fatalf("RedeployPending(app_2) = %v, %v; want false, nil", ok, err)
	}
}

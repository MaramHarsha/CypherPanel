package scheduledtasks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	apps  map[string]bool
	tasks map[string]domain.ScheduledTask
}

func newFakeStore() *fakeStore {
	return &fakeStore{apps: map[string]bool{"app_1": true}, tasks: map[string]domain.ScheduledTask{}}
}

func (f *fakeStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	if !f.apps[id] {
		return domain.Application{}, store.ErrNotFound
	}
	return domain.Application{ID: id}, nil
}
func (f *fakeStore) CreateScheduledTask(_ context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error) {
	f.tasks[t.ID] = t
	return t, nil
}
func (f *fakeStore) GetScheduledTask(_ context.Context, id string) (domain.ScheduledTask, error) {
	t, ok := f.tasks[id]
	if !ok {
		return domain.ScheduledTask{}, store.ErrNotFound
	}
	return t, nil
}
func (f *fakeStore) ListScheduledTasksByApp(_ context.Context, appID string) ([]domain.ScheduledTask, error) {
	var out []domain.ScheduledTask
	for _, t := range f.tasks {
		if t.ApplicationID == appID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeStore) UpdateScheduledTask(_ context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error) {
	f.tasks[t.ID] = t
	return t, nil
}
func (f *fakeStore) DeleteScheduledTask(_ context.Context, id string) error {
	delete(f.tasks, id)
	return nil
}
func (f *fakeStore) ListTaskRuns(_ context.Context, _ string, _ int32) ([]domain.ScheduledTaskRun, error) {
	return nil, nil
}

type fakeConverger struct{ calls []string }

func (c *fakeConverger) ConvergeApp(_ context.Context, appID string) error {
	c.calls = append(c.calls, appID)
	return nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func validInput() Input {
	return Input{Name: "nightly", Schedule: "0 3 * * *", Command: []string{"sh", "-c", "cleanup"}, Enabled: true}
}

func TestCreateValidatesAndConverges(t *testing.T) {
	cv := &fakeConverger{}
	svc := NewService(newFakeStore(), cv, quietLog())
	task, err := svc.Create(context.Background(), "app_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.Schedule != "0 3 * * *" || len(task.Command) != 3 {
		t.Fatalf("task not stored as given: %+v", task)
	}
	if len(cv.calls) != 1 || cv.calls[0] != "app_1" {
		t.Fatalf("converge calls = %v, want one for app_1", cv.calls)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]func(*Input){
		"empty name":     func(in *Input) { in.Name = "" },
		"bad cron":       func(in *Input) { in.Schedule = "not a cron" },
		"too few fields": func(in *Input) { in.Schedule = "* * *" },
		"empty command":  func(in *Input) { in.Command = nil },
		"blank command":  func(in *Input) { in.Command = []string{"  ", ""} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			svc := NewService(newFakeStore(), &fakeConverger{}, quietLog())
			in := validInput()
			mutate(&in)
			_, err := svc.Create(context.Background(), "app_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
		})
	}
}

func TestCreateUnknownApplication(t *testing.T) {
	svc := NewService(newFakeStore(), &fakeConverger{}, quietLog())
	_, err := svc.Create(context.Background(), "app_missing", validInput())
	if !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("err = %v, want ErrApplicationNotFound", err)
	}
}

func TestUpdateConvergesAndKeepsValidation(t *testing.T) {
	cv := &fakeConverger{}
	fs := newFakeStore()
	svc := NewService(fs, cv, quietLog())
	task, err := svc.Create(context.Background(), "app_1", validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A bad schedule on update is rejected.
	if _, err := svc.Update(context.Background(), task.ID, Input{Name: "x", Schedule: "bad", Command: []string{"true"}}); err == nil {
		t.Fatal("update with bad cron should fail")
	}
	// A good update converges again.
	if _, err := svc.Update(context.Background(), task.ID, Input{Name: "renamed", Schedule: "*/5 * * * *", Command: []string{"true"}, Enabled: false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(cv.calls) != 2 { // create + successful update
		t.Fatalf("converge calls = %v, want 2", cv.calls)
	}
}

func TestDeleteConverges(t *testing.T) {
	cv := &fakeConverger{}
	fs := newFakeStore()
	svc := NewService(fs, cv, quietLog())
	task, _ := svc.Create(context.Background(), "app_1", validInput())
	if err := svc.Delete(context.Background(), task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := fs.tasks[task.ID]; ok {
		t.Fatal("task not deleted")
	}
	if len(cv.calls) != 2 { // create + delete
		t.Fatalf("converge calls = %v, want 2", cv.calls)
	}
}

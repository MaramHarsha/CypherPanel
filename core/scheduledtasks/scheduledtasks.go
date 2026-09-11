// Package scheduledtasks is the control-plane CRUD surface for cron tasks that
// run inside an Application's own container (scheduled-tasks.md, ADR-011). It
// validates the cron expression and argv command, persists the task (the source
// of truth), and asks the converger to propagate the change to the app's agent
// promptly. Execution and scheduling live on the agent; this package never runs
// a command.
package scheduledtasks

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/robfig/cron/v3"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrApplicationNotFound is returned when the addressed application is absent —
// distinct from store.ErrNotFound (which, from this service, means the task).
var ErrApplicationNotFound = errors.New("scheduledtasks: application not found")

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// runHistoryLimit caps how many runs GET …/runs returns.
const runHistoryLimit = 50

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	CreateScheduledTask(ctx context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error)
	GetScheduledTask(ctx context.Context, id string) (domain.ScheduledTask, error)
	ListScheduledTasksByApp(ctx context.Context, appID string) ([]domain.ScheduledTask, error)
	UpdateScheduledTask(ctx context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error)
	DeleteScheduledTask(ctx context.Context, id string) error
	ListTaskRuns(ctx context.Context, taskID string, limit int32) ([]domain.ScheduledTaskRun, error)
}

// Converger propagates an app's current desired state to its agent without a
// redeploy (consumer-defined; *scheduler.Scheduler satisfies it —
// scheduled-tasks.md §4). Best-effort: the stored task is the source of truth,
// so a failed push just means the change lands on the next deploy/sync.
type Converger interface {
	ConvergeApp(ctx context.Context, appID string) error
}

// Service is the scheduled-task CRUD surface.
type Service struct {
	store     Store
	converger Converger
	log       *slog.Logger
}

// NewService wires the service.
func NewService(st Store, converger Converger, log *slog.Logger) *Service {
	return &Service{store: st, converger: converger, log: log}
}

// Input is a create/replace request.
type Input struct {
	Name     string
	Schedule string
	Command  []string
	Enabled  bool
}

// Create validates and stores a task under an application, then converges.
func (s *Service) Create(ctx context.Context, appID string, in Input) (domain.ScheduledTask, error) {
	if _, err := s.store.GetApplication(ctx, appID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.ScheduledTask{}, ErrApplicationNotFound
		}
		return domain.ScheduledTask{}, err
	}
	name, schedule, command, err := validate(in)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	t, err := s.store.CreateScheduledTask(ctx, domain.ScheduledTask{
		ID:            ids.New(ids.PrefixScheduledTask),
		ApplicationID: appID,
		Name:          name,
		Schedule:      schedule,
		Command:       command,
		Enabled:       in.Enabled,
	})
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	s.converge(ctx, appID)
	return t, nil
}

// Update replaces a task's mutable fields, then converges.
func (s *Service) Update(ctx context.Context, id string, in Input) (domain.ScheduledTask, error) {
	cur, err := s.store.GetScheduledTask(ctx, id)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	name, schedule, command, err := validate(in)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	cur.Name, cur.Schedule, cur.Command, cur.Enabled = name, schedule, command, in.Enabled
	t, err := s.store.UpdateScheduledTask(ctx, cur)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	s.converge(ctx, cur.ApplicationID)
	return t, nil
}

// Get returns one task.
func (s *Service) Get(ctx context.Context, id string) (domain.ScheduledTask, error) {
	return s.store.GetScheduledTask(ctx, id)
}

// List returns an application's tasks.
func (s *Service) List(ctx context.Context, appID string) ([]domain.ScheduledTask, error) {
	return s.store.ListScheduledTasksByApp(ctx, appID)
}

// Delete removes a task, then converges (so the agent drops its armed entry).
func (s *Service) Delete(ctx context.Context, id string) error {
	cur, err := s.store.GetScheduledTask(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteScheduledTask(ctx, id); err != nil {
		return err
	}
	s.converge(ctx, cur.ApplicationID)
	return nil
}

// Runs returns recent run history for a task.
func (s *Service) Runs(ctx context.Context, taskID string) ([]domain.ScheduledTaskRun, error) {
	if _, err := s.store.GetScheduledTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.ListTaskRuns(ctx, taskID, runHistoryLimit)
}

// converge asks the agent to re-read desired state; best-effort (spec §4).
func (s *Service) converge(ctx context.Context, appID string) {
	if s.converger == nil {
		return
	}
	if err := s.converger.ConvergeApp(ctx, appID); err != nil {
		s.log.Warn("scheduledtasks: converge push failed; change applies on next deploy/sync",
			"app_id", appID, "error", err)
	}
}

// validate normalises and checks a task input, returning the cleaned fields.
func validate(in Input) (name, schedule string, command []string, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", "", nil, invalid("name is required")
	}
	schedule = strings.TrimSpace(in.Schedule)
	if _, perr := cron.ParseStandard(schedule); perr != nil {
		return "", "", nil, invalid("schedule must be a valid 5-field cron expression: " + perr.Error())
	}
	command = make([]string, 0, len(in.Command))
	for _, c := range in.Command {
		if strings.TrimSpace(c) != "" {
			command = append(command, c)
		}
	}
	if len(command) == 0 {
		return "", "", nil, invalid("command is required (argv; use [\"sh\",\"-c\",\"…\"] for a shell)")
	}
	return name, schedule, command, nil
}

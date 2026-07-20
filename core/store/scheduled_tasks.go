package store

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreateScheduledTask inserts a scheduled task (scheduled-tasks.md §2).
func (s *Store) CreateScheduledTask(ctx context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error) {
	row, err := s.q.CreateScheduledTask(ctx, db.CreateScheduledTaskParams{
		ID:            t.ID,
		ApplicationID: t.ApplicationID,
		Name:          t.Name,
		Schedule:      t.Schedule,
		Command:       t.Command,
		Enabled:       t.Enabled,
	})
	if err != nil {
		return domain.ScheduledTask{}, wrapCreate("creating scheduled task", err)
	}
	return scheduledTaskFromRow(row), nil
}

func (s *Store) GetScheduledTask(ctx context.Context, id string) (domain.ScheduledTask, error) {
	row, err := s.q.GetScheduledTask(ctx, id)
	if err != nil {
		return domain.ScheduledTask{}, wrap("getting scheduled task", err)
	}
	return scheduledTaskFromRow(row), nil
}

func (s *Store) ListScheduledTasksByApp(ctx context.Context, appID string) ([]domain.ScheduledTask, error) {
	rows, err := s.q.ListScheduledTasksByApp(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing scheduled tasks: %w", err)
	}
	return scheduledTasksFromRows(rows), nil
}

// ListEnabledScheduledTasksByApp returns the app's enabled tasks — the set the
// plane carries into desired state (scheduled-tasks.md §3).
func (s *Store) ListEnabledScheduledTasksByApp(ctx context.Context, appID string) ([]domain.ScheduledTask, error) {
	rows, err := s.q.ListEnabledScheduledTasksByApp(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing enabled scheduled tasks: %w", err)
	}
	return scheduledTasksFromRows(rows), nil
}

func (s *Store) UpdateScheduledTask(ctx context.Context, t domain.ScheduledTask) (domain.ScheduledTask, error) {
	row, err := s.q.UpdateScheduledTask(ctx, db.UpdateScheduledTaskParams{
		ID:       t.ID,
		Name:     t.Name,
		Schedule: t.Schedule,
		Command:  t.Command,
		Enabled:  t.Enabled,
	})
	if err != nil {
		return domain.ScheduledTask{}, wrapUpdate("updating scheduled task", err)
	}
	return scheduledTaskFromRow(row), nil
}

func (s *Store) DeleteScheduledTask(ctx context.Context, id string) error {
	if err := s.q.DeleteScheduledTask(ctx, id); err != nil {
		return wrapDelete("deleting scheduled task", err)
	}
	return nil
}

// CreateTaskRun records one execution's terminal outcome (the agent's
// observation).
func (s *Store) CreateTaskRun(ctx context.Context, r domain.ScheduledTaskRun) (domain.ScheduledTaskRun, error) {
	row, err := s.q.CreateTaskRun(ctx, db.CreateTaskRunParams{
		ID:         r.ID,
		TaskID:     r.TaskID,
		StartedAt:  tsFromTime(r.StartedAt),
		FinishedAt: tsFromPtr(r.FinishedAt),
		ExitCode:   int4FromPtr(r.ExitCode),
		Status:     r.Status,
		OutputTail: r.OutputTail,
	})
	if err != nil {
		return domain.ScheduledTaskRun{}, wrapCreate("creating task run", err)
	}
	return taskRunFromRow(row), nil
}

func (s *Store) ListTaskRuns(ctx context.Context, taskID string, limit int32) ([]domain.ScheduledTaskRun, error) {
	rows, err := s.q.ListTaskRuns(ctx, db.ListTaskRunsParams{TaskID: taskID, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("store: listing task runs: %w", err)
	}
	out := make([]domain.ScheduledTaskRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskRunFromRow(r))
	}
	return out, nil
}

// DeleteOldTaskRuns prunes run history beyond the most recent keep rows.
func (s *Store) DeleteOldTaskRuns(ctx context.Context, taskID string, keep int32) error {
	if err := s.q.DeleteOldTaskRuns(ctx, db.DeleteOldTaskRunsParams{TaskID: taskID, Limit: keep}); err != nil {
		return wrapDelete("pruning task runs", err)
	}
	return nil
}

func scheduledTasksFromRows(rows []db.ScheduledTask) []domain.ScheduledTask {
	out := make([]domain.ScheduledTask, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduledTaskFromRow(r))
	}
	return out
}

func scheduledTaskFromRow(r db.ScheduledTask) domain.ScheduledTask {
	return domain.ScheduledTask{
		ID:            r.ID,
		ApplicationID: r.ApplicationID,
		Name:          r.Name,
		Schedule:      r.Schedule,
		Command:       r.Command,
		Enabled:       r.Enabled,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

func taskRunFromRow(r db.ScheduledTaskRun) domain.ScheduledTaskRun {
	return domain.ScheduledTaskRun{
		ID:         r.ID,
		TaskID:     r.TaskID,
		StartedAt:  r.StartedAt.Time,
		FinishedAt: ptrTime(r.FinishedAt),
		ExitCode:   ptrFromInt4(r.ExitCode),
		Status:     r.Status,
		OutputTail: r.OutputTail,
		CreatedAt:  r.CreatedAt.Time,
	}
}

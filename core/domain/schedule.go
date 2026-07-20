package domain

import "time"

// ScheduledTask run-status vocabulary (scheduled_task_runs.status).
const (
	TaskRunRunning   = "running"
	TaskRunSucceeded = "succeeded"
	TaskRunFailed    = "failed"
)

// ScheduledTask is a cron entry on an Application: run command (argv) inside the
// app's own container on schedule (ADR-011, scheduled-tasks.md §2).
type ScheduledTask struct {
	ID            string
	ApplicationID string
	Name          string
	Schedule      string   // standard 5-field cron
	Command       []string // argv
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ScheduledTaskRun records one execution's terminal outcome — the observation
// the agent reports (ADR-005). output_tail is a capped diagnostic slice.
type ScheduledTaskRun struct {
	ID         string
	TaskID     string
	StartedAt  time.Time
	FinishedAt *time.Time
	ExitCode   *int
	Status     string
	OutputTail string
	CreatedAt  time.Time
}

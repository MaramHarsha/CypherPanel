package rest

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type scheduledTaskDTO struct {
	ID            string    `json:"id"`
	ApplicationID string    `json:"application_id"`
	Name          string    `json:"name"`
	Schedule      string    `json:"schedule"`
	Command       []string  `json:"command"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toScheduledTaskDTO(t domain.ScheduledTask) scheduledTaskDTO {
	return scheduledTaskDTO{
		ID:            t.ID,
		ApplicationID: t.ApplicationID,
		Name:          t.Name,
		Schedule:      t.Schedule,
		Command:       t.Command,
		Enabled:       t.Enabled,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}
}

type taskRunDTO struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ExitCode   *int       `json:"exit_code"`
	Status     string     `json:"status"`
	OutputTail string     `json:"output_tail"`
}

func toTaskRunDTO(r domain.ScheduledTaskRun) taskRunDTO {
	return taskRunDTO{
		ID:         r.ID,
		TaskID:     r.TaskID,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		ExitCode:   r.ExitCode,
		Status:     r.Status,
		OutputTail: r.OutputTail,
	}
}

type scheduledTaskRequest struct {
	Name     string   `json:"name"`
	Schedule string   `json:"schedule"`
	Command  []string `json:"command"`
	Enabled  *bool    `json:"enabled"` // default true when omitted
}

func (a *API) handleCreateScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeError(w, http.StatusNotImplemented, "scheduled tasks are not enabled")
		return
	}
	var req scheduledTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	t, err := a.deps.ScheduledTasks.Create(r.Context(), r.PathValue("id"), scheduledtasks.Input{
		Name:     req.Name,
		Schedule: req.Schedule,
		Command:  req.Command,
		Enabled:  enabled,
	})
	if err != nil {
		a.writeScheduledTaskError(w, "creating scheduled task", err)
		return
	}
	writeJSON(w, http.StatusCreated, toScheduledTaskDTO(t))
}

func (a *API) handleListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeJSON(w, http.StatusOK, []scheduledTaskDTO{})
		return
	}
	list, err := a.deps.ScheduledTasks.List(r.Context(), r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("listing scheduled tasks", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list scheduled tasks")
		return
	}
	out := make([]scheduledTaskDTO, 0, len(list))
	for _, t := range list {
		out = append(out, toScheduledTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeError(w, http.StatusNotFound, "scheduled task not found")
		return
	}
	t, err := a.deps.ScheduledTasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "scheduled task not found")
			return
		}
		a.deps.Log.Error("getting scheduled task", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get scheduled task")
		return
	}
	writeJSON(w, http.StatusOK, toScheduledTaskDTO(t))
}

func (a *API) handlePatchScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeError(w, http.StatusNotFound, "scheduled task not found")
		return
	}
	cur, err := a.deps.ScheduledTasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "scheduled task not found")
			return
		}
		a.deps.Log.Error("getting scheduled task", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get scheduled task")
		return
	}
	var req scheduledTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// PATCH semantics: unspecified fields keep their current value.
	in := scheduledtasks.Input{Name: cur.Name, Schedule: cur.Schedule, Command: cur.Command, Enabled: cur.Enabled}
	if req.Name != "" {
		in.Name = req.Name
	}
	if req.Schedule != "" {
		in.Schedule = req.Schedule
	}
	if req.Command != nil {
		in.Command = req.Command
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	t, err := a.deps.ScheduledTasks.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		a.writeScheduledTaskError(w, "updating scheduled task", err)
		return
	}
	writeJSON(w, http.StatusOK, toScheduledTaskDTO(t))
}

func (a *API) handleDeleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeError(w, http.StatusNotFound, "scheduled task not found")
		return
	}
	if err := a.deps.ScheduledTasks.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "scheduled task not found")
			return
		}
		a.deps.Log.Error("deleting scheduled task", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete scheduled task")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListTaskRuns(w http.ResponseWriter, r *http.Request) {
	if a.deps.ScheduledTasks == nil {
		writeJSON(w, http.StatusOK, []taskRunDTO{})
		return
	}
	runs, err := a.deps.ScheduledTasks.Runs(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "scheduled task not found")
			return
		}
		a.deps.Log.Error("listing task runs", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list task runs")
		return
	}
	out := make([]taskRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, toTaskRunDTO(run))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) writeScheduledTaskError(w http.ResponseWriter, op string, err error) {
	var ve *scheduledtasks.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, scheduledtasks.ErrApplicationNotFound):
		writeError(w, http.StatusNotFound, "application not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "scheduled task not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a scheduled task with that name already exists on the application")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}

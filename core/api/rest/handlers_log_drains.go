package rest

// Log drains (log-drains.md §9).
//
// PANEL ADMIN, because a drain spends the panel's stream, CPU and egress, and a
// project-scoped drain still ships lines out of the install. Who may create one
// is a panel question even when what it carries is one project's.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/logdrain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// LogDrainService is the CRUD half (consumer-defined).
type LogDrainService interface {
	Create(ctx context.Context, in logdrain.CreateInput) (domain.LogDrain, error)
	Update(ctx context.Context, id string, in logdrain.CreateInput) (domain.LogDrain, error)
	Get(ctx context.Context, id string) (domain.LogDrain, error)
	List(ctx context.Context) ([]domain.LogDrain, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (domain.LogDrain, error)
	Delete(ctx context.Context, id string) error
	Hint(d domain.LogDrain) string
}

type logDrainDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id"`
	TargetID  string `json:"target_id"`
	Enabled   bool   `json:"enabled"`
	// ConfigHint is the masked config: built by unsealing in-process and
	// discarding the plaintext. The config itself is never returned.
	ConfigHint string `json:"config_hint"`
	// Health is DERIVED, never stored.
	Health        string  `json:"health"`
	LastShippedAt *string `json:"last_shipped_at"`
	LastError     string  `json:"last_error"`
	LastErrorAt   *string `json:"last_error_at"`
	// DroppedLines is what the retention window discarded before this drain
	// could ship it — a drain down a week resumes at the oldest message still
	// held, having lost the rest, and this is where it says so.
	DroppedLines int64 `json:"dropped_lines"`
}

type logDrainRequest struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	ProjectID string          `json:"project_id"`
	TargetID  string          `json:"target_id"`
	Config    json.RawMessage `json:"config"`
	Enabled   *bool           `json:"enabled"`
}

func (a *API) drainDTO(d domain.LogDrain) logDrainDTO {
	var shipped, errored *string
	if d.LastShippedAt != nil {
		s := d.LastShippedAt.UTC().Format(time.RFC3339)
		shipped = &s
	}
	if d.LastErrorAt != nil {
		s := d.LastErrorAt.UTC().Format(time.RFC3339)
		errored = &s
	}
	return logDrainDTO{
		ID: d.ID, Name: d.Name, Kind: d.Kind,
		ProjectID: d.ProjectID, TargetID: d.TargetID, Enabled: d.Enabled,
		ConfigHint:    a.deps.LogDrains.Hint(d),
		Health:        domain.DrainHealth(d, time.Now()),
		LastShippedAt: shipped, LastError: d.LastError, LastErrorAt: errored,
		DroppedLines: d.DroppedLines,
	}
}

func (a *API) drainsReady(w http.ResponseWriter) bool {
	if a.deps.LogDrains == nil {
		writeError(w, http.StatusNotImplemented, "log drains are not enabled on this panel")
		return false
	}
	return true
}

func (a *API) handleListLogDrains(w http.ResponseWriter, r *http.Request) {
	if !a.drainsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	drains, err := a.deps.LogDrains.List(r.Context())
	if err != nil {
		a.deps.Log.Error("listing log drains", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the drains")
		return
	}
	out := make([]logDrainDTO, 0, len(drains))
	for _, d := range drains {
		out = append(out, a.drainDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateLogDrain(w http.ResponseWriter, r *http.Request) {
	if !a.drainsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req logDrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	d, err := a.deps.LogDrains.Create(r.Context(), logdrain.CreateInput{
		Name: req.Name, Kind: req.Kind, ProjectID: req.ProjectID,
		TargetID: req.TargetID, Config: req.Config, Enabled: enabled,
	})
	var invalid *logdrain.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Msg)
		return
	}
	if err != nil {
		a.deps.Log.Error("creating a log drain", "error", err)
		writeError(w, http.StatusConflict, "a drain with that name already exists")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionLogDrainCreated,
		Resource: audit.Resource(audit.ResourcePanel, d.ID, d.Name),
		// The FACT and the destination kind, never the config: it holds the
		// credential, and the audit log is not where it becomes permanent.
		Detail: map[string]any{"kind": d.Kind, "scope": drainScope(d.ProjectID)},
	})
	writeJSON(w, http.StatusCreated, a.drainDTO(d))
}

// drainScope words the tenancy answer for the audit detail: whose logs leave
// the panel. Nothing finer than a project — every line already carries its
// environment as a label, so a sink filters better than we can.
func drainScope(projectID string) string {
	if projectID == "" {
		return "all projects"
	}
	return "one project"
}

func (a *API) handleUpdateLogDrain(w http.ResponseWriter, r *http.Request) {
	if !a.drainsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req logDrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// A body with no config is the pause/resume shape, which is a different
	// verb and does not need the credential re-sent.
	if len(req.Config) == 0 {
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "send the whole config, or just `enabled` to pause")
			return
		}
		d, err := a.deps.LogDrains.SetEnabled(r.Context(), r.PathValue("id"), *req.Enabled)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not update the drain")
			return
		}
		a.audit(r, audit.Entry{
			Action:   audit.ActionLogDrainChanged,
			Resource: audit.Resource(audit.ResourcePanel, d.ID, d.Name),
			Detail:   map[string]any{"enabled": d.Enabled},
		})
		writeJSON(w, http.StatusOK, a.drainDTO(d))
		return
	}

	// An edit that says nothing about `enabled` leaves it as it is. The screen
	// deliberately sends only the config here — pausing is the row's own
	// control — and defaulting to true meant that fixing a paused drain's
	// endpoint silently resumed it.
	var enabled bool
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else {
		cur, err := a.deps.LogDrains.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not update the drain")
			return
		}
		enabled = cur.Enabled
	}
	d, err := a.deps.LogDrains.Update(r.Context(), r.PathValue("id"), logdrain.CreateInput{
		Name: req.Name, ProjectID: req.ProjectID, TargetID: req.TargetID,
		Config: req.Config, Enabled: enabled,
	})
	var invalid *logdrain.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Msg)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("updating a log drain", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update the drain")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionLogDrainChanged,
		Resource: audit.Resource(audit.ResourcePanel, d.ID, d.Name),
		Detail:   map[string]any{"kind": d.Kind, "scope": drainScope(d.ProjectID)},
	})
	writeJSON(w, http.StatusOK, a.drainDTO(d))
}

func (a *API) handleDeleteLogDrain(w http.ResponseWriter, r *http.Request) {
	if !a.drainsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	before, _ := a.deps.LogDrains.Get(r.Context(), r.PathValue("id"))
	if err := a.deps.LogDrains.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.deps.Log.Error("deleting a log drain", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the drain")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionLogDrainDeleted,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), before.Name),
	})
	w.WriteHeader(http.StatusNoContent)
}

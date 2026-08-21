package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// Project shared variables (shared-variables.md §7).
//
// Unlike a Notifier's config_hint, a shared variable carries NO masked summary:
// it is already identified by its key, so a hint would be gratuitous partial
// disclosure (§6). There is therefore no field on this DTO — and no code path in
// this file — that could carry a value, and nothing here unseals anything.

type sharedVariableDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// EnvironmentID is null at project scope.
	EnvironmentID *string `json:"environment_id"`
	// EnvironmentName is empty at project scope; the UI writes the word
	// "project" there rather than an id nobody reads.
	EnvironmentName string `json:"environment_name"`
	Key             string `json:"key"`
	// UsedByCount is scope-accurate: an application whose environment defines a
	// shadowing row of the same key does not use this variable and is not
	// counted (§7).
	UsedByCount int       `json:"used_by_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toSharedVariableDTO(v sharedvars.View) sharedVariableDTO {
	return sharedVariableDTO{
		ID:              v.Variable.ID,
		ProjectID:       v.Variable.ProjectID,
		EnvironmentID:   v.Variable.EnvironmentID,
		EnvironmentName: v.EnvironmentName,
		Key:             v.Variable.Key,
		UsedByCount:     v.UsedByCount,
		CreatedAt:       v.Variable.CreatedAt,
		UpdatedAt:       v.Variable.UpdatedAt,
	}
}

type sharedVariableUsageDTO struct {
	ApplicationID   string `json:"application_id"`
	ApplicationName string `json:"application_name"`
	EnvironmentName string `json:"environment_name"`
	// RedeployPending is derived, never stored (§5): this variable changed
	// after the environment the application is running was frozen.
	RedeployPending bool `json:"redeploy_pending"`
}

func toSharedVariableUsageDTO(u domain.SharedVariableUsage) sharedVariableUsageDTO {
	return sharedVariableUsageDTO{
		ApplicationID:   u.ApplicationID,
		ApplicationName: u.ApplicationName,
		EnvironmentName: u.EnvironmentName,
		RedeployPending: u.RedeployPending,
	}
}

type createSharedVariableRequest struct {
	Key string `json:"key"`
	// EnvironmentID omitted or null means project scope.
	EnvironmentID *string `json:"environment_id"`
	Value         string  `json:"value"`
}

func (a *API) handleCreateSharedVariable(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeError(w, http.StatusNotImplemented, "shared variables are not enabled")
		return
	}
	var req createSharedVariableRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	v, err := a.deps.SharedVariables.Create(r.Context(), r.PathValue("id"), sharedvars.CreateInput{
		Key:           req.Key,
		EnvironmentID: req.EnvironmentID,
		Value:         req.Value,
	})
	if err != nil {
		a.writeSharedVariableError(w, "creating shared variable", err)
		return
	}
	writeJSON(w, http.StatusCreated, toSharedVariableDTO(v))
}

func (a *API) handleListSharedVariables(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeJSON(w, http.StatusOK, []sharedVariableDTO{})
		return
	}
	list, err := a.deps.SharedVariables.ListViews(r.Context(), r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("listing shared variables", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list shared variables")
		return
	}
	out := make([]sharedVariableDTO, 0, len(list))
	for _, v := range list {
		out = append(out, toSharedVariableDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetSharedVariable(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForSharedVariable(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeError(w, http.StatusNotFound, "shared variable not found")
		return
	}
	v, err := a.deps.SharedVariables.View(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeSharedVariableError(w, "getting shared variable", err)
		return
	}
	writeJSON(w, http.StatusOK, toSharedVariableDTO(v))
}

// patchSharedVariableRequest carries the only mutable field. Key and scope are
// immutable after create: changing either would silently re-point or orphan
// every referencing application, so delete-and-recreate is the explicit path
// and the delete guard fires (§7).
type patchSharedVariableRequest struct {
	Value string `json:"value"`
}

func (a *API) handlePatchSharedVariable(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForSharedVariable(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeError(w, http.StatusNotFound, "shared variable not found")
		return
	}
	var req patchSharedVariableRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	v, err := a.deps.SharedVariables.SetValue(r.Context(), r.PathValue("id"), req.Value)
	if err != nil {
		a.writeSharedVariableError(w, "updating shared variable", err)
		return
	}
	writeJSON(w, http.StatusOK, toSharedVariableDTO(v))
}

func (a *API) handleDeleteSharedVariable(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForSharedVariable(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeError(w, http.StatusNotFound, "shared variable not found")
		return
	}
	if err := a.deps.SharedVariables.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.writeSharedVariableError(w, "deleting shared variable", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListSharedVariableUsage(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForSharedVariable(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.SharedVariables == nil {
		writeJSON(w, http.StatusOK, []sharedVariableUsageDTO{})
		return
	}
	usage, err := a.deps.SharedVariables.UsedBy(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeSharedVariableError(w, "listing shared variable usage", err)
		return
	}
	out := make([]sharedVariableUsageDTO, 0, len(usage))
	for _, u := range usage {
		out = append(out, toSharedVariableUsageDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// redeployPending is the per-application drift marker for the Application DTO
// (§5). Best-effort by design: the marker is derived state, so a failure to
// derive it must never fail a request that is otherwise answerable — it reports
// "not pending" and logs, which is the same thing the panel showed before this
// feature existed.
func (a *API) redeployPending(ctx context.Context, appID string) bool {
	if a.deps.SharedVariables == nil {
		return false
	}
	pending, err := a.deps.SharedVariables.RedeployPending(ctx, appID)
	if err != nil {
		a.deps.Log.Warn("deriving redeploy-pending", "app_id", appID, "error", err)
		return false
	}
	return pending
}

// redeployPendingSet answers the same for a whole environment in one query, so
// a list screen costs one extra round trip rather than one per row.
func (a *API) redeployPendingSet(ctx context.Context, envID string) map[string]bool {
	if a.deps.SharedVariables == nil {
		return nil
	}
	set, err := a.deps.SharedVariables.PendingInEnvironment(ctx, envID)
	if err != nil {
		a.deps.Log.Warn("deriving redeploy-pending", "environment_id", envID, "error", err)
		return nil
	}
	return set
}

// writeSharedVariableError maps service errors to status codes.
func (a *API) writeSharedVariableError(w http.ResponseWriter, op string, err error) {
	var (
		ve    *sharedvars.ValidationError
		inUse *sharedvars.InUseError
	)
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.As(err, &inUse):
		// Named, so the operator can go and remove the references — there is no
		// force override (§7).
		writeError(w, http.StatusConflict,
			"still referenced by "+strings.Join(inUse.Applications, ", ")+" — remove the references first")
	case errors.Is(err, sharedvars.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "shared variable not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a shared variable with that key already exists in this scope")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}

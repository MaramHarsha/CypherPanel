package rest

// Front-door access control (app-access-control.md §9). Member rank, like every
// other application-shaped change: an operator who may deploy the app may
// decide who reaches it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type appAccessRequest struct {
	IPAllowlistEnabled bool     `json:"ip_allowlist_enabled"`
	IPAllowlist        []string `json:"ip_allowlist"`
}

type previewPasswordRequest struct {
	// Empty turns the gate off and forgets the hash.
	Passphrase string `json:"passphrase"`
}

type accessDTO struct {
	IPAllowlistEnabled bool     `json:"ip_allowlist_enabled"`
	IPAllowlist        []string `json:"ip_allowlist"`
	// The passphrase is never here. Only whether one is set, and when — which
	// is what an operator actually needs to answer "is this still the one I
	// gave the client in March?".
	PreviewPasswordEnabled bool    `json:"preview_password_enabled"`
	PreviewPasswordSetAt   *string `json:"preview_password_set_at"`
}

func toAccessDTO(a domain.Application) accessDTO {
	list := a.Access.IPAllowlist
	if list == nil {
		list = []string{}
	}
	return accessDTO{
		IPAllowlistEnabled:     a.Access.IPAllowlistEnabled,
		IPAllowlist:            list,
		PreviewPasswordEnabled: a.Access.PreviewPasswordEnabled,
		PreviewPasswordSetAt:   formatTime(a.Access.PreviewPasswordSetAt),
	}
}

func (a *API) handleGetApplicationAccess(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	app, err := a.deps.Applications.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("reading access policy", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the access policy")
		return
	}
	writeJSON(w, http.StatusOK, toAccessDTO(app))
}

// handleSetApplicationAccess replaces the allowlist wholesale. Wholesale rather
// than per-entry because the list IS the policy: adding and removing one CIDR
// at a time through two routes would make "what does this allow right now" a
// question with two answers mid-edit.
func (a *API) handleSetApplicationAccess(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, id)
	}) {
		return
	}
	var req appAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	entry := a.appAuditEntry(r.Context(), audit.ActionApplicationAccessChanged, id, map[string]any{
		"ip_allowlist_enabled": req.IPAllowlistEnabled,
		// The count, not the CIDRs: an audit entry records the fact of a change
		// (threat-model §5.15), and a list of the networks that reach a private
		// admin panel is not something to copy into a second table.
		"ip_allowlist_entries": len(req.IPAllowlist),
	})

	app, err := a.deps.Applications.SetAllowlist(r.Context(), id, req.IPAllowlistEnabled, req.IPAllowlist)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "application not found")
		return
	case errors.Is(err, applications.ErrInvalidCIDR), errors.Is(err, applications.ErrAllowlistEmpty):
		a.auditFailed(r, entry, err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	default:
		a.deps.Log.Error("setting allowlist", "app_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the access policy")
		return
	}
	a.audit(r, entry)
	a.nudgeFleet(r.Context(), "access-control")
	writeJSON(w, http.StatusOK, toAccessDTO(app))
}

// handleSetPreviewPassword sets or rotates the passphrase. It is returned in
// this response and never again — the plaintext exists in the operator's
// clipboard and nowhere else.
func (a *API) handleSetPreviewPassword(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, id)
	}) {
		return
	}
	var req previewPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Passphrase == "" {
		entry := a.appAuditEntry(r.Context(), audit.ActionApplicationAccessChanged, id, map[string]any{"preview_password": "cleared"})
		app, err := a.deps.Applications.ClearPreviewPassword(r.Context(), id)
		if err != nil {
			a.deps.Log.Error("clearing preview password", "app_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "could not clear the passphrase")
			return
		}
		a.audit(r, entry)
		a.nudgeFleet(r.Context(), "access-control")
		writeJSON(w, http.StatusOK, toAccessDTO(app))
		return
	}

	entry := a.appAuditEntry(r.Context(), audit.ActionApplicationAccessChanged, id, map[string]any{"preview_password": "set"})
	app, err := a.deps.Applications.SetPreviewPassword(r.Context(), id, req.Passphrase)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "application not found")
		return
	case errors.Is(err, applications.ErrPasswordTooShort):
		a.auditFailed(r, entry, err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	default:
		a.deps.Log.Error("setting preview password", "app_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not set the passphrase")
		return
	}
	a.audit(r, entry)
	a.nudgeFleet(r.Context(), "access-control")

	// Shown exactly once. Everything else about the policy comes back too, so
	// the caller does not need a second request to render the result.
	writeJSON(w, http.StatusOK, struct {
		accessDTO
		Passphrase string `json:"passphrase"`
	}{accessDTO: toAccessDTO(app), Passphrase: req.Passphrase})
}

// nudgeFleet asks agents to re-read desired state after a change that is not a
// deploy. Failure is logged and swallowed: the policy is already persisted, so
// the worst case is that it applies at the next reconcile rather than now
// (rule 15 — persist before publish).
func (a *API) nudgeFleet(ctx context.Context, reason string) {
	if a.deps.Scheduler == nil {
		return
	}
	if err := a.deps.Scheduler.RequestResync(ctx, reason); err != nil {
		a.deps.Log.Warn("nudging fleet after an access change", "reason", reason, "error", err)
	}
}

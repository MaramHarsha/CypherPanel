package rest

// Revision promotion (revision-promotion.md §6): ship the artifact that was
// tested, rather than rebuilding one that should be the same.

import (
	"context"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type promoteRequest struct {
	TargetApplicationID string `json:"target_application_id"`
}

// handlePlanPromotion answers exactly what would change. It writes nothing,
// which is why it is a GET — and it is the whole content of the screen, so an
// operator decides from facts rather than from a confirmation dialog.
func (a *API) handlePlanPromotion(w http.ResponseWriter, r *http.Request) {
	if a.deps.Scheduler == nil {
		writeError(w, http.StatusNotImplemented, "promotion is not enabled on this panel")
		return
	}
	user, _ := userFromContext(r.Context())
	targetID := r.URL.Query().Get("target_application_id")
	if targetID == "" {
		writeError(w, http.StatusBadRequest, "name the application to promote to")
		return
	}
	// BOTH ends are authorized: reading the source's revision is one project's
	// business and deploying to the target is another's, even when they are the
	// same project. Checking only one would let a member of one see the other's
	// environment-variable key names.
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForRevision(ctx, r.PathValue("id"))
	}) {
		return
	}
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, targetID)
	}) {
		return
	}
	plan, err := a.deps.Promotion.PlanPromotion(r.Context(), r.PathValue("id"), targetID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("planning a promotion", "revision_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not plan the promotion")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handlePromote ships it. Deploy rank on the TARGET, because a promotion is a
// deploy to the target and nothing else — the source is only read.
func (a *API) handlePromote(w http.ResponseWriter, r *http.Request) {
	if a.deps.Promotion == nil {
		writeError(w, http.StatusNotImplemented, "promotion is not enabled on this panel")
		return
	}
	var req promoteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TargetApplicationID == "" {
		writeError(w, http.StatusBadRequest, "name the application to promote to")
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForRevision(ctx, r.PathValue("id"))
	}) {
		return
	}
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, req.TargetApplicationID)
	}) {
		return
	}

	dep, err := a.deps.Promotion.Promote(r.Context(), r.PathValue("id"), req.TargetApplicationID, user.Email)
	if writeIfFrozen(w, err) {
		return
	}
	var notPromotable *scheduler.ErrNotPromotable
	if errors.As(err, &notPromotable) {
		writeError(w, http.StatusBadRequest, notPromotable.Detail)
		return
	}
	if err != nil {
		a.deps.Log.Error("promoting a revision", "revision_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the promotion")
		return
	}
	a.audit(r, a.appAuditEntry(r.Context(), audit.ActionRevisionPromoted, req.TargetApplicationID, map[string]any{
		"from_revision": r.PathValue("id"), "deployment": dep.ID,
	}))
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(dep))
}

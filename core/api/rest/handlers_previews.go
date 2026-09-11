package rest

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// previewDTO is the public view of a preview environment (preview-environments.md §7).
type previewDTO struct {
	ID            string  `json:"id"`
	SourceAppID   string  `json:"source_app_id"`
	EnvironmentID string  `json:"environment_id"`
	PreviewAppID  *string `json:"preview_app_id"`
	PRNumber      int     `json:"pr_number"`
	PRBranch      string  `json:"pr_branch"`
	Domain        string  `json:"domain"`
	Status        string  `json:"status"`
	ExpiresAt     *string `json:"expires_at"`
	CreatedAt     string  `json:"created_at"`
}

func toPreviewDTO(p domain.Preview) previewDTO {
	dto := previewDTO{
		ID:            p.ID,
		SourceAppID:   p.SourceAppID,
		EnvironmentID: p.EnvironmentID,
		PreviewAppID:  p.PreviewAppID,
		PRNumber:      p.PRNumber,
		PRBranch:      p.PRBranch,
		Domain:        p.Domain,
		Status:        p.Status,
		CreatedAt:     p.CreatedAt.UTC().Format(time.RFC3339),
	}
	if p.ExpiresAt != nil {
		s := p.ExpiresAt.UTC().Format(time.RFC3339)
		dto.ExpiresAt = &s
	}
	return dto
}

func (a *API) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.Previews == nil {
		writeJSON(w, http.StatusOK, []previewDTO{})
		return
	}
	list, err := a.deps.Previews.List(r.Context(), r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("listing previews", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list previews")
		return
	}
	out := make([]previewDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toPreviewDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetPreview(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForPreview(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.Previews == nil {
		writeError(w, http.StatusNotFound, "preview not found")
		return
	}
	p, err := a.deps.Previews.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "preview not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting preview", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get preview")
		return
	}
	writeJSON(w, http.StatusOK, toPreviewDTO(p))
}

// handleDeletePreview tears a preview down manually (same destroy path as the
// PR-closed event and the TTL sweeper). 202: teardown is asynchronous.
func (a *API) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForPreview(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.Previews == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	previewID := r.PathValue("id")
	// Snapshot the chain BEFORE the teardown: destroying a preview deletes the
	// environment the audit row would otherwise resolve its team from, and an
	// entry that cannot be scoped cannot be read by the team it belongs to
	// (audit-log.md §4). The automated teardowns — a closed PR, the TTL
	// sweeper — record themselves inside core/previews with a system actor;
	// this one has an operator to name.
	preview, _ := a.deps.Previews.Get(r.Context(), previewID)
	projectID, _ := a.projectIDForPreview(r.Context(), previewID)
	if err := a.deps.Previews.DestroyByID(r.Context(), previewID); err != nil {
		a.deps.Log.Error("destroying preview", "error", err)
		writeError(w, http.StatusInternalServerError, "could not destroy preview")
		return
	}
	a.audit(r, audit.Entry{
		Action:    audit.ActionEnvironmentDeleted,
		Resource:  audit.Resource(audit.ResourceEnvironment, preview.EnvironmentID, preview.Domain),
		ProjectID: projectID,
		Detail: map[string]any{
			"kind":       domain.EnvPreview,
			"preview_id": previewID,
			"pr":         preview.PRNumber,
			"reason":     "operator request",
		},
	})
	w.WriteHeader(http.StatusAccepted)
}

package rest

// Resource quotas (resource-quotas.md §9; permitted by ADR-012).
//
// A QUOTA IS A GUARDRAIL, NOT A METER. There is no price, no rate, no currency
// and no tier anywhere in this surface — a quota is denominated in bytes and in
// counts, and an operator who wants a bill multiplies our numbers by their own,
// off the panel.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/quota"
)

// QuotaService is the quota surface (consumer-defined).
type QuotaService interface {
	Set(ctx context.Context, scopeKind, scopeID string, q domain.ResourceQuota, actor string) (domain.ResourceQuota, error)
	Delete(ctx context.Context, scopeKind, scopeID string) error
	List(ctx context.Context) ([]domain.ResourceQuota, error)
	Report(ctx context.Context, scopeKind, scopeID string) (domain.QuotaReport, error)
}

type quotaDTO struct {
	ID        string `json:"id"`
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id"`
	// A nil limit is UNCAPPED, which reads as "no cap" rather than as zero.
	MemoryLimitBytes *int64 `json:"memory_limit_bytes"`
	DiskLimitBytes   *int64 `json:"disk_limit_bytes"`
	PreviewLimit     *int   `json:"preview_limit"`
	UpdatedBy        string `json:"updated_by"`
}

type setQuotaRequest struct {
	MemoryLimitBytes *int64 `json:"memory_limit_bytes"`
	DiskLimitBytes   *int64 `json:"disk_limit_bytes"`
	PreviewLimit     *int   `json:"preview_limit"`
}

func toQuotaDTO(q domain.ResourceQuota) quotaDTO {
	kind, id := q.Scope()
	return quotaDTO{
		ID: q.ID, ScopeKind: kind, ScopeID: id,
		MemoryLimitBytes: q.MemoryLimitBytes, DiskLimitBytes: q.DiskLimitBytes,
		PreviewLimit: q.PreviewLimit, UpdatedBy: q.UpdatedBy,
	}
}

func (a *API) quotasReady(w http.ResponseWriter) bool {
	if a.deps.Quotas == nil {
		writeError(w, http.StatusNotImplemented, "resource quotas are not enabled on this panel")
		return false
	}
	return true
}

// writeQuotaError maps the two refusals that are the operator's to fix.
func (a *API) writeQuotaError(w http.ResponseWriter, err error) {
	var invalid *quota.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Msg)
		return
	}
	// A memory cap over a scope containing a resource with no declared limit is
	// a fiction, so it is refused NAMING them: a capability is checked when it
	// is attached, not when it is spent.
	var unlimited *quota.UnlimitedError
	if errors.As(err, &unlimited) {
		writeError(w, http.StatusConflict, unlimited.Error()+
			" — give each one a memory limit first, and the panel can then hold the whole scope to a number")
		return
	}
	a.deps.Log.Error("quota", "error", err)
	writeError(w, http.StatusInternalServerError, "could not save the quota")
}

func (a *API) handleGetProjectQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleMember) {
		return
	}
	report, err := a.deps.Quotas.Report(r.Context(), domain.QuotaScopeProject, projectID)
	if err != nil {
		a.deps.Log.Error("reading the quota report", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the usage")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (a *API) handleSetProjectQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	// TEAM ADMIN: capping what a project may consume is a decision about the
	// fleet's shared capacity, not about the project's own code.
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleAdmin) {
		return
	}
	var req setQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	q, err := a.deps.Quotas.Set(r.Context(), domain.QuotaScopeProject, projectID, domain.ResourceQuota{
		MemoryLimitBytes: req.MemoryLimitBytes, DiskLimitBytes: req.DiskLimitBytes,
		PreviewLimit: req.PreviewLimit,
	}, user.Email)
	if err != nil {
		a.writeQuotaError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:    audit.ActionQuotaSet,
		Resource:  audit.Resource(audit.ResourceProject, projectID, "quota"),
		ProjectID: projectID,
		Detail: map[string]any{
			"memory_limit_bytes": req.MemoryLimitBytes, "disk_limit_bytes": req.DiskLimitBytes,
			"preview_limit": req.PreviewLimit,
		},
	})
	writeJSON(w, http.StatusOK, toQuotaDTO(q))
}

func (a *API) handleDeleteProjectQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleAdmin) {
		return
	}
	if err := a.deps.Quotas.Delete(r.Context(), domain.QuotaScopeProject, projectID); err != nil {
		a.writeQuotaError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:    audit.ActionQuotaRemoved,
		Resource:  audit.Resource(audit.ResourceProject, projectID, "quota"),
		ProjectID: projectID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleGetTeamQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requireTeamRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	report, err := a.deps.Quotas.Report(r.Context(), domain.QuotaScopeTeam, r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("reading the quota report", "team_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the usage")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (a *API) handleSetTeamQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleAdmin) {
		return
	}
	var req setQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	q, err := a.deps.Quotas.Set(r.Context(), domain.QuotaScopeTeam, teamID, domain.ResourceQuota{
		MemoryLimitBytes: req.MemoryLimitBytes, DiskLimitBytes: req.DiskLimitBytes,
		PreviewLimit: req.PreviewLimit,
	}, user.Email)
	if err != nil {
		a.writeQuotaError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionQuotaSet,
		Resource: audit.Resource(audit.ResourceTeam, teamID, "quota"),
		Detail: map[string]any{
			"memory_limit_bytes": req.MemoryLimitBytes, "disk_limit_bytes": req.DiskLimitBytes,
			"preview_limit": req.PreviewLimit,
		},
	})
	writeJSON(w, http.StatusOK, toQuotaDTO(q))
}

func (a *API) handleDeleteTeamQuota(w http.ResponseWriter, r *http.Request) {
	if !a.quotasReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleAdmin) {
		return
	}
	if err := a.deps.Quotas.Delete(r.Context(), domain.QuotaScopeTeam, teamID); err != nil {
		a.writeQuotaError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionQuotaRemoved,
		Resource: audit.Resource(audit.ResourceTeam, teamID, "quota"),
	})
	w.WriteHeader(http.StatusNoContent)
}

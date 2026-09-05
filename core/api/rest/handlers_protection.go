package rest

// Deploy protection (deploy-protection.md §6): the policy document, the
// approval queue, the two decision routes and the recorded freeze override.
//
// Authorization is the table in §5, enforced with authz.go unchanged: reads
// need team member, PUT needs team admin, a decision needs the rank the
// approval snapshotted, and break glass needs team owner. Every route that can
// let a deploy through — approve, reject, break glass, and the PUT that can
// switch the whole control off — is additionally sessionOnly, so no API token
// can reach them however broad its abilities.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/protection"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// ─── DTOs ───────────────────────────────────────────────────────────────────

type freezeWindowDTO struct {
	ID          string `json:"id"`
	StartDOW    int    `json:"start_dow"`
	StartMinute int    `json:"start_minute"`
	EndDOW      int    `json:"end_dow"`
	EndMinute   int    `json:"end_minute"`
	Timezone    string `json:"timezone"`
	// Summary is the rendered sentence — "Fri 18:00 → Mon 08:00
	// (Europe/Berlin)" — composed on the plane so the panel and a CLI print
	// the same words (CLAUDE.md rule 4).
	Summary string `json:"summary"`
}

type environmentProtectionDTO struct {
	EnvironmentID   string            `json:"environment_id"`
	RequireApproval bool              `json:"require_approval"`
	MinApproverRole string            `json:"min_approver_role"`
	FreezeEnabled   bool              `json:"freeze_enabled"`
	Windows         []freezeWindowDTO `json:"windows"`
	CreatedAt       *time.Time        `json:"created_at,omitempty"`
	UpdatedAt       *time.Time        `json:"updated_at,omitempty"`
}

func toProtectionDTO(p domain.EnvironmentProtection) environmentProtectionDTO {
	out := environmentProtectionDTO{
		EnvironmentID:   p.EnvironmentID,
		RequireApproval: p.RequireApproval,
		MinApproverRole: p.MinApproverRole,
		FreezeEnabled:   p.FreezeEnabled,
		Windows:         make([]freezeWindowDTO, 0, len(p.Windows)),
	}
	for _, w := range p.Windows {
		out.Windows = append(out.Windows, freezeWindowDTO{
			ID:          w.ID,
			StartDOW:    int(w.StartDOW),
			StartMinute: w.StartMinute,
			EndDOW:      int(w.EndDOW),
			EndMinute:   w.EndMinute,
			Timezone:    w.Timezone,
			Summary:     protection.Describe(w),
		})
	}
	// A default document has never been written, so it carries no timestamps:
	// inventing them would claim a row exists.
	if !p.CreatedAt.IsZero() {
		c, u := p.CreatedAt, p.UpdatedAt
		out.CreatedAt, out.UpdatedAt = &c, &u
	}
	return out
}

type deployApprovalDTO struct {
	DeploymentID     string     `json:"deployment_id"`
	EnvironmentID    string     `json:"environment_id"`
	RequestedBy      string     `json:"requested_by"`
	RequestedByEmail string     `json:"requested_by_email"`
	RequiredRole     string     `json:"required_role"`
	State            string     `json:"state"`
	DecidedBy        string     `json:"decided_by"`
	DecidedByEmail   string     `json:"decided_by_email"`
	DecidedAt        *time.Time `json:"decided_at"`
	Reason           string     `json:"reason"`
	CreatedAt        time.Time  `json:"created_at"`
}

func toApprovalDTO(a domain.DeployApproval) deployApprovalDTO {
	return deployApprovalDTO{
		DeploymentID:     a.DeploymentID,
		EnvironmentID:    a.EnvironmentID,
		RequestedBy:      a.RequestedBy,
		RequestedByEmail: a.RequestedByEmail,
		RequiredRole:     a.RequiredRole,
		State:            a.State,
		DecidedBy:        a.DecidedBy,
		DecidedByEmail:   a.DecidedByEmail,
		DecidedAt:        a.DecidedAt,
		Reason:           a.Reason,
		CreatedAt:        a.CreatedAt,
	}
}

type breakGlassGrantDTO struct {
	ID            string    `json:"id"`
	EnvironmentID string    `json:"environment_id"`
	OpenedBy      string    `json:"opened_by"`
	OpenedByEmail string    `json:"opened_by_email"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	// Active is derived against the plane's clock at response time, so a
	// screen needs no clock of its own to know whether the override still
	// applies.
	Active bool `json:"active"`
}

// ─── The protection document ────────────────────────────────────────────────

func (a *API) handleGetEnvironmentProtection(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		// Protection is not wired, so nothing is protected — which is exactly
		// what the default document says.
		writeJSON(w, http.StatusOK, toProtectionDTO(domain.DefaultProtection(envID)))
		return
	}
	p, err := a.deps.Protection.Get(r.Context(), envID)
	if err != nil {
		a.writeProtectionError(w, "get deploy protection", err)
		return
	}
	writeJSON(w, http.StatusOK, toProtectionDTO(p))
}

// setProtectionRequest is the WHOLE document. Every field is a pointer or a
// slice the handler checks for presence, because a wholesale PUT that silently
// treated an omitted flag as false would turn a forgotten field into "approval
// is now off" and answer 200.
type setProtectionRequest struct {
	RequireApproval *bool                    `json:"require_approval"`
	MinApproverRole *string                  `json:"min_approver_role"`
	FreezeEnabled   *bool                    `json:"freeze_enabled"`
	Windows         *[]freezeWindowInputBody `json:"windows"`
}

type freezeWindowInputBody struct {
	StartDOW    int    `json:"start_dow"`
	StartMinute int    `json:"start_minute"`
	EndDOW      int    `json:"end_dow"`
	EndMinute   int    `json:"end_minute"`
	Timezone    string `json:"timezone"`
}

func (a *API) handleSetEnvironmentProtection(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleAdmin, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		writeError(w, http.StatusNotImplemented, "deploy protection is not enabled")
		return
	}
	var req setProtectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch {
	case req.RequireApproval == nil:
		writeError(w, http.StatusBadRequest, "require_approval is required — this route replaces the whole document")
		return
	case req.MinApproverRole == nil:
		// Omitting it is the same class of silent rewrite the other three
		// checks exist to prevent, just in the tightening direction: a document
		// that read `member` would come back `owner` and answer 200.
		writeError(w, http.StatusBadRequest, "min_approver_role is required — this route replaces the whole document")
		return
	case req.FreezeEnabled == nil:
		writeError(w, http.StatusBadRequest, "freeze_enabled is required — this route replaces the whole document")
		return
	case req.Windows == nil:
		writeError(w, http.StatusBadRequest, "windows is required — send an empty array to clear the freeze calendar")
		return
	}
	doc := protection.Document{
		RequireApproval: *req.RequireApproval,
		MinApproverRole: *req.MinApproverRole,
		FreezeEnabled:   *req.FreezeEnabled,
		Windows:         make([]protection.WindowInput, 0, len(*req.Windows)),
	}
	for _, in := range *req.Windows {
		doc.Windows = append(doc.Windows, protection.WindowInput{
			StartDOW:    time.Weekday(in.StartDOW),
			StartMinute: in.StartMinute,
			EndDOW:      time.Weekday(in.EndDOW),
			EndMinute:   in.EndMinute,
			Timezone:    in.Timezone,
		})
	}
	p, err := a.deps.Protection.Set(r.Context(), envID, doc)
	if err != nil {
		a.writeProtectionError(w, "set deploy protection", err)
		return
	}
	writeJSON(w, http.StatusOK, toProtectionDTO(p))
}

// ─── Approvals ──────────────────────────────────────────────────────────────

func (a *API) handleListDeployApprovals(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		writeJSON(w, http.StatusOK, []deployApprovalDTO{})
		return
	}
	// The screens want the queue, so pending is the default; "all" is the
	// explicit word for no filter, because an empty string in a URL is
	// indistinguishable from a client that forgot the parameter.
	state := r.URL.Query().Get("state")
	if state == "" {
		state = domain.ApprovalPending
	}
	if state == "all" {
		state = ""
	}
	list, err := a.deps.Protection.Approvals(r.Context(), envID, state)
	if err != nil {
		a.writeProtectionError(w, "list deploy approvals", err)
		return
	}
	out := make([]deployApprovalDTO, 0, len(list))
	for _, ap := range list {
		out = append(out, toApprovalDTO(ap))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetDeployApproval(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	depID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDeployment(ctx, depID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		writeError(w, http.StatusNotFound, "this deployment was not gated")
		return
	}
	ap, err := a.deps.Protection.ApprovalFor(r.Context(), depID)
	if err != nil {
		a.writeProtectionError(w, "get deploy approval", err)
		return
	}
	writeJSON(w, http.StatusOK, toApprovalDTO(ap))
}

// authorizeDecision is the shared front half of approve and reject: the caller
// must be a member of the project (a non-member gets 404, never a hint that the
// deployment exists), and then must rank at or above the role the approval
// SNAPSHOTTED — not the environment's current policy, which may have been
// relaxed since the deploy parked.
func (a *API) authorizeDecision(w http.ResponseWriter, r *http.Request, user domain.User) (domain.DeployApproval, bool) {
	depID := r.PathValue("id")
	// Resolved once and reused for both rank checks: the membership gate that
	// makes a stranger's request a 404, and the snapshotted-rank gate after the
	// approval is loaded.
	projectID, err := a.projectIDForDeployment(r.Context(), depID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return domain.DeployApproval{}, false
		}
		a.deps.Log.Error("resolving deployment project", "deployment_id", depID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return domain.DeployApproval{}, false
	}
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleMember) {
		return domain.DeployApproval{}, false
	}
	if a.deps.Protection == nil {
		writeError(w, http.StatusNotFound, "this deployment was not gated")
		return domain.DeployApproval{}, false
	}
	ap, err := a.deps.Protection.ApprovalFor(r.Context(), depID)
	if err != nil {
		a.writeProtectionError(w, "get deploy approval", err)
		return domain.DeployApproval{}, false
	}
	if !a.requireProjectRole(w, r, user, projectID, ap.RequiredRole) {
		return domain.DeployApproval{}, false
	}
	return ap, true
}

func (a *API) handleApproveDeployment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if _, ok := a.authorizeDecision(w, r, user); !ok {
		return
	}
	dep, _, err := a.deps.Protection.Approve(r.Context(), r.PathValue("id"), user)
	if err != nil {
		if dep.ID != "" && errors.Is(err, scheduler.ErrRevisionNotBuilt) {
			// The decision stands; the pipeline could not start. The record
			// carries the reason, so return it rather than an opaque 500.
			writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
			return
		}
		a.writeProtectionError(w, "approve deployment", err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
}

type rejectDeploymentRequest struct {
	Reason string `json:"reason"`
}

func (a *API) handleRejectDeployment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if _, ok := a.authorizeDecision(w, r, user); !ok {
		return
	}
	var req rejectDeploymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dep, _, err := a.deps.Protection.Reject(r.Context(), r.PathValue("id"), req.Reason, user)
	if err != nil {
		a.writeProtectionError(w, "reject deployment", err)
		return
	}
	writeJSON(w, http.StatusOK, a.withApproval(r.Context(), dep))
}

// ─── Break glass ────────────────────────────────────────────────────────────

type breakGlassRequest struct {
	Reason string `json:"reason"`
}

func (a *API) handleOpenBreakGlass(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleOwner, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		writeError(w, http.StatusNotImplemented, "deploy protection is not enabled")
		return
	}
	var req breakGlassRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	g, err := a.deps.Protection.OpenBreakGlass(r.Context(), envID, user, req.Reason)
	if err != nil {
		a.writeProtectionError(w, "open break glass", err)
		return
	}
	writeJSON(w, http.StatusCreated, a.toGrantDTO(g))
}

func (a *API) handleListBreakGlassGrants(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	if a.deps.Protection == nil {
		writeJSON(w, http.StatusOK, []breakGlassGrantDTO{})
		return
	}
	list, err := a.deps.Protection.BreakGlassGrants(r.Context(), envID)
	if err != nil {
		a.writeProtectionError(w, "list break-glass grants", err)
		return
	}
	out := make([]breakGlassGrantDTO, 0, len(list))
	for _, g := range list {
		out = append(out, a.toGrantDTO(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// toGrantDTO derives `active` against the service's own clock, so the response
// and the gate agree about what "still open" means.
func (a *API) toGrantDTO(g domain.BreakGlassGrant) breakGlassGrantDTO {
	return breakGlassGrantDTO{
		ID:            g.ID,
		EnvironmentID: g.EnvironmentID,
		OpenedBy:      g.OpenedBy,
		OpenedByEmail: g.OpenedByEmail,
		Reason:        g.Reason,
		CreatedAt:     g.CreatedAt,
		ExpiresAt:     g.ExpiresAt,
		Active:        g.Active(a.deps.Protection.Now()),
	}
}

// ─── errors ─────────────────────────────────────────────────────────────────

// writeProtectionError maps service errors to status codes.
func (a *API) writeProtectionError(w http.ResponseWriter, op string, err error) {
	var ve *protection.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, protection.ErrSelfApproval):
		writeError(w, http.StatusForbidden,
			"you asked for this deploy — someone else at or above the required role has to approve it")
	case errors.Is(err, protection.ErrAlreadyDecided), errors.Is(err, scheduler.ErrNotParked):
		writeError(w, http.StatusConflict, "this deploy has already been approved or rejected")
	case errors.Is(err, protection.ErrPreviewProtection):
		writeError(w, http.StatusConflict,
			"preview environments cannot be protected — freezing them would strand every open pull request")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}

package rest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/previews"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

type deploymentDTO struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	RevisionID    string `json:"revision_id"`
	Status        string `json:"status"`
	Trigger       string `json:"trigger"`
	Detail        string `json:"detail"`
	// Approval is the gate decision on this deployment, present only when it
	// was gated (deploy-protection.md §6). It is what lets the drawer render
	// the pending card — who asked, which rank must decide, and, once decided,
	// who decided and why — without a second request. Additive and optional:
	// an ungated deployment omits it entirely (ENGINEERING rule 17).
	Approval   *deployApprovalDTO `json:"approval,omitempty"`
	CreatedAt  string             `json:"created_at"`
	FinishedAt *string            `json:"finished_at"`
}

func toDeploymentDTO(d domain.Deployment) deploymentDTO {
	dto := deploymentDTO{
		ID:            d.ID,
		ApplicationID: d.ApplicationID,
		RevisionID:    d.RevisionID,
		Status:        string(d.Status),
		Trigger:       d.Trigger,
		Detail:        d.Detail,
		CreatedAt:     d.CreatedAt.UTC().Format(time.RFC3339),
	}
	if d.FinishedAt != nil {
		s := d.FinishedAt.UTC().Format(time.RFC3339)
		dto.FinishedAt = &s
	}
	return dto
}

// withApproval renders one deployment with its gate decision attached.
// Best-effort by design: the summary is a read model, so a failure to load it
// must never fail a request that is otherwise answerable — the deployment comes
// back without it, which is exactly what an ungated deployment looks like.
func (a *API) withApproval(ctx context.Context, d domain.Deployment) deploymentDTO {
	dto := toDeploymentDTO(d)
	if a.deps.Protection == nil {
		return dto
	}
	ap, err := a.deps.Protection.ApprovalFor(ctx, d.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.deps.Log.Warn("loading deploy approval", "deployment_id", d.ID, "error", err)
		}
		return dto
	}
	summary := toApprovalDTO(ap)
	dto.Approval = &summary
	return dto
}

// withApprovals renders a whole list, attaching gate decisions in ONE round
// trip rather than one lookup per row.
func (a *API) withApprovals(ctx context.Context, appID string, list []domain.Deployment) []deploymentDTO {
	var approvals map[string]domain.DeployApproval
	if a.deps.Protection != nil && len(list) > 0 {
		// Only the ids on this page: decorating one bounded page with every
		// gate decision the application has ever had would grow with its whole
		// history rather than with the answer.
		ids := make([]string, 0, len(list))
		for _, d := range list {
			ids = append(ids, d.ID)
		}
		var err error
		if approvals, err = a.deps.Protection.ApprovalsForApplication(ctx, appID, ids); err != nil {
			a.deps.Log.Warn("loading deploy approvals", "app_id", appID, "error", err)
		}
	}
	out := make([]deploymentDTO, 0, len(list))
	for _, d := range list {
		dto := toDeploymentDTO(d)
		if ap, ok := approvals[d.ID]; ok {
			summary := toApprovalDTO(ap)
			dto.Approval = &summary
		}
		out = append(out, dto)
	}
	return out
}

type deployRequest struct {
	// Ref, when set, is the exact commit SHA to build; empty builds the head
	// of the application's configured branch.
	Ref string `json:"ref"`
}

// handleDeployApplication starts a deployment pipeline: 202, progress via the
// deployment record (and log streams).
func (a *API) handleDeployApplication(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	var req deployRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	dep, err := a.deps.Scheduler.DeployAs(r.Context(), r.PathValue("id"), "manual", req.Ref, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if writeIfFrozen(w, err) {
		return
	}
	if err != nil {
		if dep.ID != "" {
			// The pipeline started and failed fast (no builder available, a
			// dangling deploy key): the deployment record carries the reason
			// in its detail — return it rather than an opaque 500.
			writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
			return
		}
		a.deps.Log.Error("starting deployment", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start deployment")
		return
	}
	writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
}

// writeIfFrozen answers 409 when a deploy was refused by a freeze window,
// naming the window and when it lifts so the caller can simply retry after it
// (deploy-protection.md §6). Nothing was written, so there is no record to
// return alongside it.
func writeIfFrozen(w http.ResponseWriter, err error) bool {
	var frozen *scheduler.FrozenError
	if errors.As(err, &frozen) {
		writeError(w, http.StatusConflict, frozen.Detail)
		return true
	}
	return false
}

func (a *API) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	appID := r.PathValue("id")
	if _, err := a.deps.Applications.Get(r.Context(), appID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		a.deps.Log.Error("listing deployments", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list deployments")
		return
	}
	list, err := a.deps.Deployments.ListDeploymentsByApplication(r.Context(), appID, 50)
	if err != nil {
		a.deps.Log.Error("listing deployments", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list deployments")
		return
	}
	writeJSON(w, http.StatusOK, a.withApprovals(r.Context(), appID, list))
}

func (a *API) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDeployment(ctx, r.PathValue("id"))
	}) {
		return
	}
	dep, err := a.deps.Deployments.GetDeployment(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting deployment", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get deployment")
		return
	}
	writeJSON(w, http.StatusOK, a.withApproval(r.Context(), dep))
}

// handleGetDeploymentLogs streams a deployment's build logs as SSE: retained
// history first (a client connecting mid- or post-build replays what the
// bounded LOGS stream still holds), then the live tail.
func (a *API) handleGetDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDeployment(ctx, r.PathValue("id"))
	}) {
		return
	}
	depID := r.PathValue("id")
	dep, err := a.deps.Deployments.GetDeployment(r.Context(), depID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not get deployment")
		return
	}
	app, err := a.deps.Applications.Get(r.Context(), dep.ApplicationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get application for deployment")
		return
	}
	a.streamLogSSE(w, r, subjects.BuildLog(app.Runtime.ServerID, depID))
}

// handleRollback starts a deployment that restores the revision this
// deployment shipped (build skipped — the image exists).
func (a *API) handleRollback(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDeployment(ctx, r.PathValue("id"))
	}) {
		return
	}
	dep, err := a.deps.Scheduler.RollbackAs(r.Context(), r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if errors.Is(err, scheduler.ErrRevisionNotBuilt) {
		writeError(w, http.StatusConflict, "that deployment's revision was never built — nothing to roll back to")
		return
	}
	if writeIfFrozen(w, err) {
		return
	}
	if err != nil {
		a.deps.Log.Error("starting rollback", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start rollback")
		return
	}
	writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
}

// githubPushEvent is the subset of GitHub's push payload the webhook needs.
type githubPushEvent struct {
	Ref     string `json:"ref"` // refs/heads/<branch>
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
}

// githubPullRequestEvent is the subset of GitHub's pull_request payload the
// preview manager needs (preview-environments.md §4).
type githubPullRequestEvent struct {
	Action      string `json:"action"` // opened | reopened | synchronize | closed | ...
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			Ref string `json:"ref"` // PR branch
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"` // target branch
		} `json:"base"`
	} `json:"pull_request"`
}

// webhookMaxBody bounds the accepted payload (GitHub's own cap is 25 MB; push
// events are far smaller).
const webhookMaxBody = 1 << 20

// handleGitHubWebhook authenticates by per-app HMAC (the only unauthenticated
// mutating route — spec §4) and triggers a deploy for pushes to the app's
// configured branch.
func (a *API) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	app, err := a.deps.Applications.GetByWebhookID(r.Context(), r.PathValue("id"))
	if err != nil {
		// 404 regardless of cause: the webhook id is a capability handle and
		// must not leak which ids exist vs. fail (rule 20 adjacent).
		writeError(w, http.StatusNotFound, "unknown webhook")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookMaxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	if !a.verifyWebhookSignature(app, body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, http.StatusUnauthorized, "signature verification failed")
		return
	}

	switch ev := r.Header.Get("X-GitHub-Event"); ev {
	case "pull_request":
		a.handlePullRequestWebhook(w, r, app, body)
		return
	case "", "push":
		// fall through to the push-deploy path below
	default:
		w.WriteHeader(http.StatusNoContent) // pings and other events are fine, just not deploys
		return
	}
	var push githubPushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, "invalid push payload")
		return
	}
	if push.Deleted || push.Ref != "refs/heads/"+app.Source.Branch {
		w.WriteHeader(http.StatusNoContent) // authenticated, but not a deployable push
		return
	}
	// No requester: a push is not a person, so a parked webhook deploy stores
	// a NULL requested_by and its approval card says "pushed via webhook"
	// (deploy-protection.md §2).
	dep, err := a.deps.Scheduler.DeployAs(r.Context(), app.ID, "webhook", push.After, "")
	if writeIfFrozen(w, err) {
		return
	}
	if err != nil {
		a.deps.Log.Error("webhook deploy", "app_id", app.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not start deployment")
		return
	}
	writeJSON(w, http.StatusAccepted, a.withApproval(r.Context(), dep))
}

// handlePullRequestWebhook drives preview environments from an already-
// authenticated pull_request delivery (preview-environments.md §4). It acts
// only when the app opted into previews and the PR targets its base branch.
func (a *API) handlePullRequestWebhook(w http.ResponseWriter, r *http.Request, app domain.Application, body []byte) {
	if a.deps.Previews == nil {
		w.WriteHeader(http.StatusNoContent) // preview manager not wired
		return
	}
	var pr githubPullRequestEvent
	if err := json.Unmarshal(body, &pr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid pull_request payload")
		return
	}
	// A disabled app still gets its live previews cleaned up on close — an
	// operator may disable previews while some are running; only non-close
	// actions are a no-op for a disabled app.
	if !app.PreviewEnabled && pr.Action != previews.ActionClosed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Only preview PRs into the app's configured base branch (§4). Closed
	// events always run the destroy path regardless of base, so a preview is
	// never leaked by a base-branch mismatch at close time.
	if pr.Action != previews.ActionClosed && pr.PullRequest.Base.Ref != app.Source.Branch {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.deps.Previews.OnPullRequest(r.Context(), app, pr.Action, pr.Number, pr.PullRequest.Head.Ref, pr.PullRequest.Head.SHA); err != nil {
		a.deps.Log.Error("preview webhook", "app_id", app.ID, "pr", pr.Number, "action", pr.Action, "error", err)
		writeError(w, http.StatusInternalServerError, "could not process pull request")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// verifyWebhookSignature checks GitHub's X-Hub-Signature-256 ("sha256=<hex>")
// against the app's unsealed HMAC secret, in constant time (rule 21).
func (a *API) verifyWebhookSignature(app domain.Application, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	theirs, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	secret, err := a.deps.Opener.Open(app.WebhookSecretCT, app.WebhookSecretNonce)
	if err != nil {
		a.deps.Log.Error("unsealing webhook secret", "app_id", app.ID, "error", err)
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), theirs)
}

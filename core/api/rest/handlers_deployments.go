package rest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type deploymentDTO struct {
	ID            string  `json:"id"`
	ApplicationID string  `json:"application_id"`
	RevisionID    string  `json:"revision_id"`
	Status        string  `json:"status"`
	Trigger       string  `json:"trigger"`
	Detail        string  `json:"detail"`
	CreatedAt     string  `json:"created_at"`
	FinishedAt    *string `json:"finished_at"`
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

type deployRequest struct {
	// Ref, when set, is the exact commit SHA to build; empty builds the head
	// of the application's configured branch.
	Ref string `json:"ref"`
}

// handleDeployApplication starts a deployment pipeline: 202, progress via the
// deployment record (and log streams).
func (a *API) handleDeployApplication(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	dep, err := a.deps.Scheduler.Deploy(r.Context(), r.PathValue("id"), "manual", req.Ref)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("starting deployment", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start deployment")
		return
	}
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(dep))
}

func (a *API) handleListDeployments(w http.ResponseWriter, r *http.Request) {
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
	out := make([]deploymentDTO, 0, len(list))
	for _, d := range list {
		out = append(out, toDeploymentDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, toDeploymentDTO(dep))
}

func (a *API) handleGetDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	depID := r.PathValue("id")
	if _, err := a.deps.Deployments.GetDeployment(r.Context(), depID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not get deployment")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	subject := "logs.build." + depID
	sub, err := a.deps.NATSConn.SubscribeSync(subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not subscribe to logs")
		return
	}
	defer sub.Unsubscribe()

	// Notify client that connection is open.
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	for {
		msg, err := sub.NextMsgWithContext(r.Context())
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", msg.Data)
		flusher.Flush()
	}
}

// handleRollback starts a deployment that restores the revision this
// deployment shipped (build skipped — the image exists).
func (a *API) handleRollback(w http.ResponseWriter, r *http.Request) {
	dep, err := a.deps.Scheduler.Rollback(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if errors.Is(err, scheduler.ErrRevisionNotBuilt) {
		writeError(w, http.StatusConflict, "that deployment's revision was never built — nothing to roll back to")
		return
	}
	if err != nil {
		a.deps.Log.Error("starting rollback", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start rollback")
		return
	}
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(dep))
}

// githubPushEvent is the subset of GitHub's push payload the webhook needs.
type githubPushEvent struct {
	Ref     string `json:"ref"` // refs/heads/<branch>
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
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

	if ev := r.Header.Get("X-GitHub-Event"); ev != "" && ev != "push" {
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
	dep, err := a.deps.Scheduler.Deploy(r.Context(), app.ID, "webhook", push.After)
	if err != nil {
		a.deps.Log.Error("webhook deploy", "app_id", app.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not start deployment")
		return
	}
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(dep))
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

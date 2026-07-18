package rest

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// ─── DTOs (secrets always masked — ENGINEERING rule 20) ─────────────────────

type applicationDTO struct {
	ID                string        `json:"id"`
	EnvironmentID     string        `json:"environment_id"`
	Name              string        `json:"name"`
	Source            appSourceDTO  `json:"source"`
	Build             appBuildDTO   `json:"build"`
	Runtime           appRuntimeDTO `json:"runtime"`
	Route             appRouteDTO   `json:"route"`
	Health            appHealthDTO  `json:"health"`
	WebhookID         string        `json:"webhook_id"`
	DesiredRevisionID *string       `json:"desired_revision_id"`
	// Status is observed state (ADR-005): what the agent last reported, with
	// the revision actually serving.
	Status             string `json:"status"`
	StatusDetail       string `json:"status_detail"`
	ObservedRevisionID string `json:"observed_revision_id"`
	CreatedAt          string `json:"created_at"`
}

type appSourceDTO struct {
	Kind        string  `json:"kind"`
	Repo        string  `json:"repo"`
	Branch      string  `json:"branch"`
	DeployKeyID *string `json:"deploy_key_id"`
}

type appBuildDTO struct {
	Kind           string `json:"kind"`
	DockerfilePath string `json:"dockerfile_path"`
	Context        string `json:"context"`
}

type appRuntimeDTO struct {
	ServerID string `json:"server_id"`
	Port     int    `json:"port"`
	Replicas int    `json:"replicas"`
}

type appRouteDTO struct {
	Domain     string `json:"domain"`
	HTTPS      bool   `json:"https"`
	PathPrefix string `json:"path_prefix"`
}

type appHealthDTO struct {
	Path            string `json:"path"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	Retries         int    `json:"retries"`
}

func toApplicationDTO(a domain.Application) applicationDTO {
	return applicationDTO{
		ID:                 a.ID,
		EnvironmentID:      a.EnvironmentID,
		Name:               a.Name,
		Source:             appSourceDTO{Kind: a.Source.Kind, Repo: a.Source.Repo, Branch: a.Source.Branch, DeployKeyID: a.Source.DeployKeyID},
		Build:              appBuildDTO{Kind: a.Build.Kind, DockerfilePath: a.Build.DockerfilePath, Context: a.Build.Context},
		Runtime:            appRuntimeDTO{ServerID: a.Runtime.ServerID, Port: a.Runtime.Port, Replicas: a.Runtime.Replicas},
		Route:              appRouteDTO{Domain: a.Route.Domain, HTTPS: a.Route.HTTPS, PathPrefix: a.Route.PathPrefix},
		Health:             appHealthDTO{Path: a.Health.Path, IntervalSeconds: a.Health.IntervalSeconds, TimeoutSeconds: a.Health.TimeoutSeconds, Retries: a.Health.Retries},
		WebhookID:          a.WebhookID,
		DesiredRevisionID:  a.DesiredRevisionID,
		Status:             a.Status,
		StatusDetail:       a.StatusDetail,
		ObservedRevisionID: a.ObservedRevisionID,
		CreatedAt:          a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ─── requests / responses ────────────────────────────────────────────────────

type createApplicationRequest struct {
	Name   string `json:"name"`
	Source struct {
		Kind        string  `json:"kind"`
		Repo        string  `json:"repo"`
		Branch      string  `json:"branch"`
		DeployKeyID *string `json:"deploy_key_id"`
	} `json:"source"`
	Build struct {
		DockerfilePath string `json:"dockerfile_path"`
		Context        string `json:"context"`
	} `json:"build"`
	Runtime struct {
		ServerID string `json:"server_id"`
		Port     int    `json:"port"`
		Replicas int    `json:"replicas"`
	} `json:"runtime"`
	Route struct {
		Domain     string `json:"domain"`
		HTTPS      *bool  `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health struct {
		Path            string `json:"path"`
		IntervalSeconds int    `json:"interval_seconds"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		Retries         int    `json:"retries"`
	} `json:"health"`
	EnvVars map[string]string `json:"env_vars"`
}

type webhookInfo struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type createApplicationResponse struct {
	Application applicationDTO `json:"application"`
	// Webhook secret is shown exactly once here and never retrievable again.
	Webhook webhookInfo `json:"webhook"`
}

func (r createApplicationRequest) toInput() applications.CreateInput {
	https := true
	if r.Route.HTTPS != nil {
		https = *r.Route.HTTPS
	}
	return applications.CreateInput{
		Name:    r.Name,
		Source:  domain.AppSource{Kind: r.Source.Kind, Repo: r.Source.Repo, Branch: r.Source.Branch, DeployKeyID: r.Source.DeployKeyID},
		Build:   domain.AppBuild{DockerfilePath: r.Build.DockerfilePath, Context: r.Build.Context},
		Runtime: domain.AppRuntime{ServerID: r.Runtime.ServerID, Port: r.Runtime.Port, Replicas: r.Runtime.Replicas},
		Route:   domain.AppRoute{Domain: r.Route.Domain, HTTPS: https, PathPrefix: r.Route.PathPrefix},
		Health:  domain.AppHealth{Path: r.Health.Path, IntervalSeconds: r.Health.IntervalSeconds, TimeoutSeconds: r.Health.TimeoutSeconds, Retries: r.Health.Retries},
		EnvVars: r.EnvVars,
	}
}

// ─── handlers ────────────────────────────────────────────────────────────────

func (a *API) handleCreateApplication(w http.ResponseWriter, r *http.Request) {
	var req createApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	app, secret, err := a.deps.Applications.Create(r.Context(), r.PathValue("id"), req.toInput())
	if err != nil {
		a.writeAppError(w, err, "could not create application")
		return
	}
	writeJSON(w, http.StatusCreated, createApplicationResponse{
		Application: toApplicationDTO(app),
		Webhook: webhookInfo{
			URL:    a.deps.ConsoleURL + "/webhooks/github/" + app.WebhookID,
			Secret: secret,
		},
	})
}

func (a *API) handleListApplications(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Applications.List(r.Context(), r.PathValue("id"))
	if errors.Is(err, applications.ErrEnvironmentNotFound) {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("listing applications", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list applications")
		return
	}
	out := make([]applicationDTO, 0, len(list))
	for _, app := range list {
		out = append(out, toApplicationDTO(app))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetApplication(w http.ResponseWriter, r *http.Request) {
	app, err := a.deps.Applications.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting application", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get application")
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(app))
}

func (a *API) handleGetApplicationLogs(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("id")
	app, err := a.deps.Applications.Get(r.Context(), appID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not get application")
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

	subject := "logs." + app.Runtime.ServerID + ".runtime." + appID
	sub, err := a.deps.NATSConn.SubscribeSync(subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not subscribe to logs")
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Notify client that connection is open.
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {}\n\n"); err != nil {
		return
	}
	flusher.Flush()

	for {
		msg, err := sub.NextMsgWithContext(r.Context())
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", msg.Data); err != nil {
			return
		}
		flusher.Flush()
	}
}

// patchApplicationRequest mirrors createApplicationRequest with every section
// optional: absent sections stay unchanged, present ones replace wholesale.
type patchApplicationRequest struct {
	Name   *string `json:"name"`
	Source *struct {
		Kind        string  `json:"kind"`
		Repo        string  `json:"repo"`
		Branch      string  `json:"branch"`
		DeployKeyID *string `json:"deploy_key_id"`
	} `json:"source"`
	Build *struct {
		DockerfilePath string `json:"dockerfile_path"`
		Context        string `json:"context"`
	} `json:"build"`
	Runtime *struct {
		Port int `json:"port"`
	} `json:"runtime"`
	Route *struct {
		Domain     string `json:"domain"`
		HTTPS      *bool  `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health *struct {
		Path            string `json:"path"`
		IntervalSeconds int    `json:"interval_seconds"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		Retries         int    `json:"retries"`
	} `json:"health"`
}

func (a *API) handlePatchApplication(w http.ResponseWriter, r *http.Request) {
	var req patchApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := applications.UpdateInput{Name: req.Name}
	if req.Source != nil {
		in.Source = &domain.AppSource{Kind: req.Source.Kind, Repo: req.Source.Repo, Branch: req.Source.Branch, DeployKeyID: req.Source.DeployKeyID}
	}
	if req.Build != nil {
		in.Build = &domain.AppBuild{DockerfilePath: req.Build.DockerfilePath, Context: req.Build.Context}
	}
	if req.Runtime != nil {
		in.Port = &req.Runtime.Port
	}
	if req.Route != nil {
		https := true
		if req.Route.HTTPS != nil {
			https = *req.Route.HTTPS
		}
		in.Route = &domain.AppRoute{Domain: req.Route.Domain, HTTPS: https, PathPrefix: req.Route.PathPrefix}
	}
	if req.Health != nil {
		in.Health = &domain.AppHealth{Path: req.Health.Path, IntervalSeconds: req.Health.IntervalSeconds, TimeoutSeconds: req.Health.TimeoutSeconds, Retries: req.Health.Retries}
	}
	app, err := a.deps.Applications.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		a.writeAppError(w, err, "could not update application")
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(app))
}

func (a *API) handleDeleteApplication(w http.ResponseWriter, r *http.Request) {
	// Load first: after the row is gone we still need the server to publish
	// desired absence. A missing app deletes idempotently (204).
	app, err := a.deps.Applications.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		a.deps.Log.Error("deleting application", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete application")
		return
	}
	if err := a.deps.Applications.Delete(r.Context(), app.ID); err != nil {
		a.deps.Log.Error("deleting application", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete application")
		return
	}
	if err := a.deps.Scheduler.RemoveApp(r.Context(), app.Runtime.ServerID, app.ID); err != nil {
		// The row is gone; the agent's next desired-state sync converges the
		// removal anyway. Degraded immediacy, not failure.
		a.deps.Log.Error("publishing app removal", "app_id", app.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListEnvVars(w http.ResponseWriter, r *http.Request) {
	keys, err := a.deps.Applications.ListEnvVarKeys(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("listing env vars", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list environment variables")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"keys": keys})
}

type setEnvVarRequest struct {
	Value string `json:"value"`
}

func (a *API) handleSetEnvVar(w http.ResponseWriter, r *http.Request) {
	var req setEnvVarRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := a.deps.Applications.SetEnvVar(r.Context(), r.PathValue("id"), r.PathValue("key"), req.Value)
	if err != nil {
		a.writeAppError(w, err, "could not set environment variable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteEnvVar(w http.ResponseWriter, r *http.Request) {
	err := a.deps.Applications.DeleteEnvVar(r.Context(), r.PathValue("id"), r.PathValue("key"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("deleting env var", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete environment variable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAppError maps applications-service errors to HTTP status codes: client
// validation to 400, a missing environment or application to 404 (each named
// correctly), a missing target server to 400, a duplicate name to 409, and
// anything else to 500.
func (a *API) writeAppError(w http.ResponseWriter, err error, genericMsg string) {
	var ve *applications.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, applications.ErrServerNotFound):
		writeError(w, http.StatusBadRequest, "target server not found")
	case errors.Is(err, applications.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "application not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "an application with that name already exists in this environment")
	default:
		a.deps.Log.Error("application request failed", "error", err)
		writeError(w, http.StatusInternalServerError, genericMsg)
	}
}

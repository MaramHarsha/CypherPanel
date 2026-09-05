package rest

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ─── DTOs (secrets always masked — ENGINEERING rule 20) ─────────────────────

type applicationDTO struct {
	ID                string         `json:"id"`
	EnvironmentID     string         `json:"environment_id"`
	Name              string         `json:"name"`
	Source            appSourceDTO   `json:"source"`
	Build             appBuildDTO    `json:"build"`
	Runtime           appRuntimeDTO  `json:"runtime"`
	Route             appRouteDTO    `json:"route"`
	Health            appHealthDTO   `json:"health"`
	Volumes           []appVolumeDTO `json:"volumes"`
	Ports             []appPortDTO   `json:"ports"`
	WebhookID         string         `json:"webhook_id"`
	DesiredRevisionID *string        `json:"desired_revision_id"`
	// Status is observed state (ADR-005): what the agent last reported, with
	// the revision actually serving.
	Status             string `json:"status"`
	StatusDetail       string `json:"status_detail"`
	ObservedRevisionID string `json:"observed_revision_id"`
	PreviewEnabled     bool   `json:"preview_enabled"`
	PreviewBaseDomain  string `json:"preview_base_domain"`
	PreviewTTLHours    int    `json:"preview_ttl_hours"`
	// RedeployPending is DERIVED, never stored (shared-variables.md §5): a
	// shared variable this application references changed after the environment
	// it is running was frozen onto the wire. It is not a status word — the
	// six-word vocabulary in ui-principles §5 is closed and "needs a redeploy"
	// is not an observed state — so the UI renders it as a badge beside the
	// status, never in place of one.
	RedeployPending bool `json:"redeploy_pending"`
	// TLSState is DERIVED, never stored (agent-identity-and-tls.md §5): what
	// this route is actually served as, given what the panel knows. Omitted
	// when there is no domain — there is then no route to describe. It exists
	// so the UI can say "serving over HTTP meanwhile" instead of printing
	// "HTTPS · auto-renews" off the https flag alone, which asserted a
	// certificate the panel had never seen issued (ui-principles §10).
	TLSState  string `json:"tls_state,omitempty"`
	CreatedAt string `json:"created_at"`
}

type appSourceDTO struct {
	Kind        string  `json:"kind"`
	Repo        string  `json:"repo"`
	Branch      string  `json:"branch"`
	DeployKeyID *string `json:"deploy_key_id"`
	Image       string  `json:"image"` // OCI reference; set iff kind == "image"
}

type appBuildDTO struct {
	Kind           string `json:"kind"`
	DockerfilePath string `json:"dockerfile_path"`
	Context        string `json:"context"`
}

type appVolumeDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// appVolumeReq is the request shape for a volume mount (create and patch).
type appVolumeReq struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func reqVolumes(vs []appVolumeReq) []domain.VolumeMount {
	out := make([]domain.VolumeMount, 0, len(vs))
	for _, v := range vs {
		out = append(out, domain.VolumeMount{Name: v.Name, Path: v.Path})
	}
	return out
}

// appPortDTO / appPortReq describe a raw host-port publish (create, patch, and
// response).
type appPortDTO struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

type appPortReq struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

func reqPorts(ps []appPortReq) []domain.PortMapping {
	out := make([]domain.PortMapping, 0, len(ps))
	for _, p := range ps {
		out = append(out, domain.PortMapping{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
	}
	return out
}

func toPortDTOs(ps []domain.PortMapping) []appPortDTO {
	out := make([]appPortDTO, 0, len(ps))
	for _, p := range ps {
		out = append(out, appPortDTO{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
	}
	return out
}

type appRuntimeDTO struct {
	ServerID      string   `json:"server_id"`
	Port          int      `json:"port"`
	Replicas      int      `json:"replicas"`
	CPULimit      *float64 `json:"cpu_limit,omitempty"`
	MemoryLimitMB *int     `json:"memory_limit_mb,omitempty"`
}

type appRouteDTO struct {
	Domain     string `json:"domain"`
	HTTPS      bool   `json:"https"`
	PathPrefix string `json:"path_prefix"`
}

type appHealthDTO struct {
	Kind            string `json:"kind"`
	Path            string `json:"path"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	Retries         int    `json:"retries"`
}

func toVolumeDTOs(vs []domain.VolumeMount) []appVolumeDTO {
	out := make([]appVolumeDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, appVolumeDTO{Name: v.Name, Path: v.Path})
	}
	return out
}

func toApplicationDTO(a domain.Application) applicationDTO {
	return applicationDTO{
		ID:                 a.ID,
		EnvironmentID:      a.EnvironmentID,
		Name:               a.Name,
		Source:             appSourceDTO{Kind: a.Source.Kind, Repo: a.Source.Repo, Branch: a.Source.Branch, DeployKeyID: a.Source.DeployKeyID, Image: a.Source.Image},
		Build:              appBuildDTO{Kind: a.Build.Kind, DockerfilePath: a.Build.DockerfilePath, Context: a.Build.Context},
		Runtime:            appRuntimeDTO{ServerID: a.Runtime.ServerID, Port: a.Runtime.Port, Replicas: a.Runtime.Replicas, CPULimit: a.Runtime.CPULimit, MemoryLimitMB: a.Runtime.MemoryLimitMB},
		Route:              appRouteDTO{Domain: a.Route.Domain, HTTPS: a.Route.HTTPS, PathPrefix: a.Route.PathPrefix},
		Health:             appHealthDTO{Kind: a.Health.Kind, Path: a.Health.Path, IntervalSeconds: a.Health.IntervalSeconds, TimeoutSeconds: a.Health.TimeoutSeconds, Retries: a.Health.Retries},
		Volumes:            toVolumeDTOs(a.Volumes),
		Ports:              toPortDTOs(a.Ports),
		WebhookID:          a.WebhookID,
		DesiredRevisionID:  a.DesiredRevisionID,
		Status:             a.Status,
		StatusDetail:       a.StatusDetail,
		ObservedRevisionID: a.ObservedRevisionID,
		PreviewEnabled:     a.PreviewEnabled,
		PreviewBaseDomain:  a.PreviewBaseDomain,
		PreviewTTLHours:    a.PreviewTTLHours,
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
		Image       string  `json:"image"`
	} `json:"source"`
	Build struct {
		// AppBuild.kind is required by the OpenAPI schema, so every generated
		// client sends it. decodeJSON runs with DisallowUnknownFields, so
		// omitting it here rejected the documented request shape outright with
		// "invalid request body" — creating an application through the contract
		// was impossible. The service defaults an empty kind to "dockerfile"
		// and rejects anything else (applications.go validateAndDefault).
		Kind           string `json:"kind"`
		DockerfilePath string `json:"dockerfile_path"`
		Context        string `json:"context"`
	} `json:"build"`
	Runtime struct {
		ServerID      string   `json:"server_id"`
		Port          int      `json:"port"`
		Replicas      int      `json:"replicas"`
		CPULimit      *float64 `json:"cpu_limit"`
		MemoryLimitMB *int     `json:"memory_limit_mb"`
	} `json:"runtime"`
	Route struct {
		Domain     string `json:"domain"`
		HTTPS      *bool  `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health struct {
		Kind            string `json:"kind"`
		Path            string `json:"path"`
		IntervalSeconds int    `json:"interval_seconds"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		Retries         int    `json:"retries"`
	} `json:"health"`
	Volumes           []appVolumeReq    `json:"volumes"`
	Ports             []appPortReq      `json:"ports"`
	EnvVars           map[string]string `json:"env_vars"`
	PreviewEnabled    bool              `json:"preview_enabled"`
	PreviewBaseDomain string            `json:"preview_base_domain"`
	PreviewTTLHours   int               `json:"preview_ttl_hours"`
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
		Source:  domain.AppSource{Kind: r.Source.Kind, Repo: r.Source.Repo, Branch: r.Source.Branch, DeployKeyID: r.Source.DeployKeyID, Image: r.Source.Image},
		Build:   domain.AppBuild{Kind: r.Build.Kind, DockerfilePath: r.Build.DockerfilePath, Context: r.Build.Context},
		Runtime: domain.AppRuntime{ServerID: r.Runtime.ServerID, Port: r.Runtime.Port, Replicas: r.Runtime.Replicas, CPULimit: r.Runtime.CPULimit, MemoryLimitMB: r.Runtime.MemoryLimitMB},
		Route:   domain.AppRoute{Domain: r.Route.Domain, HTTPS: https, PathPrefix: r.Route.PathPrefix},
		Health:  domain.AppHealth{Kind: r.Health.Kind, Path: r.Health.Path, IntervalSeconds: r.Health.IntervalSeconds, TimeoutSeconds: r.Health.TimeoutSeconds, Retries: r.Health.Retries},
		Volumes: reqVolumes(r.Volumes),
		Ports:   reqPorts(r.Ports),
		EnvVars: r.EnvVars,

		PreviewEnabled:    r.PreviewEnabled,
		PreviewBaseDomain: r.PreviewBaseDomain,
		PreviewTTLHours:   r.PreviewTTLHours,
	}
}

// ─── handlers ────────────────────────────────────────────────────────────────

func (a *API) handleCreateApplication(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	a.audit(r, audit.Entry{
		Action:        audit.ActionApplicationCreated,
		Resource:      audit.Resource(audit.ResourceApplication, app.ID, app.Name),
		EnvironmentID: app.EnvironmentID,
		Detail:        map[string]any{"server_id": app.Runtime.ServerID, "source_kind": app.Source.Kind, "domain": app.Route.Domain},
	})
	a.syncApplicationDNS(r.Context(), app)
	created := toApplicationDTO(app)
	created.TLSState = domain.RouteTLSState(app.Route, a.acmeConfigured(r.Context()))
	writeJSON(w, http.StatusCreated, createApplicationResponse{
		Application: created,
		Webhook: webhookInfo{
			URL:    a.deps.ConsoleURL + "/webhooks/github/" + app.WebhookID,
			Secret: secret,
		},
	})
}

func (a *API) handleListApplications(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	// One query for the whole environment rather than one per row
	// (shared-variables.md §5).
	pending := a.redeployPendingSet(r.Context(), r.PathValue("id"))
	// One TLS read for the whole list: the account is panel-wide, so asking
	// once per application would be the same answer N times.
	acme := a.acmeConfigured(r.Context())
	out := make([]applicationDTO, 0, len(list))
	for _, app := range list {
		dto := toApplicationDTO(app)
		dto.RedeployPending = pending[app.ID]
		dto.TLSState = domain.RouteTLSState(app.Route, acme)
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetApplication(w http.ResponseWriter, r *http.Request) {
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
		a.deps.Log.Error("getting application", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get application")
		return
	}
	dto := toApplicationDTO(app)
	dto.RedeployPending = a.redeployPending(r.Context(), app.ID)
	dto.TLSState = domain.RouteTLSState(app.Route, a.acmeConfigured(r.Context()))
	writeJSON(w, http.StatusOK, dto)
}

// handleGetApplicationLogs streams an application's runtime logs as SSE:
// retained history from the bounded LOGS stream, then the live tail.
func (a *API) handleGetApplicationLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	a.streamRuntimeLogSSE(w, r, subjects.RuntimeLog(app.Runtime.ServerID, appID))
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
		Image       string  `json:"image"`
	} `json:"source"`
	Build *struct {
		// Same contract mismatch as createApplicationRequest.Build — a client
		// echoing back the application it just read could not PATCH it.
		Kind           string `json:"kind"`
		DockerfilePath string `json:"dockerfile_path"`
		Context        string `json:"context"`
	} `json:"build"`
	Runtime *struct {
		Port          *int     `json:"port"`
		CPULimit      *float64 `json:"cpu_limit"`
		MemoryLimitMB *int     `json:"memory_limit_mb"`
	} `json:"runtime"`
	Route *struct {
		Domain     string `json:"domain"`
		HTTPS      *bool  `json:"https"`
		PathPrefix string `json:"path_prefix"`
	} `json:"route"`
	Health *struct {
		Kind            string `json:"kind"`
		Path            string `json:"path"`
		IntervalSeconds int    `json:"interval_seconds"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		Retries         int    `json:"retries"`
	} `json:"health"`
	Volumes           *[]appVolumeReq `json:"volumes"`
	Ports             *[]appPortReq   `json:"ports"`
	PreviewEnabled    *bool           `json:"preview_enabled"`
	PreviewBaseDomain *string         `json:"preview_base_domain"`
	PreviewTTLHours   *int            `json:"preview_ttl_hours"`
}

func (a *API) handlePatchApplication(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	var req patchApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := applications.UpdateInput{Name: req.Name}
	if req.Source != nil {
		in.Source = &domain.AppSource{Kind: req.Source.Kind, Repo: req.Source.Repo, Branch: req.Source.Branch, DeployKeyID: req.Source.DeployKeyID, Image: req.Source.Image}
	}
	if req.Build != nil {
		in.Build = &domain.AppBuild{Kind: req.Build.Kind, DockerfilePath: req.Build.DockerfilePath, Context: req.Build.Context}
	}
	if req.Runtime != nil {
		in.Port = req.Runtime.Port // nil = unchanged; explicit 0 is rejected by validation
		in.CPULimit = req.Runtime.CPULimit
		in.MemoryLimitMB = req.Runtime.MemoryLimitMB
	}
	if req.Route != nil {
		https := true
		if req.Route.HTTPS != nil {
			https = *req.Route.HTTPS
		}
		in.Route = &domain.AppRoute{Domain: req.Route.Domain, HTTPS: https, PathPrefix: req.Route.PathPrefix}
	}
	if req.Health != nil {
		in.Health = &domain.AppHealth{Kind: req.Health.Kind, Path: req.Health.Path, IntervalSeconds: req.Health.IntervalSeconds, TimeoutSeconds: req.Health.TimeoutSeconds, Retries: req.Health.Retries}
	}
	if req.Volumes != nil {
		v := reqVolumes(*req.Volumes)
		in.Volumes = &v
	}
	if req.Ports != nil {
		p := reqPorts(*req.Ports)
		in.Ports = &p
	}
	in.PreviewEnabled, in.PreviewBaseDomain, in.PreviewTTLHours = req.PreviewEnabled, req.PreviewBaseDomain, req.PreviewTTLHours
	app, err := a.deps.Applications.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		a.writeAppError(w, err, "could not update application")
		return
	}
	// The changed field NAMES, not their contents: what an operator needs to
	// see is that the domain moved, and the new domain is on the application
	// itself (§6).
	a.audit(r, audit.Entry{
		Action:        audit.ActionApplicationUpdated,
		Resource:      audit.Resource(audit.ResourceApplication, app.ID, app.Name),
		EnvironmentID: app.EnvironmentID,
		Detail:        map[string]any{"fields": patchedApplicationFields(req)},
	})
	a.syncApplicationDNS(r.Context(), app)
	dto := toApplicationDTO(app)
	dto.RedeployPending = a.redeployPending(r.Context(), app.ID)
	dto.TLSState = domain.RouteTLSState(app.Route, a.acmeConfigured(r.Context()))
	writeJSON(w, http.StatusOK, dto)
}

func (a *API) handleDeleteApplication(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	// Tombstone BEFORE the row goes, while application_id is still readable.
	// Correctness does not depend on this — ON DELETE SET NULL leaves an orphan
	// the sweeper reaps anyway (dns-automation.md §4.3) — but doing it here
	// makes the delete from Cloudflare happen on the next tick rather than
	// waiting to be noticed.
	if a.deps.DNS != nil {
		if err := a.deps.DNS.ForgetApplication(r.Context(), app.ID); err != nil {
			a.deps.Log.Error("tombstoning application dns", "app_id", app.ID, "error", err)
		}
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
	// The environment survives the application, so the ownership chain still
	// resolves from it — this is the answer to "who deleted notify-svc?" that
	// the 404 screen promises the log remembers (canvas 13p).
	a.audit(r, audit.Entry{
		Action:        audit.ActionApplicationDeleted,
		Resource:      audit.Resource(audit.ResourceApplication, app.ID, app.Name),
		EnvironmentID: app.EnvironmentID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// patchedApplicationFields names the top-level fields a PATCH actually carried,
// for the audit detail. Names only: the values are on the application, and one
// of them (an env var) is sealed (§6).
func patchedApplicationFields(req patchApplicationRequest) []string {
	fields := []string{}
	for name, present := range map[string]bool{
		"name":                req.Name != nil,
		"source":              req.Source != nil,
		"build":               req.Build != nil,
		"runtime":             req.Runtime != nil,
		"route":               req.Route != nil,
		"health":              req.Health != nil,
		"volumes":             req.Volumes != nil,
		"ports":               req.Ports != nil,
		"preview_enabled":     req.PreviewEnabled != nil,
		"preview_base_domain": req.PreviewBaseDomain != nil,
		"preview_ttl_hours":   req.PreviewTTLHours != nil,
	} {
		if present {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}

func (a *API) handleListEnvVars(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	view, err := a.deps.Applications.ListEnv(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("listing env vars", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list environment variables")
		return
	}
	// shared_refs is additive (ENGINEERING rule 17): keys is unchanged, and the
	// new object maps each env key to the shared variables its value references,
	// so the Env vars tab can show the wiring without a reveal
	// (shared-variables.md §7).
	writeJSON(w, http.StatusOK, envVarKeysDTO{Keys: view.Keys, SharedRefs: view.SharedRefs})
}

// envVarKeysDTO is the env-var listing: keys only — a value is never returned
// (ui-principles §6) — plus the cleartext shared-variable wiring.
type envVarKeysDTO struct {
	Keys       []string            `json:"keys"`
	SharedRefs map[string][]string `json:"shared_refs"`
}

type setEnvVarRequest struct {
	Value string `json:"value"`
}

func (a *API) handleSetEnvVar(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	// The KEY, never the value (§6). `key` is deliberately not on the audit
	// package's refused-key list: the name of an env var is exactly what this
	// row is for, and its content is exactly what it must not carry.
	a.auditApplication(r, audit.ActionEnvVarSet, r.PathValue("id"), map[string]any{"key": r.PathValue("key")})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteEnvVar(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
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
	a.auditApplication(r, audit.ActionEnvVarRemoved, r.PathValue("id"), map[string]any{"key": r.PathValue("key")})
	w.WriteHeader(http.StatusNoContent)
}

// auditApplication records an action on an application the handler addressed by
// id alone, resolving its name and environment for the snapshot. A lookup that
// fails still records the row: an entry that names only the id is worth more
// than no entry at all.
func (a *API) auditApplication(r *http.Request, action, appID string, detail map[string]any) {
	if a.deps.Audit == nil {
		return
	}
	app, _ := a.deps.Applications.Get(r.Context(), appID)
	a.audit(r, audit.Entry{
		Action:        action,
		Resource:      audit.Resource(audit.ResourceApplication, appID, app.Name),
		EnvironmentID: app.EnvironmentID,
		Detail:        detail,
	})
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

// syncApplicationDNS re-derives this application's desired DNS Record after its
// route changed (dns-automation.md §4.3). It writes desired state only; the
// sweeper does the talking to Cloudflare, so a provider outage cannot fail an
// otherwise valid update.
//
// Best effort by design: a failure here leaves the record as it was, and the
// sweeper's next pass reconciles from the truth in the database. Blocking a
// route change on a DNS write would make the panel less useful than it is
// without the feature.
func (a *API) syncApplicationDNS(ctx context.Context, app domain.Application) {
	if a.deps.DNS == nil {
		return
	}
	var publicAddress string
	if srv, err := a.deps.Servers.Get(ctx, app.Runtime.ServerID); err == nil {
		publicAddress = srv.PublicAddress
	}
	if err := a.deps.DNS.SyncApplication(ctx, app, publicAddress); err != nil {
		a.deps.Log.Error("syncing application dns", "app_id", app.ID, "error", err)
	}
}

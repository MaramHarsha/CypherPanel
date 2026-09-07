package rest

// Public per-project status pages (status-pages.md §8).
//
// The authorization split here is the feature's central decision. ENABLING a
// page is TEAM ADMIN: publishing a project's health, under names of somebody's
// choosing, at a hostname customers will read, is a disclosure decision — so it
// sits at the rank that already decides who joins the team, not at the rank
// that ships code. ANNOTATING a live incident is a MEMBER, deliberately: the
// person who notices at 02:00 is on call, not an admin, and making them find
// one before they can write "we are aware and investigating" is how a status
// page stops being used.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/statuspage"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

var statusSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

const maxIncidentMessage = 280

type statusPageDTO struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Enabled       bool   `json:"enabled"`
	Domain        string `json:"domain"`
	HTTPS         bool   `json:"https"`
	RouteServerID string `json:"route_server_id"`
	// PanelURL is where the page is reachable without any DNS at all. It is the
	// address the operator can copy before they own a domain, and the one that
	// keeps working if the domain is wrong.
	PanelURL string `json:"panel_url"`
}

type statusComponentDTO struct {
	ID           string `json:"id"`
	ResourceKind string `json:"resource_kind"`
	ResourceID   string `json:"resource_id"`
	Label        string `json:"label"`
	Position     int    `json:"position"`
}

type setStatusPageRequest struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Enabled       bool   `json:"enabled"`
	Domain        string `json:"domain"`
	HTTPS         bool   `json:"https"`
	RouteServerID string `json:"route_server_id"`
}

type setStatusComponentsRequest struct {
	Components []struct {
		ResourceKind string `json:"resource_kind"`
		ResourceID   string `json:"resource_id"`
		Label        string `json:"label"`
	} `json:"components"`
}

type annotateIncidentRequest struct {
	Message string `json:"message"`
}

func (a *API) statusPageDTO(p domain.StatusPage) statusPageDTO {
	return statusPageDTO{
		ID: p.ID, ProjectID: p.ProjectID, Slug: p.Slug, Title: p.Title,
		Enabled: p.Enabled, Domain: p.Domain, HTTPS: p.HTTPS,
		RouteServerID: p.RouteServerID,
		PanelURL:      strings.TrimRight(a.deps.PanelURL, "/") + "/status/" + p.Slug,
	}
}

// statusPageFor resolves the page named in the path and checks the caller's
// rank on its project in one step.
func (a *API) statusPageFor(w http.ResponseWriter, r *http.Request, min string) (domain.StatusPage, bool) {
	if a.deps.StatusPages == nil {
		writeError(w, http.StatusNotImplemented, "status pages are not enabled on this panel")
		return domain.StatusPage{}, false
	}
	page, err := a.deps.StatusPages.GetStatusPage(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return domain.StatusPage{}, false
	}
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, page.ProjectID, min) {
		return domain.StatusPage{}, false
	}
	return page, true
}

func (a *API) handleGetStatusPage(w http.ResponseWriter, r *http.Request) {
	if a.deps.StatusPages == nil {
		writeError(w, http.StatusNotImplemented, "status pages are not enabled on this panel")
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleMember) {
		return
	}
	page, err := a.deps.StatusPages.GetStatusPageByProject(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this project has no status page")
		return
	}
	if err != nil {
		a.deps.Log.Error("reading status page", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the status page")
		return
	}
	writeJSON(w, http.StatusOK, a.statusPageDTO(page))
}

func (a *API) handleSetStatusPage(w http.ResponseWriter, r *http.Request) {
	if a.deps.StatusPages == nil {
		writeError(w, http.StatusNotImplemented, "status pages are not enabled on this panel")
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleAdmin) {
		return
	}

	var req setStatusPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	req.Title = strings.TrimSpace(req.Title)
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if !statusSlug.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "the slug is 3–40 lowercase letters, digits and dashes")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "a page title is required")
		return
	}
	// A page with a domain and no serving node cannot be routed, and pretending
	// otherwise would leave the operator pointing DNS at nothing.
	if req.Domain != "" && req.RouteServerID == "" {
		writeError(w, http.StatusBadRequest, "pick the server whose Proxy serves this domain")
		return
	}

	existing, existed := domain.StatusPage{}, false
	if p, err := a.deps.StatusPages.GetStatusPageByProject(r.Context(), projectID); err == nil {
		existing, existed = p, true
	}

	id := existing.ID
	if id == "" {
		id = ids.New(ids.PrefixStatusPage)
	}
	page, err := a.deps.StatusPages.UpsertStatusPage(r.Context(), domain.StatusPage{
		ID: id, ProjectID: projectID, Slug: req.Slug, Title: req.Title,
		Enabled: req.Enabled, Domain: req.Domain, HTTPS: req.HTTPS,
		RouteServerID: req.RouteServerID,
	})
	if err != nil {
		// A slug is a public handle and the table enforces it globally; a
		// collision is the operator's to resolve, not a server fault.
		a.deps.Log.Error("saving status page", "project_id", projectID, "error", err)
		writeError(w, http.StatusConflict, "that address is already taken by another status page")
		return
	}

	// The old slug's cached document would otherwise outlive the rename.
	if a.deps.StatusServer != nil {
		a.deps.StatusServer.Invalidate(page.Slug)
		if existed && existing.Slug != page.Slug {
			a.deps.StatusServer.Invalidate(existing.Slug)
		}
	}
	// Nudge the fleet so the fragment appears without waiting for drift.
	// Best-effort by design: the page is already in Postgres, which is what
	// makes it true — the nudge only decides whether it is routed in a second
	// or at the node's next reconcile.
	if a.deps.Scheduler != nil && page.RouteServerID != "" {
		if err := a.deps.Scheduler.RequestResync(r.Context(), "status page route changed"); err != nil {
			a.deps.Log.Warn("status page: resync nudge failed", "page_id", page.ID, "error", err)
		}
	}

	action := audit.ActionStatusPageUpdated
	switch {
	case !existed:
		action = audit.ActionStatusPageCreated
	case !existing.Enabled && page.Enabled:
		action = audit.ActionStatusPagePublished
	case existing.Enabled && !page.Enabled:
		action = audit.ActionStatusPageUnpublished
	}
	a.audit(r, audit.Entry{
		Action:   action,
		Resource: audit.Resource(audit.ResourceProject, projectID, page.Title),
		Detail:   map[string]any{"slug": page.Slug, "domain": page.Domain, "enabled": page.Enabled},
	})
	writeJSON(w, http.StatusOK, a.statusPageDTO(page))
}

func (a *API) handleDeleteStatusPage(w http.ResponseWriter, r *http.Request) {
	if a.deps.StatusPages == nil {
		writeError(w, http.StatusNotImplemented, "status pages are not enabled on this panel")
		return
	}
	user, _ := userFromContext(r.Context())
	projectID := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleAdmin) {
		return
	}
	page, err := a.deps.StatusPages.GetStatusPageByProject(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the status page")
		return
	}
	if err := a.deps.StatusPages.DeleteStatusPage(r.Context(), projectID); err != nil {
		a.deps.Log.Error("deleting status page", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the status page")
		return
	}
	if a.deps.StatusServer != nil {
		a.deps.StatusServer.Invalidate(page.Slug)
	}
	if a.deps.Scheduler != nil && page.RouteServerID != "" {
		if err := a.deps.Scheduler.RequestResync(r.Context(), "status page removed"); err != nil {
			a.deps.Log.Warn("status page: resync nudge failed", "page_id", page.ID, "error", err)
		}
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionStatusPageDeleted,
		Resource: audit.Resource(audit.ResourceProject, projectID, page.Title),
		Detail:   map[string]any{"slug": page.Slug},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListStatusComponents(w http.ResponseWriter, r *http.Request) {
	page, ok := a.statusPageFor(w, r, domain.RoleMember)
	if !ok {
		return
	}
	comps, err := a.deps.StatusPages.ListStatusPageComponents(r.Context(), page.ID)
	if err != nil {
		a.deps.Log.Error("listing status components", "page_id", page.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the components")
		return
	}
	out := make([]statusComponentDTO, 0, len(comps))
	for _, c := range comps {
		out = append(out, statusComponentDTO{
			ID: c.ID, ResourceKind: c.ResourceKind, ResourceID: c.ResourceID,
			Label: c.Label, Position: c.Position,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetStatusComponents replaces the whole ordered list. Wholesale because
// the list IS the page: adding and removing one row at a time through two
// routes would make "what does this page publish right now" a question with
// two answers mid-edit.
func (a *API) handleSetStatusComponents(w http.ResponseWriter, r *http.Request) {
	page, ok := a.statusPageFor(w, r, domain.RoleAdmin)
	if !ok {
		return
	}
	var req setStatusComponentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	existing, err := a.deps.StatusPages.ListStatusPageComponents(r.Context(), page.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the components")
		return
	}
	// Existing rows keep their id — and with it their tracking_since and their
	// whole interval history. Re-creating a row on every save would reset the
	// bars each time somebody fixed a typo in a label.
	byResource := map[string]string{}
	for _, c := range existing {
		byResource[c.ResourceKind+"/"+c.ResourceID] = c.ID
	}

	keep := make([]string, 0, len(req.Components))
	for i, c := range req.Components {
		label := strings.TrimSpace(c.Label)
		if label == "" {
			writeError(w, http.StatusBadRequest, "every component needs a public label")
			return
		}
		if reason, ok := a.statusComponentAllowed(r.Context(), page.ProjectID, c.ResourceKind, c.ResourceID); !ok {
			writeError(w, http.StatusBadRequest, reason)
			return
		}
		id := byResource[c.ResourceKind+"/"+c.ResourceID]
		if id == "" {
			id = ids.New(ids.PrefixStatusComponent)
		}
		saved, err := a.deps.StatusPages.UpsertStatusPageComponent(r.Context(), domain.StatusPageComponent{
			ID: id, StatusPageID: page.ID, ResourceKind: c.ResourceKind,
			ResourceID: c.ResourceID, Label: label, Position: i,
		})
		if err != nil {
			a.deps.Log.Error("saving status component", "page_id", page.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not save the components")
			return
		}
		keep = append(keep, saved.ID)
	}
	if err := a.deps.StatusPages.DeleteStatusPageComponentsNotIn(r.Context(), page.ID, keep); err != nil {
		a.deps.Log.Error("pruning status components", "page_id", page.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the components")
		return
	}
	if a.deps.StatusServer != nil {
		a.deps.StatusServer.Invalidate(page.Slug)
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionStatusPageUpdated,
		Resource: audit.Resource(audit.ResourceProject, page.ProjectID, page.Title),
		Detail:   map[string]any{"components": len(keep)},
	})
	a.handleListStatusComponents(w, r)
}

// statusComponentAllowed is the disclosure gate, and it is a rejection rather
// than a default. A Preview Environment is created by a machine from an
// outsider's pull request: a page that could include one would publish branch
// names and PR titles written by people who do not work here. The resource
// must also belong to THIS project — a page that can name another project's
// application is a page that can leak across a tenancy boundary.
func (a *API) statusComponentAllowed(ctx context.Context, projectID, kind, resourceID string) (string, bool) {
	var envID string
	switch kind {
	case domain.StatusResourceApplication:
		app, err := a.deps.StatusPages.GetApplication(ctx, resourceID)
		if err != nil {
			return "no such application", false
		}
		envID = app.EnvironmentID
	case domain.StatusResourceComposeStack:
		st, err := a.deps.StatusPages.GetComposeStack(ctx, resourceID)
		if err != nil {
			return "no such compose stack", false
		}
		envID = st.EnvironmentID
	case domain.StatusResourceDatabase:
		db, err := a.deps.StatusPages.GetDatabase(ctx, resourceID)
		if err != nil {
			return "no such database", false
		}
		envID = db.EnvironmentID
	default:
		return "a component is an application, a compose stack or a database", false
	}
	env, err := a.deps.StatusPages.GetEnvironment(ctx, envID)
	if err != nil {
		return "could not resolve the resource's environment", false
	}
	if env.ProjectID != projectID {
		return "that resource belongs to another project", false
	}
	if env.Kind == "preview" {
		return "preview environments cannot appear on a status page: they carry branch names and pull request titles written by people outside your team", false
	}
	return "", true
}

// handlePreviewStatusPage renders the real public payload for a page that is
// not enabled yet. It IS the disclosure control, and it is a better one than a
// warning: a confirmation dialog that says "this will be public" is read by
// nobody, while a rendered page with the customer's name on it is read by
// everybody, because it looks like the thing it is.
func (a *API) handlePreviewStatusPage(w http.ResponseWriter, r *http.Request) {
	page, ok := a.statusPageFor(w, r, domain.RoleMember)
	if !ok {
		return
	}
	payload, err := statuspage.Build(r.Context(), a.deps.StatusPages, page, time.Now())
	if err != nil {
		a.deps.Log.Error("building status page preview", "page_id", page.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not build the preview")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *API) handleCheckStatusPageDomain(w http.ResponseWriter, r *http.Request) {
	page, ok := a.statusPageFor(w, r, domain.RoleMember)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, checkDomain(r.Context(), strings.TrimSpace(page.Domain), page.HTTPS))
}

// handleAnnotateIncident writes the operator's one line onto a live incident.
// The blast radius is bounded to 280 characters of plain text on a page that is
// already public, it is audited with the author's name, and it is never
// linkified — operator text is escaped and rendered as text, so the page cannot
// become a redirect on a hostname the customer trusts.
func (a *API) handleAnnotateIncident(w http.ResponseWriter, r *http.Request) {
	page, ok := a.statusPageFor(w, r, domain.RoleMember)
	if !ok {
		return
	}
	var req annotateIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	msg := strings.TrimSpace(req.Message)
	if len([]rune(msg)) > maxIncidentMessage {
		writeError(w, http.StatusBadRequest, "keep the message under 280 characters")
		return
	}
	// The interval must belong to a component of THIS page, or a member of one
	// team could annotate another team's incident by guessing an id.
	interval, err := a.deps.StatusPages.GetStatusInterval(r.Context(), r.PathValue("iid"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	comps, err := a.deps.StatusPages.ListStatusPageComponents(r.Context(), page.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the components")
		return
	}
	owned := false
	for _, c := range comps {
		if c.ID == interval.ComponentID {
			owned = true
			break
		}
	}
	if !owned {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.deps.StatusPages.SetStatusIntervalMessage(r.Context(), interval.ID, msg); err != nil {
		a.deps.Log.Error("annotating incident", "interval_id", interval.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the message")
		return
	}
	if a.deps.StatusServer != nil {
		a.deps.StatusServer.Invalidate(page.Slug)
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionStatusPageIncidentAnnotated,
		Resource: audit.Resource(audit.ResourceProject, page.ProjectID, page.Title),
		Detail:   map[string]any{"incident_id": interval.ID, "message": msg},
	})
	w.WriteHeader(http.StatusNoContent)
}

package rest

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/templates"
)

type installTemplateRequest struct {
	EnvironmentID string `json:"environment_id"`
	ServerID      string `json:"server_id"`
	Domain        string `json:"domain"`
	Name          string `json:"name"`
}

type installTemplateResponse struct {
	Applications []string `json:"applications"`
	Databases    []string `json:"databases"`
	// FirstLogin is how to get into what was just installed. Returned ONCE — a
	// generated password appears here and nowhere else, ever (managed-databases
	// §9's discipline). Absent when the template declares nothing.
	FirstLogin *firstLoginDTO `json:"first_login,omitempty"`
}

type firstLoginDTO struct {
	// Kind: "credentials" (sign in with these), "setup" (the app makes you
	// create the account), "none" (nothing to sign into).
	Kind          string `json:"kind"`
	ApplicationID string `json:"application_id,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	// Generated distinguishes a password the panel invented — shown once and
	// unrecoverable — from a documented upstream default, which is public
	// knowledge and can be shown at any time.
	Generated bool   `json:"generated"`
	Note      string `json:"note,omitempty"`
}

func (a *API) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	if a.deps.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "template catalog is not configured")
		return
	}
	writeJSON(w, http.StatusOK, a.deps.Templates.List())
}

func (a *API) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	if a.deps.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "template catalog is not configured")
		return
	}
	tpl, ok := a.deps.Templates.Get(r.PathValue("slug"))
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	writeJSON(w, http.StatusOK, tpl)
}

func (a *API) handleInstallTemplate(w http.ResponseWriter, r *http.Request) {
	if a.deps.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "template catalog is not configured")
		return
	}
	var req installTemplateRequest
	if err := decodeJSON(r, &req); err != nil || req.EnvironmentID == "" || req.ServerID == "" {
		writeError(w, http.StatusBadRequest, "environment_id and server_id are required")
		return
	}
	user, _ := userFromContext(r.Context())
	projectID, err := a.projectIDForEnvironment(r.Context(), req.EnvironmentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	if !a.requireProjectRole(w, r, user, projectID, domain.RoleMember) {
		return
	}
	result, err := a.deps.Templates.Install(r.Context(), r.PathValue("slug"), templates.InstallInput{
		EnvironmentID: req.EnvironmentID,
		ServerID:      req.ServerID,
		Domain:        req.Domain,
		Name:          req.Name,
	})
	var validation *templates.ValidationError
	var partial *templates.PartialInstallError
	switch {
	case errors.Is(err, templates.ErrNotFound):
		writeError(w, http.StatusNotFound, "template not found")
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, validation.Error())
	case errors.As(err, &partial):
		// The install failed *and* left resources behind. Still a 500 — the
		// operator did nothing wrong — but the response has to name what
		// survived, or those resources are unfindable.
		a.deps.Log.Error("installing template: rollback incomplete", "slug", r.PathValue("slug"),
			"environment_id", req.EnvironmentID, "remaining", partial.Remaining, "error", partial.Cause)
		writeError(w, http.StatusInternalServerError,
			"could not install template, and rolling it back left resources behind: "+strings.Join(partial.Remaining, ", "))
	case err != nil:
		a.deps.Log.Error("installing template", "slug", r.PathValue("slug"), "environment_id", req.EnvironmentID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not install template")
	default:
		writeJSON(w, http.StatusAccepted, installTemplateResponse{
			Applications: result.ApplicationIDs, Databases: result.DatabaseIDs,
			FirstLogin: firstLoginToDTO(result.FirstLogin),
		})
	}
}

func firstLoginToDTO(fl *templates.FirstLogin) *firstLoginDTO {
	if fl == nil {
		return nil
	}
	return &firstLoginDTO{
		Kind: fl.Kind, ApplicationID: fl.ApplicationID,
		Username: fl.Username, Password: fl.Password,
		Generated: fl.Generated, Note: fl.Note,
	}
}

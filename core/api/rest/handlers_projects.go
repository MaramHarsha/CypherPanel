package rest

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type projectDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TeamID    string `json:"team_id"`
	CreatedAt string `json:"created_at"`
}

func toProjectDTO(p domain.Project) projectDTO {
	return projectDTO{ID: p.ID, Name: p.Name, TeamID: p.TeamID, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339)}
}

type environmentDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func toEnvironmentDTO(e domain.Environment) environmentDTO {
	return environmentDTO{ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339)}
}

type createProjectRequest struct {
	Name string `json:"name"`
	// TeamID is optional when the caller belongs to exactly one team
	// (teams-and-roles.md §4).
	TeamID string `json:"team_id"`
}

type createProjectResponse struct {
	Project            projectDTO     `json:"project"`
	DefaultEnvironment environmentDTO `json:"default_environment"`
}

type projectDetailResponse struct {
	Project      projectDTO       `json:"project"`
	Environments []environmentDTO `json:"environments"`
}

type createEnvironmentRequest struct {
	Name string `json:"name"`
}

// resolveCreateTeam picks the team for a new project: the explicit team_id, or
// the caller's only team; ambiguity is a 400 (spec §4). Returns "" after
// writing the response on failure.
func (a *API) resolveCreateTeam(w http.ResponseWriter, r *http.Request, user domain.User, teamID string) string {
	if teamID != "" {
		return teamID
	}
	list, err := a.deps.Teams.ListFor(r.Context(), user)
	if err != nil {
		a.deps.Log.Error("resolving default team", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create project")
		return ""
	}
	if len(list) != 1 {
		writeError(w, http.StatusBadRequest, "team_id is required (you belong to more than one team)")
		return ""
	}
	return list[0].ID
}

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	teamID := a.resolveCreateTeam(w, r, user, req.TeamID)
	if teamID == "" {
		return
	}
	// Creating a project is a team-structure change: team admin+ (spec §1).
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleAdmin) {
		return
	}
	proj, env, err := a.deps.Projects.Create(r.Context(), req.Name, teamID)
	if errors.Is(err, projects.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "name must be 1–100 characters")
		return
	}
	if err != nil {
		a.deps.Log.Error("creating project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create project")
		return
	}
	writeJSON(w, http.StatusCreated, createProjectResponse{
		Project:            toProjectDTO(proj),
		DefaultEnvironment: toEnvironmentDTO(env),
	})
}

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	var list []domain.Project
	var err error
	if user.Role == domain.RoleOwner {
		list, err = a.deps.Projects.List(r.Context()) // panel owner sees all (spec §1)
	} else {
		list, err = a.deps.Projects.ListForUser(r.Context(), user.ID)
	}
	if err != nil {
		a.deps.Log.Error("listing projects", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list projects")
		return
	}
	out := make([]projectDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, id, domain.RoleMember) {
		return
	}
	proj, err := a.deps.Projects.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get project")
		return
	}
	envs, err := a.deps.Projects.ListEnvironments(r.Context(), id)
	if err != nil {
		a.deps.Log.Error("listing environments", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get project")
		return
	}
	envDTOs := make([]environmentDTO, 0, len(envs))
	for _, e := range envs {
		envDTOs = append(envDTOs, toEnvironmentDTO(e))
	}
	writeJSON(w, http.StatusOK, projectDetailResponse{Project: toProjectDTO(proj), Environments: envDTOs})
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, id, domain.RoleAdmin) {
		return
	}
	// The projects row cascades: environments, applications and databases all
	// disappear with it. For applications that is survivable — the docker
	// driver tears down anything absent from desired state — but a managed
	// database is removed by a two-phase flow keyed on its own row
	// (pending_delete -> agent removes the container -> row deleted). Cascading
	// the row away skips that entirely, leaving the container AND its data
	// volume running on the server with nothing in the panel that knows they
	// exist. So the delete is refused while the project still holds resources,
	// which is the same stance server deletion already takes.
	if inUse, detail := a.projectResourcesInUse(r.Context(), id); inUse {
		writeError(w, http.StatusConflict, detail)
		return
	}
	if err := a.deps.Projects.Delete(r.Context(), id); err != nil {
		a.deps.Log.Error("deleting project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, id, domain.RoleMember) {
		return
	}
	envs, err := a.deps.Projects.ListEnvironments(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("listing environments", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list environments")
		return
	}
	out := make([]environmentDTO, 0, len(envs))
	for _, e := range envs {
		out = append(out, toEnvironmentDTO(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	var req createEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	env, err := a.deps.Projects.CreateEnvironment(r.Context(), r.PathValue("id"), req.Name)
	if errors.Is(err, projects.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "name must be 1–100 characters")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "an environment with that name already exists in this project")
		return
	}
	if err != nil {
		a.deps.Log.Error("creating environment", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create environment")
		return
	}
	writeJSON(w, http.StatusCreated, toEnvironmentDTO(env))
}

// cannotConfirmEmpty is the fail-closed answer: never cascade a project away
// on a guess.
const cannotConfirmEmpty = "could not confirm the project is empty, so it was not deleted — try again"

// projectResourcesInUse reports whether a project still holds applications or
// databases, and a message naming what to remove first.
//
// This lives here rather than in the projects service because the service
// deliberately knows nothing about applications or databases — wiring those in
// would invert the dependency for one guard. The counts come from the services
// that already own them.
func (a *API) projectResourcesInUse(ctx context.Context, projectID string) (bool, string) {
	envs, err := a.deps.Projects.ListEnvironments(ctx, projectID)
	if err != nil {
		// Fail closed: if we cannot prove the project is empty, do not cascade.
		a.deps.Log.Error("listing environments before project delete", "error", err)
		return true, cannotConfirmEmpty
	}
	apps, dbs := 0, 0
	for _, env := range envs {
		switch list, lerr := a.deps.Applications.List(ctx, env.ID); {
		case lerr == nil:
			apps += len(list)
		case errors.Is(lerr, applications.ErrEnvironmentNotFound):
			// An environment the applications side does not know cannot hold
			// applications; that is a zero, not an unknown.
		default:
			a.deps.Log.Error("listing applications before project delete", "error", lerr)
			return true, cannotConfirmEmpty
		}
		if list, lerr := a.deps.Databases.List(ctx, env.ID); lerr == nil {
			dbs += len(list)
		} else {
			a.deps.Log.Error("listing databases before project delete", "error", lerr)
			return true, cannotConfirmEmpty
		}
	}
	if apps == 0 && dbs == 0 {
		return false, ""
	}
	parts := make([]string, 0, 2)
	if apps > 0 {
		parts = append(parts, plural(apps, "application", "applications"))
	}
	if dbs > 0 {
		parts = append(parts, plural(dbs, "database", "databases"))
	}
	return true, "this project still contains " + strings.Join(parts, " and ") +
		" — delete them first so their containers and data volumes are properly torn down"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

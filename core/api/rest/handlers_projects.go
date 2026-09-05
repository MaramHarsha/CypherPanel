package rest

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type projectDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	TeamID string `json:"team_id"`
	// DefaultEnvironmentID is where "open this project" lands. Empty only if the
	// project has no environments left.
	DefaultEnvironmentID string `json:"default_environment_id,omitempty"`
	LastActivityAt       string `json:"last_activity_at"`
	CreatedAt            string `json:"created_at"`

	// The rollup the projects list renders: how much is in here and the worst
	// thing happening. Omitted on single-project reads, where the page shows the
	// resources themselves.
	ApplicationCount *int64 `json:"application_count,omitempty"`
	DatabaseCount    *int64 `json:"database_count,omitempty"`
	ErrorCount       *int64 `json:"error_count,omitempty"`
	WorstStatus      string `json:"worst_status,omitempty"`
}

func toProjectDTO(p domain.Project) projectDTO {
	return projectDTO{
		ID: p.ID, Name: p.Name, Slug: p.Slug, TeamID: p.TeamID,
		DefaultEnvironmentID: p.DefaultEnvironmentID,
		LastActivityAt:       p.LastActivityAt.UTC().Format(time.RFC3339),
		CreatedAt:            p.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// withRollup attaches list-only counts. A project holding nothing gets explicit
// zeros rather than omitted fields, so the UI can say "no resources yet"
// without guessing whether the number is missing or absent.
func withRollup(dto projectDTO, r domain.ProjectRollup, ok bool) projectDTO {
	apps, dbs, errs := r.ApplicationCount, r.DatabaseCount, r.ErrorCount
	if !ok {
		apps, dbs, errs = 0, 0, 0
	}
	dto.ApplicationCount, dto.DatabaseCount, dto.ErrorCount = &apps, &dbs, &errs
	dto.WorstStatus = r.WorstStatus
	if !ok {
		dto.WorstStatus = ""
	}
	return dto
}

type environmentDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Kind separates a preview, which its pull request owns, from a standing
	// environment the operator manages.
	Kind      string `json:"kind"`
	IsDefault bool   `json:"is_default"`
	CreatedAt string `json:"created_at"`
}

func toEnvironmentDTO(e domain.Environment) environmentDTO {
	return environmentDTO{
		ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, Kind: e.Kind,
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
	}
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
	a.audit(r, audit.Entry{
		Action:    audit.ActionProjectCreated,
		Resource:  audit.Resource(audit.ResourceProject, proj.ID, proj.Name),
		ProjectID: proj.ID,
		TeamID:    proj.TeamID,
		Detail:    map[string]any{"slug": proj.Slug, "default_environment_id": env.ID},
	})
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
	// One rollup query for the whole page rather than one per project.
	rollups, err := a.deps.Projects.Rollups(r.Context())
	if err != nil {
		a.deps.Log.Error("rolling up projects", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list projects")
		return
	}
	out := make([]projectDTO, 0, len(list))
	for _, p := range list {
		roll, ok := rollups[p.ID]
		out = append(out, withRollup(toProjectDTO(p), roll, ok))
	}
	writeJSON(w, http.StatusOK, out)
}

// patchProjectRequest is a partial edit. Slug is absent on purpose: it is
// chosen once and never changes, because URLs and scripts depend on it.
type patchProjectRequest struct {
	Name                 *string `json:"name"`
	TeamID               *string `json:"team_id"`
	DefaultEnvironmentID *string `json:"default_environment_id"`
}

// handlePatchProject renames a project, moves its default environment, or
// transfers it to another team.
//
// Rename and default-environment need team admin. A transfer is different in
// kind: it changes who can see everything inside, so it needs ownership of the
// destination as well as the source, and an interactive session — an API token
// that leaked must not be able to hand a project to a team the attacker
// controls.
func (a *API) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	var req patchProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !a.requireProjectRole(w, r, user, id, domain.RoleAdmin) {
		return
	}
	if req.TeamID != nil {
		p, ok := principalFromContext(r.Context())
		if !ok || p.Kind != auth.KindSession {
			writeError(w, http.StatusForbidden, "transferring a project requires an interactive session, not an API token")
			return
		}
		if !a.requireProjectRole(w, r, user, id, domain.RoleOwner) {
			return
		}
		// Ownership of the destination too: a transfer into a team you merely
		// belong to would let a member move work under someone else's roof.
		if !a.requireTeamRole(w, r, user, *req.TeamID, domain.RoleOwner) {
			return
		}
	}

	// Read the project before the update so a transfer can record where it came
	// FROM: the row after the write only knows where it went.
	before, _ := a.deps.Projects.Get(r.Context(), id)
	proj, err := a.deps.Projects.Update(r.Context(), id, projects.UpdateInput{
		Name:                 req.Name,
		TeamID:               req.TeamID,
		DefaultEnvironmentID: req.DefaultEnvironmentID,
	})
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "project not found")
		return
	case errors.Is(err, projects.ErrInvalidName):
		writeError(w, http.StatusBadRequest, projects.ErrInvalidName.Error())
		return
	case errors.Is(err, projects.ErrEnvironmentNotInProject):
		writeError(w, http.StatusBadRequest, "that environment is not in this project")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a project with that name already exists in the team")
		return
	default:
		a.deps.Log.Error("updating project", "project_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not update the project")
		return
	}
	// A transfer gets its OWN verb: moving a project between teams moves who
	// can see everything inside it — including, from this moment on, the
	// project's own audit trail (§5). A rename does not.
	entry := audit.Entry{
		Action:    audit.ActionProjectUpdated,
		Resource:  audit.Resource(audit.ResourceProject, proj.ID, proj.Name),
		ProjectID: proj.ID,
		TeamID:    proj.TeamID,
		Detail:    map[string]any{"previous_name": before.Name},
	}
	if before.TeamID != "" && before.TeamID != proj.TeamID {
		entry.Action = audit.ActionProjectTransferred
		entry.Detail["from_team_id"] = before.TeamID
		entry.Detail["to_team_id"] = proj.TeamID
	}
	a.audit(r, entry)
	writeJSON(w, http.StatusOK, toProjectDTO(proj))
}

type patchEnvironmentRequest struct {
	Name *string `json:"name"`
}

// handlePatchEnvironment renames a standing environment.
func (a *API) handlePatchEnvironment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleAdmin, func(ctx context.Context) (string, error) {
		env, err := a.deps.Projects.GetEnvironment(ctx, id)
		return env.ProjectID, err
	}) {
		return
	}
	var req patchEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == nil {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	env, err := a.deps.Projects.RenameEnvironment(r.Context(), id, *req.Name)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
		return
	case errors.Is(err, projects.ErrInvalidName):
		writeError(w, http.StatusBadRequest, projects.ErrInvalidName.Error())
		return
	case errors.Is(err, projects.ErrPreviewEnvironment):
		writeError(w, http.StatusConflict, "a preview environment is managed by its pull request")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "an environment with that name already exists in the project")
		return
	default:
		a.deps.Log.Error("renaming environment", "environment_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not rename the environment")
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionEnvironmentRenamed,
		Resource:      audit.Resource(audit.ResourceEnvironment, env.ID, env.Name),
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
	})
	writeJSON(w, http.StatusOK, toEnvironmentDTO(env))
}

// handleDeleteEnvironment removes a standing environment. Resources still
// inside are refused by the store, the same protection deleting a project gets.
func (a *API) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleAdmin, func(ctx context.Context) (string, error) {
		env, err := a.deps.Projects.GetEnvironment(ctx, id)
		return env.ProjectID, err
	}) {
		return
	}
	// The environment row is about to vanish, and with it the link the insert
	// would resolve the project from (§4) — so both are read first.
	before, _ := a.deps.Projects.GetEnvironment(r.Context(), id)
	err := a.deps.Projects.DeleteEnvironment(r.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
		return
	case errors.Is(err, projects.ErrPreviewEnvironment):
		writeError(w, http.StatusConflict, "a preview environment is managed by its pull request")
		return
	case errors.Is(err, projects.ErrLastEnvironment):
		writeError(w, http.StatusConflict, "a project keeps at least one environment — create another before removing this one")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "the environment still holds resources — remove them first")
		return
	default:
		a.deps.Log.Error("deleting environment", "environment_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the environment")
		return
	}
	a.audit(r, audit.Entry{
		Action:    audit.ActionEnvironmentDeleted,
		Resource:  audit.Resource(audit.ResourceEnvironment, id, before.Name),
		ProjectID: before.ProjectID,
	})
	w.WriteHeader(http.StatusNoContent)
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
	// Read the project first: after the delete there is no row to resolve the
	// team from, and an entry that lost its team_id would be invisible to
	// exactly the person who made it (§4).
	before, _ := a.deps.Projects.Get(r.Context(), id)
	if err := a.deps.Projects.Delete(r.Context(), id); err != nil {
		a.deps.Log.Error("deleting project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete project")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionProjectDeleted,
		Resource: audit.Resource(audit.ResourceProject, id, before.Name),
		TeamID:   before.TeamID,
	})
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
	a.audit(r, audit.Entry{
		Action:        audit.ActionEnvironmentCreated,
		Resource:      audit.Resource(audit.ResourceEnvironment, env.ID, env.Name),
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
	})
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

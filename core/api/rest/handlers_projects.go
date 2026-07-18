package rest

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type projectDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func toProjectDTO(p domain.Project) projectDTO {
	return projectDTO{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339)}
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

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	proj, env, err := a.deps.Projects.Create(r.Context(), req.Name)
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
	list, err := a.deps.Projects.List(r.Context())
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
	id := r.PathValue("id")
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
	if err := a.deps.Projects.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.deps.Log.Error("deleting project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := a.deps.Projects.ListEnvironments(r.Context(), r.PathValue("id"))
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
	if err != nil {
		a.deps.Log.Error("creating environment", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create environment")
		return
	}
	writeJSON(w, http.StatusCreated, toEnvironmentDTO(env))
}

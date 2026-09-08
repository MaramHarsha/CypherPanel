package rest

// Container registry credentials (registries.md; ADR-008 path 3).
//
// Team-scoped, like every other shared credential here: listing filters to the
// caller's teams rather than refusing, and a registry in a team the caller is
// not in is not found rather than forbidden — the same rule the project routes
// follow, for the same reason (no tenancy probing).

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/registries"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// RegistryService is the registry surface (consumer-defined;
// *registries.Service satisfies it).
type RegistryService interface {
	Create(ctx context.Context, teamID string, in registries.Input) (domain.Registry, error)
	Update(ctx context.Context, id string, in registries.UpdateInput) (domain.Registry, error)
	Get(ctx context.Context, id string) (domain.Registry, error)
	ListForTeams(ctx context.Context, teamIDs []string) ([]domain.Registry, error)
	UsedBy(ctx context.Context, id string) ([]domain.RegistryUse, error)
	Delete(ctx context.Context, id string) error
	TestConfig(ctx context.Context, in registries.Input) (registries.TestResult, error)
	Test(ctx context.Context, id string) (registries.TestResult, error)
}

type registryDTO struct {
	ID       string `json:"id"`
	TeamID   string `json:"team_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	CanPull  bool   `json:"can_pull"`
	CanPush  bool   `json:"can_push"`
	// TokenSet says a credential exists without saying what it is — the
	// notifier contract, for the same reason.
	TokenSet       bool       `json:"token_set"`
	LastTestAt     *time.Time `json:"last_test_at,omitempty"`
	LastTestOK     bool       `json:"last_test_ok"`
	LastTestDetail string     `json:"last_test_detail,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toRegistryDTO(r domain.Registry) registryDTO {
	return registryDTO{
		ID: r.ID, TeamID: r.TeamID, Name: r.Name, URL: r.URL, Username: r.Username,
		CanPull: r.CanPull, CanPush: r.CanPush,
		TokenSet:   len(r.TokenCT) > 0,
		LastTestAt: r.LastTestAt, LastTestOK: r.LastTestOK, LastTestDetail: r.LastTestDetail,
		CreatedAt: r.CreatedAt,
	}
}

type registryUseDTO struct {
	ApplicationID   string `json:"application_id"`
	ApplicationName string `json:"application_name"`
	EnvironmentName string `json:"environment_name"`
	ProjectName     string `json:"project_name"`
	Pulls           bool   `json:"pulls"`
	Pushes          bool   `json:"pushes"`
}

type createRegistryRequest struct {
	TeamID   string `json:"team_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Token    string `json:"token"`
	CanPull  *bool  `json:"can_pull"`
	CanPush  *bool  `json:"can_push"`
}

type patchRegistryRequest struct {
	Name     *string `json:"name"`
	URL      *string `json:"url"`
	Username *string `json:"username"`
	Token    *string `json:"token"`
	CanPull  *bool   `json:"can_pull"`
	CanPush  *bool   `json:"can_push"`
}

type testRegistryRequest struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// registryTeam resolves the registry and checks the caller's rank in its team.
// Not found and not-a-member answer the same way, so membership is not
// probeable through this route.
func (a *API) registryTeam(w http.ResponseWriter, r *http.Request, user domain.User, id, min string) (domain.Registry, bool) {
	reg, err := a.deps.Registries.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return domain.Registry{}, false
		}
		a.deps.Log.Error("getting registry", "registry_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the registry")
		return domain.Registry{}, false
	}
	role, err := a.teamRoleIn(r.Context(), user, reg.TeamID)
	if err != nil {
		a.deps.Log.Error("resolving team role", "team_id", reg.TeamID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return domain.Registry{}, false
	}
	if role == "" {
		writeError(w, http.StatusNotFound, "registry not found")
		return domain.Registry{}, false
	}
	if domain.RoleRank(role) < domain.RoleRank(min) {
		writeError(w, http.StatusForbidden, "insufficient role")
		return domain.Registry{}, false
	}
	return reg, true
}

func (a *API) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	teams, err := a.deps.Teams.ListFor(r.Context(), user)
	if err != nil {
		a.deps.Log.Error("listing teams", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list registries")
		return
	}
	teamIDs := make([]string, 0, len(teams))
	for _, t := range teams {
		teamIDs = append(teamIDs, t.ID)
	}
	list, err := a.deps.Registries.ListForTeams(r.Context(), teamIDs)
	if err != nil {
		a.deps.Log.Error("listing registries", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list registries")
		return
	}
	out := make([]registryDTO, 0, len(list))
	for _, reg := range list {
		out = append(out, toRegistryDTO(reg))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	var req createRegistryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	teamID, ok := a.resolveRegistryTeam(w, r, user, req.TeamID)
	if !ok {
		return
	}
	// Pull defaults on, push defaults off: pulling a private base image is the
	// common case, and a credential that can push is a bigger grant than one
	// that cannot.
	canPull, canPush := true, false
	if req.CanPull != nil {
		canPull = *req.CanPull
	}
	if req.CanPush != nil {
		canPush = *req.CanPush
	}
	reg, err := a.deps.Registries.Create(r.Context(), teamID, registries.Input{
		Name: req.Name, URL: req.URL, Username: req.Username, Token: req.Token,
		CanPull: canPull, CanPush: canPush,
	})
	if !a.writeRegistryError(w, "creating registry", err) {
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionRegistryCreated,
		Resource: audit.Resource(audit.ResourceRegistry, reg.ID, reg.Name),
		TeamID:   reg.TeamID,
		Detail:   map[string]any{"url": reg.URL, "can_pull": reg.CanPull, "can_push": reg.CanPush},
	})
	writeJSON(w, http.StatusCreated, toRegistryDTO(reg))
}

// resolveRegistryTeam picks the team a new registry belongs to, and checks the
// caller is an admin of it. Omitted when the caller is in exactly one team,
// matching how creating a project resolves its team.
func (a *API) resolveRegistryTeam(w http.ResponseWriter, r *http.Request, user domain.User, requested string) (string, bool) {
	if requested != "" {
		if !a.requireTeamRole(w, r, user, requested, domain.RoleAdmin) {
			return "", false
		}
		return requested, true
	}
	teams, err := a.deps.Teams.ListFor(r.Context(), user)
	if err != nil {
		a.deps.Log.Error("listing teams", "error", err)
		writeError(w, http.StatusInternalServerError, "could not resolve the team")
		return "", false
	}
	if len(teams) != 1 {
		writeError(w, http.StatusBadRequest, "team_id is required when you belong to more than one team")
		return "", false
	}
	if !a.requireTeamRole(w, r, user, teams[0].ID, domain.RoleAdmin) {
		return "", false
	}
	return teams[0].ID, true
}

func (a *API) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	reg, ok := a.registryTeam(w, r, user, r.PathValue("id"), domain.RoleMember)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toRegistryDTO(reg))
}

func (a *API) handlePatchRegistry(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	reg, ok := a.registryTeam(w, r, user, r.PathValue("id"), domain.RoleAdmin)
	if !ok {
		return
	}
	var req patchRegistryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated, err := a.deps.Registries.Update(r.Context(), reg.ID, registries.UpdateInput{
		Name: req.Name, URL: req.URL, Username: req.Username, Token: req.Token,
		CanPull: req.CanPull, CanPush: req.CanPush,
	})
	if !a.writeRegistryError(w, "updating registry", err) {
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionRegistryUpdated,
		Resource: audit.Resource(audit.ResourceRegistry, updated.ID, updated.Name),
		TeamID:   updated.TeamID,
		// Which fields moved, never their values: the token is the whole point
		// of the secret discipline here.
		Detail: map[string]any{"token_rotated": req.Token != nil},
	})
	writeJSON(w, http.StatusOK, toRegistryDTO(updated))
}

func (a *API) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	reg, ok := a.registryTeam(w, r, user, r.PathValue("id"), domain.RoleAdmin)
	if !ok {
		return
	}
	err := a.deps.Registries.Delete(r.Context(), reg.ID)
	switch {
	case err == nil:
	case errors.Is(err, registries.ErrInUse):
		// The 409 names nothing here; the caller asks used-by for the list,
		// which is the same shape the deploy-key conflict uses.
		writeError(w, http.StatusConflict, "applications still use this registry — see its used-by list")
		return
	default:
		a.deps.Log.Error("deleting registry", "registry_id", reg.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the registry")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionRegistryDeleted,
		Resource: audit.Resource(audit.ResourceRegistry, reg.ID, reg.Name),
		TeamID:   reg.TeamID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRegistryUsedBy(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	reg, ok := a.registryTeam(w, r, user, r.PathValue("id"), domain.RoleMember)
	if !ok {
		return
	}
	uses, err := a.deps.Registries.UsedBy(r.Context(), reg.ID)
	if err != nil {
		a.deps.Log.Error("listing registry users", "registry_id", reg.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list what uses this registry")
		return
	}
	out := make([]registryUseDTO, 0, len(uses))
	for _, u := range uses {
		out = append(out, registryUseDTO{
			ApplicationID: u.ApplicationID, ApplicationName: u.ApplicationName,
			EnvironmentName: u.EnvironmentName, ProjectName: u.ProjectName,
			Pulls: u.Pulls, Pushes: u.Pushes,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTestRegistryConfig proves a credential before it is saved, the same
// shape the notifier connection test answers with.
func (a *API) handleTestRegistryConfig(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	// Testing an arbitrary host is an outbound request the caller chooses, so
	// it takes the same rank as creating one rather than merely being a member.
	if _, ok := a.resolveRegistryTeam(w, r, user, r.URL.Query().Get("team_id")); !ok {
		return
	}
	var req testRegistryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := a.deps.Registries.TestConfig(r.Context(), registries.Input{
		// Name and scopes are not part of an authentication attempt; the
		// placeholders satisfy the shared validation without inventing input.
		Name: "test", URL: req.URL, Username: req.Username, Token: req.Token, CanPull: true,
	})
	var ve *registries.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return
	}
	if err != nil {
		a.deps.Log.Error("testing registry config", "error", err)
		writeError(w, http.StatusInternalServerError, "could not test the registry")
		return
	}
	writeJSON(w, http.StatusOK, connectionTestDTO{OK: res.OK, Detail: res.Detail})
}

func (a *API) handleTestRegistry(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Registries == nil {
		writeError(w, http.StatusNotImplemented, "registries are not enabled")
		return
	}
	reg, ok := a.registryTeam(w, r, user, r.PathValue("id"), domain.RoleAdmin)
	if !ok {
		return
	}
	res, err := a.deps.Registries.Test(r.Context(), reg.ID)
	if err != nil {
		a.deps.Log.Error("testing registry", "registry_id", reg.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not test the registry")
		return
	}
	writeJSON(w, http.StatusOK, connectionTestDTO{OK: res.OK, Detail: res.Detail})
}

// writeRegistryError maps service errors and reports whether the caller should
// continue.
func (a *API) writeRegistryError(w http.ResponseWriter, op string, err error) bool {
	var ve *registries.ValidationError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registry not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a registry with that name already exists in the team")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
	return false
}

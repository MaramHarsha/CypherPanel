package rest

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/teams"
)

type teamDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role,omitempty"` // the caller's role, on listings
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type teamMemberDTO struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func toTeamMemberDTO(m domain.TeamMember) teamMemberDTO {
	return teamMemberDTO{UserID: m.UserID, Email: m.Email, Role: m.Role, CreatedAt: m.CreatedAt}
}

// ─── Teams ──────────────────────────────────────────────────────────────────

func (a *API) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t, err := a.deps.Teams.Create(r.Context(), req.Name, user)
	if err != nil {
		a.writeTeamError(w, "creating team", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamCreated,
		Resource: audit.Resource(audit.ResourceTeam, t.ID, t.Name),
		TeamID:   t.ID,
	})
	writeJSON(w, http.StatusCreated, teamDTO{ID: t.ID, Name: t.Name, Role: domain.RoleOwner, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
}

func (a *API) handleListTeams(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	list, err := a.deps.Teams.ListFor(r.Context(), user)
	if err != nil {
		a.deps.Log.Error("listing teams", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list teams")
		return
	}
	out := make([]teamDTO, 0, len(list))
	for _, t := range list {
		out = append(out, teamDTO{ID: t.ID, Name: t.Name, Role: t.Role, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleMember) {
		return
	}
	t, err := a.deps.Teams.Get(r.Context(), teamID)
	if err != nil {
		a.writeTeamError(w, "getting team", err)
		return
	}
	writeJSON(w, http.StatusOK, teamDTO{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
}

func (a *API) handleRenameTeam(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleOwner) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	before, _ := a.deps.Teams.Get(r.Context(), teamID)
	t, err := a.deps.Teams.Rename(r.Context(), teamID, req.Name)
	if err != nil {
		a.writeTeamError(w, "renaming team", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamRenamed,
		Resource: audit.Resource(audit.ResourceTeam, t.ID, t.Name),
		TeamID:   t.ID,
		Detail:   map[string]any{"previous_name": before.Name},
	})
	writeJSON(w, http.StatusOK, teamDTO{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
}

func (a *API) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleOwner) {
		return
	}
	// Read the name before the row is gone: the entry outlives the team, and
	// "tm_k7q2…" is not an answer to "which team was deleted?" (§2).
	before, _ := a.deps.Teams.Get(r.Context(), teamID)
	if err := a.deps.Teams.Delete(r.Context(), teamID); err != nil {
		a.writeTeamError(w, "deleting team", err)
		return
	}
	// TeamID is passed explicitly because there is nothing left to resolve it
	// from — the deleting member must still be able to see what they did (§4).
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamDeleted,
		Resource: audit.Resource(audit.ResourceTeam, teamID, before.Name),
		TeamID:   teamID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Members ────────────────────────────────────────────────────────────────

func (a *API) handleListTeamMembers(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if !a.requireTeamRole(w, r, user, teamID, domain.RoleMember) {
		return
	}
	members, err := a.deps.Teams.Members(r.Context(), teamID)
	if err != nil {
		a.deps.Log.Error("listing team members", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list members")
		return
	}
	out := make([]teamMemberDTO, 0, len(members))
	for _, m := range members {
		out = append(out, toTeamMemberDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// actorTeamRole resolves the caller's effective role and writes 404 when they
// have none (spec §3). The service's grant rules do the finer 403s.
func (a *API) actorTeamRole(w http.ResponseWriter, r *http.Request, user domain.User, teamID string) (string, bool) {
	role, err := a.teamRoleIn(r.Context(), user, teamID)
	if err != nil {
		a.deps.Log.Error("resolving team role", "team_id", teamID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return "", false
	}
	if role == "" {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return role, true
}

func (a *API) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	actorRole, ok := a.actorTeamRole(w, r, user, teamID)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" {
		req.Role = domain.RoleMember
	}
	m, err := a.deps.Teams.AddMember(r.Context(), teamID, req.Email, req.Role, actorRole)
	if err != nil {
		a.writeTeamError(w, "adding team member", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamMemberAdded,
		Resource: audit.Resource(audit.ResourceTeam, teamID, ""),
		TeamID:   teamID,
		Detail:   map[string]any{"member_user_id": m.UserID, "member_email": m.Email, "role": m.Role},
	})
	writeJSON(w, http.StatusCreated, toTeamMemberDTO(m))
}

func (a *API) handleChangeTeamMemberRole(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	actorRole, ok := a.actorTeamRole(w, r, user, teamID)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	m, err := a.deps.Teams.ChangeMemberRole(r.Context(), teamID, r.PathValue("uid"), req.Role, actorRole)
	if err != nil {
		a.writeTeamError(w, "changing member role", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamMemberRoleChanged,
		Resource: audit.Resource(audit.ResourceTeam, teamID, ""),
		TeamID:   teamID,
		Detail:   map[string]any{"member_user_id": m.UserID, "member_email": m.Email, "role": m.Role},
	})
	writeJSON(w, http.StatusOK, toTeamMemberDTO(m))
}

func (a *API) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	actorRole, ok := a.actorTeamRole(w, r, user, teamID)
	if !ok {
		return
	}
	if err := a.deps.Teams.RemoveMember(r.Context(), teamID, r.PathValue("uid"), actorRole); err != nil {
		a.writeTeamError(w, "removing team member", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTeamMemberRemoved,
		Resource: audit.Resource(audit.ResourceTeam, teamID, ""),
		TeamID:   teamID,
		Detail:   map[string]any{"member_user_id": r.PathValue("uid")},
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Users (panel-level) ────────────────────────────────────────────────────

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := a.deps.Teams.CreateUser(r.Context(), req.Email, req.Password, req.Role, user.Role)
	if err != nil {
		a.writeTeamError(w, "creating user", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionUserCreated,
		Resource: audit.Resource(audit.ResourceUser, u.ID, u.Email),
		Detail:   map[string]any{"role": u.Role},
	})
	writeJSON(w, http.StatusCreated, userDTO{ID: u.ID, Email: u.Email, Role: u.Role})
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	users, err := a.deps.Teams.ListUsers(r.Context())
	if err != nil {
		a.deps.Log.Error("listing users", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, userDTO{ID: u.ID, Email: u.Email, Role: u.Role})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := a.deps.Teams.SetUserRole(r.Context(), r.PathValue("id"), req.Role, user)
	if err != nil {
		a.writeTeamError(w, "setting user role", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionUserRoleChanged,
		Resource: audit.Resource(audit.ResourceUser, u.ID, u.Email),
		Detail:   map[string]any{"role": u.Role},
	})
	writeJSON(w, http.StatusOK, userDTO{ID: u.ID, Email: u.Email, Role: u.Role})
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if err := a.deps.Teams.DeleteUser(r.Context(), r.PathValue("id"), user); err != nil {
		a.writeTeamError(w, "deleting user", err)
		return
	}
	// The deleted account's own entries stay, still naming it: the actor label
	// on every past row is a snapshot, not a live lookup (§2, canvas 14k).
	a.audit(r, audit.Entry{
		Action:   audit.ActionUserDeleted,
		Resource: audit.Resource(audit.ResourceUser, r.PathValue("id"), ""),
	})
	w.WriteHeader(http.StatusNoContent)
}

// writeTeamError maps teams-service errors to statuses.
func (a *API) writeTeamError(w http.ResponseWriter, op string, err error) {
	var ve *teams.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, teams.ErrForbidden):
		writeError(w, http.StatusForbidden, "insufficient role")
	case errors.Is(err, teams.ErrLastOwner):
		writeError(w, http.StatusConflict, "a team must keep at least one owner")
	case errors.Is(err, teams.ErrTeamNotEmpty):
		writeError(w, http.StatusConflict, "delete or move the team's projects first")
	case errors.Is(err, teams.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}

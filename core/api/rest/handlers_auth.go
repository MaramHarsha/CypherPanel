package rest

import (
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/auth"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type loginResponse struct {
	Token string  `json:"token"`
	User  userDTO `json:"user"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	token, user, err := a.deps.Auth.Login(r.Context(), req.Email, req.Password, clientIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again in a moment")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	case err != nil:
		a.deps.Log.Error("login failed", "error", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token: token,
		User:  userDTO{ID: user.ID, Email: user.Email, Role: user.Role},
	})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := bearerToken(r)
	if err := a.deps.Auth.Logout(r.Context(), token); err != nil {
		a.deps.Log.Error("logout failed", "error", err)
		writeError(w, http.StatusInternalServerError, "logout failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// meResponse extends the user with their teams and per-team roles
// (teams-and-roles.md §4).
type meResponse struct {
	userDTO
	Teams []teamDTO `json:"teams"`
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	resp := meResponse{userDTO: userDTO{ID: user.ID, Email: user.Email, Role: user.Role}, Teams: []teamDTO{}}
	if a.deps.Teams != nil {
		list, err := a.deps.Teams.ListFor(r.Context(), user)
		if err != nil {
			a.deps.Log.Error("listing user teams", "error", err)
			writeError(w, http.StatusInternalServerError, "could not load account")
			return
		}
		for _, t := range list {
			resp.Teams = append(resp.Teams, teamDTO{ID: t.ID, Name: t.Name, Role: t.Role, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

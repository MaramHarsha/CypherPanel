package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/auth"
)

type updateProfileRequest struct {
	DisplayName string `json:"display_name"`
	Timezone    string `json:"timezone"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	// RevokeOtherSessions signs every other device out. The dialog asks
	// (canvas 9i) rather than deciding, so the field is explicit here too.
	RevokeOtherSessions bool `json:"revoke_other_sessions"`
}

type changePasswordResponse struct {
	Revoked int64 `json:"revoked"`
}

// handleUpdateProfile sets the caller's own display name and timezone. Session
// only: an API token is for scripts, and a script has no profile to edit.
func (a *API) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	user, err := a.deps.Auth.UpdateProfile(r.Context(), p.User.ID, req.DisplayName, req.Timezone)
	if err != nil {
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		a.deps.Log.Error("updating profile", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save your profile")
		return
	}
	writeJSON(w, http.StatusOK, userDTO{
		ID: user.ID, Email: user.Email, Role: user.Role,
		DisplayName: user.DisplayName, Timezone: user.Timezone,
	})
}

// handleChangePassword rotates the caller's password after proving the current
// one, optionally signing every other device out.
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	n, err := a.deps.Auth.ChangePassword(
		r.Context(), p.User.ID, req.CurrentPassword, req.NewPassword,
		rawTokenFromContext(r.Context()), req.RevokeOtherSessions,
	)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, changePasswordResponse{Revoked: n})
	case errors.Is(err, auth.ErrInvalidCredentials):
		// 401 rather than 400: the current password is a credential, and this
		// is the same answer a wrong one gets at the login form.
		writeError(w, http.StatusUnauthorized, "that is not your current password")
	case errors.Is(err, auth.ErrSamePassword):
		writeError(w, http.StatusBadRequest, "the new password is the same as the current one")
	default:
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		a.deps.Log.Error("changing password", "error", err)
		writeError(w, http.StatusInternalServerError, "could not change your password")
	}
}

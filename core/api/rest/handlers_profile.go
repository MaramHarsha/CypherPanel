package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/store"
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

type avatarResponse struct {
	ETag string `json:"etag"`
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

// handleSetAvatar takes the image as the raw request body. Multipart would add
// a parser and a filename we would only throw away; the browser can send a File
// directly, and MaxBytesReader caps it before anything is buffered.
func (a *API) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, auth.MaxAvatarBytes+1)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("an avatar is at most %d MB", auth.MaxAvatarBytes>>20))
		return
	}
	etag, err := a.deps.Auth.SetAvatar(r.Context(), p.User.ID, data)
	if err != nil {
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		a.deps.Log.Error("setting avatar", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save that image")
		return
	}
	writeJSON(w, http.StatusOK, avatarResponse{ETag: etag})
}

// handleDeleteAvatar removes the photo; initials take its place.
func (a *API) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	if err := a.deps.Auth.RemoveAvatar(r.Context(), p.User.ID); err != nil {
		a.deps.Log.Error("removing avatar", "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove that image")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetAvatar serves a user's photo to any signed-in caller — a panel's
// members are not anonymous to each other, and a member list wants faces.
//
// The response is locked down rather than trusted: the content type is the one
// the bytes were recognised as, nosniff stops a browser from reconsidering it,
// and a default-src 'none' policy means that even a file that somehow got past
// the allowlist has nothing to reach for.
func (a *API) handleGetAvatar(w http.ResponseWriter, r *http.Request) {
	av, err := a.deps.Auth.Avatar(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no avatar")
			return
		}
		a.deps.Log.Error("reading avatar", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read that image")
		return
	}
	w.Header().Set("Content-Type", av.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "inline")
	// Private: it is per-user content behind a session, so a shared cache must
	// not keep it. The ETag still saves the round trip for the browser itself.
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", `"`+av.ETag+`"`)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, av.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "", av.UpdatedAt, bytes.NewReader(av.Bytes))
}

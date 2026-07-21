package rest

import (
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/onboarding"
)

// setupStatusResponse tells the UI whether to show the first-run setup screen
// or the login screen (first-run-setup.md §3).
type setupStatusResponse struct {
	NeedsSetup bool `json:"needs_setup"`
}

// handleSetupStatus is public: it reveals only whether the panel has any
// account yet — no secret, and the UI needs it before anyone can authenticate.
func (a *API) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if a.deps.Onboarding == nil {
		writeJSON(w, http.StatusOK, setupStatusResponse{NeedsSetup: false})
		return
	}
	needs, err := a.deps.Onboarding.NeedsSetup(r.Context())
	if err != nil {
		a.deps.Log.Error("checking setup status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not check setup status")
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{NeedsSetup: needs})
}

type setupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleSetup creates the first owner account when the panel has none, and
// signs it straight in. Public and one-time: once any account exists it returns
// 409, so it can never mint a second account on an open panel (first-run-setup.md §4).
func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	if a.deps.Onboarding == nil {
		writeError(w, http.StatusConflict, "setup is not available")
		return
	}
	var req setupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	owner, err := a.deps.Onboarding.CreateFirstOwner(r.Context(), req.Email, req.Password)
	if err != nil {
		var ve *onboarding.ValidationError
		switch {
		case errors.As(err, &ve):
			writeError(w, http.StatusBadRequest, ve.Msg)
		case errors.Is(err, onboarding.ErrAlreadySetUp):
			writeError(w, http.StatusConflict, "this panel is already set up — sign in instead")
		default:
			a.deps.Log.Error("first-run setup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not complete setup")
		}
		return
	}
	token, err := a.deps.Auth.StartSession(r.Context(), owner.ID)
	if err != nil {
		// The account exists now; the operator can just sign in.
		a.deps.Log.Error("setup: starting session", "error", err)
		writeError(w, http.StatusInternalServerError, "account created — please sign in")
		return
	}
	writeJSON(w, http.StatusCreated, loginResponse{
		Token: token,
		User:  userDTO{ID: owner.ID, Email: owner.Email, Role: owner.Role},
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TOTPCode is the optional second factor: an authenticator code or a
	// recovery code. Required only when the account has two-factor enabled.
	TOTPCode string `json:"totp_code"`
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
	token, user, err := a.deps.Auth.Login(r.Context(), req.Email, req.Password, req.TOTPCode, clientIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again in a moment")
		return
	case errors.Is(err, auth.ErrTOTPRequired):
		// Password was correct; the client must supply a second factor and
		// retry. A distinct flag lets the UI prompt for the code (not the
		// password) without leaking whether 2FA is on before the password check.
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":         "two-factor authentication code required",
			"totp_required": true,
		})
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

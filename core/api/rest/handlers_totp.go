package rest

import (
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
)

type totpStatusResponse struct {
	Enabled           bool `json:"enabled"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

type totpEnrollResponse struct {
	Secret string `json:"secret"`      // base32, for manual entry
	URI    string `json:"otpauth_uri"` // for QR rendering by the client
}

type totpCodeRequest struct {
	Code string `json:"code"`
}

type totpVerifyResponse struct {
	RecoveryCodes []string `json:"recovery_codes"` // shown exactly once
}

func (a *API) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	st, err := a.deps.Auth.TOTPStatus(r.Context(), user.ID)
	if err != nil {
		a.deps.Log.Error("totp status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load two-factor status")
		return
	}
	writeJSON(w, http.StatusOK, totpStatusResponse{Enabled: st.Enabled, RecoveryCodesLeft: st.RecoveryCodesLeft})
}

func (a *API) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	enr, err := a.deps.Auth.EnrollTOTP(r.Context(), user.ID, user.Email)
	if errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		writeError(w, http.StatusConflict, "two-factor is already enabled; disable it first to re-enroll")
		return
	}
	if err != nil {
		a.deps.Log.Error("totp enroll", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start two-factor enrollment")
		return
	}
	writeJSON(w, http.StatusOK, totpEnrollResponse{Secret: enr.Secret, URI: enr.URI})
}

func (a *API) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req totpCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	codes, err := a.deps.Auth.VerifyTOTP(r.Context(), user.ID, req.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		writeError(w, http.StatusBadRequest, "start enrollment before verifying")
		return
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		// 400, not 401: the session is valid — only the submitted factor is
		// wrong. A 401 here would be indistinguishable from an expired session,
		// and clients that sign the operator out on 401 would log them out for
		// a typo mid-enrollment.
		writeError(w, http.StatusBadRequest, "invalid two-factor code")
		return
	case err != nil:
		a.deps.Log.Error("totp verify", "error", err)
		writeError(w, http.StatusInternalServerError, "could not enable two-factor")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionTOTPEnabled,
		Resource: audit.Resource(audit.ResourceUser, user.ID, user.Email),
	})
	writeJSON(w, http.StatusOK, totpVerifyResponse{RecoveryCodes: codes})
}

func (a *API) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req totpCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := a.deps.Auth.DisableTOTP(r.Context(), user.ID, req.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		writeError(w, http.StatusBadRequest, "two-factor is not enabled")
		return
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		// 400, not 401: the session is valid — only the submitted factor is
		// wrong. A 401 here would be indistinguishable from an expired session,
		// and clients that sign the operator out on 401 would log them out for
		// a typo mid-enrollment.
		writeError(w, http.StatusBadRequest, "invalid two-factor code")
		return
	case err != nil:
		a.deps.Log.Error("totp disable", "error", err)
		writeError(w, http.StatusInternalServerError, "could not disable two-factor")
		return
	}
	// Turning 2FA off is the last step of an account takeover (threat-model
	// §5.8), so it is one of the rows the log exists for.
	a.audit(r, audit.Entry{
		Action:   audit.ActionTOTPDisabled,
		Resource: audit.Resource(audit.ResourceUser, user.ID, user.Email),
	})
	w.WriteHeader(http.StatusNoContent)
}

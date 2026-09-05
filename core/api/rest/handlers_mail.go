package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/mail"
	"github.com/MaramHarsha/cypherpanel/core/notify"
)

// MailService is the panel's own transport (consumer-defined, rule 6).
type MailService interface {
	Get(ctx context.Context) (mail.Settings, error)
	Set(ctx context.Context, c mail.Config) (mail.Settings, error)
	Delete(ctx context.Context) error
	Send(ctx context.Context, to []string, subject, body string) error
	Test(ctx context.Context) error
}

type mailSettingsDTO struct {
	Configured bool       `json:"configured"`
	ConfigHint string     `json:"config_hint"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// The password goes in and is never read back — the notifier contract, for the
// same reason: a settings screen that can display a credential is a settings
// screen that can leak one.
type setMailRequest struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
}

func mailDTO(s mail.Settings) mailSettingsDTO {
	dto := mailSettingsDTO{Configured: s.Configured, ConfigHint: s.Hint}
	if !s.UpdatedAt.IsZero() {
		u := s.UpdatedAt
		dto.UpdatedAt = &u
	}
	return dto
}

func (a *API) handleGetPanelMail(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	s, err := a.deps.Mail.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading panel mail", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the mail settings")
		return
	}
	writeJSON(w, http.StatusOK, mailDTO(s))
}

func (a *API) handleSetPanelMail(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req setMailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	s, err := a.deps.Mail.Set(r.Context(), mail.Config{
		SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort,
		Username: req.Username, Password: req.Password, From: req.From,
	})
	if err != nil {
		var ve *mail.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		a.deps.Log.Error("saving panel mail", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the mail settings")
		return
	}
	writeJSON(w, http.StatusOK, mailDTO(s))
}

func (a *API) handleDeletePanelMail(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if err := a.deps.Mail.Delete(r.Context()); err != nil {
		a.deps.Log.Error("deleting panel mail", "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove the mail settings")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestPanelMail proves the settings by using them. A failure returns the
// server's own words: "connection refused" is the whole answer, and paraphrasing
// it would only make the operator guess.
func (a *API) handleTestPanelMail(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	err := a.deps.Mail.Test(r.Context())
	switch {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, mail.ErrNotConfigured):
		writeError(w, http.StatusBadRequest, "save the mail settings first, then send a test")
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

// ─── email change (docs/features/panel-mail.md §3) ──────────────────────────

type requestEmailChangeRequest struct {
	NewEmail        string `json:"new_email"`
	CurrentPassword string `json:"current_password"`
}

type confirmEmailChangeRequest struct {
	Token string `json:"token"`
}

type confirmEmailChangeResponse struct {
	Email   string `json:"email"`
	Revoked int64  `json:"revoked"`
}

// handleRequestEmailChange mails a confirmation to the new address, and a notice
// to the old one. The notice is not a courtesy: it is the only warning the
// rightful owner gets if the session and the password are already lost
// (threat-model §5.10).
func (a *API) handleRequestEmailChange(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var req requestEmailChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}

	// Refused before anything is recorded: a pending change nobody can be told
	// about is a row that only ever expires.
	settings, err := a.deps.Mail.Get(r.Context())
	if err == nil && !settings.Configured {
		writeError(w, http.StatusBadRequest, "the panel cannot send email yet — set it up in Settings → Mail first")
		return
	}

	change, err := a.deps.Auth.RequestEmailChange(r.Context(), p.User.ID, req.NewEmail, req.CurrentPassword, a.clientIP(r))
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrRateLimited):
		// Throttled like sign-in: the current password is a guessing surface
		// here too (panel-mail.md §5; control-plane-hardening.md §5).
		rateLimited(w, err, "too many attempts — wait before trying again")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "that is not your current password")
		return
	case errors.Is(err, auth.ErrEmailInUse):
		writeError(w, http.StatusConflict, "that address is already in use")
		return
	case errors.Is(err, auth.ErrSameEmail):
		writeError(w, http.StatusBadRequest, "that is already your address")
		return
	default:
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		a.deps.Log.Error("requesting email change", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the change")
		return
	}

	// Everything below uses the addresses the authenticator validated and stored,
	// never req.NewEmail: what a person typed reaches a recipient list and an
	// email body, and untrusted input in either is how a trusted sender becomes
	// someone else's relay (CWE-640). The link's host comes from the panel's own
	// advertised base URL for the same reason — never from a request header.
	link := a.deps.ConsoleURL + "/settings/profile?confirm=" + change.Token
	if err := a.deps.Mail.Send(r.Context(), []string{change.NewEmail},
		"Confirm your new CypherPanel address",
		"Open this link while signed in to the panel to finish moving your account to this address:\n\n"+
			link+"\n\nThe link works once and expires in 30 minutes. If you did not ask for this, ignore it — nothing has changed.",
	); err != nil {
		a.deps.Log.Error("mailing email-change confirmation", "error", err)
		writeError(w, http.StatusBadGateway, "could not send the confirmation email: "+err.Error())
		return
	}

	// Best effort, and deliberately after the confirmation: if the warning
	// cannot be delivered, the change still stands or falls on the link.
	if err := a.deps.Mail.Send(r.Context(), []string{change.OldEmail},
		"Someone asked to move your CypherPanel account",
		"A request was made to change this account's sign-in address to "+notify.SanitizeHeader(change.NewEmail)+".\n\n"+
			"If that was you, no action is needed here — confirm it from the link sent to the new address.\n"+
			"If it was not, change your password now: whoever asked already had your password and a signed-in session.",
	); err != nil {
		a.deps.Log.Error("mailing email-change notice to the old address", "error", err)
	}

	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleConfirmEmailChange(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var req confirmEmailChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	user, revoked, err := a.deps.Auth.ConfirmEmailChange(r.Context(), p.User.ID, req.Token, rawTokenFromContext(r.Context()), a.clientIP(r))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, confirmEmailChangeResponse{Email: user.Email, Revoked: revoked})
	case errors.Is(err, auth.ErrRateLimited):
		rateLimited(w, err, "too many attempts — wait before trying again")
	case errors.Is(err, auth.ErrEmailInUse):
		writeError(w, http.StatusConflict, "that address was taken while you were confirming")
	case errors.Is(err, auth.ErrInvalidEmailChange):
		writeError(w, http.StatusBadRequest, "that confirmation link is not valid — it may have been used already, or expired")
	default:
		a.deps.Log.Error("confirming email change", "error", err)
		writeError(w, http.StatusInternalServerError, "could not finish the change")
	}
}

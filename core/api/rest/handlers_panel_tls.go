package rest

// The panel's ACME account (agent-identity-and-tls.md §4–5). One panel, one
// account, carried to every server as desired state — so turning TLS on is one
// action in one place instead of an environment variable on every host, and the
// panel can say honestly what each route is actually served as.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/paneltls"
)

// PanelTLSService is the panel's ACME account surface (consumer-defined, rule
// 6; *paneltls.Service satisfies it).
type PanelTLSService interface {
	Get(ctx context.Context) (domain.PanelTLS, error)
	Set(ctx context.Context, in paneltls.Input) (domain.PanelTLS, error)
	// Configured is the one question the application routes ask, kept separate
	// so they do not carry a settings struct around to ask it.
	Configured(ctx context.Context) (bool, error)
}

type panelTLSDTO struct {
	Configured bool `json:"configured"`
	// Not masked, deliberately: an ACME account email is published by the CA in
	// the account record and a directory URL is well-known. The secret in ACME
	// is the account key, which is generated and held by the proxy on the
	// serving node and never reaches the plane (ADR-004). A config_hint here
	// would imply a confidentiality this value does not have.
	ACMEEmail    string     `json:"acme_email"`
	ACMECAServer string     `json:"acme_ca_server"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

type setPanelTLSRequest struct {
	ACMEEmail    string `json:"acme_email"`
	ACMECAServer string `json:"acme_ca_server"`
}

func panelTLSToDTO(t domain.PanelTLS) panelTLSDTO {
	dto := panelTLSDTO{
		Configured:   t.Configured(),
		ACMEEmail:    t.ACMEEmail,
		ACMECAServer: t.ACMECAServer,
	}
	if !t.UpdatedAt.IsZero() {
		u := t.UpdatedAt
		dto.UpdatedAt = &u
	}
	return dto
}

func (a *API) handleGetPanelTLS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	// Owner, not admin: this decides how every application on every server is
	// served to the public internet, and it names an account the panel
	// registers on the operator's behalf.
	if !ok || !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.PanelTLS == nil {
		writeJSON(w, http.StatusOK, panelTLSDTO{})
		return
	}
	t, err := a.deps.PanelTLS.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading panel tls", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the TLS settings")
		return
	}
	writeJSON(w, http.StatusOK, panelTLSToDTO(t))
}

func (a *API) handleSetPanelTLS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.PanelTLS == nil {
		writeError(w, http.StatusNotFound, "TLS settings are not available")
		return
	}
	var req setPanelTLSRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t, err := a.deps.PanelTLS.Set(r.Context(), paneltls.Input{
		ACMEEmail:    req.ACMEEmail,
		ACMECAServer: req.ACMECAServer,
	})
	switch {
	case errors.Is(err, paneltls.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest,
			"acme_email must be a single plain email address, e.g. ops@example.com")
		return
	case errors.Is(err, paneltls.ErrInvalidCAServer):
		writeError(w, http.StatusBadRequest,
			"acme_ca_server must be an absolute http(s) URL, e.g. https://acme-v02.api.letsencrypt.org/directory")
		return
	case err != nil:
		a.deps.Log.Error("saving panel tls", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the TLS settings")
		return
	}
	writeJSON(w, http.StatusOK, panelTLSToDTO(t))
}

// acmeConfigured answers "does the panel have a certificate resolver to offer?"
// for the derived Application.tls_state.
//
// A read failure answers false. That is the honest direction: with no answer,
// claiming HTTPS would be exactly the false certainty this feature exists to
// remove (ui-principles §10), while claiming HTTP understates at worst.
func (a *API) acmeConfigured(ctx context.Context) bool {
	if a.deps.PanelTLS == nil {
		return false
	}
	ok, err := a.deps.PanelTLS.Configured(ctx)
	if err != nil {
		a.deps.Log.Error("reading panel tls for route state", "error", err)
		return false
	}
	return ok
}

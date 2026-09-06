package rest

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
)

// sessionDTO is one live sign-in. No token material is ever included — a
// session list must not be a source of credentials.
type sessionDTO struct {
	ID        string `json:"id"`
	Current   bool   `json:"current"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

type revokeSessionsResponse struct {
	Revoked int64 `json:"revoked"`
}

func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	sessions, err := a.deps.Auth.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		a.deps.Log.Error("listing sessions", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	out := make([]sessionDTO, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionDTO{
			ID:        s.ID,
			Current:   s.ID == p.SessionID,
			CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
			ExpiresAt: s.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	err := a.deps.Auth.RevokeSession(r.Context(), p.User.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, auth.ErrSessionNotFound):
		// A session that is not the caller's is reported exactly like one that
		// does not exist: no cross-account probing.
		writeError(w, http.StatusNotFound, "session not found")
	case err != nil:
		a.deps.Log.Error("revoking session", "error", err)
		writeError(w, http.StatusInternalServerError, "could not revoke session")
	default:
		a.audit(r, audit.Entry{
			Action:   audit.ActionSessionRevoked,
			Resource: audit.Resource(audit.ResourceSession, r.PathValue("id"), p.User.Email),
			Detail:   map[string]any{"scope": "one"},
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRevokeOtherSessions signs the caller out of every other device. The
// surviving session is the one presenting this request's bearer token.
func (a *API) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	n, err := a.deps.Auth.RevokeOtherSessions(r.Context(), p.User.ID, rawTokenFromContext(r.Context()))
	if err != nil {
		a.deps.Log.Error("revoking other sessions", "error", err)
		writeError(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionSessionRevoked,
		Resource: audit.Resource(audit.ResourceSession, p.SessionID, p.User.Email),
		Detail:   map[string]any{"scope": "others", "revoked": n},
	})
	writeJSON(w, http.StatusOK, revokeSessionsResponse{Revoked: n})
}

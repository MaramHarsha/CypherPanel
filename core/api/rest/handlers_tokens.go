package rest

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// maxTokenExpiryDays bounds the operator-supplied lifetime so the day count
// cannot overflow when added to now (defence in depth; CWE-190). Ten years is
// well beyond any legitimate CI credential rotation window.
const maxTokenExpiryDays = 3650

type createTokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays int    `json:"expires_in_days"` // 0 = never expires
}

type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// createTokenResponse carries the raw secret, shown exactly once. It embeds the
// metadata so a caller can store the id alongside the value it just received.
type createTokenResponse struct {
	tokenDTO
	Token string `json:"token"`
}

func toTokenDTO(t domain.APIToken) tokenDTO {
	return tokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		LastUsedAt: t.LastUsedAt,
		ExpiresAt:  t.ExpiresAt,
		CreatedAt:  t.CreatedAt,
	}
}

func (a *API) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req createTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "name must be 1–100 characters")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenExpiryDays {
		writeError(w, http.StatusBadRequest, "expires_in_days out of range")
		return
	}
	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	raw, tok, err := a.deps.Auth.CreateToken(r.Context(), user.ID, req.Name, expiresAt)
	if err != nil {
		a.deps.Log.Error("creating api token", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, http.StatusCreated, createTokenResponse{tokenDTO: toTokenDTO(tok), Token: raw})
}

func (a *API) handleListTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	toks, err := a.deps.Auth.ListTokens(r.Context(), user.ID)
	if err != nil {
		a.deps.Log.Error("listing api tokens", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	out := make([]tokenDTO, 0, len(toks))
	for _, t := range toks {
		out = append(out, toTokenDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	err := a.deps.Auth.DeleteToken(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, auth.ErrTokenNotFound) {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("deleting api token", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

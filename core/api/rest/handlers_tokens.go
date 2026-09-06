package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
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
	// Abilities is raw so that *presence* is what decides the default, not the
	// decoded value. A pointer would still be nil for an explicit `null`,
	// making `"abilities": null` — a request for no authority — indistinguishable
	// from an omitted field and silently granting full access. Omitted means
	// "full access" (preserving clients written before abilities existed);
	// anything present, including `null` and `[]`, is taken literally and
	// rejected when it grants nothing.
	Abilities json.RawMessage `json:"abilities"`
	// ProjectID narrows the token to one project. Empty or omitted leaves it
	// unscoped, which is what every token was before scoping existed.
	ProjectID string `json:"project_id"`
}

type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Abilities  []string   `json:"abilities"`
	ProjectID  string     `json:"project_id,omitempty"`
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
	abilities := make([]string, 0, len(t.Abilities))
	for _, a := range t.Abilities {
		abilities = append(abilities, string(a))
	}
	return tokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		Abilities:  abilities,
		ProjectID:  t.ProjectID,
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

	// An omitted ability list means "same authority as before this feature
	// existed" — full access — so existing clients keep working. A present
	// list is taken literally: `null` and `[]` both decode to nothing and are
	// refused by the service rather than widened.
	abilities := domain.AllAbilities()
	if req.Abilities != nil {
		var list []string
		if err := json.Unmarshal(req.Abilities, &list); err != nil {
			writeError(w, http.StatusBadRequest, "abilities must be an array of read, write, deploy, env, servers, admin")
			return
		}
		abilities = make([]domain.Ability, 0, len(list))
		for _, s := range list {
			abilities = append(abilities, domain.Ability(s))
		}
	}

	// A token can only be scoped to a project its owner can actually reach, and
	// only by an interactive session — the scope is part of the credential's
	// authority, so minting one is credential management like the rest of this
	// route.
	if req.ProjectID != "" && !a.requireProjectRole(w, r, user, req.ProjectID, domain.RoleMember) {
		return
	}

	raw, tok, err := a.deps.Auth.CreateToken(r.Context(), user.ID, req.Name, abilities, expiresAt, req.ProjectID)
	if errors.Is(err, auth.ErrInvalidAbility) {
		writeError(w, http.StatusBadRequest, "abilities must be a non-empty subset of read, write, deploy, env, servers, admin")
		return
	}
	if err != nil {
		a.deps.Log.Error("creating api token", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	// The abilities and the expiry, never the token itself (§6). A minted
	// credential is the row a later leak is traced back from.
	a.audit(r, audit.Entry{
		Action:   audit.ActionTokenCreated,
		Resource: audit.Resource(audit.ResourceAPIToken, tok.ID, tok.Name),
		Detail: map[string]any{
			"abilities": abilityNames(tok.Abilities), "expires_in_days": req.ExpiresInDays,
			"project_id": tok.ProjectID,
		},
	})
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
	a.audit(r, audit.Entry{
		Action:   audit.ActionTokenRevoked,
		Resource: audit.Resource(audit.ResourceAPIToken, r.PathValue("id"), ""),
	})
	w.WriteHeader(http.StatusNoContent)
}

// abilityNames renders a token's abilities for an audit detail. The audit row
// stores plain JSON, so the closed ability vocabulary crosses as strings.
func abilityNames(in []domain.Ability) []string {
	out := make([]string, 0, len(in))
	for _, ab := range in {
		out = append(out, string(ab))
	}
	return out
}

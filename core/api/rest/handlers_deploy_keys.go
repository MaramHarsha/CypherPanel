package rest

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// deployKeyDTO is the public view of a deploy key. The sealed private key
// (ciphertext and nonce) never crosses the API boundary — mask by default
// (ENGINEERING rule 20; openapi.yaml DeployKey schema).
type deployKeyDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

func toDeployKeyDTO(dk domain.DeployKey) deployKeyDTO {
	return deployKeyDTO{
		ID:          dk.ID,
		Name:        dk.Name,
		PublicKey:   dk.PublicKey,
		Fingerprint: dk.Fingerprint,
		CreatedAt:   dk.CreatedAt,
	}
}

type createDeployKeyRequest struct {
	Name string `json:"name"`
}

type createDeployKeyResponse struct {
	DeployKey deployKeyDTO `json:"deploy_key"`
}

func (a *API) handleCreateDeployKey(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req createDeployKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	dk, err := a.deps.DeployKeys.Create(r.Context(), req.Name)
	if err != nil {
		var ve *deploykeys.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Msg)
			return
		}
		a.deps.Log.Error("creating deploy key", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create deploy key")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionDeployKeyCreated,
		Resource: audit.Resource(audit.ResourceDeployKey, dk.ID, dk.Name),
		Detail:   map[string]any{"fingerprint": dk.Fingerprint},
	})
	writeJSON(w, http.StatusCreated, createDeployKeyResponse{DeployKey: toDeployKeyDTO(dk)})
}

func (a *API) handleListDeployKeys(w http.ResponseWriter, r *http.Request) {
	dks, err := a.deps.DeployKeys.List(r.Context())
	if err != nil {
		a.deps.Log.Error("listing deploy keys", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list deploy keys")
		return
	}
	out := make([]deployKeyDTO, 0, len(dks))
	for _, dk := range dks {
		out = append(out, toDeployKeyDTO(dk))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetDeployKey(w http.ResponseWriter, r *http.Request) {
	dk, err := a.deps.DeployKeys.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deploy key not found")
			return
		}
		a.deps.Log.Error("getting deploy key", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get deploy key")
		return
	}
	writeJSON(w, http.StatusOK, toDeployKeyDTO(dk))
}

// deployKeyInUseResponse is the Error envelope plus the blockers — additive,
// so a client that only reads `error` is unaffected (ENGINEERING rule 17).
type deployKeyInUseResponse struct {
	Error        string             `json:"error"`
	TraceID      string             `json:"trace_id,omitempty"`
	Applications []deployKeyBlocker `json:"applications"`
}

type deployKeyBlocker struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *API) handleDeleteDeployKey(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if err := a.deps.DeployKeys.Delete(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "deploy key not found")
		case errors.Is(err, store.ErrInUse):
			// Name the blockers: "in use" without saying by what leaves the
			// operator clicking through every application
			// (deploy-key-private-repos.md §3).
			writeDeployKeyInUse(w, err)
		default:
			a.deps.Log.Error("deleting deploy key", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete deploy key")
		}
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionDeployKeyDeleted,
		Resource: audit.Resource(audit.ResourceDeployKey, r.PathValue("id"), ""),
	})
	w.WriteHeader(http.StatusNoContent)
}

// writeDeployKeyInUse answers 409 listing the applications that still
// reference the key, so the operator can go detach them.
func writeDeployKeyInUse(w http.ResponseWriter, err error) {
	var inUse *deploykeys.InUseError
	if !errors.As(err, &inUse) || len(inUse.Applications) == 0 {
		writeError(w, http.StatusConflict, "deploy key is in use by one or more applications")
		return
	}
	apps := make([]deployKeyBlocker, 0, len(inUse.Applications))
	names := make([]string, 0, len(inUse.Applications))
	for _, app := range inUse.Applications {
		apps = append(apps, deployKeyBlocker{ID: app.ID, Name: app.Name})
		names = append(names, app.Name)
	}
	writeJSON(w, http.StatusConflict, deployKeyInUseResponse{
		Error:        "deploy key is in use by " + strings.Join(names, ", "),
		TraceID:      w.Header().Get(TraceIDHeader),
		Applications: apps,
	})
}

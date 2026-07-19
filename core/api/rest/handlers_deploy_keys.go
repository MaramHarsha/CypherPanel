package rest

import (
	"errors"
	"net/http"
	"time"

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

func (a *API) handleDeleteDeployKey(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.DeployKeys.Delete(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "deploy key not found")
		case errors.Is(err, store.ErrInUse):
			writeError(w, http.StatusConflict, "deploy key is in use by one or more applications")
		default:
			a.deps.Log.Error("deleting deploy key", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete deploy key")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

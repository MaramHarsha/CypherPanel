package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type createDeployKeyRequest struct {
	Name string `json:"name"`
}

func (a *API) handleCreateDeployKey(w http.ResponseWriter, r *http.Request) {
	var req createDeployKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		writeError(w, http.StatusInternalServerError, "failed to create deploy key")
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"deploy_key": dk,
	})
}

func (a *API) handleListDeployKeys(w http.ResponseWriter, r *http.Request) {
	dks, err := a.deps.DeployKeys.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deploy keys")
		return
	}
	
	if dks == nil {
		dks = make([]domain.DeployKey, 0)
	}

	json.NewEncoder(w).Encode(dks)
}

func (a *API) handleGetDeployKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dk, err := a.deps.DeployKeys.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deploy key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get deploy key")
		return
	}
	json.NewEncoder(w).Encode(dk)
}

func (a *API) handleDeleteDeployKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.deps.DeployKeys.Delete(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrInUse) {
			writeError(w, http.StatusConflict, "deploy key is in use by one or more applications")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete deploy key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

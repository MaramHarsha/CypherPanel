package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// notifierDTO never carries the raw channel config (rule 20) — only a masked
// hint derived from its non-secret fields (notifications.md §7).
type notifierDTO struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Name       string    `json:"name"`
	Channel    string    `json:"channel"`
	Events     []string  `json:"events"`
	Enabled    bool      `json:"enabled"`
	ConfigHint string    `json:"config_hint"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// toNotifierDTO builds the response. The hint needs the non-secret config
// fields, so it unseals in-process and immediately discards the plaintext; a
// failure to unseal degrades to the channel name, never an error.
func (a *API) toNotifierDTO(n domain.Notifier) notifierDTO {
	hint := n.Channel
	if cfg, err := a.deps.Opener.Open(n.ConfigCT, n.ConfigNonce); err == nil {
		hint = notify.ConfigHint(n.Channel, cfg)
	}
	return notifierDTO{
		ID:         n.ID,
		ProjectID:  n.ProjectID,
		Name:       n.Name,
		Channel:    n.Channel,
		Events:     n.Events,
		Enabled:    n.Enabled,
		ConfigHint: hint,
		CreatedAt:  n.CreatedAt,
		UpdatedAt:  n.UpdatedAt,
	}
}

type createNotifierRequest struct {
	Name    string          `json:"name"`
	Channel string          `json:"channel"`
	Config  json.RawMessage `json:"config"`
	Events  []string        `json:"events"`
	Enabled *bool           `json:"enabled"` // default true when omitted
}

func (a *API) handleCreateNotifier(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil {
		writeError(w, http.StatusNotImplemented, "notifications are not enabled")
		return
	}
	var req createNotifierRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	n, err := a.deps.Notifiers.Create(r.Context(), r.PathValue("id"), notify.CreateInput{
		Name:    req.Name,
		Channel: req.Channel,
		Config:  req.Config,
		Events:  req.Events,
		Enabled: enabled,
	})
	if err != nil {
		a.writeNotifierError(w, "creating notifier", err)
		return
	}
	writeJSON(w, http.StatusCreated, a.toNotifierDTO(n))
}

func (a *API) handleListNotifiers(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil {
		writeJSON(w, http.StatusOK, []notifierDTO{})
		return
	}
	list, err := a.deps.Notifiers.List(r.Context(), r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("listing notifiers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list notifiers")
		return
	}
	out := make([]notifierDTO, 0, len(list))
	for _, n := range list {
		out = append(out, a.toNotifierDTO(n))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetNotifier(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil {
		writeError(w, http.StatusNotFound, "notifier not found")
		return
	}
	n, err := a.deps.Notifiers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "notifier not found")
			return
		}
		a.deps.Log.Error("getting notifier", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get notifier")
		return
	}
	writeJSON(w, http.StatusOK, a.toNotifierDTO(n))
}

type patchNotifierRequest struct {
	Name    string          `json:"name"`
	Events  []string        `json:"events"`
	Enabled *bool           `json:"enabled"`
	Config  json.RawMessage `json:"config"` // omit to keep the sealed value
}

func (a *API) handlePatchNotifier(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil {
		writeError(w, http.StatusNotFound, "notifier not found")
		return
	}
	cur, err := a.deps.Notifiers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "notifier not found")
			return
		}
		a.deps.Log.Error("getting notifier", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get notifier")
		return
	}
	var req patchNotifierRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// PATCH semantics: unspecified fields keep their current value.
	in := notify.UpdateInput{Name: cur.Name, Events: cur.Events, Enabled: cur.Enabled, Config: req.Config}
	if req.Name != "" {
		in.Name = req.Name
	}
	if req.Events != nil {
		in.Events = req.Events
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	n, err := a.deps.Notifiers.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		a.writeNotifierError(w, "updating notifier", err)
		return
	}
	writeJSON(w, http.StatusOK, a.toNotifierDTO(n))
}

func (a *API) handleDeleteNotifier(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil {
		writeError(w, http.StatusNotFound, "notifier not found")
		return
	}
	if err := a.deps.Notifiers.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "notifier not found")
			return
		}
		a.deps.Log.Error("deleting notifier", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete notifier")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestNotifier delivers a synthetic event through one notifier so an
// operator can confirm wiring at setup time (notifications.md §7). Delivery is
// synchronous here (unlike real events) so the 202 means "attempted"; a channel
// failure is logged, not surfaced, to avoid leaking endpoint details.
func (a *API) handleTestNotifier(w http.ResponseWriter, r *http.Request) {
	if a.deps.Notifiers == nil || a.deps.NotifyDelivery == nil {
		writeError(w, http.StatusNotFound, "notifier not found")
		return
	}
	n, err := a.deps.Notifiers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "notifier not found")
			return
		}
		a.deps.Log.Error("getting notifier", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get notifier")
		return
	}
	ev := domain.NotifyEvent{
		Type:  domain.EventDeploySucceeded,
		Level: domain.NotifyInfo,
		Title: "CypherPanel test notification",
		Body:  "This is a test message confirming your notifier is wired correctly.",
	}
	if err := a.deps.NotifyDelivery.Deliver(r.Context(), n, ev); err != nil {
		a.deps.Log.Warn("test notification failed", "notifier_id", n.ID, "channel", n.Channel, "error", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

// writeNotifierError maps service errors to status codes.
func (a *API) writeNotifierError(w http.ResponseWriter, op string, err error) {
	var ve *notify.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, notify.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "notifier not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a notifier with that name already exists in the project")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}

package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type serverDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Driver       string  `json:"driver"`
	AgentVersion string  `json:"agent_version"`
	Hostname     string  `json:"hostname"`
	Enrolled     bool    `json:"enrolled"`
	EnrolledAt   *string `json:"enrolled_at"`
	LastSeenAt   *string `json:"last_seen_at"`
	CreatedAt    string  `json:"created_at"`
}

func toServerDTO(s domain.Server) serverDTO {
	return serverDTO{
		ID:           s.ID,
		Name:         s.Name,
		Status:       string(s.Status),
		Driver:       s.Driver,
		AgentVersion: s.AgentVersion,
		Hostname:     s.Hostname,
		Enrolled:     s.Enrolled(),
		EnrolledAt:   formatTime(s.EnrolledAt),
		LastSeenAt:   formatTime(s.LastSeenAt),
		CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

type createServerRequest struct {
	Name string `json:"name"`
}

// joinInstructions is everything an operator needs to enroll the new server.
// The token appears exactly once — it is single-use and never retrievable
// again (threat-model §5.3).
type joinInstructions struct {
	Token      string `json:"token"`
	EnrollAddr string `json:"enroll_addr"`
	NATSURL    string `json:"nats_url"`
	CACertPEM  string `json:"ca_cert_pem"`
	Command    string `json:"command"`
}

type createServerResponse struct {
	Server serverDTO        `json:"server"`
	Join   joinInstructions `json:"join"`
}

func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Servers.List(r.Context())
	if err != nil {
		a.deps.Log.Error("listing servers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list servers")
		return
	}
	out := make([]serverDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toServerDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	srv, token, err := a.deps.Servers.Create(r.Context(), req.Name)
	if errors.Is(err, servers.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "name must be 1–100 characters")
		return
	}
	if err != nil {
		a.deps.Log.Error("creating server", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create server")
		return
	}
	writeJSON(w, http.StatusCreated, createServerResponse{
		Server: toServerDTO(srv),
		Join: joinInstructions{
			Token:      token,
			EnrollAddr: a.deps.EnrollAddr,
			NATSURL:    a.deps.NATSURL,
			CACertPEM:  string(a.deps.CACertPEM),
			Command: fmt.Sprintf(
				"cypher-agent enroll --plane %s --token %s --ca-file ca.pem",
				a.deps.EnrollAddr, token,
			),
		},
	})
}

func (a *API) handleGetServer(w http.ResponseWriter, r *http.Request) {
	srv, err := a.deps.Servers.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting server", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(srv))
}

func (a *API) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Servers.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.deps.Log.Error("deleting server", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete server")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCAPem(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write(a.deps.CACertPEM)
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.deps.Pinger.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

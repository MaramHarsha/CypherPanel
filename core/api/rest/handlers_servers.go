package rest

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
	Role         string  `json:"role"`
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
		Role:         s.Role,
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
// again (threat-model §5.3). InstallCommand is the curl|sh join line (the
// Phase 1 acceptance path); Command is the manual alternative when the agent
// binary is already installed. The CA fingerprint rides in the command so the
// installer can verify the CA it fetches over plain HTTP (threat-model §5.1).
type joinInstructions struct {
	Token          string `json:"token"`
	EnrollAddr     string `json:"enroll_addr"`
	NATSURL        string `json:"nats_url"`
	CACertPEM      string `json:"ca_cert_pem"`
	CAFingerprint  string `json:"ca_fingerprint"`
	Command        string `json:"command"`
	InstallCommand string `json:"install_command"`
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
	caSum := sha256.Sum256(a.deps.CACertPEM)
	fingerprint := hex.EncodeToString(caSum[:])
	writeJSON(w, http.StatusCreated, createServerResponse{
		Server: toServerDTO(srv),
		Join: joinInstructions{
			Token:         token,
			EnrollAddr:    a.deps.EnrollAddr,
			NATSURL:       a.deps.NATSURL,
			CACertPEM:     string(a.deps.CACertPEM),
			CAFingerprint: fingerprint,
			Command: fmt.Sprintf(
				"cypher-agent enroll --plane %s --token %s --ca-file ca.pem",
				a.deps.EnrollAddr, token,
			),
			// CYPHER_PLANE_HTTP is passed explicitly: the plane knows its own
			// advertised URL; the installer must not guess a port.
			InstallCommand: fmt.Sprintf(
				"curl -fsSL %s/install/agent.sh | CYPHER_PLANE=%s CYPHER_PLANE_HTTP=%s CYPHER_TOKEN=%s CYPHER_CA_FINGERPRINT=%s sh",
				a.deps.ConsoleURL, a.deps.EnrollAddr, a.deps.ConsoleURL, token, fingerprint,
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
		if errors.Is(err, store.ErrInUse) {
			writeError(w, http.StatusConflict, "server still runs applications — move or delete them first")
			return
		}
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

//go:embed openapi.yaml
var openapiSpec []byte

func (a *API) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(openapiSpec)
}

//go:embed install-agent.sh
var installScript []byte

func (a *API) handleInstallScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write(installScript)
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

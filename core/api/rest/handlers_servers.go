package rest

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/updates"
)

type serverDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Driver       string `json:"driver"`
	Role         string `json:"role"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	// PublicAddress is where DNS records for this server's applications point
	// (dns-automation.md §3.4). Empty until an operator sets it.
	PublicAddress string `json:"public_address"`
	// DiskTotalBytes / DiskFreeBytes are the Docker data root's filesystem as
	// of the last heartbeat (disk-management.md §4). Zero means not reported —
	// an older agent, or a host where it could not be read — which a client
	// should show as unknown rather than as full.
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	// DiskLow is whether the server is currently past the panel's threshold.
	DiskLow    bool    `json:"disk_low"`
	Enrolled   bool    `json:"enrolled"`
	EnrolledAt *string `json:"enrolled_at"`
	LastSeenAt *string `json:"last_seen_at"`
	CreatedAt  string  `json:"created_at"`
}

func toServerDTO(s domain.Server) serverDTO {
	return serverDTO{
		ID:             s.ID,
		Name:           s.Name,
		Status:         string(s.Status),
		Driver:         s.Driver,
		DiskTotalBytes: s.DiskTotalBytes,
		DiskFreeBytes:  s.DiskFreeBytes,
		DiskLow:        s.DiskLow,
		Role:           s.Role,
		AgentVersion:   s.AgentVersion,
		Hostname:       s.Hostname,
		PublicAddress:  s.PublicAddress,
		Enrolled:       s.Enrolled(),
		EnrolledAt:     formatTime(s.EnrolledAt),
		LastSeenAt:     formatTime(s.LastSeenAt),
		CreatedAt:      s.CreatedAt.UTC().Format(time.RFC3339),
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
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
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
	// The server, never the join token beside it (§6): the token is a
	// single-use credential and an audit row is not where one gets a second
	// life.
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerCreated,
		Resource: audit.Resource(audit.ResourceServer, srv.ID, srv.Name),
	})
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
			InstallCommand: a.installCommand(token, fingerprint),
		},
	})
}

// installCommand builds the ready-to-paste join line.
//
// It pins CYPHER_AGENT_URL to this panel's OWN version when the panel is a
// release build (agent-identity-and-tls.md §6): a server then joins running the
// agent that matches the plane, which is the version pairing ADR-010 assumes,
// and the paste works on a host that has never seen the binary. A development
// build names no URL and the installer falls back to the project's latest
// release — the plane still stores and serves no binaries either way (ADR-010).
func (a *API) installCommand(token, fingerprint string) string {
	agentURL := ""
	if a.deps.Panel != nil {
		agentURL = updates.AgentAssetURL(a.deps.Panel.Current().Version)
	}
	cmd := fmt.Sprintf(
		"curl -fsSL %s/install/agent.sh | CYPHER_PLANE=%s CYPHER_PLANE_HTTP=%s CYPHER_TOKEN=%s CYPHER_CA_FINGERPRINT=%s",
		a.deps.ConsoleURL, a.deps.EnrollAddr, a.deps.ConsoleURL, token, fingerprint,
	)
	if agentURL != "" {
		// Quoted: the URL carries a literal {arch} placeholder the installer
		// substitutes, and an unquoted brace is at the mercy of whichever shell
		// the operator pastes into.
		cmd += fmt.Sprintf(" CYPHER_AGENT_URL='%s'", agentURL)
	}
	return cmd + " sh"
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
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	before, _ := a.deps.Servers.Get(r.Context(), r.PathValue("id"))
	if err := a.deps.Servers.Delete(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrInUse) {
			writeError(w, http.StatusConflict, "server still runs applications — move or delete them first")
			return
		}
		a.deps.Log.Error("deleting server", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete server")
		return
	}
	// Deleting a server revokes its certificate and disconnects the agent —
	// the mirror of enrollment, and audited for the same reason (§5.3).
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerDeleted,
		Resource: audit.Resource(audit.ResourceServer, r.PathValue("id"), before.Name),
	})
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

// patchServerRequest carries the only operator-editable field on a server. A
// pointer so an omitted field is distinguishable from an explicit "" — clearing
// the address is a real action (it stops new records being written), and it
// must not be something a partial body does by accident.
type patchServerRequest struct {
	PublicAddress *string `json:"public_address"`
}

// handlePatchServer sets where this server's applications' DNS records point.
// Panel admin, like every other server-shaped decision: a public address is
// infrastructure, not a project's business.
func (a *API) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req patchServerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PublicAddress == nil {
		writeError(w, http.StatusBadRequest, "public_address is required")
		return
	}
	addr := strings.TrimSpace(*req.PublicAddress)
	// An A record's content must be an IP. Refusing a hostname here is kinder
	// than letting Cloudflare refuse it later against a record nobody is
	// watching (ui-principles §11).
	if addr != "" && net.ParseIP(addr) == nil {
		writeError(w, http.StatusBadRequest, "public_address must be an IP address — that is what an A record points at")
		return
	}
	srv, err := a.deps.ServerAddresses.SetServerPublicAddress(r.Context(), r.PathValue("id"), addr)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		a.deps.Log.Error("setting server public address", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update the server")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerUpdated,
		Resource: audit.Resource(audit.ResourceServer, srv.ID, srv.Name),
		Detail:   map[string]any{"public_address": srv.PublicAddress},
	})
	writeJSON(w, http.StatusOK, toServerDTO(srv))
}

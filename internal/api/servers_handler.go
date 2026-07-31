package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/services"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type ServersHandler struct {
	Servers     *store.Servers
	Accounts    *store.Accounts
	Tasks       *store.Tasks
	Publisher   *jobs.Publisher
	Audit       *audit.Logger
	PHPVersions []string // operator-permitted PHP branches (install allowlist)
}

type serviceStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type serverResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Hostname         string          `json:"hostname"`
	IPAddress        string          `json:"ip_address"`
	AgentStatus      string          `json:"agent_status"`
	Region           string          `json:"region"`
	LastSeenAt       *time.Time      `json:"last_seen_at"`
	CreatedAt        time.Time       `json:"created_at"`
	Load1m           float64         `json:"load_1m"`
	MemoryTotalBytes uint64          `json:"memory_total_bytes"`
	MemoryUsedBytes  uint64          `json:"memory_used_bytes"`
	DiskTotalBytes   uint64          `json:"disk_total_bytes"`
	DiskUsedBytes    uint64          `json:"disk_used_bytes"`
	Services         []serviceStatus `json:"services"`
}

// List returns registered servers with their latest host snapshot, optionally
// scoped to one region.
//
//	@Summary  List servers (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    region query string false "Filter to one region"
//	@Success  200 {array} serverResponse
//	@Security BearerAuth
//	@Router   /admin/servers [get]
func (h *ServersHandler) List(c *gin.Context) {
	servers, err := h.Servers.List(c.Request.Context(), c.Query("region"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]serverResponse, 0, len(servers))
	for _, s := range servers {
		out = append(out, toServerResponse(s))
	}
	c.JSON(http.StatusOK, out)
}

func toServerResponse(s store.Server) serverResponse {
	svcs := make([]serviceStatus, 0, len(s.Services))
	for _, sv := range s.Services {
		svcs = append(svcs, serviceStatus{Name: sv.Name, State: sv.State})
	}
	return serverResponse{
		ID: s.ID, Name: s.Name, Hostname: s.Hostname, IPAddress: s.IPAddress,
		AgentStatus: s.AgentStatus, Region: s.Region, LastSeenAt: s.LastSeenAt, CreatedAt: s.CreatedAt,
		Load1m:           s.Stats.Load1m,
		MemoryTotalBytes: s.Stats.MemoryTotalBytes,
		MemoryUsedBytes:  s.Stats.MemoryUsedBytes,
		DiskTotalBytes:   s.Stats.DiskTotalBytes,
		DiskUsedBytes:    s.Stats.DiskUsedBytes,
		Services:         svcs,
	}
}

// Get returns a single server's detail.
//
//	@Summary  Get a server (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Server ID"
//	@Success  200 {object} serverResponse
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/servers/{id} [get]
func (h *ServersHandler) Get(c *gin.Context) {
	srv, err := h.Servers.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, toServerResponse(*srv))
}

// Delete de-registers a server. Refused while accounts still reference it, so a
// node is never removed out from under live hosting (the agent would re-register
// on its next heartbeat anyway, but the row's accounts would be orphaned).
//
//	@Summary  Remove a server (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Server ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Failure  409 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/servers/{id} [delete]
func (h *ServersHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)
	if n, err := h.Accounts.CountByServer(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	} else if n > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "server still hosts accounts; terminate or migrate them first"})
		return
	}
	if err := h.Servers.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "server.remove", TargetType: "server", TargetID: id, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"status": "removed"})
}

type phpRuntimeRequest struct {
	Version string `json:"version" binding:"required"`
	Action  string `json:"action" binding:"required"` // install | uninstall
}

// ManagePHPRuntime installs or removes a PHP-FPM branch on a server. Root-admin
// only (server infrastructure); the version must be in the operator's permitted
// set and the agent re-validates before touching the package manager.
//
//	@Summary  Install or uninstall a PHP version on a server (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string             true "Server ID"
//	@Param    request body phpRuntimeRequest  true "Version + action"
//	@Success  202 {object} map[string]string
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/servers/{id}/php [post]
func (h *ServersHandler) ManagePHPRuntime(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)

	var req phpRuntimeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version and action are required"})
		return
	}
	if req.Action != "install" && req.Action != "uninstall" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be install or uninstall"})
		return
	}
	permitted := false
	for _, v := range h.PHPVersions {
		if v == req.Version {
			permitted = true
			break
		}
	}
	if !permitted {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version is not in the permitted PHP set"})
		return
	}
	if _, err := h.Servers.GetByID(c.Request.Context(), id); errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	payload, err := json.Marshal(jobs.PHPRuntimePayload{Version: req.Version, Action: req.Action})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	task, err := h.Tasks.Create(c.Request.Context(), id, jobs.TypePHPRuntime, payload, claims.Subject, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.Publisher.Publish(c.Request.Context(), jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	}); err != nil {
		slog.Error("dispatching php runtime task", "server_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "server.php_runtime", TargetType: "server", TargetID: id,
		Detail: map[string]any{"version": req.Version, "action": req.Action}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "dispatched", "task_id": task.ID})
}

type serviceControlRequest struct {
	Action string `json:"action" binding:"required"`
}

// ControlService dispatches a start/stop/restart/reload to a managed service on
// a server. Root-admin only (fleet infrastructure), allowlist-validated before
// dispatch and again on the agent.
//
//	@Summary  Control a managed service on a server (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string                 true "Server ID"
//	@Param    name    path string                 true "Service name"
//	@Param    request body serviceControlRequest  true "Action"
//	@Success  202 {object} map[string]string
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/servers/{id}/services/{name}/control [post]
func (h *ServersHandler) ControlService(c *gin.Context) {
	id := c.Param("id")
	name := c.Param("name")
	claims := auth.ClaimsFrom(c)

	var req serviceControlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action is required"})
		return
	}
	if !services.ValidAction(req.Action) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be one of start, stop, restart, reload"})
		return
	}
	if !services.IsManaged(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown or unmanaged service"})
		return
	}
	if _, err := h.Servers.GetByID(c.Request.Context(), id); errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	payload, err := json.Marshal(jobs.ServiceControlPayload{Service: name, Action: req.Action})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	task, err := h.Tasks.Create(c.Request.Context(), id, jobs.TypeServiceControl, payload, claims.Subject, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.Publisher.Publish(c.Request.Context(), jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	}); err != nil {
		slog.Error("dispatching service control", "server_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "service.control", TargetType: "server", TargetID: id,
		Detail: map[string]any{"service": name, "action": req.Action}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "dispatched", "task_id": task.ID})
}

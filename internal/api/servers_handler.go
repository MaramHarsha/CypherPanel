package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type ServersHandler struct {
	Servers *store.Servers
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
	LastSeenAt       *time.Time      `json:"last_seen_at"`
	CreatedAt        time.Time       `json:"created_at"`
	Load1m           float64         `json:"load_1m"`
	MemoryTotalBytes uint64          `json:"memory_total_bytes"`
	MemoryUsedBytes  uint64          `json:"memory_used_bytes"`
	DiskTotalBytes   uint64          `json:"disk_total_bytes"`
	DiskUsedBytes    uint64          `json:"disk_used_bytes"`
	Services         []serviceStatus `json:"services"`
}

// List returns all registered servers with their latest host snapshot.
//
//	@Summary  List servers (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} serverResponse
//	@Security BearerAuth
//	@Router   /admin/servers [get]
func (h *ServersHandler) List(c *gin.Context) {
	servers, err := h.Servers.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]serverResponse, 0, len(servers))
	for _, s := range servers {
		svcs := make([]serviceStatus, 0, len(s.Services))
		for _, sv := range s.Services {
			svcs = append(svcs, serviceStatus{Name: sv.Name, State: sv.State})
		}
		out = append(out, serverResponse{
			ID: s.ID, Name: s.Name, Hostname: s.Hostname, IPAddress: s.IPAddress,
			AgentStatus: s.AgentStatus, LastSeenAt: s.LastSeenAt, CreatedAt: s.CreatedAt,
			Load1m:           s.Stats.Load1m,
			MemoryTotalBytes: s.Stats.MemoryTotalBytes,
			MemoryUsedBytes:  s.Stats.MemoryUsedBytes,
			DiskTotalBytes:   s.Stats.DiskTotalBytes,
			DiskUsedBytes:    s.Stats.DiskUsedBytes,
			Services:         svcs,
		})
	}
	c.JSON(http.StatusOK, out)
}

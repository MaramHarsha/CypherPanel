package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/plugins"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// PluginsHandler reserves the /api/v1/plugins surface (plan.md §11). The
// loader/runtime is post-MVP; today only a read-only listing exists so the
// route namespace and response shape are stable before any plugin ships.
type PluginsHandler struct {
	Plugins *store.Plugins
}

type pluginResponse struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Kind        string    `json:"kind"`
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installed_at"`
}

// List returns installed plugins (empty until the runtime lands).
//
//	@Summary  List installed plugins (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} pluginResponse
//	@Security BearerAuth
//	@Router   /admin/plugins [get]
func (h *PluginsHandler) List(c *gin.Context) {
	list, err := h.Plugins.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]pluginResponse, 0, len(list))
	for _, p := range list {
		out = append(out, pluginResponse{
			Name: p.Name, Version: p.Version, Kind: p.Kind,
			Enabled: p.Enabled, InstalledAt: p.InstalledAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ManifestSchema returns the finalized plugin manifest schema version and the
// accepted kinds, so tooling can validate a plugin.yaml against a stable
// contract before the loader exists.
//
//	@Summary  Plugin manifest schema info (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {object} map[string]any
//	@Security BearerAuth
//	@Router   /admin/plugins/manifest-schema [get]
func (h *PluginsHandler) ManifestSchema(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"api_version": plugins.ManifestAPIVersion,
		"kinds":       []string{plugins.KindPlugin, plugins.KindTheme, plugins.KindLanguagePack},
	})
}

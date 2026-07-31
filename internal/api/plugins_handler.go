package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/plugins"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// PluginsHandler owns the /api/v1/plugins surface (plan.md §11): manifest
// validation, install/enable/disable/uninstall lifecycle, and the declared UI
// surfaces the panel renders for enabled plugins.
//
// Scope note: the process-isolated *backend* loader is still post-MVP. A
// plugin installed here contributes its declared UI surfaces and is recorded
// with its permission list, but no third-party backend code is executed — so
// nothing here can grant a plugin ambient access.
type PluginsHandler struct {
	Plugins *store.Plugins
	Audit   *audit.Logger
}

type pluginResponse struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Kind        string            `json:"kind"`
	Enabled     bool              `json:"enabled"`
	Manifest    *plugins.Manifest `json:"manifest,omitempty"`
	InstalledAt time.Time         `json:"installed_at"`
}

func toPluginResponse(p store.Plugin) pluginResponse {
	out := pluginResponse{
		Name: p.Name, Version: p.Version, Kind: p.Kind,
		Enabled: p.Enabled, InstalledAt: p.InstalledAt,
	}
	// A stored manifest that no longer parses (schema drift after an upgrade)
	// must not take the whole listing down — surface the row without it.
	var m plugins.Manifest
	if len(p.Manifest) > 0 && json.Unmarshal(p.Manifest, &m) == nil {
		out.Manifest = &m
	}
	return out
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
		out = append(out, toPluginResponse(p))
	}
	c.JSON(http.StatusOK, out)
}

type installPluginRequest struct {
	// Manifest is the raw plugin.yaml text. It is parsed and validated against
	// the finalized schema before anything is recorded.
	Manifest string `json:"manifest" binding:"required"`
}

// Install validates a plugin.yaml and records the plugin (disabled).
//
// A newly installed plugin is always disabled: installing declares intent,
// enabling grants the surfaces. Splitting the two means an operator reviews
// the manifest's permission list before it takes effect.
//
//	@Summary  Install a plugin from its manifest (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body installPluginRequest true "plugin.yaml"
//	@Success  201 {object} pluginResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/plugins [post]
func (h *PluginsHandler) Install(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req installPluginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "manifest is required"})
		return
	}
	manifest, err := plugins.Parse([]byte(req.Manifest))
	if err != nil {
		// The validation error is the actionable part — pass it through.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Every declared event subscription must be a subject this build actually
	// publishes, or the plugin would wait forever on an event that never fires.
	for _, ev := range manifest.Events {
		if !events.KnownSubject(ev) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "manifest subscribes to unknown event: " + ev})
			return
		}
	}

	blob, err := json.Marshal(manifest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	rec, err := h.Plugins.Install(c.Request.Context(), manifest.Name, manifest.Version, manifest.Kind, blob)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "plugin.install", TargetType: "plugin", TargetID: rec.Name,
		Detail: map[string]any{
			"version": rec.Version, "kind": rec.Kind, "permissions": manifest.Permissions,
		},
		IP: c.ClientIP(),
	})
	c.JSON(http.StatusCreated, toPluginResponse(*rec))
}

type setPluginEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// SetEnabled enables or disables an installed plugin.
//
//	@Summary  Enable or disable a plugin (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    name    path string                  true "Plugin name"
//	@Param    request body setPluginEnabledRequest true "State"
//	@Success  200 {object} pluginResponse
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/plugins/{name} [patch]
func (h *PluginsHandler) SetEnabled(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req setPluginEnabledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}
	name := c.Param("name")
	err := h.Plugins.SetEnabled(c.Request.Context(), name, req.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	rec, err := h.Plugins.GetByName(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "plugin.set_enabled", TargetType: "plugin", TargetID: name,
		Detail: map[string]any{"enabled": req.Enabled}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, toPluginResponse(*rec))
}

// Uninstall removes an installed plugin and its declared surfaces.
//
//	@Summary  Uninstall a plugin (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    name path string true "Plugin name"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/plugins/{name} [delete]
func (h *PluginsHandler) Uninstall(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	name := c.Param("name")
	err := h.Plugins.Delete(c.Request.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "plugin.uninstall", TargetType: "plugin", TargetID: name, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"status": "uninstalled"})
}

// pluginSurface is one UI slot contributed by an enabled plugin.
type pluginSurface struct {
	Plugin         string                   `json:"plugin"`
	Sidebar        []plugins.SidebarEntry   `json:"sidebar"`
	DashboardCards []plugins.DashboardCard  `json:"dashboard_cards"`
	SettingsPages  []plugins.SettingsPage   `json:"settings_pages"`
}

// Surfaces returns the UI slots every enabled plugin declares, so the panel
// renders plugin navigation from manifests instead of anyone editing core UI.
//
//	@Summary  UI surfaces contributed by enabled plugins
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} pluginSurface
//	@Security BearerAuth
//	@Router   /admin/plugins/surfaces [get]
func (h *PluginsHandler) Surfaces(c *gin.Context) {
	list, err := h.Plugins.ListEnabled(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]pluginSurface, 0, len(list))
	for _, p := range list {
		var m plugins.Manifest
		if len(p.Manifest) == 0 || json.Unmarshal(p.Manifest, &m) != nil {
			continue
		}
		out = append(out, pluginSurface{
			Plugin:         p.Name,
			Sidebar:        m.UI.Sidebar,
			DashboardCards: m.UI.DashboardCards,
			SettingsPages:  m.UI.SettingsPages,
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

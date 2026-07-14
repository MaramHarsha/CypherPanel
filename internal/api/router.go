// Package api wires the CypherCore HTTP surface. All routes live under
// /api/v1 from day one (plan.md: API Contract & Repository Strategy).
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/swaggo/swag/v2"

	_ "github.com/MaramHarsha/CypherPanel/docs" // registers the generated OpenAPI spec
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/config"
)

type Deps struct {
	Config   config.Core
	Tokens   *auth.TokenService
	Auth     *AuthHandler
	Tasks    *TasksHandler
	Servers  *ServersHandler
	Packages  *PackagesHandler
	Accounts  *AccountsHandler
	Plugins   *PluginsHandler
	Resellers *ResellersHandler
}

func NewRouter(d Deps) *gin.Engine {
	if d.Config.Env == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/api/v1")

	// Machine-readable API contract (regenerate with `make openapi`).
	v1.GET("/openapi.json", func(c *gin.Context) {
		doc, err := swag.ReadDoc()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "spec unavailable"})
			return
		}
		c.Data(http.StatusOK, "application/json", []byte(doc))
	})

	// Unauthenticated
	v1.POST("/auth/login", d.Auth.Login)
	v1.POST("/auth/refresh", d.Auth.Refresh)

	// Authenticated
	authed := v1.Group("", auth.Middleware(d.Tokens))
	authed.POST("/auth/logout", d.Auth.Logout)
	authed.GET("/me", d.Auth.Me)

	// Root-admin-only surface: fleet, reseller management, plugin registry.
	admin := authed.Group("/admin", auth.RequireRole(auth.RoleRootAdmin))
	admin.POST("/servers/:id/tasks", d.Tasks.Create)
	admin.GET("/tasks/:id", d.Tasks.Get)
	admin.GET("/resellers", d.Resellers.List)
	admin.POST("/resellers", d.Resellers.Create)
	admin.GET("/plugins", d.Plugins.List)
	admin.GET("/plugins/manifest-schema", d.Plugins.ManifestSchema)

	// Shared management surface: root admin AND resellers. Every handler here
	// scopes results/actions to the caller (root = unrestricted, reseller =
	// own pool) via the auth scoping helpers — role gating alone is not enough.
	mgr := authed.Group("/admin", auth.RequireRole(auth.RoleRootAdmin, auth.RoleReseller))
	mgr.GET("/servers", d.Servers.List)
	mgr.GET("/packages", d.Packages.List)
	mgr.POST("/packages", d.Packages.Create)
	mgr.DELETE("/packages/:id", d.Packages.Delete)
	mgr.GET("/accounts", d.Accounts.List)
	mgr.POST("/accounts", d.Accounts.Create)
	mgr.POST("/accounts/:id/suspend", d.Accounts.Suspend)
	mgr.POST("/accounts/:id/unsuspend", d.Accounts.Unsuspend)
	mgr.POST("/accounts/:id/terminate", d.Accounts.Terminate)
	mgr.PATCH("/accounts/:id/php-settings", d.Accounts.UpdatePHPSettings)
	mgr.GET("/php/ini-keys", d.Accounts.PHPINIKeys)

	return r
}

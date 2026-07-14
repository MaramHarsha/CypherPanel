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
	Packages *PackagesHandler
	Accounts *AccountsHandler
	Plugins  *PluginsHandler
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

	// Root-admin-only surface.
	admin := authed.Group("/admin", auth.RequireRole(auth.RoleRootAdmin))
	admin.GET("/servers", d.Servers.List)
	admin.POST("/servers/:id/tasks", d.Tasks.Create)
	admin.GET("/tasks/:id", d.Tasks.Get)
	admin.GET("/packages", d.Packages.List)
	admin.POST("/packages", d.Packages.Create)
	admin.DELETE("/packages/:id", d.Packages.Delete)
	admin.GET("/accounts", d.Accounts.List)
	admin.POST("/accounts", d.Accounts.Create)
	admin.POST("/accounts/:id/suspend", d.Accounts.Suspend)
	admin.POST("/accounts/:id/unsuspend", d.Accounts.Unsuspend)
	admin.POST("/accounts/:id/terminate", d.Accounts.Terminate)
	// Plugin surface reservation (§11) — read-only until the runtime lands.
	admin.GET("/plugins", d.Plugins.List)
	admin.GET("/plugins/manifest-schema", d.Plugins.ManifestSchema)

	return r
}

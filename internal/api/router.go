// Package api wires the CypherCore HTTP surface. All routes live under
// /api/v1 from day one (plan.md: API Contract & Repository Strategy).
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/config"
)

type Deps struct {
	Config config.Core
	Tokens *auth.TokenService
	Auth   *AuthHandler
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

	// Unauthenticated
	v1.POST("/auth/login", d.Auth.Login)
	v1.POST("/auth/refresh", d.Auth.Refresh)

	// Authenticated
	authed := v1.Group("", auth.Middleware(d.Tokens))
	authed.POST("/auth/logout", d.Auth.Logout)
	authed.GET("/me", d.Auth.Me)

	// Role-gated groups grow here as features land (Phase 2+):
	//   admin := authed.Group("/admin", auth.RequireRole(auth.RoleRootAdmin))
	//   reseller := authed.Group("/reseller", auth.RequireRole(auth.RoleRootAdmin, auth.RoleReseller))

	return r
}

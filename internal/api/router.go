// Package api wires the CypherCore HTTP surface. All routes live under
// /api/v1 from day one (plan.md: API Contract & Repository Strategy).
package api

import (
	"net/http"
	"time"

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
	Resellers   *ResellersHandler
	Databases   *DatabasesHandler
	FTP         *FTPHandler
	FileManager *FileManagerHandler
	DNS         *DNSHandler
	Metrics     *MetricsHandler
	AuditLog    *AuditHandler
	Cron        *CronHandler
	Mail        *MailHandler
}

func NewRouter(d Deps) *gin.Engine {
	if d.Config.Env == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), SecurityHeaders())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus scrape endpoint (unauthenticated like /healthz — operators must
	// network-restrict it). Time-series history lives in the scraping TSDB.
	r.GET("/metrics", d.Metrics.Prometheus)

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

	// Unauthenticated auth endpoints, rate-limited per IP to blunt credential
	// stuffing / refresh abuse (20 requests / minute / IP).
	authLimited := v1.Group("", RateLimit(20, time.Minute))
	authLimited.POST("/auth/login", d.Auth.Login)
	authLimited.POST("/auth/refresh", d.Auth.Refresh)

	// Authenticated
	authed := v1.Group("", auth.Middleware(d.Tokens))
	authed.POST("/auth/logout", d.Auth.Logout)
	authed.GET("/me", d.Auth.Me)

	// Root-admin-only surface: fleet, reseller management, plugin registry.
	admin := authed.Group("/admin", auth.RequireRole(auth.RoleRootAdmin))
	admin.POST("/servers/:id/tasks", d.Tasks.Create)
	admin.GET("/servers/:id", d.Servers.Get)
	admin.DELETE("/servers/:id", d.Servers.Delete)
	admin.POST("/servers/:id/services/:name/control", d.Servers.ControlService)
	admin.POST("/servers/:id/php", d.Servers.ManagePHPRuntime)
	admin.GET("/tasks/:id", d.Tasks.Get)
	admin.GET("/resellers", d.Resellers.List)
	admin.POST("/resellers", d.Resellers.Create)
	admin.GET("/plugins", d.Plugins.List)
	admin.GET("/plugins/manifest-schema", d.Plugins.ManifestSchema)
	admin.GET("/audit", d.AuditLog.List)

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
	mgr.PATCH("/accounts/:id/php-version", d.Accounts.ChangePHPVersion)
	mgr.POST("/accounts/:id/ssl", d.Accounts.IssueSSL)
	mgr.GET("/php/ini-keys", d.Accounts.PHPINIKeys)
	mgr.GET("/php/versions", d.Accounts.PHPVersionList)
	mgr.GET("/accounts/:id/databases", d.Databases.List)
	mgr.POST("/accounts/:id/databases", d.Databases.Create)
	mgr.DELETE("/accounts/:id/databases/:dbid", d.Databases.Delete)
	mgr.GET("/accounts/:id/databases/:dbid/password", d.Databases.RevealPassword)
	mgr.GET("/accounts/:id/databases/:dbid/adminer", d.Databases.AdminerHandoff)
	mgr.GET("/accounts/:id/ftp", d.FTP.List)
	mgr.POST("/accounts/:id/ftp", d.FTP.Create)
	mgr.DELETE("/accounts/:id/ftp/:ftpid", d.FTP.Delete)
	mgr.GET("/accounts/:id/ftp/:ftpid/password", d.FTP.RevealPassword)
	mgr.GET("/accounts/:id/files", d.FileManager.List)
	mgr.DELETE("/accounts/:id/files", d.FileManager.Delete)
	mgr.POST("/accounts/:id/files/dir", d.FileManager.Mkdir)
	mgr.POST("/accounts/:id/files/rename", d.FileManager.Rename)
	mgr.POST("/accounts/:id/files/extract", d.FileManager.Extract)
	mgr.GET("/accounts/:id/file", d.FileManager.ReadFile)
	mgr.PUT("/accounts/:id/file", d.FileManager.WriteFile)
	mgr.GET("/accounts/:id/dns", d.DNS.List)
	mgr.POST("/accounts/:id/dns", d.DNS.Upsert)
	mgr.DELETE("/accounts/:id/dns", d.DNS.Delete)
	mgr.GET("/dns/record-types", d.DNS.RecordTypes)
	mgr.GET("/metrics/:scope", d.Metrics.Scoped)
	mgr.GET("/accounts/:id/cron", d.Cron.Get)
	mgr.PUT("/accounts/:id/cron", d.Cron.Set)
	mgr.GET("/accounts/:id/mail", d.Mail.List)
	mgr.POST("/accounts/:id/mail", d.Mail.Create)
	mgr.DELETE("/accounts/:id/mail/:mailid", d.Mail.Delete)

	return r
}

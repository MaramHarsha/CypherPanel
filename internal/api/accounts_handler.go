package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/phpini"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type AccountsHandler struct {
	Accounts   *store.Accounts
	Packages   *store.Packages
	Resellers  *store.Resellers
	Tasks      *store.Tasks
	Publisher  *jobs.Publisher
	Events     *events.Bus
	Audit      *audit.Logger
	PHPVersion string // default PHP version for new sites
}

// dispatch records a task and publishes it to the target server's agent. The
// task ID doubles as the JetStream dedup key, so this is crash-retry safe.
func (h *AccountsHandler) dispatch(ctx context.Context, serverID, taskType string, payload any, actorID, accountID string) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task, err := h.Tasks.Create(ctx, serverID, taskType, blob, actorID, accountID)
	if err != nil {
		return err
	}
	return h.Publisher.Publish(ctx, jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	})
}

// accountEvent is the minimal, secret-free snapshot carried on account events.
type accountEvent struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
	ServerID string `json:"server_id"`
	Status   string `json:"status"`
}

type accountResponse struct {
	ID             string            `json:"id"`
	Username       string            `json:"username"`
	Email          string            `json:"email"`
	ServerID       string            `json:"server_id"`
	ServerName     string            `json:"server_name"`
	PackageID      string            `json:"package_id"`
	PackageName    string            `json:"package_name"`
	SystemUsername string            `json:"system_username"`
	PrimaryDomain  string            `json:"primary_domain"`
	Status         string            `json:"status"`
	PHPVersion     string            `json:"php_version"`
	PHPSettings    map[string]string `json:"php_settings"`
	SSLStatus      string            `json:"ssl_status"`
	SSLExpiresAt   *time.Time        `json:"ssl_expires_at"`
	CreatedAt      time.Time         `json:"created_at"`
}

type createAccountRequest struct {
	Username  string `json:"username" binding:"required"`
	Email     string `json:"email" binding:"required,email"`
	Password  string `json:"password" binding:"required,min=12"`
	Domain    string `json:"domain" binding:"required"`
	ServerID  string `json:"server_id" binding:"required"`
	PackageID string `json:"package_id" binding:"required"`
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)

func toAccountResponse(a store.Account) accountResponse {
	settings := a.PHPSettings
	if settings == nil {
		settings = map[string]string{}
	}
	return accountResponse{
		ID: a.ID, Username: a.Username, Email: a.Email,
		ServerID: a.ServerID, ServerName: a.ServerName,
		PackageID: a.PackageID, PackageName: a.PackageName,
		SystemUsername: a.SystemUsername, PrimaryDomain: a.PrimaryDomain,
		Status: a.Status, PHPVersion: a.PHPVersion, PHPSettings: settings,
		SSLStatus: a.SSLStatus, SSLExpiresAt: a.SSLExpiresAt,
		CreatedAt: a.CreatedAt,
	}
}

// List returns all hosting accounts.
//
//	@Summary  List accounts (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} accountResponse
//	@Security BearerAuth
//	@Router   /admin/accounts [get]
func (h *AccountsHandler) List(c *gin.Context) {
	// Root admin sees all accounts; a reseller sees only their pool.
	accounts, err := h.Accounts.List(c.Request.Context(), auth.OwnerFilter(auth.ClaimsFrom(c)))
	if err != nil {
		slog.Error("listing accounts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]accountResponse, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, toAccountResponse(a))
	}
	c.JSON(http.StatusOK, out)
}

// Create provisions a hosting account: panel user + account row, then a
// system_user.create task to the target server's agent. The account becomes
// active when the agent reports success.
//
//	@Summary  Create a hosting account (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body createAccountRequest true "Account definition"
//	@Success  202 {object} accountResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts [post]
func (h *AccountsHandler) Create(c *gin.Context) {
	var req createAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username, email, password (12+ chars), domain, server_id and package_id are required"})
		return
	}
	if !usernameRe.MatchString(req.Username) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username must be 3-32 chars: lowercase letters, digits, - or _, starting with a letter"})
		return
	}

	claims := auth.ClaimsFrom(c)
	// resellerID is "" for a root admin (unrestricted) or the reseller's own
	// ID (scoped). A scoped caller may only use their own packages and is
	// bound by their pool quota — checked BEFORE any provisioning.
	resellerID := auth.OwnerFilter(claims)
	if resellerID != "" {
		pkg, perr := h.Packages.GetByID(c.Request.Context(), req.PackageID)
		if errors.Is(perr, store.ErrNotFound) || (perr == nil && !auth.CanAct(claims, pkg.OwnerID)) {
			c.JSON(http.StatusNotFound, gin.H{"error": "package not found"})
			return
		}
		if perr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if pool, perr := h.Resellers.GetPool(c.Request.Context(), resellerID); perr == nil && pool.MaxAccounts > 0 {
			count, cerr := h.Accounts.CountByReseller(c.Request.Context(), resellerID)
			if cerr == nil && count >= pool.MaxAccounts {
				c.JSON(http.StatusForbidden, gin.H{"error": "account pool limit reached"})
				return
			}
		}
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// System username is panel-generated and collision-resistant, never
	// user-chosen (it becomes a Linux login name).
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	sysUser := fmt.Sprintf("cyph_%.10s%s", req.Username, hex.EncodeToString(suffix))

	account, err := h.Accounts.CreateWithUser(c.Request.Context(),
		req.Username, req.Email, hash, resellerID, req.ServerID, req.PackageID, sysUser, req.Domain, h.PHPVersion)
	if err != nil {
		slog.Error("creating account", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create account (duplicate username/email/domain, or bad server/package id?)"})
		return
	}

	// Package memory limit feeds the PHP-FPM pool. Best-effort: a missing
	// package here shouldn't block provisioning the system user.
	memoryMB := 0
	if pkg, perr := h.Packages.GetByID(c.Request.Context(), req.PackageID); perr == nil {
		memoryMB = pkg.Limits.MemoryMaxMB
	}

	// 1) Create the Linux user (drives account → active on success).
	err = h.dispatch(c.Request.Context(), req.ServerID, jobs.TypeSystemUserCreate,
		jobs.SystemUserCreatePayload{Username: sysUser}, claims.Subject, account.ID)
	if err != nil {
		slog.Error("dispatching provisioning task", "account_id", account.ID, "error", err)
		_ = h.Accounts.SetStatus(c.Request.Context(), account.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account recorded but provisioning dispatch failed"})
		return
	}
	// 2) Provision the site (nginx vhost + PHP-FPM pool). Runs after the user
	//    on the same agent consumer; retries harmlessly if it races ahead.
	if err := h.dispatch(c.Request.Context(), req.ServerID, jobs.TypeSiteProvision,
		jobs.SiteProvisionPayload{Username: sysUser, Domain: req.Domain, PHPVersion: h.PHPVersion, MemoryMB: memoryMB},
		claims.Subject, account.ID); err != nil {
		slog.Error("dispatching site provision task", "account_id", account.ID, "error", err)
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "account.create", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"username": req.Username, "domain": req.Domain, "server_id": req.ServerID},
		IP:     c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), events.SubjectAccountCreated, "account", account.ID, accountEvent{
		ID: account.ID, Username: account.Username, Domain: account.PrimaryDomain,
		ServerID: account.ServerID, Status: account.Status,
	})
	c.JSON(http.StatusAccepted, toAccountResponse(*account))
}

// Suspend disables an account (panel login blocked immediately).
//
//	@Summary  Suspend an account (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/suspend [post]
func (h *AccountsHandler) Suspend(c *gin.Context) {
	h.setStatus(c, "suspended", "account.suspend", events.SubjectAccountSuspended)
}

// Unsuspend re-enables a suspended account.
//
//	@Summary  Unsuspend an account (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/unsuspend [post]
func (h *AccountsHandler) Unsuspend(c *gin.Context) {
	h.setStatus(c, "active", "account.unsuspend", events.SubjectAccountUnsuspended)
}

// ownedByCaller verifies the account is within the caller's scope, writing a
// 404 (indistinguishable from "missing", so scope can't be probed) and
// returning false if not. Root admins always pass.
func (h *AccountsHandler) ownedByCaller(c *gin.Context, claims *auth.Claims, id string) bool {
	account, err := h.Accounts.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return false
	}
	return true
}

func (h *AccountsHandler) setStatus(c *gin.Context, status, action, subject string) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)
	if !h.ownedByCaller(c, claims, id) {
		return // response already written
	}
	if err := h.Accounts.SetStatus(c.Request.Context(), id, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: action, TargetType: "account", TargetID: id, IP: c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), subject, "account", id, accountEvent{ID: id, Status: status})
	c.JSON(http.StatusOK, gin.H{"status": status})
}

type updatePHPSettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

// PHPINIKeys returns the allowlisted php.ini directives the INI editor exposes.
//
//	@Summary  List editable php.ini directive keys
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} string
//	@Security BearerAuth
//	@Router   /admin/php/ini-keys [get]
func (h *AccountsHandler) PHPINIKeys(c *gin.Context) {
	c.JSON(http.StatusOK, phpini.AllowedKeys())
}

// UpdatePHPSettings validates and stores an account's php.ini overrides, then
// re-provisions the site so the PHP-FPM pool is regenerated and reloaded.
//
//	@Summary  Update an account's PHP INI settings (MultiPHP INI editor)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string                   true "Account ID"
//	@Param    request body updatePHPSettingsRequest true "Allowlisted php.ini overrides"
//	@Success  202 {object} map[string]any
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/php-settings [patch]
func (h *AccountsHandler) UpdatePHPSettings(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)

	account, err := h.Accounts.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	var req updatePHPSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "settings object is required"})
		return
	}
	clean, err := phpini.Validate(req.Settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.Accounts.UpdatePHPSettings(c.Request.Context(), id, clean); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Re-provision to apply the new pool config (site.provision is idempotent).
	memoryMB := 0
	if pkg, perr := h.Packages.GetByID(c.Request.Context(), account.PackageID); perr == nil {
		memoryMB = pkg.Limits.MemoryMaxMB
	}
	if err := h.dispatch(c.Request.Context(), account.ServerID, jobs.TypeSiteProvision,
		jobs.SiteProvisionPayload{
			Username: account.SystemUsername, Domain: account.PrimaryDomain,
			PHPVersion: account.PHPVersion, MemoryMB: memoryMB, PHPSettings: clean,
		}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching php reconfigure", "account_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings saved but reconfigure dispatch failed"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "account.php_settings_update", TargetType: "account", TargetID: id,
		Detail: map[string]any{"settings": clean}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "applying", "settings": clean})
}

// IssueSSL requests a Let's Encrypt certificate for the account's primary
// domain and switches it to HTTPS. Only meaningful for active accounts.
//
//	@Summary  Issue/renew an SSL certificate for the account domain
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Failure  409 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/ssl [post]
func (h *AccountsHandler) IssueSSL(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)

	account, err := h.Accounts.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if account.Status != "active" {
		c.JSON(http.StatusConflict, gin.H{"error": "account must be active to issue a certificate"})
		return
	}

	if err := h.Accounts.SetSSL(c.Request.Context(), id, "issuing", account.SSLExpiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.dispatch(c.Request.Context(), account.ServerID, jobs.TypeSSLIssue,
		jobs.SSLIssuePayload{Username: account.SystemUsername, Domain: account.PrimaryDomain, Email: account.Email},
		claims.Subject, account.ID); err != nil {
		slog.Error("dispatching ssl issue", "account_id", id, "error", err)
		_ = h.Accounts.SetSSL(c.Request.Context(), id, "failed", account.SSLExpiresAt)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ssl issuance dispatch failed"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "account.ssl_issue", TargetType: "account", TargetID: id,
		Detail: map[string]any{"domain": account.PrimaryDomain}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "issuing"})
}

// Terminate starts account removal: status → terminating, then a
// system_user.remove task; the row is deleted when the agent reports success.
//
//	@Summary  Terminate an account (root admin only, irreversible)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/terminate [post]
func (h *AccountsHandler) Terminate(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)
	account, err := h.Accounts.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if err := h.Accounts.SetStatus(c.Request.Context(), id, "terminating"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Remove the site's configs first, then the system user (whose success
	// deletes the account row).
	if err := h.dispatch(c.Request.Context(), account.ServerID, jobs.TypeSiteDeprovision,
		jobs.SiteDeprovisionPayload{Username: account.SystemUsername, Domain: account.PrimaryDomain, PHPVersion: h.PHPVersion},
		claims.Subject, account.ID); err != nil {
		slog.Error("dispatching site deprovision task", "account_id", id, "error", err)
	}
	if err := h.dispatch(c.Request.Context(), account.ServerID, jobs.TypeSystemUserRemove,
		jobs.SystemUserRemovePayload{Username: account.SystemUsername}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching termination task", "account_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "termination dispatch failed; account left in terminating state"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "account.terminate", TargetType: "account", TargetID: id,
		Detail: map[string]any{"system_username": account.SystemUsername}, IP: c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), events.SubjectAccountTerminating, "account", id, accountEvent{
		ID: id, Username: account.Username, ServerID: account.ServerID, Status: "terminating",
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "terminating"})
}

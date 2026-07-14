package api

import (
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
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type AccountsHandler struct {
	Accounts  *store.Accounts
	Tasks     *store.Tasks
	Publisher *jobs.Publisher
	Events    *events.Bus
	Audit     *audit.Logger
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
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	Email          string    `json:"email"`
	ServerID       string    `json:"server_id"`
	ServerName     string    `json:"server_name"`
	PackageID      string    `json:"package_id"`
	PackageName    string    `json:"package_name"`
	SystemUsername string    `json:"system_username"`
	PrimaryDomain  string    `json:"primary_domain"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
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
	return accountResponse{
		ID: a.ID, Username: a.Username, Email: a.Email,
		ServerID: a.ServerID, ServerName: a.ServerName,
		PackageID: a.PackageID, PackageName: a.PackageName,
		SystemUsername: a.SystemUsername, PrimaryDomain: a.PrimaryDomain,
		Status: a.Status, CreatedAt: a.CreatedAt,
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
	accounts, err := h.Accounts.List(c.Request.Context())
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
		req.Username, req.Email, hash, req.ServerID, req.PackageID, sysUser, req.Domain)
	if err != nil {
		slog.Error("creating account", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create account (duplicate username/email/domain, or bad server/package id?)"})
		return
	}

	claims := auth.ClaimsFrom(c)
	payload, _ := json.Marshal(jobs.SystemUserCreatePayload{Username: sysUser})
	task, err := h.Tasks.Create(c.Request.Context(), req.ServerID, jobs.TypeSystemUserCreate, payload, claims.Subject, account.ID)
	if err == nil {
		err = h.Publisher.Publish(c.Request.Context(), jobs.Task{
			ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
		})
	}
	if err != nil {
		slog.Error("dispatching provisioning task", "account_id", account.ID, "error", err)
		_ = h.Accounts.SetStatus(c.Request.Context(), account.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account recorded but provisioning dispatch failed"})
		return
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

func (h *AccountsHandler) setStatus(c *gin.Context, status, action, subject string) {
	id := c.Param("id")
	if err := h.Accounts.SetStatus(c.Request.Context(), id, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	claims := auth.ClaimsFrom(c)
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: action, TargetType: "account", TargetID: id, IP: c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), subject, "account", id, accountEvent{ID: id, Status: status})
	c.JSON(http.StatusOK, gin.H{"status": status})
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
	account, err := h.Accounts.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
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

	claims := auth.ClaimsFrom(c)
	payload, _ := json.Marshal(jobs.SystemUserRemovePayload{Username: account.SystemUsername})
	task, err := h.Tasks.Create(c.Request.Context(), account.ServerID, jobs.TypeSystemUserRemove, payload, claims.Subject, account.ID)
	if err == nil {
		err = h.Publisher.Publish(c.Request.Context(), jobs.Task{
			ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
		})
	}
	if err != nil {
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

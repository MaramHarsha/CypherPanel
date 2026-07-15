package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/secretcrypt"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// DatabasesHandler manages hosted-account MariaDB databases. Actions are scoped
// to the caller's owned accounts (root = all, reseller = own pool).
type DatabasesHandler struct {
	Accounts  *store.Accounts
	Databases *store.Databases
	Packages  *store.Packages
	Tasks     *store.Tasks
	Publisher *jobs.Publisher
	Audit     *audit.Logger
	Crypt     *secretcrypt.Cipher
}

// dbNameRe validates the operator-supplied database suffix (namespaced with the
// account's system user before it ever reaches MariaDB).
var dbNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

type databaseResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	DBUser    string    `json:"db_user"`
	DBHost    string    `json:"db_host"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func toDatabaseResponse(d store.Database) databaseResponse {
	return databaseResponse{
		ID: d.ID, Name: d.Name, DBUser: d.DBUser, DBHost: d.DBHost,
		Status: d.Status, CreatedAt: d.CreatedAt,
	}
}

// scopedAccount loads the path account and enforces caller scope, writing a 404
// and returning nil if it is missing or out of scope.
func (h *DatabasesHandler) scopedAccount(c *gin.Context) *store.Account {
	claims := auth.ClaimsFrom(c)
	account, err := h.Accounts.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return nil
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return nil
	}
	return account
}

// List returns an account's databases.
//
//	@Summary  List an account's databases
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {array} databaseResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/databases [get]
func (h *DatabasesHandler) List(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	dbs, err := h.Databases.ListByAccount(c.Request.Context(), account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]databaseResponse, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, toDatabaseResponse(d))
	}
	c.JSON(http.StatusOK, out)
}

type createDatabaseRequest struct {
	Name string `json:"name" binding:"required"`
}

// Create provisions a new database + user on the account's server.
//
//	@Summary  Create a database for an account
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string                 true "Account ID"
//	@Param    request body createDatabaseRequest  true "Database name (suffix)"
//	@Success  202 {object} databaseResponse
//	@Failure  400 {object} map[string]string
//	@Failure  403 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/databases [post]
func (h *DatabasesHandler) Create(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)

	var req createDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil || !dbNameRe.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be 1-16 chars: lowercase letters, digits or _, starting with a letter"})
		return
	}

	// Enforce the package's databases limit (0 = unlimited).
	if pkg, err := h.Packages.GetByID(c.Request.Context(), account.PackageID); err == nil && pkg.Limits.Databases > 0 {
		if n, cerr := h.Databases.CountByAccount(c.Request.Context(), account.ID); cerr == nil && n >= pkg.Limits.Databases {
			c.JSON(http.StatusForbidden, gin.H{"error": "database limit reached for this package"})
			return
		}
	}

	// Namespace by the account's system user so names never collide across
	// accounts and ownership is obvious.
	name := account.SystemUsername + "_" + req.Name
	dbUser := name
	dbHost := "localhost"

	rec, err := h.Databases.Create(c.Request.Context(), account.ID, name, dbUser, dbHost)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create database (duplicate name?)"})
		return
	}

	// Payload is secret-free: the agent generates the password and returns it
	// as result metadata (see jobs.DBCreatePayload).
	if err := h.dispatch(c, account.ServerID, jobs.TypeDBCreate,
		jobs.DBCreatePayload{Name: name, DBUser: dbUser, DBHost: dbHost}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching db create", "db_id", rec.ID, "error", err)
		_ = h.Databases.SetStatus(c.Request.Context(), rec.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database recorded but provisioning dispatch failed"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "database.create", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"database": name}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, toDatabaseResponse(*rec))
}

// Delete drops a database and its user.
//
//	@Summary  Delete an account database
//	@Tags     admin
//	@Produce  json
//	@Param    id   path string true "Account ID"
//	@Param    dbid path string true "Database ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/databases/{dbid} [delete]
func (h *DatabasesHandler) Delete(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	rec, err := h.Databases.GetByID(c.Request.Context(), c.Param("dbid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "database not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if err := h.Databases.SetStatus(c.Request.Context(), rec.ID, "deleting"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.dispatch(c, account.ServerID, jobs.TypeDBDrop,
		jobs.DBDropPayload{Name: rec.Name, DBUser: rec.DBUser, DBHost: rec.DBHost}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching db drop", "db_id", rec.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "drop dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "database.delete", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"database": rec.Name}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "deleting"})
}

// RevealPassword returns the decrypted DB password once, for display. Scoped to
// the caller's account; audited (without the secret).
//
//	@Summary  Reveal an account database password
//	@Tags     admin
//	@Produce  json
//	@Param    id   path string true "Account ID"
//	@Param    dbid path string true "Database ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Failure  409 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/databases/{dbid}/password [get]
func (h *DatabasesHandler) RevealPassword(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	rec, err := h.Databases.GetByID(c.Request.Context(), c.Param("dbid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "database not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if len(rec.PasswordEnc) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "password not available yet (still provisioning?)"})
		return
	}
	plain, err := h.Crypt.Decrypt(rec.PasswordEnc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not decrypt password"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "database.reveal_password", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"database": rec.Name}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"username": rec.DBUser, "host": rec.DBHost, "password": string(plain)})
}

// dispatch records + publishes a task (mirrors AccountsHandler.dispatch).
func (h *DatabasesHandler) dispatch(c *gin.Context, serverID, taskType string, payload any, actorID, accountID string) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task, err := h.Tasks.Create(c.Request.Context(), serverID, taskType, blob, actorID, accountID)
	if err != nil {
		return err
	}
	return h.Publisher.Publish(c.Request.Context(), jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	})
}

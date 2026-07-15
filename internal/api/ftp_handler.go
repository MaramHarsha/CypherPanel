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

// FTPHandler manages per-account FTP virtual users. Scoped to the caller's
// accounts, mirroring the database surface.
type FTPHandler struct {
	Accounts  *store.Accounts
	FTP       *store.FTPAccounts
	Tasks     *store.Tasks
	Publisher *jobs.Publisher
	Audit     *audit.Logger
	Crypt     *secretcrypt.Cipher
}

var ftpNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

type ftpResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	HomeDir   string    `json:"home_dir"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func toFTPResponse(f store.FTPAccount) ftpResponse {
	return ftpResponse{ID: f.ID, Username: f.Username, HomeDir: f.HomeDir, Status: f.Status, CreatedAt: f.CreatedAt}
}

func (h *FTPHandler) scopedAccount(c *gin.Context) *store.Account {
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

// List returns an account's FTP users.
//
//	@Summary  List an account's FTP users
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {array} ftpResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/ftp [get]
func (h *FTPHandler) List(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	items, err := h.FTP.ListByAccount(c.Request.Context(), account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]ftpResponse, 0, len(items))
	for _, f := range items {
		out = append(out, toFTPResponse(f))
	}
	c.JSON(http.StatusOK, out)
}

type createFTPRequest struct {
	Name string `json:"name" binding:"required"`
}

// Create provisions an FTP virtual user on the account's server.
//
//	@Summary  Create an FTP user for an account
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string           true "Account ID"
//	@Param    request body createFTPRequest true "FTP name (suffix)"
//	@Success  202 {object} ftpResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/ftp [post]
func (h *FTPHandler) Create(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	var req createFTPRequest
	if err := c.ShouldBindJSON(&req); err != nil || !ftpNameRe.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be 1-16 chars: lowercase letters, digits or _, starting with a letter"})
		return
	}

	username := account.SystemUsername + "_" + req.Name
	rec, err := h.FTP.Create(c.Request.Context(), account.ID, username, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create ftp user (duplicate name?)"})
		return
	}

	if err := h.dispatch(c, account.ServerID, jobs.TypeFTPCreate,
		jobs.FTPCreatePayload{Username: username, SystemUser: account.SystemUsername}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching ftp create", "ftp_id", rec.ID, "error", err)
		_ = h.FTP.SetStatus(c.Request.Context(), rec.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ftp user recorded but provisioning dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "ftp.create", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"username": username}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, toFTPResponse(*rec))
}

// Delete removes an FTP user.
//
//	@Summary  Delete an account FTP user
//	@Tags     admin
//	@Produce  json
//	@Param    id     path string true "Account ID"
//	@Param    ftpid  path string true "FTP user ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/ftp/{ftpid} [delete]
func (h *FTPHandler) Delete(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	rec, err := h.FTP.GetByID(c.Request.Context(), c.Param("ftpid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "ftp user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.FTP.SetStatus(c.Request.Context(), rec.ID, "deleting"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.dispatch(c, account.ServerID, jobs.TypeFTPDelete,
		jobs.FTPDeletePayload{Username: rec.Username}, claims.Subject, account.ID); err != nil {
		slog.Error("dispatching ftp delete", "ftp_id", rec.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "ftp.delete", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"username": rec.Username}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "deleting"})
}

// RevealPassword returns the decrypted FTP password once.
//
//	@Summary  Reveal an FTP user's password
//	@Tags     admin
//	@Produce  json
//	@Param    id    path string true "Account ID"
//	@Param    ftpid path string true "FTP user ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Failure  409 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/ftp/{ftpid}/password [get]
func (h *FTPHandler) RevealPassword(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	rec, err := h.FTP.GetByID(c.Request.Context(), c.Param("ftpid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "ftp user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if len(rec.PasswordEnc) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "password not available yet"})
		return
	}
	plain, err := h.Crypt.Decrypt(rec.PasswordEnc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not decrypt password"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "ftp.reveal_password", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"username": rec.Username}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"username": rec.Username, "home_dir": rec.HomeDir, "password": string(plain)})
}

func (h *FTPHandler) dispatch(c *gin.Context, serverID, taskType string, payload any, actorID, accountID string) error {
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

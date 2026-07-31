package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/filemanager"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// FileManagerHandler proxies file operations to the account's server over NATS
// request-reply. Every request is scoped to the caller's account; the agent
// enforces path safety and account-uid isolation.
type FileManagerHandler struct {
	Accounts *store.Accounts
	Packages *store.Packages
	NC       *nats.Conn
	Audit    *audit.Logger
	Timeout  time.Duration
}

func (h *FileManagerHandler) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return 10 * time.Second
}

func (h *FileManagerHandler) scopedAccount(c *gin.Context) *store.Account {
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

// do sends one request to the account's agent and returns the response. It
// stamps the account's disk and inode quotas so the agent can enforce them on
// writes/extracts.
func (h *FileManagerHandler) do(account *store.Account, req filemanager.Request) (filemanager.Response, error) {
	req.Username = account.SystemUsername
	if h.Packages != nil {
		if pkg, err := h.Packages.GetByID(context.Background(), account.PackageID); err == nil {
			if pkg.Limits.DiskMB > 0 {
				req.QuotaBytes = int64(pkg.Limits.DiskMB) << 20
			}
			if pkg.Limits.Inodes > 0 {
				req.QuotaInodes = int64(pkg.Limits.Inodes)
			}
		}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return filemanager.Response{}, err
	}
	msg, err := h.NC.Request(filemanager.Subject(account.ServerID), data, h.timeout())
	if err != nil {
		return filemanager.Response{}, err
	}
	var resp filemanager.Response
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return filemanager.Response{}, err
	}
	return resp, nil
}

// respond writes the agent response, mapping an operation-level error to 400.
func respondFS(c *gin.Context, resp filemanager.Response, err error) {
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "server unreachable: " + err.Error()})
		return
	}
	if resp.Error != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": resp.Error})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// List returns a directory listing.
//
//	@Summary  List files in an account directory
//	@Tags     admin
//	@Produce  json
//	@Param    id   path  string true  "Account ID"
//	@Param    path query string false "Path relative to account home"
//	@Success  200 {object} filemanager.Response
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/files [get]
func (h *FileManagerHandler) List(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpList, Path: c.Query("path")})
	respondFS(c, resp, err)
}

type fileContentResponse struct {
	Content string `json:"content"`
}

// ReadFile returns a text file's contents.
//
//	@Summary  Read a file's contents
//	@Tags     admin
//	@Produce  json
//	@Param    id   path  string true "Account ID"
//	@Param    path query string true "File path relative to account home"
//	@Success  200 {object} fileContentResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/file [get]
func (h *FileManagerHandler) ReadFile(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpRead, Path: c.Query("path")})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "server unreachable"})
		return
	}
	if resp.Error != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": resp.Error})
		return
	}
	c.JSON(http.StatusOK, fileContentResponse{Content: string(resp.Content)})
}

type writeFileRequest struct {
	Path    string `json:"path" binding:"required"`
	Content string `json:"content"`
}

// WriteFile saves a text file.
//
//	@Summary  Write a file's contents
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string           true "Account ID"
//	@Param    request body writeFileRequest true "Path + content"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/file [put]
func (h *FileManagerHandler) WriteFile(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req writeFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	resp, err := h.do(account, filemanager.Request{
		Op: filemanager.OpWrite, Path: req.Path, Content: []byte(req.Content),
	})
	if err == nil && resp.Error == "" {
		h.audit(c, account.ID, "file.write", req.Path)
	}
	respondFS(c, resp, err)
}

type pathRequest struct {
	Path string `json:"path" binding:"required"`
}

// Mkdir creates a directory.
//
//	@Summary  Create a directory
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string      true "Account ID"
//	@Param    request body pathRequest true "Directory path"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/files/dir [post]
func (h *FileManagerHandler) Mkdir(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req pathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpMkdir, Path: req.Path})
	respondFS(c, resp, err)
}

// Delete removes a file or directory (recursive).
//
//	@Summary  Delete a file or directory
//	@Tags     admin
//	@Produce  json
//	@Param    id   path  string true "Account ID"
//	@Param    path query string true "Path relative to account home"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/files [delete]
func (h *FileManagerHandler) Delete(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpDelete, Path: c.Query("path")})
	if err == nil && resp.Error == "" {
		h.audit(c, account.ID, "file.delete", c.Query("path"))
	}
	respondFS(c, resp, err)
}

type renameRequest struct {
	Path    string `json:"path" binding:"required"`
	NewPath string `json:"new_path" binding:"required"`
}

// Extract unpacks a zip archive already in the account tree (zip-slip-safe,
// quota-enforced on the agent).
//
//	@Summary  Extract a zip archive
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string      true "Account ID"
//	@Param    request body pathRequest true "Archive path"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/files/extract [post]
func (h *FileManagerHandler) Extract(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req pathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpExtract, Path: req.Path})
	if err == nil && resp.Error == "" {
		h.audit(c, account.ID, "file.extract", req.Path)
	}
	respondFS(c, resp, err)
}

// Rename moves/renames a file or directory.
//
//	@Summary  Rename a file or directory
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string        true "Account ID"
//	@Param    request body renameRequest true "Path + new_path"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/files/rename [post]
func (h *FileManagerHandler) Rename(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req renameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path and new_path are required"})
		return
	}
	resp, err := h.do(account, filemanager.Request{Op: filemanager.OpRename, Path: req.Path, NewPath: req.NewPath})
	respondFS(c, resp, err)
}

func (h *FileManagerHandler) audit(c *gin.Context, accountID, action, path string) {
	claims := auth.ClaimsFrom(c)
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: action, TargetType: "account", TargetID: accountID,
		Detail: map[string]any{"path": path}, IP: c.ClientIP(),
	})
}

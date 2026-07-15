package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/cron"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// CronHandler proxies per-account crontab get/set to the account's agent over
// NATS request-reply. Scoped to the caller's accounts.
type CronHandler struct {
	Accounts *store.Accounts
	NC       *nats.Conn
	Audit    *audit.Logger
	Timeout  time.Duration
}

func (h *CronHandler) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return 10 * time.Second
}

func (h *CronHandler) scopedAccount(c *gin.Context) *store.Account {
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

func (h *CronHandler) do(account *store.Account, req cron.Request) (cron.Response, error) {
	req.Username = account.SystemUsername
	data, err := json.Marshal(req)
	if err != nil {
		return cron.Response{}, err
	}
	msg, err := h.NC.Request(cron.Subject(account.ServerID), data, h.timeout())
	if err != nil {
		return cron.Response{}, err
	}
	var resp cron.Response
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return cron.Response{}, err
	}
	return resp, nil
}

type cronResponse struct {
	Content string `json:"content"`
}

// Get returns the account's crontab.
//
//	@Summary  Get an account's crontab
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {object} cronResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/cron [get]
func (h *CronHandler) Get(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	resp, err := h.do(account, cron.Request{Op: cron.OpGet})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "server unreachable"})
		return
	}
	if resp.Error != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": resp.Error})
		return
	}
	c.JSON(http.StatusOK, cronResponse{Content: resp.Content})
}

type setCronRequest struct {
	Content string `json:"content"`
}

// Set replaces the account's crontab.
//
//	@Summary  Replace an account's crontab
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string         true "Account ID"
//	@Param    request body setCronRequest true "Crontab content"
//	@Success  200 {object} map[string]string
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/cron [put]
func (h *CronHandler) Set(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req setCronRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content is required"})
		return
	}
	resp, err := h.do(account, cron.Request{Op: cron.OpSet, Content: req.Content})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "server unreachable"})
		return
	}
	if resp.Error != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": resp.Error})
		return
	}
	claims := auth.ClaimsFrom(c)
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "cron.set", TargetType: "account", TargetID: account.ID, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"status": "saved"})
}

package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// ResellersHandler manages reseller users and their resource pools. Root-admin
// only — resellers cannot create other resellers.
type ResellersHandler struct {
	Resellers *store.Resellers
	Events    *events.Bus
	Audit     *audit.Logger
}

var resellerUsernameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)

type resellerResponse struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	MaxAccounts  int       `json:"max_accounts"`
	MaxDiskMB    int       `json:"max_disk_mb"`
	AccountCount int       `json:"account_count"`
	CreatedAt    time.Time `json:"created_at"`
}

type createResellerRequest struct {
	Username    string `json:"username" binding:"required"`
	Email       string `json:"email" binding:"required,email"`
	Password    string `json:"password" binding:"required,min=12"`
	MaxAccounts int    `json:"max_accounts"`
	MaxDiskMB   int    `json:"max_disk_mb"`
}

// List returns all resellers with pool limits and current usage.
//
//	@Summary  List resellers (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} resellerResponse
//	@Security BearerAuth
//	@Router   /admin/resellers [get]
func (h *ResellersHandler) List(c *gin.Context) {
	list, err := h.Resellers.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]resellerResponse, 0, len(list))
	for _, r := range list {
		out = append(out, resellerResponse{
			ID: r.ID, Username: r.Username, Email: r.Email,
			MaxAccounts: r.MaxAccounts, MaxDiskMB: r.MaxDiskMB,
			AccountCount: r.AccountCount, CreatedAt: r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// Create provisions a reseller user and its resource pool.
//
//	@Summary  Create a reseller (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body createResellerRequest true "Reseller definition"
//	@Success  201 {object} resellerResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/resellers [post]
func (h *ResellersHandler) Create(c *gin.Context) {
	var req createResellerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username, email, password (12+ chars) are required"})
		return
	}
	if !resellerUsernameRe.MatchString(req.Username) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username must be 3-32 chars: lowercase letters, digits, - or _, starting with a letter"})
		return
	}
	if req.MaxAccounts < 0 || req.MaxDiskMB < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pool limits cannot be negative"})
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	reseller, err := h.Resellers.Create(c.Request.Context(), req.Username, req.Email, hash, req.MaxAccounts, req.MaxDiskMB)
	if err != nil {
		slog.Error("creating reseller", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create reseller (duplicate username or email?)"})
		return
	}

	claims := auth.ClaimsFrom(c)
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "reseller.create", TargetType: "reseller", TargetID: reseller.ID,
		Detail: map[string]any{"username": req.Username, "max_accounts": req.MaxAccounts, "max_disk_mb": req.MaxDiskMB},
		IP:     c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), events.SubjectResellerCreated, "reseller", reseller.ID,
		map[string]any{"id": reseller.ID, "username": reseller.Username})

	c.JSON(http.StatusCreated, resellerResponse{
		ID: reseller.ID, Username: reseller.Username, Email: reseller.Email,
		MaxAccounts: reseller.MaxAccounts, MaxDiskMB: reseller.MaxDiskMB, CreatedAt: reseller.CreatedAt,
	})
}

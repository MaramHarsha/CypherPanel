package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type AuthHandler struct {
	Users  *store.Users
	Tokens *auth.TokenService
	Audit  *audit.Logger
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

// Login authenticates a user.
//
//	@Summary  Log in with username and password
//	@Tags     auth
//	@Accept   json
//	@Produce  json
//	@Param    request body loginRequest true "Credentials"
//	@Success  200 {object} tokenResponse
//	@Failure  400 {object} map[string]string
//	@Failure  401 {object} map[string]string
//	@Router   /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	user, err := h.Users.GetByUsername(c.Request.Context(), req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Same rejection for unknown user and wrong password: no username probing.
	ok := false
	if user != nil {
		ok, err = auth.VerifyPassword(req.Password, user.PasswordHash)
		if err != nil {
			slog.Error("verifying password", "error", err)
		}
	}
	if !ok || user.SuspendedAt != nil {
		_ = h.Audit.Record(c.Request.Context(), audit.Entry{
			Action: "auth.login_failed", TargetType: "user", TargetID: req.Username, IP: c.ClientIP(),
		})
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	resp, err := h.issueTokens(c, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: user.ID, ActorRole: user.Role,
		Action: "auth.login", TargetType: "user", TargetID: user.ID, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, resp)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Refresh rotates a refresh token for a new token pair.
//
//	@Summary  Exchange a refresh token for new tokens (single-use rotation)
//	@Tags     auth
//	@Accept   json
//	@Produce  json
//	@Param    request body refreshRequest true "Refresh token"
//	@Success  200 {object} tokenResponse
//	@Failure  401 {object} map[string]string
//	@Router   /auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refresh_token is required"})
		return
	}

	userID, err := h.Tokens.ConsumeRefresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}

	user, err := h.Users.GetByID(c.Request.Context(), userID)
	if err != nil || user.SuspendedAt != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}

	resp, err := h.issueTokens(c, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout revokes the caller's refresh token.
//
//	@Summary  Log out (revokes the refresh token)
//	@Tags     auth
//	@Accept   json
//	@Produce  json
//	@Param    request body logoutRequest false "Refresh token to revoke"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	var req logoutRequest
	_ = c.ShouldBindJSON(&req)
	if req.RefreshToken != "" {
		_ = h.Tokens.RevokeRefresh(c.Request.Context(), req.RefreshToken)
	}
	if claims := auth.ClaimsFrom(c); claims != nil {
		_ = h.Audit.Record(c.Request.Context(), audit.Entry{
			ActorID: claims.Subject, ActorRole: string(claims.Role),
			Action: "auth.logout", TargetType: "user", TargetID: claims.Subject, IP: c.ClientIP(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"status": "logged out"})
}

// Me returns the authenticated user's profile.
//
//	@Summary  Current user profile
//	@Tags     auth
//	@Produce  json
//	@Success  200 {object} map[string]string
//	@Failure  401 {object} map[string]string
//	@Security BearerAuth
//	@Router   /me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	user, err := h.Users.GetByID(c.Request.Context(), claims.Subject)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"email":    user.Email,
		"role":     user.Role,
	})
}

func (h *AuthHandler) issueTokens(c *gin.Context, user *store.User) (*tokenResponse, error) {
	access, err := h.Tokens.IssueAccess(user.ID, auth.Role(user.Role), user.ResellerID, "")
	if err != nil {
		slog.Error("issuing access token", "error", err)
		return nil, err
	}
	refresh, err := h.Tokens.IssueRefresh(c.Request.Context(), user.ID)
	if err != nil {
		slog.Error("issuing refresh token", "error", err)
		return nil, err
	}
	return &tokenResponse{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer"}, nil
}

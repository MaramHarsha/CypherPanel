package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// claimsKey is the gin context key under which validated claims are stored.
const claimsKey = "cypher.claims"

// Middleware validates the Bearer access token and stores its claims in the
// request context. It is the single entry point for authentication; handlers
// never parse tokens themselves.
func Middleware(ts *TokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		claims, err := ts.ParseAccess(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Set(claimsKey, claims)
		c.Next()
	}
}

// RequireRole allows the request through only for the listed roles. This is
// the centralized policy gate from plan.md Section 6 — handlers must use it
// (plus scope checks via ClaimsFrom) instead of ad-hoc role comparisons.
func RequireRole(roles ...Role) gin.HandlerFunc {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		claims := ClaimsFrom(c)
		if claims == nil || !allowed[claims.Role] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			return
		}
		c.Next()
	}
}

// ClaimsFrom returns the validated claims for the current request, or nil if
// the request did not pass Middleware.
func ClaimsFrom(c *gin.Context) *Claims {
	v, ok := c.Get(claimsKey)
	if !ok {
		return nil
	}
	claims, _ := v.(*Claims)
	return claims
}

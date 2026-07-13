package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

// Claims carry the caller's role and resource scope in every access token, so
// the authorization layer never needs a database lookup to know what the
// caller may touch (plan.md Section 6).
type Claims struct {
	Role       Role   `json:"role"`
	ResellerID string `json:"reseller_id,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	jwt.RegisteredClaims
}

var (
	ErrInvalidToken = errors.New("auth: invalid or expired token")
)

// TokenService issues short-lived JWT access tokens and manages revocable
// refresh tokens in Redis. Access tokens are stateless; refresh tokens are
// server-side so logout/suspension revokes immediately.
type TokenService struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	redis      *redis.Client
}

func NewTokenService(secret string, accessTTL, refreshTTL time.Duration, rdb *redis.Client) *TokenService {
	return &TokenService{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL, redis: rdb}
}

// IssueAccess creates a signed access token for the given identity.
func (s *TokenService) IssueAccess(userID string, role Role, resellerID, accountID string) (string, error) {
	now := time.Now()
	claims := Claims{
		Role:       role,
		ResellerID: resellerID,
		AccountID:  accountID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    "cypherpanel",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// ParseAccess validates a token string and returns its claims.
func (s *TokenService) ParseAccess(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !claims.Role.Valid() {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func refreshKey(token string) string { return "refresh:" + token }

// IssueRefresh creates an opaque refresh token bound to userID in Redis.
func (s *TokenService) IssueRefresh(ctx context.Context, userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generating refresh token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := s.redis.Set(ctx, refreshKey(token), userID, s.refreshTTL).Err(); err != nil {
		return "", fmt.Errorf("auth: storing refresh token: %w", err)
	}
	return token, nil
}

// ConsumeRefresh atomically validates and deletes a refresh token (rotation:
// each refresh token is single-use), returning the bound user ID.
func (s *TokenService) ConsumeRefresh(ctx context.Context, token string) (string, error) {
	userID, err := s.redis.GetDel(ctx, refreshKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("auth: consuming refresh token: %w", err)
	}
	return userID, nil
}

// RevokeRefresh deletes a refresh token (logout).
func (s *TokenService) RevokeRefresh(ctx context.Context, token string) error {
	return s.redis.Del(ctx, refreshKey(token)).Err()
}

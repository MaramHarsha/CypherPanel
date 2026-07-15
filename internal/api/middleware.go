package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders sets conservative security headers on every API response.
// (The UI sets its own CSP/HSTS in next.config; these protect the API surface.)
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// The API returns JSON, never HTML to render — lock scripting down hard.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Next()
	}
}

// rateLimiter is a tiny in-memory fixed-window limiter keyed by client IP. It
// blunts credential stuffing on the auth endpoints without a Redis round-trip;
// a distributed limiter is a scale-out follow-up.
type rateLimiter struct {
	mu       sync.Mutex
	hits     map[string]*window
	limit    int
	window   time.Duration
	lastSweep time.Time
}

type window struct {
	count int
	reset time.Time
}

// RateLimit allows `limit` requests per `per` window per client IP.
func RateLimit(limit int, per time.Duration) gin.HandlerFunc {
	rl := &rateLimiter{hits: make(map[string]*window), limit: limit, window: per, lastSweep: time.Now()}
	return func(c *gin.Context) {
		if !rl.allow(c.ClientIP(), time.Now()) {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, slow down"})
			return
		}
		c.Next()
	}
}

func (rl *rateLimiter) allow(ip string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Opportunistic sweep of expired windows so the map can't grow unbounded.
	if now.Sub(rl.lastSweep) > rl.window {
		for k, w := range rl.hits {
			if now.After(w.reset) {
				delete(rl.hits, k)
			}
		}
		rl.lastSweep = now
	}

	w := rl.hits[ip]
	if w == nil || now.After(w.reset) {
		rl.hits[ip] = &window{count: 1, reset: now.Add(rl.window)}
		return true
	}
	if w.count >= rl.limit {
		return false
	}
	w.count++
	return true
}

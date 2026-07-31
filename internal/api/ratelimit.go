package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// fixedWindowScript is the standard atomic fixed-window counter: increment,
// and set the expiry only on the first hit of a window.
//
// It must be a script rather than INCR+EXPIRE as separate commands: between
// the two, a crash (or a lost connection) would leave a counter with no TTL,
// permanently locking that IP out.
var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current
`)

// distributedLimiter is a fixed-window limiter shared across every Core
// instance via Redis, with a per-instance in-memory limiter as the fallback.
//
// Falling back rather than failing closed is deliberate: a Redis outage must
// not lock every operator out of the panel. Falling back rather than failing
// *open* is equally deliberate: an attacker who can knock Redis over must not
// thereby remove rate limiting altogether.
type distributedLimiter struct {
	rdb      *redis.Client
	fallback *rateLimiter
	limit    int
	window   time.Duration

	// warnOnce keeps a Redis outage from writing a log line per request.
	warnOnce sync.Once
}

func (l *distributedLimiter) allow(ctx context.Context, key string, now time.Time) bool {
	if l.rdb == nil {
		return l.fallback.allow(key, now)
	}
	// Bound the Redis call: a hung Redis must not hold the request open.
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	count, err := fixedWindowScript.Run(ctx, l.rdb,
		[]string{"cypher:ratelimit:" + key},
		l.window.Milliseconds(),
	).Int64()
	if err != nil {
		l.warnOnce.Do(func() {
			slog.Warn("rate limiter: redis unavailable, falling back to per-instance limiting", "error", err)
		})
		return l.fallback.allow(key, now)
	}
	return count <= int64(l.limit)
}

// RateLimit allows `limit` requests per `per` window per client IP.
//
// When rdb is non-nil the window is shared across every Core instance, so the
// limit is a real fleet-wide limit rather than limit×instances — which is what
// makes it meaningful behind a load balancer.
func RateLimit(rdb *redis.Client, limit int, per time.Duration) gin.HandlerFunc {
	l := &distributedLimiter{
		rdb:      rdb,
		limit:    limit,
		window:   per,
		fallback: &rateLimiter{hits: make(map[string]*window), limit: limit, window: per, lastSweep: time.Now()},
	}
	retryAfter := strconv.Itoa(max(1, int(per.Seconds())))
	return func(c *gin.Context) {
		if !l.allow(c.Request.Context(), c.ClientIP(), time.Now()) {
			c.Header("Retry-After", retryAfter)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, slow down"})
			return
		}
		c.Next()
	}
}

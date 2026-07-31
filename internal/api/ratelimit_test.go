package api

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// unreachableRedis points at a closed port so every command fails fast,
// exercising the fallback path without needing a real Redis.
func unreachableRedis(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 50 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newFallbackLimiter(limit int, per time.Duration) *distributedLimiter {
	return &distributedLimiter{
		limit:    limit,
		window:   per,
		fallback: &rateLimiter{hits: make(map[string]*window), limit: limit, window: per, lastSweep: time.Now()},
	}
}

// With no Redis configured the limiter must still limit — falling back is a
// degradation of scope (per-instance), never a removal of the limit.
func TestLimiterWithoutRedisStillLimits(t *testing.T) {
	l := newFallbackLimiter(3, time.Minute)
	now := time.Now()
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if !l.allow(ctx, "1.2.3.4", now) {
			t.Fatalf("request %d should have been allowed", i)
		}
	}
	if l.allow(ctx, "1.2.3.4", now) {
		t.Error("the 4th request in the window should have been rejected")
	}
}

func TestLimiterIsPerKey(t *testing.T) {
	l := newFallbackLimiter(1, time.Minute)
	now := time.Now()
	ctx := context.Background()

	if !l.allow(ctx, "1.1.1.1", now) {
		t.Fatal("first IP should be allowed")
	}
	if !l.allow(ctx, "2.2.2.2", now) {
		t.Error("a different IP must have its own window")
	}
	if l.allow(ctx, "1.1.1.1", now) {
		t.Error("the first IP is over its limit and should be rejected")
	}
}

func TestLimiterWindowResets(t *testing.T) {
	l := newFallbackLimiter(1, time.Minute)
	now := time.Now()
	ctx := context.Background()

	if !l.allow(ctx, "1.1.1.1", now) {
		t.Fatal("first request should be allowed")
	}
	if l.allow(ctx, "1.1.1.1", now.Add(30*time.Second)) {
		t.Error("still inside the window; should be rejected")
	}
	if !l.allow(ctx, "1.1.1.1", now.Add(90*time.Second)) {
		t.Error("the window has elapsed; the request should be allowed again")
	}
}

// A Redis client pointed at nothing must not fail closed and lock operators
// out — it degrades to the in-memory limiter.
func TestLimiterFallsBackWhenRedisIsUnreachable(t *testing.T) {
	l := newFallbackLimiter(2, time.Minute)
	l.rdb = unreachableRedis(t)
	now := time.Now()
	ctx := context.Background()

	if !l.allow(ctx, "9.9.9.9", now) || !l.allow(ctx, "9.9.9.9", now) {
		t.Fatal("requests within the limit should be allowed via the fallback")
	}
	if l.allow(ctx, "9.9.9.9", now) {
		t.Error("the fallback limiter should still enforce the limit")
	}
}

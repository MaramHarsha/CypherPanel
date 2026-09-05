package auth

import (
	"errors"
	"testing"
	"time"
)

func TestLimiterBlocksAfterMaxFailures(t *testing.T) {
	now := time.Now()
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	key := "1.2.3.4"
	for i := range 3 {
		if !l.Allow(key) {
			t.Fatalf("attempt %d should be allowed", i)
		}
		l.Fail(key)
	}
	if l.Allow(key) {
		t.Fatal("4th attempt should be blocked")
	}
}

func TestLimiterForgivesAfterWindow(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	key := "1.2.3.4"
	l.Fail(key)
	l.Fail(key)
	if l.Allow(key) {
		t.Fatal("should be blocked at the limit")
	}
	now = now.Add(2 * time.Minute) // window elapses
	if !l.Allow(key) {
		t.Fatal("should be forgiven after the window")
	}
}

func TestLimiterResetClearsHistory(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	key := "1.2.3.4"
	l.Fail(key)
	if l.Allow(key) {
		t.Fatal("should be blocked")
	}
	l.Reset(key)
	if !l.Allow(key) {
		t.Fatal("Reset should clear failures")
	}
}

// TestLimiterRetryAfterCountsDownToTheOldestFailure: the wait is exactly the
// time until the oldest in-window failure ages out, zero once below the limit.
func TestLimiterRetryAfterCountsDownToTheOldestFailure(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	key := "1.2.3.4"
	if got := l.RetryAfter(key); got != 0 {
		t.Fatalf("RetryAfter before any failure = %v, want 0", got)
	}
	l.Fail(key)
	now = now.Add(10 * time.Second)
	l.Fail(key)
	if got := l.RetryAfter(key); got != 50*time.Second {
		t.Fatalf("RetryAfter at the limit = %v, want 50s (oldest failure + window)", got)
	}
	now = now.Add(30 * time.Second)
	if got := l.RetryAfter(key); got != 20*time.Second {
		t.Fatalf("RetryAfter 30s later = %v, want 20s", got)
	}
	now = now.Add(25 * time.Second) // the oldest failure has aged out
	if got := l.RetryAfter(key); got != 0 || !l.Allow(key) {
		t.Fatalf("RetryAfter after the oldest failure aged out = %v, Allow = %v; want 0, true", got, l.Allow(key))
	}
}

// A nil limiter never throttles, so unrelated paths need no guard.
func TestNilLimiterNeverThrottles(t *testing.T) {
	var l *Limiter
	l.Fail("k")
	if !l.Allow("k") || l.RetryAfter("k") != 0 {
		t.Fatal("a nil limiter throttled")
	}
	l.Reset("k")
}

// TestRateLimitedErrorRoundsUpAndMatchesTheSentinel: Retry-After is whole
// seconds, rounded up and never zero; errors.Is still finds ErrRateLimited.
func TestRateLimitedErrorRoundsUpAndMatchesTheSentinel(t *testing.T) {
	for _, tc := range []struct {
		wait time.Duration
		want int
	}{
		{0, 1}, {200 * time.Millisecond, 1}, {time.Second, 1}, {1500 * time.Millisecond, 2}, {14*time.Minute + 59*time.Second + time.Millisecond, 900},
	} {
		e := &RateLimitedError{RetryAfter: tc.wait}
		if got := e.RetryAfterSeconds(); got != tc.want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", tc.wait, got, tc.want)
		}
		if !errors.Is(e, ErrRateLimited) {
			t.Errorf("errors.Is(%v, ErrRateLimited) = false", e)
		}
	}
}

// Refuse is what lets a caller record a throttled attempt DURABLY without
// letting an anonymous caller drive one write per packet: it is true once per
// throttle episode, and true again only after the key has been allowed back.
func TestRefuseIsTrueOncePerThrottleEpisode(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	key := "1.2.3.4"

	if l.Refuse(key) {
		t.Fatal("Refuse reported an episode while the key was still allowed")
	}

	l.Fail(key)
	l.Fail(key)
	if l.Allow(key) {
		t.Fatal("the key should be blocked at the limit")
	}
	if !l.Refuse(key) {
		t.Fatal("the first refusal of an episode must report the transition")
	}
	for i := range 5 {
		if l.Refuse(key) {
			t.Fatalf("refusal %d reported a second transition inside one episode", i+2)
		}
	}

	// The window elapses: the key is allowed again, so the NEXT throttle is a
	// new event and worth recording again.
	now = now.Add(2 * time.Minute)
	if !l.Allow(key) {
		t.Fatal("the key should be forgiven after the window")
	}
	l.Fail(key)
	l.Fail(key)
	if l.Allow(key) {
		t.Fatal("the key should be blocked again")
	}
	if !l.Refuse(key) {
		t.Fatal("a second throttle episode was not reported")
	}

	// A successful sign-in clears everything, including the episode marker.
	l.Reset(key)
	l.Fail(key)
	l.Fail(key)
	if !l.Refuse(key) {
		t.Fatal("an episode after a Reset was not reported")
	}
	var nilLimiter *Limiter
	if nilLimiter.Refuse(key) {
		t.Fatal("a nil limiter reported a refusal")
	}
}

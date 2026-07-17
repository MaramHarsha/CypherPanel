package auth

import (
	"sync"
	"time"
)

// Limiter is an in-memory sliding-window counter used to throttle failed
// logins per client key (threat-model §5.8). Single control-plane node (v1,
// vision.md), so in-memory state is sufficient; a restart simply forgives
// prior failures, which is acceptable for brute-force defense at this scale.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	now      func() time.Time
	attempts map[string][]time.Time
}

// NewLimiter allows up to max failures per key within window before Allow
// returns false.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:      max,
		window:   window,
		now:      time.Now,
		attempts: make(map[string][]time.Time),
	}
}

// Allow reports whether another attempt is permitted for key right now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) < l.max
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.prune(key), l.now())
}

// Reset clears the failure history for key, called after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// prune drops entries older than the window and returns the survivors. Callers
// hold the lock. It also garbage-collects emptied keys to bound memory.
func (l *Limiter) prune(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, key)
		return nil
	}
	l.attempts[key] = kept
	return kept
}

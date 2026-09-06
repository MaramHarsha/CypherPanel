package auth

import (
	"errors"
	"sync"
	"time"
)

// Limiter is an in-memory sliding-window counter used to throttle failed
// logins per key (threat-model §5.8). Single control-plane node (v1,
// vision.md), so in-memory state is sufficient; a restart simply forgives
// prior failures, which is acceptable for brute-force defense at this scale.
//
// Two limiters guard sign-in (control-plane-hardening.md §5): one keyed by
// client address, one keyed by account. A nil *Limiter never throttles, so a
// caller that has no limiter — unit tests of unrelated paths — needs no guard.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	now      func() time.Time
	attempts map[string][]time.Time
	// refused marks keys whose current throttle episode has already been
	// reported, so a caller that records the refusal durably (the audit log)
	// writes one row per episode rather than one per packet. Cleared by prune
	// the moment the key drops back below max, and by Reset.
	refused map[string]bool
}

// NewLimiter allows up to max failures per key within window before Allow
// returns false.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:      max,
		window:   window,
		now:      time.Now,
		attempts: make(map[string][]time.Time),
		refused:  make(map[string]bool),
	}
}

// Allow reports whether another attempt is permitted for key right now.
func (l *Limiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) < l.max
}

// RetryAfter is how long until key is allowed again: the time until the
// oldest failure inside the window ages out. Zero when key is not throttled.
// It is what a 429 carries as Retry-After, so a client can count down rather
// than guess (canvas 13t).
func (l *Limiter) RetryAfter(key string) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.prune(key)
	if len(kept) < l.max {
		return 0
	}
	// prune keeps the slice in insertion order, so the first survivor is the
	// oldest; once it leaves the window the count drops below max.
	return kept[0].Add(l.window).Sub(l.now())
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.prune(key), l.now())
}

// Reset clears the failure history for key, called after a successful login.
func (l *Limiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
	delete(l.refused, key)
}

// Refuse records that an attempt was turned away because key is throttled, and
// reports whether this is the FIRST refusal of the current episode.
//
// It exists so that turning a refusal into a durable record — an audit row —
// costs one write per throttle episode rather than one per request. Without
// it, an unauthenticated caller could drive unbounded inserts at their own
// request rate, which is precisely the work the throttle exists to bound
// (control-plane-hardening.md §5, audit-log.md §9).
func (l *Limiter) Refuse(key string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.prune(key)) < l.max {
		// Not throttled: nothing was turned away, so there is no episode.
		// Self-guarding rather than trusting the caller to have asked Allow
		// first, so the method means the same thing wherever it is called.
		return false
	}
	if l.refused[key] {
		return false
	}
	l.refused[key] = true
	return true
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
	if len(kept) < l.max {
		// Below the threshold the episode is over: the next time this key is
		// throttled it is a new event and worth recording again.
		delete(l.refused, key)
	}
	if len(kept) == 0 {
		delete(l.attempts, key)
		return nil
	}
	l.attempts[key] = kept
	return kept
}

// RateLimitedError is the throttled answer, carrying how long the caller must
// wait. It matches errors.Is(err, ErrRateLimited), so existing callers that
// only map the status keep working; handlers use errors.As to read the delay.
type RateLimitedError struct {
	RetryAfter time.Duration
	// FirstRefusal is true only for the refusal that opened this throttle
	// episode. A caller that records refusals durably uses it to write once
	// per episode; a caller that only answers 429 ignores it.
	FirstRefusal bool
}

func (e *RateLimitedError) Error() string { return ErrRateLimited.Error() }

// Is lets errors.Is(err, ErrRateLimited) hold.
func (e *RateLimitedError) Is(target error) bool { return errors.Is(target, ErrRateLimited) }

// RetryAfterSeconds is the header/body value for a 429: the delay rounded up
// to whole seconds, never below one, so "0" is never promised for a throttle
// that is still in force.
func (e *RateLimitedError) RetryAfterSeconds() int {
	secs := int((e.RetryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}

// throttle answers whether every key is allowed; when one is not it returns a
// RateLimitedError carrying the longest wait among the blocked keys.
func throttle(limiters []*Limiter, keys []string) error {
	var wait time.Duration
	blocked, first := false, false
	for i, l := range limiters {
		if l.Allow(keys[i]) {
			continue
		}
		blocked = true
		// Asked of every blocked limiter, not just the first: an episode that
		// begins on the account key is as much a new event as one that begins
		// on the address key.
		if l.Refuse(keys[i]) {
			first = true
		}
		if d := l.RetryAfter(keys[i]); d > wait {
			wait = d
		}
	}
	if !blocked {
		return nil
	}
	return &RateLimitedError{RetryAfter: wait, FirstRefusal: first}
}

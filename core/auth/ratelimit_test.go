package auth

import (
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

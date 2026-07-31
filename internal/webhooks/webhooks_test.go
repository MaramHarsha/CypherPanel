package webhooks

import (
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	secret := []byte("super-secret-key")
	body := []byte(`{"subject":"events.account.created"}`)
	ts := time.Unix(1_700_000_000, 0)

	sig := Sign(secret, body, ts)
	if !Verify(secret, body, ts, sig) {
		t.Fatal("a signature must verify against the material that produced it")
	}
}

// The timestamp is part of the signed material specifically so a captured
// request cannot be replayed with a fresh timestamp header.
func TestSignatureIsBoundToTimestamp(t *testing.T) {
	secret := []byte("super-secret-key")
	body := []byte(`{"a":1}`)
	ts := time.Unix(1_700_000_000, 0)

	sig := Sign(secret, body, ts)
	if Verify(secret, body, ts.Add(time.Second), sig) {
		t.Error("signature must not verify at a different timestamp")
	}
}

func TestSignatureIsBoundToBodyAndSecret(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	sig := Sign([]byte("key-a"), []byte(`{"a":1}`), ts)

	if Verify([]byte("key-b"), []byte(`{"a":1}`), ts, sig) {
		t.Error("signature must not verify under a different secret")
	}
	if Verify([]byte("key-a"), []byte(`{"a":2}`), ts, sig) {
		t.Error("signature must not verify for a different body")
	}
}

func TestWants(t *testing.T) {
	tests := []struct {
		name       string
		subscribed []string
		subject    string
		want       bool
	}{
		{"empty list means every event", nil, "events.account.created", true},
		{"empty slice means every event", []string{}, "events.package.deleted", true},
		{"exact match", []string{"events.account.created"}, "events.account.created", true},
		{"no match", []string{"events.account.created"}, "events.account.suspended", false},
		{"one of several", []string{"events.a.b", "events.account.suspended"}, "events.account.suspended", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wants(tt.subscribed, tt.subject); got != tt.want {
				t.Errorf("Wants(%v, %q) = %v, want %v", tt.subscribed, tt.subject, got, tt.want)
			}
		})
	}
}

// Backoff must grow (so a dead endpoint is not hammered) and stay bounded (so
// a recovered endpoint is retried within a useful window).
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		d := Backoff(attempt)
		if d < prev {
			t.Errorf("backoff decreased at attempt %d: %v after %v", attempt, d, prev)
		}
		if d > 2*time.Hour {
			t.Errorf("backoff at attempt %d exceeded the cap: %v", attempt, d)
		}
		prev = d
	}
	if Backoff(0) != Backoff(1) {
		t.Error("a non-positive attempt should be treated as the first attempt")
	}
	if Backoff(100) != 2*time.Hour {
		t.Errorf("backoff should saturate at the cap, got %v", Backoff(100))
	}
}

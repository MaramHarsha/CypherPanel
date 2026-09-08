package rest

import (
	"testing"
	"time"
)

// The audit page offers "last hour / 24 hours / 7 days / 30 days" and sends
// 1h/24h/168h/720h. The handler took RFC 3339 only, so every choice was a 400
// and an empty screen. Both forms now, like the log streams.
func TestAuditSinceTakesADurationOrAnInstant(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	got, err := parseSince("24h", now)
	if err != nil || !got.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("24h: %v, %v", got, err)
	}
	got, err = parseSince("2026-09-01T00:00:00Z", now)
	if err != nil || got.Year() != 2026 || got.Day() != 1 {
		t.Fatalf("instant: %v, %v", got, err)
	}
	if got, err := parseSince("", now); err != nil || !got.IsZero() {
		t.Fatalf("empty: %v, %v", got, err)
	}
	for _, bad := range []string{"yesterday", "-1h", "0s"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

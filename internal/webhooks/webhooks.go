// Package webhooks delivers CypherPanel domain events to operator-registered
// HTTP endpoints, signed with HMAC-SHA256 (plan.md §15).
//
// Delivery is deliberately not modelled as JetStream redelivery. One event
// fans out to many endpoints; NAKing the event message would re-deliver to
// endpoints that already succeeded. Instead a durable consumer records one
// delivery row per (webhook, event) and acks immediately, and a worker owns
// retry, dead-lettering, and manual redelivery from those rows.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Signature headers. These are part of the public integration contract — a
// receiver verifies against them — so they must not be renamed once shipped.
const (
	HeaderSignature = "X-CypherPanel-Signature"
	HeaderEvent     = "X-CypherPanel-Event"
	HeaderDelivery  = "X-CypherPanel-Delivery"
	HeaderTimestamp = "X-CypherPanel-Timestamp"
)

// MaxAttempts is how many times a delivery is tried before it is dead-lettered.
// Past this point an endpoint is considered down rather than flaky, and the
// operator redelivers manually once they have fixed it.
const MaxAttempts = 6

// Sign returns the hex HMAC-SHA256 of timestamp + "." + body, prefixed with the
// algorithm.
//
// The timestamp is inside the signed material so a captured request cannot be
// replayed later with a fresh timestamp header — the receiver rejects
// signatures whose timestamp is outside its tolerance window.
func Sign(secret, body []byte, ts time.Time) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", ts.Unix())
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is a valid signature for body at ts. It exists so
// SDKs and the docs share exactly one implementation of the check, and uses a
// constant-time compare so a receiver cannot be walked toward a valid
// signature by timing.
func Verify(secret, body []byte, ts time.Time, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, body, ts)), []byte(sig))
}

// Backoff returns how long to wait before attempt n (1-based). Exponential
// with a ceiling: a flapping endpoint is retried quickly at first, then
// backed off so a dead endpoint does not pin a worker.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Clamp the exponent before shifting: 1<<63 overflows and would wrap a
	// long-dead endpoint's backoff back around to zero, turning the retry
	// loop into a hot spin.
	const maxShift = 12
	shift := attempt - 1
	if shift > maxShift {
		shift = maxShift
	}
	d := time.Duration(1<<uint(shift)) * 30 * time.Second
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

const maxBackoff = 2 * time.Hour

// Wants reports whether a subscription list wants the given subject. An empty
// list means "every event" — the common case for a billing or CRM integration
// that consumes the whole feed.
func Wants(subscribed []string, subject string) bool {
	if len(subscribed) == 0 {
		return true
	}
	for _, s := range subscribed {
		if s == subject {
			return true
		}
	}
	return false
}

package engine

import (
	"strconv"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
)

// A container label is DATA ON A CONTAINER, not a value this panel wrote and
// can trust. Parsing it with Atoi and narrowing the result is a silent
// truncation: on a 64-bit host "4294967297" narrows to 1 and collides with a
// real replica, which would make the reconciler treat somebody else's container
// as the app's first replica.
//
// This is the fix asserted rather than assumed (code scanning
// go/incorrect-integer-conversion).
func TestAReplicaIndexLabelCannotTruncateIntoARealIndex(t *testing.T) {
	cases := []struct {
		label string
		want  uint32
		why   string
	}{
		{"", 1, "absent means index 1 — every container that existed before replicas did"},
		{"1", 1, "the ordinary case"},
		{"3", 3, "the ordinary case"},
		{"0", 1, "zero is not an index"},
		{"-1", 1, "negative is not an index"},
		{"21", 1, "above the ceiling is not a replica this panel created"},
		// The one that matters: 2^32 + 1. A narrowing parse would make this 1.
		{"4294967297", 1, "must not truncate into index 1"},
		{"18446744073709551615", 1, "must not wrap"},
		{"nonsense", 1, "unparseable reads like an unrecognised label"},
	}
	for _, c := range cases {
		got := replicaIndex(map[string]string{driver.LabelReplicaIndex: c.label})
		if got != c.want {
			t.Errorf("replicaIndex(%q) = %d, want %d — %s", c.label, got, c.want, c.why)
		}
	}
}

// Every value the parser CAN return is one the ceiling admits, so no caller has
// to re-check it.
func TestEveryAcceptedIndexIsWithinTheCeiling(t *testing.T) {
	for n := 1; n <= driver.MaxReplicaIndex; n++ {
		got := replicaIndex(map[string]string{driver.LabelReplicaIndex: strconv.Itoa(n)})
		if got != uint32(n) {
			t.Fatalf("replicaIndex(%d) = %d; a legal index must round-trip", n, got)
		}
	}
	if got := replicaIndex(map[string]string{driver.LabelReplicaIndex: strconv.Itoa(driver.MaxReplicaIndex + 1)}); got != 1 {
		t.Errorf("one past the ceiling = %d, want 1", got)
	}
}

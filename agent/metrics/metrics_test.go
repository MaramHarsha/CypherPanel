package metrics

import (
	"testing"
	"time"
)

// The cardinality bound is the part of this feature that can destroy a
// database, so it is the part with the most tests.
func TestPathNormalisation(t *testing.T) {
	cases := []struct{ in, want string }{
		// The query string goes first and unconditionally: this is where
		// secrets end up in a URL.
		{"/reset?token=hunter2seekrit", "/reset"},
		{"/api/auth?api_key=abc&next=/x", "/api/auth"},
		// Id-shaped segments become :id, which is what keeps one row per route
		// instead of one per contact.
		{"/api/contacts/8f3ac1d0/notes", "/api/contacts/:id"},
		{"/users/12345", "/users/:id"},
		{"/orders/f47ac10b-58cc-4372-a567-0e02b2c3d479", "/orders/:id"},
		// Depth is truncated to three segments.
		{"/a/b/c/d/e/f", "/a/b/c"},
		// A slug is a real route and belongs in the table — "anything long" is
		// deliberately NOT the rule.
		{"/blog/how-we-scaled-postgres", "/blog/how-we-scaled-postgres"},
		{"/", "/"},
		{"", "/"},
	}
	for _, c := range cases {
		if got := NormalisePath(c.in); got != c.want {
			t.Errorf("NormalisePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A path this heuristic gets wrong, asserted so nobody later thinks it is
// exact. The spec says so in words; this says so in code.
func TestNormalisationIsAHeuristicAndSaysSo(t *testing.T) {
	if got := NormalisePath("/v1/2024/report"); got != "/v1/:id/report" {
		t.Errorf("got %q; the year IS rewritten, and that is the stated cost of having no route declaration", got)
	}
}

// Redirects must not be counted as visits: a routed HTTPS application has two
// routers, and counting both would double every visit and drag p95 toward the
// 1 ms redirect.
func TestARedirectIsNotAVisit(t *testing.T) {
	line, ok := ParseAccessLine([]byte(`{"RouterName":"app_1-http@file","RequestPath":"/","DownstreamStatus":308,"Duration":900000,"DownstreamContentSize":0}`))
	if !ok {
		t.Fatal("the line did not parse")
	}
	if !line.Redirect || line.ResourceID != "app_1" {
		t.Fatalf("got %+v; want a redirect attributed to app_1", line)
	}

	b := newBucket(time.Now(), 300)
	b.AddRequest("application", "app_1", 308, 0.9, 0, "/", true, 1)
	b.AddRequest("application", "app_1", 200, 42, 1024, "/", false, 1)

	c := b.requests["app_1"]
	if c.requests != 1 {
		t.Errorf("requests = %d, want 1 — the redirect must not be counted as a visit", c.requests)
	}
	if c.redirects != 1 {
		t.Errorf("redirects = %d, want 1", c.redirects)
	}
	var histTotal uint64
	for _, n := range c.latency {
		histTotal += n
	}
	if histTotal != 1 {
		t.Errorf("the histogram holds %d entries; the redirect must be excluded from it", histTotal)
	}
	if c.paths["/"].requests != 1 {
		t.Errorf("the path table counted the redirect")
	}
}

// A request that matched no router is COUNTED, not dropped: "traffic is
// arriving at this node and hitting nothing" is a question worth answering.
func TestAnUnroutedRequestIsAttributedToTheServer(t *testing.T) {
	line, ok := ParseAccessLine([]byte(`{"RouterName":"","RequestPath":"/wp-admin","DownstreamStatus":404,"Duration":1000000}`))
	if !ok {
		t.Fatal("the line did not parse")
	}
	if !line.Unrouted {
		t.Fatal("a line with no router must be marked unrouted")
	}
}

// The hard cap: past PathCap distinct paths, everything folds into one row
// rather than creating an entry per contact id.
func TestThePathCapFoldsEverythingElseIntoOneRow(t *testing.T) {
	b := newBucket(time.Now(), 300)
	for i := 0; i < PathCap+50; i++ {
		b.AddRequest("application", "app_1", 200, 5, 100, "/p"+string(rune('a'+i%26))+itoa(i), false, 1)
	}
	c := b.requests["app_1"]
	if len(c.paths) > PathCap+1 {
		t.Fatalf("the map holds %d paths; the cap is %d plus the (other) row", len(c.paths), PathCap)
	}
	if !c.pathCapHit {
		t.Error("the cap was reached but not recorded")
	}
	if c.paths[OtherPath] == nil {
		t.Error("there is no (other) row for the paths past the cap")
	}
}

// The rows must always reconcile with the request count for the same window —
// that is what makes the top-paths table trustworthy rather than suggestive.
func TestTopPathsReconcileWithTheRequestCount(t *testing.T) {
	b := newBucket(time.Now(), 300)
	for i := 0; i < 40; i++ {
		for n := 0; n <= i; n++ {
			b.AddRequest("application", "app_1", 200, 5, 10, "/route"+itoa(i), false, 1)
		}
	}
	c := b.requests["app_1"]
	rows := topPaths(c)
	if len(rows) != TopPaths+1 {
		t.Fatalf("got %d rows, want %d plus (other)", len(rows), TopPaths)
	}
	var sum uint64
	for _, r := range rows {
		sum += r.requests
	}
	if sum != c.requests {
		t.Errorf("the rows sum to %d but the bucket counted %d requests", sum, c.requests)
	}
}

// Latency slots are the histogram's whole contract.
func TestLatencySlots(t *testing.T) {
	cases := []struct {
		ms   float64
		want int
	}{
		{0.5, 0}, {1, 0}, {1.5, 1}, {100, 6}, {60000, 14}, {90000, 15},
	}
	for _, c := range cases {
		if got := latencySlot(c.ms); got != c.want {
			t.Errorf("latencySlot(%v) = %d, want %d", c.ms, got, c.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

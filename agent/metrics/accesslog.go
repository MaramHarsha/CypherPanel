package metrics

// Reading the Proxy's access log (metrics-and-usage.md §4.3).
//
// Traefik already knows which resource served each request: the fragment
// writer names each router after the resource id, so attribution is read
// straight off RouterName and there is no Host-header-to-resource lookup table
// to build, keep in sync, or get wrong.
//
// EVERY LINE IS DISCARDED AFTER COUNTING. Access lines never reach logs.*,
// never reach the plane, and never reach an operator's log pane. What is kept
// per line is the router, the status, the duration, the response size and the
// normalised path; the client address, user agent, referrer, cookies, headers
// and the entire query string are dropped at the Proxy by the keep-list in its
// static config — so they are never serialised on the node either, not merely
// ignored here.

import (
	"encoding/json"
	"strings"
)

// accessLine is the keep-list, and it is the whole struct on purpose: a field
// that is not here cannot be counted, logged or forwarded by accident.
type accessLine struct {
	RouterName       string `json:"RouterName"`
	RequestPath      string `json:"RequestPath"`
	RequestMethod    string `json:"RequestMethod"`
	DownstreamStatus int    `json:"DownstreamStatus"`
	// Traefik reports duration in nanoseconds.
	Duration              int64 `json:"Duration"`
	DownstreamContentSize int64 `json:"DownstreamContentSize"`
}

// ParsedLine is what the collector folds into a bucket.
type ParsedLine struct {
	ResourceID string
	Redirect   bool
	Status     int
	DurationMs float64
	Bytes      uint64
	Path       string
	// Unrouted is a request that matched no router — a scanner, a wrong Host
	// header, a domain whose fragment was removed. Counted against the SERVER
	// rather than dropped, because "traffic is arriving at this node and
	// hitting nothing" is a question worth being able to answer.
	Unrouted bool
}

// ParseAccessLine reads one JSON access-log line. A line that will not parse is
// reported as not-ok and dropped: one malformed line must not cost the bucket.
func ParseAccessLine(raw []byte) (ParsedLine, bool) {
	var l accessLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return ParsedLine{}, false
	}
	if l.DownstreamStatus == 0 {
		return ParsedLine{}, false
	}
	out := ParsedLine{
		Status:     l.DownstreamStatus,
		DurationMs: float64(l.Duration) / 1e6,
		Path:       NormalisePath(l.RequestPath),
	}
	if l.DownstreamContentSize > 0 {
		out.Bytes = uint64(l.DownstreamContentSize)
	}

	router := l.RouterName
	// Traefik suffixes the provider: "app_abc123@file".
	if i := strings.IndexByte(router, '@'); i >= 0 {
		router = router[:i]
	}
	switch {
	case router == "":
		out.Unrouted = true
	case strings.HasSuffix(router, "-http"):
		// The redirect sibling of an HTTPS route. Counted as a redirect on the
		// same resource and excluded from the histogram and the path table.
		out.ResourceID = strings.TrimSuffix(router, "-http")
		out.Redirect = true
	default:
		out.ResourceID = router
	}
	return out, true
}

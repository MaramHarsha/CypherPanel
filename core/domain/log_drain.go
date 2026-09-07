package domain

import "time"

// Log drains (log-drains.md). The panel's outbox for log lines, and the design
// is three refusals: it must never block or slow a deploy, it must never grow
// unbounded, and it must never become a second log store on the plane.

// Drain kinds.
const (
	DrainLoki   = "loki"
	DrainSyslog = "syslog"
	DrainS3     = "s3"
)

// Drain health, DERIVED and never stored — the same pattern outbound webhooks
// use for endpoint health, and a small separate vocabulary rather than the
// workload words, because a drain is not a workload.
const (
	DrainShipping = "shipping"
	DrainIdle     = "idle"
	DrainRetrying = "retrying"
	DrainFailing  = "failing"
	DrainDisabled = "disabled"
)

// LogDrain is one destination for runtime log lines.
type LogDrain struct {
	ID   string
	Name string
	Kind string
	// ProjectID empty means every project.
	ProjectID string
	// TargetID names an existing Backup Target, for kind=s3 only.
	TargetID    string
	ConfigCT    []byte
	ConfigNonce []byte
	Enabled     bool
	// Observed, written by the shipper.
	LastShippedAt *time.Time
	LastError     string
	LastErrorAt   *time.Time
	DroppedLines  int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// DrainHealth derives the state shown in the panel.
//
// A FAILING DRAIN IS NEVER AUTO-DISABLED, which is why `disabled` is only ever
// the operator's own choice: auto-disabling turns a visible failure into a
// silently stopped pipeline that does not resume when the sink returns, and the
// operator finds out when they go looking for last week's logs.
func DrainHealth(d LogDrain, now time.Time) string {
	if !d.Enabled {
		return DrainDisabled
	}
	if d.LastErrorAt != nil {
		if now.Sub(*d.LastErrorAt) >= 5*time.Minute {
			return DrainFailing
		}
		return DrainRetrying
	}
	if d.LastShippedAt != nil && now.Sub(*d.LastShippedAt) < 5*time.Minute {
		return DrainShipping
	}
	return DrainIdle
}

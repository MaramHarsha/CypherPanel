package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Threshold alerts (threshold-alerts.md).
//
// THE SENTENCE IS THE DATA MODEL. There is no name column: the rule's name is
// rendered from the row, so the list, the modal, the Discord message and the
// API all say the same words, and no label typed in March can drift from what
// the rule now does.

// Alert signals. Four of them are the words app-scaling's autoscale rule uses,
// deliberately — two screens offering `p95 latency` and `p95_latency_ms` for one
// number is a vocabulary bug.
const (
	SignalCPU               = "cpu"
	SignalMemory            = "memory"
	SignalDisk              = "disk"
	SignalP95LatencyMs      = "p95_latency_ms"
	SignalRequestsPerSecond = "requests_per_second"
)

// Threshold units. The row says what its own numbers mean.
const (
	UnitPercent      = "percent"
	UnitBytes        = "bytes"
	UnitMilliseconds = "milliseconds"
	UnitPerSecond    = "per_second"
)

// Rule states. All four are visible in the API and the list, INCLUDING the two
// that deliver nothing: a rule quiet for a bad reason must not look like one
// quiet for a good reason.
const (
	AlertOK       = "ok"
	AlertFiring   = "firing"
	AlertNoData   = "no_data"
	AlertFlapping = "flapping"
)

// Alert target kinds.
const (
	AlertTargetServer      = "server"
	AlertTargetApplication = "application"
)

// AlertRule is one sentence: "tell <notifier> when <target>'s <signal> is above
// <threshold> for <window>".
type AlertRule struct {
	ID              string
	TargetKind      string
	TargetID        string
	Signal          string
	Threshold       float64
	ThresholdUnit   string
	WindowSeconds   int
	NotifierID      string
	Enabled         bool
	State           string
	StateSince      time.Time
	RearmUntil      *time.Time
	QuietNotifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AlertEvent is one episode, not one evaluation.
type AlertEvent struct {
	ID         string
	RuleID     string
	StartedAt  time.Time
	ResolvedAt *time.Time
	PeakValue  float64
	// Delivered is false when the flap guard held it. The episode still
	// happened and is still counted; it just was not sent.
	Delivered bool
}

// SignalOnServer reports whether a signal is meaningful for a server.
//
// p95 and requests/second are refused, and the refusal is substantive rather
// than an omission. A server does have request buckets — the unmatched-router
// rows that keep "traffic is arriving here and hitting nothing" answerable — but
// those are scanners and wrong Host headers. "p95 on srv-frankfurt-1" would
// silently mean "p95 of the requests that matched nothing", and a signal whose
// meaning needs a footnote is the wrong signal.
func SignalOnServer(signal string) bool {
	switch signal {
	case SignalCPU, SignalMemory, SignalDisk:
		return true
	default:
		return false
	}
}

// SignalOnApplication reports whether a signal is meaningful for an application.
func SignalOnApplication(signal string) bool {
	switch signal {
	case SignalCPU, SignalMemory, SignalDisk, SignalP95LatencyMs, SignalRequestsPerSecond:
		return true
	default:
		return false
	}
}

// AlertSentence renders the rule as the words it is. Everything that displays a
// rule calls this, so there is exactly one wording.
func AlertSentence(r AlertRule, targetName, notifierName string) string {
	if targetName == "" {
		targetName = r.TargetID
	}
	if notifierName == "" {
		notifierName = "the notifier"
	}
	return fmt.Sprintf("Tell %s when %s's %s is above %s for %s",
		notifierName, targetName, signalWords(r), formatThreshold(r), humanWindow(r.WindowSeconds))
}

func signalWords(r AlertRule) string {
	switch r.Signal {
	case SignalCPU:
		if r.TargetKind == AlertTargetServer {
			return "CPU (of all its cores)"
		}
		return "CPU (of one core)"
	case SignalMemory:
		if r.TargetKind == AlertTargetServer {
			return "memory (of host RAM)"
		}
		if r.ThresholdUnit == UnitPercent {
			return "memory (of its limit)"
		}
		return "memory"
	case SignalDisk:
		if r.TargetKind == AlertTargetServer {
			return "disk (of the Docker data root)"
		}
		return "disk"
	case SignalP95LatencyMs:
		return "p95 latency"
	case SignalRequestsPerSecond:
		return "request rate"
	}
	return r.Signal
}

func formatThreshold(r AlertRule) string {
	switch r.ThresholdUnit {
	case UnitPercent:
		return trimFloat(r.Threshold) + "%"
	case UnitMilliseconds:
		return trimFloat(r.Threshold) + " ms"
	case UnitPerSecond:
		return trimFloat(r.Threshold) + "/s"
	case UnitBytes:
		return humanBytes(r.Threshold)
	}
	return trimFloat(r.Threshold)
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return s
}

func humanBytes(f float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0") + " " + units[i]
}

func humanWindow(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d seconds", seconds)
	case seconds < 3600:
		m := seconds / 60
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	default:
		h := seconds / 3600
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
}

// RearmDelay is derived, not configured: the right cooldown is a function of
// the window the operator already chose, and asking twice is asking them to be
// consistent with themselves.
func RearmDelay(windowSeconds int) time.Duration {
	d := 2 * time.Duration(windowSeconds) * time.Second
	if d < 15*time.Minute {
		return 15 * time.Minute
	}
	return d
}

package upgrade

// The pre-flight (panel-updates.md §4): five checks that change nothing, which
// is why the route that runs them is a GET.
//
// Each check answers one question an operator would otherwise answer with a
// shell and their own nerve, and each failure names BOTH numbers rather than
// saying "not enough". A disk that fills DURING an upgrade is how the reference
// platforms produce a panel that is neither the old version nor the new one.

import (
	"fmt"
	"time"
)

// CheckStatus is one line of the pre-flight.
const (
	CheckOK      = "ok"
	CheckWarn    = "warn"
	CheckRefused = "refused"
)

// Check is one pre-flight line, rendered as written.
type Check struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Text   string `json:"text"`
	// Remedy is what to do about it, present only when there is something to
	// do. A refusal with no remedy is a dead end (ui-principles §1).
	Remedy string `json:"remedy,omitempty"`
}

// Preflight is the whole answer.
type Preflight struct {
	Version string  `json:"version"`
	Mode    string  `json:"mode"`
	Checks  []Check `json:"checks"`
	// CanProceed is false when any check refused. A warning never blocks.
	CanProceed bool `json:"can_proceed"`
	// NeedsTypedConfirm is true when proceeding would orphan agents below the
	// release's floor — close enough to irreversible to earn a typed confirm,
	// and it stays ONE dialog because confirmations never stack.
	NeedsTypedConfirm  bool     `json:"needs_typed_confirm"`
	IncompatibleAgents []string `json:"incompatible_agents"`
	// RunningWork is the courtesy count, not a correctness requirement: builds
	// and restores execute on AGENTS, work items are persisted before they are
	// published, and the heartbeat-stale window is 90s against a restart of a
	// few seconds. What waiting buys is ATTRIBUTION — a deploy that fails for
	// an unrelated reason during an upgrade is blamed on the upgrade forever.
	RunningWork int       `json:"running_work"`
	CheckedAt   time.Time `json:"checked_at"`
}

// PreflightInput is everything the checks need, gathered by the caller so this
// stays pure enough to test.
type PreflightInput struct {
	Mode          string
	FromVersion   string
	Release       VerifiedRelease
	VerifyErr     error
	DatabaseBytes int64
	FreeBytes     int64
	MinFreeBytes  int64
	Agents        []AgentVersion
	RunningWork   int
	SnapshotTool  string
	SnapshotErr   error
	Now           time.Time
}

// AgentVersion is one server's observed agent build.
type AgentVersion struct {
	Name    string
	Version string
	Online  bool
}

// RunPreflight computes the five checks. It performs no I/O: the caller has
// already done the fetching, and keeping that split is what makes every branch
// here reachable from a test.
func RunPreflight(in PreflightInput) Preflight {
	out := Preflight{
		Version: in.Release.Manifest.Version, Mode: in.Mode,
		CanProceed: true, RunningWork: in.RunningWork, CheckedAt: in.Now,
	}
	if out.Version == "" {
		out.Version = "unknown"
	}

	// 1. Signature. A failure here is a REFUSAL, never a warning: an
	// unverifiable artifact is the one thing this feature exists to not
	// install.
	if in.VerifyErr != nil {
		out.add(Check{Key: "signature", Status: CheckRefused,
			Text:   "The release could not be verified against the offline release key.",
			Remedy: "Upgrade by hand only if you can verify the artifact yourself. " + in.VerifyErr.Error()})
		out.CanProceed = false
	} else {
		out.add(Check{Key: "signature", Status: CheckOK,
			Text: "Signature verified against the offline release key."})
	}

	// 2. Disk. ×2 rather than ×1 because a restore must be able to exist beside
	// the thing it replaces.
	required := in.DatabaseBytes*2 + in.MinFreeBytes
	switch {
	case in.FreeBytes <= 0:
		out.add(Check{Key: "disk", Status: CheckWarn,
			Text: "Free space could not be read, so the snapshot's headroom is unknown."})
	case in.FreeBytes < required:
		out.add(Check{Key: "disk", Status: CheckRefused,
			Text:   fmt.Sprintf("%s free, and the snapshot needs %s.", humanBytes(in.FreeBytes), humanBytes(required)),
			Remedy: "Free space on the panel's data directory first. A disk that fills during an upgrade leaves a panel that is neither the old version nor the new one."})
		out.CanProceed = false
	default:
		out.add(Check{Key: "disk", Status: CheckOK,
			Text: fmt.Sprintf("Disk headroom %s — the snapshot fits.", humanBytes(in.FreeBytes))})
	}

	// 3. Agents. An agent that is merely OFFLINE neither blocks nor counts: a
	// refusal an operator judges irrelevant is a refusal they stop reading.
	floor := in.Release.Manifest.AgentMinVersion
	total, ok := 0, 0
	for _, a := range in.Agents {
		if !a.Online {
			continue
		}
		total++
		if floor == "" || !olderThan(a.Version, floor) {
			ok++
			continue
		}
		out.IncompatibleAgents = append(out.IncompatibleAgents, a.Name)
	}
	switch {
	case len(out.IncompatibleAgents) > 0:
		out.add(Check{Key: "agents", Status: CheckRefused,
			Text:   fmt.Sprintf("%d of %d agents are below %s: %s.", len(out.IncompatibleAgents), total, floor, joinNames(out.IncompatibleAgents)),
			Remedy: "Update those agents first, or type the target version to proceed anyway — an orphaned agent is a server the panel can no longer manage."})
		out.CanProceed = false
		out.NeedsTypedConfirm = true
	case total == 0:
		out.add(Check{Key: "agents", Status: CheckOK, Text: "No agents are currently reporting, so none can be orphaned."})
	default:
		out.add(Check{Key: "agents", Status: CheckOK,
			Text: fmt.Sprintf("%d/%d agents compatible (≥ %s).", ok, total, floor)})
	}

	// 4. Snapshot readiness — not the snapshot itself, which is taken at swap
	// time, but proof that it CAN be taken.
	if in.SnapshotErr != nil {
		out.add(Check{Key: "snapshot", Status: CheckRefused,
			Text:   "No usable pg_dump could be found, so no fallback snapshot can be taken.",
			Remedy: "Install postgresql-client on the panel host, or set CYPHERD_SNAPSHOT_PGDUMP. " + in.SnapshotErr.Error()})
		out.CanProceed = false
	} else {
		text := "Fallback snapshot ready."
		if in.FromVersion != "" {
			text = "Fallback snapshot of " + in.FromVersion + " ready."
		}
		out.add(Check{Key: "snapshot", Status: CheckOK, Text: text})
	}

	// 5. Quiescence. A courtesy, and the distinction is the architecture paying
	// off rather than a hedge.
	if in.RunningWork > 0 {
		out.add(Check{Key: "quiescence", Status: CheckWarn,
			Text:   fmt.Sprintf("%s running — the upgrade waits until it finishes.", plural(in.RunningWork, "deploy", "deploys")),
			Remedy: "Your applications keep serving either way; waiting only stops an unrelated failure being blamed on the upgrade."})
	} else {
		out.add(Check{Key: "quiescence", Status: CheckOK, Text: "Nothing is running."})
	}

	// The manual install cannot be helped, and says so rather than pretending.
	if in.Mode == ModeManual {
		out.CanProceed = false
		out.add(Check{Key: "mode", Status: CheckWarn,
			Text:   "This panel runs as a container, so it cannot upgrade itself.",
			Remedy: "The checks above still apply. Pull the new image and recreate the container; nothing else changes."})
	}
	return out
}

func (p *Preflight) add(c Check) { p.Checks = append(p.Checks, c) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
			out = n
		case i == len(names)-1:
			out += " and " + n
		default:
			out += ", " + n
		}
	}
	return out
}

func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// Package upgrade is the panel's guided self-upgrade (panel-updates.md).
//
// THE PLANE NEVER PERFORMS THE SWAP, and that sentence stays literally true.
// The swap is done by a separate, root, one-shot systemd unit started when a
// request file appears in a handoff directory; the plane's entire power is to
// ASK. Three independent reasons, any one sufficient:
//
//   - IT CANNOT WORK AS INSTALLED. cypherd.service runs with DynamicUser=true
//     and ProtectSystem=strict, so /usr is read-only to that process and it is
//     not root. Relaxing that converts any RCE in the API surface into
//     persistence on the control-plane host — precisely the blast radius
//     ADR-010 §3 refuses to hand a compromised plane over the fleet, and it
//     would be strange to refuse it there and grant it here.
//   - A PROCESS CANNOT HEALTH-GATE ITS OWN REPLACEMENT. The thing deciding
//     whether the new build is alive is the thing being replaced; if the new
//     binary crashes on boot, nothing is left to notice.
//   - ADR-010 SAYS THE PLANE NEVER UPDATES ITSELF, and that should stay a
//     sentence rather than a slogan the implementation steps around.
//
// What this grants, stated rather than discovered: a compromised plane can now
// write a request file and cause its host to install a genuine,
// signature-verified CypherPanel release as root. What bounds it is the same
// bound ADR-010 §3 gives the fleet — the helper verifies against the release
// public key baked into its own binary and installs nothing else, so the choice
// available to an attacker is WHICH AUTHENTIC RELEASE, not WHAT CODE.
package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phases, in order. Each is written to status.json by the helper and mirrored
// into panel_upgrades by the plane whenever it is up.
const (
	PhasePreflight      = "preflight"
	PhaseWaitingQuiesce = "waiting_for_quiesce"
	PhaseSnapshotting   = "snapshotting"
	PhaseDownloading    = "downloading"
	PhaseVerifying      = "verifying"
	PhaseMigrating      = "migrating"
	PhaseRestarting     = "restarting"
	PhaseHealthGate     = "health_gate"
	PhaseSucceeded      = "succeeded"
	PhaseRolledBack     = "rolled_back"
	PhaseFailed         = "failed"
)

// Terminal reports whether a phase is an end state.
func Terminal(phase string) bool {
	switch phase {
	case PhaseSucceeded, PhaseRolledBack, PhaseFailed:
		return true
	}
	return false
}

// Modes. `assisted` is a systemd install where the helper exists; `manual` is
// the compose install, which cannot be helped and SAYS SO rather than
// pretending to a capability it does not have.
const (
	ModeAssisted = "assisted"
	ModeManual   = "manual"
)

// Request is what the plane writes and the helper consumes. It carries an
// actor, a nonce and an expiry, and it is deliberately NOT desired state: a
// `desired_panel_version` the host converges on is an auto-install with extra
// steps, which is the shape ADR-010 forbids. A one-shot request that a person
// created is the correct shape for a decision that is deliberately not
// automatic.
//
// NOTHING SENSITIVE IS EVER WRITTEN HERE. The plane names a version and an
// intent; the helper fetches its own credentials from the root-owned env file
// it is allowed to read and forbidden to write.
type Request struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	// Rollback marks a deliberate downgrade. The helper refuses a version below
	// the running one without it AND without the target appearing in the host's
	// own slot history, so "walk the panel back to a release with a known CVE"
	// is not available for a version this host never ran.
	Rollback bool `json:"rollback"`
	// SnapshotRetentionDays is the one decision the operator makes and that
	// must not be made for them. Zero means keep forever.
	SnapshotRetentionDays int `json:"snapshot_retention_days"`
	// RestoreSnapshot names a snapshot to put back, for the last-resort path.
	RestoreSnapshot string    `json:"restore_snapshot,omitempty"`
	Actor           string    `json:"actor"`
	Nonce           string    `json:"nonce"`
	ExpiresAt       time.Time `json:"expires_at"`
	RequestedAt     time.Time `json:"requested_at"`
}

// Expired reports whether the request may no longer be acted on. A stale
// request file left by a crash must not run an upgrade an hour later.
func (r Request) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}

// Status is what the helper writes and the plane reads. It is the only channel
// back: the helper is a root process with no dependency on the session that
// asked, which is what makes "safe to leave" true by construction rather than
// by hope.
type Status struct {
	RequestID string `json:"request_id"`
	Version   string `json:"version"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	// Step and Steps make progress honest rather than a spinner: "migrating
	// database · 3 of 4 migrations" is a fact, and a bar that moves on a timer
	// is not (ui-principles §3).
	Step         int       `json:"step,omitempty"`
	Steps        int       `json:"steps,omitempty"`
	SnapshotPath string    `json:"snapshot_path,omitempty"`
	SnapshotSize int64     `json:"snapshot_size,omitempty"`
	FromVersion  string    `json:"from_version,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Paths of the handoff directory. Group-writable by cypherpanel-upgrade, which
// the service unit joins with SupplementaryGroups — so it works regardless of
// what UID DynamicUser picked this boot.
const (
	RequestFile = "request.json"
	StatusFile  = "status.json"
	SlotsDir    = "slots"
)

// Dir is the handoff directory, shared between the plane and the helper.
type Dir string

func (d Dir) request() string { return filepath.Join(string(d), RequestFile) }
func (d Dir) status() string  { return filepath.Join(string(d), StatusFile) }

// Slots is where the staged and previous binaries live, on the same filesystem
// as the target so the swap is a rename.
func (d Dir) Slots() string { return filepath.Join(string(d), SlotsDir) }

// WriteRequest places the request atomically at 0640: readable by the helper's
// group, writable by nobody else. A partially written request must never be
// consumable, hence the rename.
func (d Dir) WriteRequest(r Request) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("upgrade: marshaling request: %w", err)
	}
	tmp := d.request() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o640); err != nil {
		return fmt.Errorf("upgrade: writing request: %w", err)
	}
	if err := os.Rename(tmp, d.request()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("upgrade: placing request: %w", err)
	}
	return nil
}

// ReadRequest consumes the request. The helper unlinks it so a restart of the
// path unit cannot run the same upgrade twice.
func (d Dir) ReadRequest() (Request, bool, error) {
	body, err := os.ReadFile(d.request())
	if os.IsNotExist(err) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, fmt.Errorf("upgrade: reading request: %w", err)
	}
	var r Request
	if err := json.Unmarshal(body, &r); err != nil {
		return Request{}, false, fmt.Errorf("upgrade: parsing request: %w", err)
	}
	return r, true, nil
}

func (d Dir) ConsumeRequest() error { return os.Remove(d.request()) }

// WriteStatus is 0644: the plane reads it, and it carries nothing secret.
func (d Dir) WriteStatus(s Status) error {
	s.UpdatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("upgrade: marshaling status: %w", err)
	}
	tmp := d.status() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("upgrade: writing status: %w", err)
	}
	if err := os.Rename(tmp, d.status()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("upgrade: placing status: %w", err)
	}
	return nil
}

func (d Dir) ReadStatus() (Status, bool, error) {
	body, err := os.ReadFile(d.status())
	if os.IsNotExist(err) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("upgrade: reading status: %w", err)
	}
	var s Status
	if err := json.Unmarshal(body, &s); err != nil {
		return Status{}, false, fmt.Errorf("upgrade: parsing status: %w", err)
	}
	return s, true, nil
}

// Available reports whether the helper is installed on this host. A directory
// the plane can write is the observable proof; absent it, the panel is in
// `manual` mode and hands over the two commands instead of drawing a button.
func (d Dir) Available() bool {
	if d == "" {
		return false
	}
	info, err := os.Stat(string(d))
	if err != nil || !info.IsDir() {
		return false
	}
	// Writability is what actually matters: a directory the plane cannot write
	// is a helper it cannot ask.
	probe := filepath.Join(string(d), ".probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

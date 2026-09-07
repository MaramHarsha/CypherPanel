// Package localjoin enrolls the panel's OWN host as a server without a command
// to paste (docs/features/local-server.md).
//
// THE PLANE NEVER INSTALLS THE AGENT, and that sentence stays literally true.
// cypherd.service runs with DynamicUser=true, ProtectSystem=strict and
// NoNewPrivileges=true, so writing /usr/local/bin/cypher-agent, dropping a unit
// into /etc/systemd/system and calling systemctl are all forbidden to it —
// deliberately, because relaxing that turns any RCE in the API surface into
// persistence on the control-plane host. The plane's entire power is to write a
// REQUEST FILE; a root one-shot does the work.
//
// That is the same handoff panel-updates.md §3 established for the upgrade
// helper, reused rather than reinvented: same group, same directory, same
// atomic write and status read-back. One trust boundary to audit instead of two.
//
// ADR-002 is not bent. The plane reaches out to nothing: the request never
// leaves the host, names no address, opens no connection and holds no remote
// credential. The agent still dials home to enroll exactly as a pasted
// command's agent does. What changes is who types the command.
//
// AND IT CAN ONLY EVER BE THIS HOST. Request has no field naming a machine and
// must never gain one — the helper runs on the box it is already on. That
// absence is what stops a later edit from turning this into a remote-execution
// primitive.
package localjoin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// States the panel reports before it offers the button. Each is NAMED rather
// than discovered by a failure (local-server.md §5).
const (
	// StateAvailable: the handoff directory exists and is writable.
	StateAvailable = "available"
	// StateHelperMissing: no handoff directory. Every panel installed before
	// this feature is in this state, so it must read as a version gap and not
	// as a bug.
	StateHelperMissing = "helper_missing"
	// StateUnsupported: a container install, where there is no host systemd to
	// install into — the `manual` mode panel updates already reports.
	StateUnsupported = "unsupported"
	// StateAlreadyJoined: this host already runs an enrolled agent.
	StateAlreadyJoined = "already_joined"
)

// Phases the helper writes as it works. Real phases from a real process, so the
// dialog shows a fact rather than a spinner on a timer (ui-principles §3).
const (
	PhaseInstalling = "installing"
	PhaseEnrolling  = "enrolling"
	PhaseSucceeded  = "succeeded"
	PhaseFailed     = "failed"
)

// Terminal reports whether a phase is an end state.
func Terminal(phase string) bool { return phase == PhaseSucceeded || phase == PhaseFailed }

// requestTTL is two minutes. This is a one-shot a person just clicked; a stale
// request left by a crash must not enroll an agent an hour later.
const requestTTL = 2 * time.Minute

// Files in the handoff directory, beside the upgrade helper's own.
const (
	RequestFile = "localjoin.json"
	StatusFile  = "localjoin-status.json"
)

// AgentIdentityPath is where the agent records which Server it IS. Reading it
// is how the panel knows this host is already joined — the honest check, where
// matching hostnames against the fleet would be a guess (local-server.md §6).
const AgentIdentityPath = "/var/lib/cypher-agent/identity.json"

// Request is what the plane writes and the helper consumes.
//
// It carries the same variables the pasted command carries and nothing more.
// There is deliberately NO target field: see the package comment.
type Request struct {
	ID string `json:"id"`
	// Token is the ordinary single-use join token. Written 0640 into a
	// directory only root and the helper's group can read, which is a shorter
	// and better-guarded life than the same token has in a terminal's
	// scrollback.
	Token         string `json:"token"`
	EnrollAddr    string `json:"enroll_addr"`
	PlaneHTTP     string `json:"plane_http"`
	CAFingerprint string `json:"ca_fingerprint"`
	// AgentURL pins the binary when the panel is a release build, exactly as
	// installCommand does. Empty lets agent.sh reuse an installed binary or
	// fall back to the latest release — the plane serves no binary either way.
	AgentURL string `json:"agent_url,omitempty"`
	// ServerID is the row this enrollment belongs to, so the status can name it
	// and the dialog can link to it.
	ServerID    string    `json:"server_id"`
	Actor       string    `json:"actor"`
	Nonce       string    `json:"nonce"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Expired reports whether the request may no longer be acted on.
func (r Request) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}

// Status is what the helper writes and the plane reads. It is the only channel
// back: the helper is a root process with no dependency on the session that
// asked, which is what makes closing the tab safe by construction.
type Status struct {
	RequestID string    `json:"request_id"`
	ServerID  string    `json:"server_id,omitempty"`
	Phase     string    `json:"phase"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Dir is the handoff directory, shared with the upgrade helper.
type Dir string

func (d Dir) request() string { return filepath.Join(string(d), RequestFile) }
func (d Dir) status() string  { return filepath.Join(string(d), StatusFile) }

// NewRequest stamps the one-shot fields, so no caller has to remember the TTL.
func NewRequest(r Request, now time.Time, nonce string) Request {
	r.Nonce = nonce
	r.RequestedAt = now
	r.ExpiresAt = now.Add(requestTTL)
	return r
}

// WriteRequest places the request atomically at 0640: readable by the helper's
// group, writable by nobody else. A partially written request must never be
// consumable, hence the rename.
func (d Dir) WriteRequest(r Request) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("localjoin: marshaling request: %w", err)
	}
	tmp := d.request() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o640); err != nil { //nolint:gosec // 0640 is the point: the helper's group reads it
		return fmt.Errorf("localjoin: writing request: %w", err)
	}
	if err := os.Rename(tmp, d.request()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("localjoin: placing request: %w", err)
	}
	return nil
}

// ReadRequest consumes the request. It is READ AND DELETED in one step, before
// any work happens: a helper that crashed mid-install must not find the same
// request waiting and enroll a second agent.
func (d Dir) ReadRequest() (Request, error) {
	body, err := os.ReadFile(d.request()) //nolint:gosec // the handoff path from config
	if err != nil {
		return Request{}, err
	}
	if rmErr := os.Remove(d.request()); rmErr != nil {
		return Request{}, fmt.Errorf("localjoin: clearing the request: %w", rmErr)
	}
	var r Request
	if err := json.Unmarshal(body, &r); err != nil {
		return Request{}, fmt.Errorf("localjoin: parsing request: %w", err)
	}
	return r, nil
}

// WriteStatus records where the helper has got to.
func (d Dir) WriteStatus(s Status) error {
	s.UpdatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("localjoin: marshaling status: %w", err)
	}
	tmp := d.status() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil { //nolint:gosec // the plane reads this back; it carries no secret
		return fmt.Errorf("localjoin: writing status: %w", err)
	}
	if err := os.Rename(tmp, d.status()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("localjoin: placing status: %w", err)
	}
	return nil
}

// ReadStatus reports the helper's last written state. A missing file is not an
// error — it means nothing has been asked for yet.
func (d Dir) ReadStatus() (Status, bool, error) {
	body, err := os.ReadFile(d.status()) //nolint:gosec // the handoff path from config
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("localjoin: reading status: %w", err)
	}
	var s Status
	if err := json.Unmarshal(body, &s); err != nil {
		return Status{}, false, fmt.Errorf("localjoin: parsing status: %w", err)
	}
	return s, true, nil
}

// Writable reports whether the plane can actually place a request here. It
// WRITES rather than stats: the directory is group-writable through
// SupplementaryGroups, and whether this process is in that group is a question
// only an attempted write answers honestly.
func (d Dir) Writable() bool {
	if d == "" {
		return false
	}
	probe := filepath.Join(string(d), ".localjoin-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil { //nolint:gosec // removed immediately
		return false
	}
	_ = os.Remove(probe)
	return true
}

// LocalServerID reads the Server this host is already enrolled as, if any.
func LocalServerID(identityPath string) (string, bool) {
	body, err := os.ReadFile(identityPath) //nolint:gosec // a fixed path, not input
	if err != nil {
		return "", false
	}
	var id struct {
		ServerID string `json:"server_id"`
	}
	if err := json.Unmarshal(body, &id); err != nil || id.ServerID == "" {
		return "", false
	}
	return id.ServerID, true
}

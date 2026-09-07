// Package updater converges the agent's own binary onto the version desired
// state names (ADR-010, docs/features/agent-updates.md).
//
// It is a reconciler like any other: given desired state, converge and report.
// Its convergence happens to end with the process exiting. Converging twice
// equals converging once (ENGINEERING rule 13), and here that is load-bearing
// rather than a formality, because the second convergence is performed by a
// DIFFERENT BINARY: it reads the same desired state, finds its version equal,
// and does nothing.
//
// There is no "update now" verb, and there could not be one: ADR-005 has no
// imperative path and hard rule 3 forbids inventing one. Setting the channel is
// the button; the jitter and the quiescence gate are what make "now" mean "as
// soon as it is safe".
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/pkg/fetch"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Release layout: one prefix, three fixed names. A mirror mirrors the prefix
// (agent-updates.md §7).
const (
	manifestName  = "SHA256SUMS"
	signatureName = "SHA256SUMS.sig"
	// releaseBase derives an artifact base from a version when desired state
	// names none, which is every fleet using the project's own releases.
	releaseBase = "https://github.com/MaramHarsha/CypherPanel/releases/download/"
)

// Caps. The manifest and its signature are small by construction; the binary is
// bounded well above any plausible agent and well below a disk-filling body.
const (
	maxManifestBytes  = 256 << 10
	maxSignatureBytes = 4 << 10
	maxBinaryBytes    = 128 << 20
	preflightTimeout  = 20 * time.Second
)

// Defaults for the two knobs §9 exposes.
const (
	DefaultProbation = 120 * time.Second
	DefaultJitter    = 60 * time.Second
)

// Fetcher is the bounded HTTP getter the updater pulls artifacts with
// (consumer-defined; *fetch.Client satisfies it). It is an interface so a test
// can serve a manifest without a network, never so a caller can substitute a
// softer one.
type Fetcher interface {
	Get(ctx context.Context, url string, maxBytes int64) ([]byte, error)
	Stream(ctx context.Context, url string, maxBytes int64) (io.ReadCloser, error)
}

// Config wires the updater. Everything it needs to know about the host is
// passed in, so the whole reconciler is testable against a temp dir.
type Config struct {
	// Version is this binary's build stamp — the running half of the
	// comparison.
	Version string
	// BinaryPath is the file to replace. Empty asks the OS.
	BinaryPath string
	// StateDir holds the boot marker only. The swap never touches it.
	StateDir string
	// GOARCH names the release artifact. Empty takes runtime.GOARCH.
	GOARCH string
	// Disabled is the host-local opt-out (CYPHER_UPDATE_DISABLE) for an agent
	// managed by a package manager or baked into an immutable image.
	Disabled bool
	// Quiet reports whether the host has gone quiet — no work item in flight.
	// A restart mid-build throws away ten minutes of CPU; a restart mid-restore
	// interrupts a database that is already offline. Nil means always quiet.
	Quiet func() bool
	// Probation is how long a new binary has to dial home before it rolls
	// itself back. Jitter bounds the random delay before an update starts, so a
	// promotion does not restart forty agents at once.
	Probation time.Duration
	Jitter    time.Duration
	// Fetch pulls the artifacts. Nil builds the bounded default.
	Fetch Fetcher
	// Exit ends the process after a successful swap or rollback. Nil uses
	// os.Exit; a test supplies its own and asserts it was called.
	Exit func(code int)
	Log  *slog.Logger
}

// Updater is the agent's self-update reconciler. Construct with New.
type Updater struct {
	cfg    Config
	arch   string
	binary string

	mu sync.RWMutex
	// status is a plain struct rather than the proto message: a proto message
	// carries a mutex and cannot be copied, and every read here hands back a
	// copy so the caller cannot mutate what the heartbeat will publish.
	status state
	// busy guards against a second Apply entering while one is mid-download.
	// A sync nudge and the periodic sync both call Apply, and two downloads
	// racing onto one staged path is a corrupt binary with a valid digest.
	busy bool
	// dialed records that this process reached the bus, which is what clears
	// the boot marker. Kept so a second dial-home is a no-op.
	dialed bool
}

// state is the observed half, held in a shape that is safe to copy.
type state struct {
	phase    agentv1.AgentUpdateStatus_Phase
	target   string
	previous string
	detail   string
}

// New wires the updater and resolves what it can about the host. It never
// touches the network and never reads desired state.
func New(cfg Config) *Updater {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Probation <= 0 {
		cfg.Probation = DefaultProbation
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = DefaultJitter
	}
	if cfg.Exit == nil {
		cfg.Exit = os.Exit
	}
	if cfg.Fetch == nil {
		cfg.Fetch = fetch.New("cypher-agent/"+cfg.Version, fetch.DefaultTimeout)
	}
	arch := cfg.GOARCH
	if arch == "" {
		arch = runtime.GOARCH
	}
	binary := cfg.BinaryPath
	if binary == "" {
		if exe, err := os.Executable(); err == nil {
			binary = exe
		}
	}
	u := &Updater{cfg: cfg, arch: arch, binary: binary}
	u.setStatus(agentv1.AgentUpdateStatus_PHASE_IDLE, "", "")
	if reason := u.disabledReason(); reason != "" {
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_DISABLED, "", reason)
	}
	return u
}

// disabledReason names why this host is excluded from updates, or "" when it is
// not. All three reasons are visible rather than silent: a host that will never
// update should say so in the fleet table, not sit at IDLE forever looking like
// one that simply has nothing to do.
func (u *Updater) disabledReason() string {
	switch {
	case u.cfg.Disabled:
		return "CYPHER_UPDATE_DISABLE is set on this host"
	case u.binary == "":
		return "this agent cannot locate its own binary"
	case !writable(u.binary):
		return "this agent's binary is not writable; it is managed outside the panel"
	case len(trustedKeys()) == 0:
		// Not a stub and not a soft failure: with no baked-in key there is
		// nothing to verify against, and an updater that cannot verify does not
		// run. The release pipeline supplies the key (release-signing.md).
		return "this build trusts no release key, so it can verify no artifact"
	}
	return ""
}

// SetQuiet supplies the quiescence predicate after construction: the worker
// that answers it is built later than the updater, which has to exist before
// anything else in main so its probation timer is armed first.
func (u *Updater) SetQuiet(fn func() bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cfg.Quiet = fn
}

// quiet reads the predicate under the lock SetQuiet writes it under.
func (u *Updater) quiet() func() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.cfg.Quiet
}

// Status is the observed half, for the heartbeat.
func (u *Updater) Status() *agentv1.AgentUpdateStatus {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return &agentv1.AgentUpdateStatus{
		Phase:           u.status.phase,
		TargetVersion:   u.status.target,
		PreviousVersion: u.status.previous,
		Detail:          u.status.detail,
	}
}

// Degraded reports whether the last update rolled back. PHASE_ROLLED_BACK
// raises the agent's own AgentStatus to degraded, so the server goes amber in
// the ordinary status vocabulary rather than only in this feature's column.
func (u *Updater) Degraded() error {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.status.phase != agentv1.AgentUpdateStatus_PHASE_ROLLED_BACK {
		return nil
	}
	return fmt.Errorf("agent update rolled back: %s", u.status.detail)
}

func (u *Updater) setStatus(phase agentv1.AgentUpdateStatus_Phase, target, detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// PHASE_DISABLED is sticky against the ordinary phases: a host excluded
	// from updates does not flicker to PENDING because desired state named a
	// version.
	if u.status.phase == agentv1.AgentUpdateStatus_PHASE_DISABLED &&
		phase != agentv1.AgentUpdateStatus_PHASE_DISABLED {
		return
	}
	u.status.phase, u.status.target, u.status.detail = phase, target, detail
}

// Recover runs before anything else in main, and its ordering is the whole
// design: the probation timer is armed BEFORE identity is loaded and before any
// network I/O, so a binary that hangs on either is still rolled back.
//
// It returns immediately for a process that is not on probation, which is every
// ordinary start.
func (u *Updater) Recover(ctx context.Context) {
	if u.binary == "" || u.cfg.StateDir == "" {
		return
	}
	m, err := readMarker(u.cfg.StateDir)
	if err != nil {
		u.cfg.Log.Warn("reading the boot marker", "error", err)
		return
	}
	if m == nil {
		return
	}
	if m.Attempts >= maxAttempts {
		// This binary has already had its one attempt and is starting again,
		// which means it did not dial home last time. No network, no plane, no
		// decision.
		u.cfg.Log.Warn("rolling back the agent binary", "target", m.Target, "previous", m.Previous)
		u.rollNow(m, "the new binary did not reach the bus")
		return
	}
	m.Attempts++
	if err := writeMarker(u.cfg.StateDir, *m); err != nil {
		u.cfg.Log.Error("recording the update attempt", "error", err)
	}
	u.mu.Lock()
	u.status.previous = m.Previous
	u.mu.Unlock()
	go u.probation(ctx, *m)
}

// probation fires when the new binary has not dialled home in time. It is a
// timer rather than a check, because the failure it catches is a HANG: a
// process that never reaches its next decision point still reaches this one.
func (u *Updater) probation(ctx context.Context, m marker) {
	wait := time.Until(m.Probation)
	if wait <= 0 {
		wait = u.cfg.Probation
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
	}
	u.mu.RLock()
	dialed := u.dialed
	u.mu.RUnlock()
	if dialed {
		return
	}
	u.cfg.Log.Warn("agent update probation expired; rolling back", "target", m.Target)
	u.rollNow(&m, "the new binary did not reach the bus within its probation")
}

func (u *Updater) rollNow(m *marker, why string) {
	binary := m.BinaryPath
	if binary == "" {
		binary = u.binary
	}
	if err := rollback(binary); err != nil {
		// Nothing left to try that does not need the plane. Say so loudly and
		// keep running: this binary is at least started, which beats a rename
		// loop.
		u.cfg.Log.Error("rolling back the agent binary", "error", err)
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_FAILED, m.Target, "rollback failed: "+err.Error())
		return
	}
	// The marker is cleared before exiting so the OLD binary — which starts
	// next — is not itself put on probation for an update it never made.
	if err := clearMarker(u.cfg.StateDir); err != nil {
		u.cfg.Log.Error("clearing the boot marker", "error", err)
	}
	if err := writeRolledBack(u.cfg.StateDir, m.Target, why); err != nil {
		u.cfg.Log.Error("recording the rollback", "error", err)
	}
	u.cfg.Exit(0)
}

// DialedHome clears the boot marker. It is called when the agent has
// established the mTLS bus connection and published its first heartbeat —
// deliberately NOT when the desired-state sync answers. ADR-005 requires the
// plane to answer nothing rather than a partial set, so a plane briefly unable
// to assemble desired state would look, to every agent at once, exactly like a
// bad binary, and would roll back a perfectly good release fleet-wide.
func (u *Updater) DialedHome() {
	u.mu.Lock()
	if u.dialed {
		u.mu.Unlock()
		return
	}
	u.dialed = true
	u.mu.Unlock()
	if u.cfg.StateDir == "" {
		return
	}
	if err := clearMarker(u.cfg.StateDir); err != nil {
		u.cfg.Log.Error("clearing the boot marker", "error", err)
	}
}

// Apply converges toward the spec. Equal or empty is zero work, which is the
// common case forever.
func (u *Updater) Apply(ctx context.Context, spec *agentv1.AgentUpdateSpec) {
	if u.disabledReason() != "" {
		return
	}
	want := strings.TrimSpace(spec.GetVersion())
	if want == "" || want == u.cfg.Version {
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_IDLE, "", "")
		return
	}
	if isDowngrade(u.cfg.Version, want) && !spec.GetRollback() {
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_FAILED, want,
			"refusing to move backwards from "+u.cfg.Version+"; the panel must allow a rollback explicitly")
		return
	}

	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	u.setStatus(agentv1.AgentUpdateStatus_PHASE_PENDING, want, "waiting for the host to go quiet")
	if err := u.wait(ctx); err != nil {
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_IDLE, want, "")
		return
	}
	if err := u.update(ctx, want, spec.GetArtifactBase()); err != nil {
		// Nothing has been renamed on any of these paths, so nothing is at
		// risk: report, stay connected, and let the next sync retry.
		u.cfg.Log.Warn("agent update failed", "target", want, "error", err)
		u.setStatus(agentv1.AgentUpdateStatus_PHASE_FAILED, want, err.Error())
	}
}

// wait holds for the jitter and then until the host is quiet. Desired state has
// no deadline, so waiting costs nothing a redelivery would have to recover.
func (u *Updater) wait(ctx context.Context) error {
	delay := time.Duration(0)
	if u.cfg.Jitter > 0 {
		delay = time.Duration(rand.Int64N(int64(u.cfg.Jitter))) //nolint:gosec // scheduling jitter, not a secret
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	quiet := u.quiet()
	if quiet == nil {
		return nil
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if quiet() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (u *Updater) update(ctx context.Context, want, artifactBase string) error {
	base := strings.TrimSpace(artifactBase)
	if base == "" {
		base = releaseBase + want
	}
	base = strings.TrimSuffix(base, "/")

	u.setStatus(agentv1.AgentUpdateStatus_PHASE_VERIFYING, want, "verifying the release manifest")
	sums, err := u.cfg.Fetch.Get(ctx, base+"/"+manifestName, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", manifestName, err)
	}
	sig, err := u.cfg.Fetch.Get(ctx, base+"/"+signatureName, maxSignatureBytes)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", signatureName, err)
	}
	man, err := verifyManifest(sums, sig)
	if err != nil {
		return err
	}
	name := artifactName(u.arch)
	digest, listed := man[name]
	if !listed {
		return fmt.Errorf("%w: %s", ErrNotInManifest, name)
	}

	u.setStatus(agentv1.AgentUpdateStatus_PHASE_DOWNLOADING, want, "downloading "+name)
	body, err := u.cfg.Fetch.Stream(ctx, base+"/"+name, maxBinaryBytes)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	staged, err := stage(u.binary, body, digest)
	_ = body.Close()
	if err != nil {
		return err
	}

	u.setStatus(agentv1.AgentUpdateStatus_PHASE_VERIFYING, want, "pre-flighting the new binary")
	if err := preflight(staged, want, preflightTimeout); err != nil {
		_ = os.Remove(staged)
		return err
	}

	u.setStatus(agentv1.AgentUpdateStatus_PHASE_SWAPPING, want, "installing "+want)
	m := marker{
		Target:     want,
		Previous:   u.cfg.Version,
		Attempts:   0,
		Probation:  time.Now().Add(u.cfg.Probation),
		BinaryPath: u.binary,
	}
	// The marker is written BEFORE the swap: a crash between the two leaves a
	// marker for an update that did not happen, which the next start reads as
	// attempt one of a binary that is already the old one — harmless. The
	// reverse order would leave a new binary with no probation at all.
	if err := writeMarker(u.cfg.StateDir, m); err != nil {
		_ = os.Remove(staged)
		return err
	}
	if err := swap(u.binary, staged); err != nil {
		_ = clearMarker(u.cfg.StateDir)
		_ = os.Remove(staged)
		return err
	}
	u.cfg.Log.Info("agent binary replaced; restarting", "from", u.cfg.Version, "to", want)
	u.cfg.Exit(0)
	return nil
}

// rolledBackName records, for the binary that starts NEXT, that the version it
// replaced was rolled back and why. Without it the old binary comes back with
// no idea it is the survivor of a failed update, and the panel's amber row —
// the one thing an operator can act on — would never appear.
const rolledBackName = "update-rolled-back.json"

func writeRolledBack(stateDir, target, why string) error {
	if stateDir == "" {
		return nil
	}
	body := fmt.Sprintf("{%q:%q,%q:%q}", "target", target, "detail", why)
	return writeFileSynced(filepath.Join(stateDir, rolledBackName), []byte(body), 0o600)
}

// ReadRolledBack consumes the note a previous process left, if any, and folds
// it into this one's reported status. Consumed rather than read: the amber row
// describes the update that just failed, not every update that ever failed.
func (u *Updater) ReadRolledBack() {
	if u.cfg.StateDir == "" {
		return
	}
	p := filepath.Join(u.cfg.StateDir, rolledBackName)
	b, err := os.ReadFile(p) //nolint:gosec // agent-owned state dir
	if err != nil {
		return
	}
	_ = os.Remove(p)
	target, detail := parseRolledBack(string(b))
	u.mu.Lock()
	u.status.phase = agentv1.AgentUpdateStatus_PHASE_ROLLED_BACK
	u.status.target = target
	u.status.previous = u.cfg.Version
	u.status.detail = detail
	u.mu.Unlock()
}

func parseRolledBack(s string) (target, detail string) {
	var note struct {
		Target string `json:"target"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(s), &note); err != nil {
		return "", "an update was rolled back"
	}
	if note.Detail == "" {
		note.Detail = "an update was rolled back"
	}
	return note.Target, note.Detail
}

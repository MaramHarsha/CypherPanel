package upgrade

// The helper: the root, one-shot process that actually performs the swap
// (panel-updates.md §§5, 6). It runs as `cypherd upgrade`, so there stays one
// artifact to sign and ship.
//
// It is a root process on the host with NO dependency on the session that asked
// — which is what makes "safe to leave" true by construction rather than by
// hope. Closing the tab, losing the network or signing out changes nothing.
//
// Two things it deliberately never does, both because the reference platforms
// did them and destroyed installs: it opens the env file READ-ONLY and never
// writes anywhere under /etc (that file holds the master key, and destroying it
// is exactly what coolify#3687 did), and it never installs anything but a
// signature-verified release.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HelperOptions configures one run.
type HelperOptions struct {
	Dir         Dir
	BinaryPath  string // the installed cypherd, e.g. /usr/local/bin/cypherd
	Unit        string // the systemd unit, e.g. cypherd.service
	BaseURL     string // release asset URL with a %s for the tag
	ReadyURL    string // the plane's own /readyz
	FromVersion string
	SnapshotDir string
	DatabaseURL string
	Probation   time.Duration
	Fetcher     Fetcher
	Log         *slog.Logger
	Now         func() time.Time
	// Runner executes host commands. Injected so the whole helper is testable
	// without a systemd or a Postgres (ENGINEERING rule 9's discipline applied
	// to processes rather than to the clock).
	Runner Runner
	// Probe answers /readyz. Injected for the same reason.
	Probe func(ctx context.Context, url string) (int, error)
}

// Runner executes one host command.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Helper performs one upgrade.
type Helper struct {
	o HelperOptions
}

func NewHelper(o HelperOptions) *Helper {
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Probation <= 0 {
		o.Probation = 120 * time.Second
	}
	if o.Probe == nil {
		o.Probe = httpProbe
	}
	return &Helper{o: o}
}

func httpProbe(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// Run consumes one request and performs it. It always leaves a terminal status
// behind, because a helper that dies silently leaves the panel read-only until
// the lock's own expiry — which works, but tells the operator nothing.
func (h *Helper) Run(ctx context.Context) error {
	req, ok, err := h.o.Dir.ReadRequest()
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing to do; the path unit fired on something else
	}
	// Consume first: a request that is read twice is an upgrade that runs
	// twice, which is the pain-points row's own failure
	// (coolify#3687 ran the updater twice).
	if err := h.o.Dir.ConsumeRequest(); err != nil {
		return fmt.Errorf("upgrade: consuming the request: %w", err)
	}
	if req.Expired(h.o.Now()) {
		return h.fail(req, "This upgrade request expired before it could run. Start it again from the panel.")
	}

	if req.RestoreSnapshot != "" {
		return h.restoreOnly(ctx, req)
	}
	return h.upgrade(ctx, req)
}

func (h *Helper) status(req Request, phase, detail string, step, steps int) {
	_ = h.o.Dir.WriteStatus(Status{
		RequestID: req.ID, Version: req.Version, Phase: phase, Detail: detail,
		Step: step, Steps: steps, FromVersion: h.o.FromVersion,
	})
}

func (h *Helper) fail(req Request, detail string) error {
	h.status(req, PhaseFailed, detail, 0, 0)
	return errors.New(detail)
}

func (h *Helper) upgrade(ctx context.Context, req Request) error {
	log := h.o.Log

	// ── verify ────────────────────────────────────────────────────────────
	h.status(req, PhaseVerifying, "Checking the release signature.", 1, 6)
	rel, err := VerifyRelease(ctx, h.o.Fetcher, h.o.BaseURL, req.Version)
	if err != nil {
		return h.fail(req, "Refused: "+err.Error())
	}
	// A downgrade is bounded: it needs an explicit rollback intent AND the
	// target must appear in this host's own slot history, so "walk the panel
	// back to a release with a known CVE" is not available for a version this
	// host never ran.
	if Older(req.Version, h.o.FromVersion) {
		if !req.Rollback {
			return h.fail(req, fmt.Sprintf("Refused: %s is older than the running %s, and this request is not marked as a rollback.", req.Version, h.o.FromVersion))
		}
		if !h.ranBefore(req.Version) {
			return h.fail(req, fmt.Sprintf("Refused: this host has never run %s, so it cannot be rolled back to it.", req.Version))
		}
	}

	// ── snapshot ──────────────────────────────────────────────────────────
	// Taken AFTER the read-only lock is held, not at pre-flight time: a
	// snapshot taken when the operator opened the screen is stale by the time
	// they press the button, and everything written in between would be
	// silently lost by a restore that claims to rewind "to the moment of
	// upgrade".
	h.status(req, PhaseSnapshotting, "Saving a fallback snapshot of "+h.o.FromVersion+".", 2, 6)
	snapshot, size, err := h.snapshot(ctx)
	if err != nil {
		return h.fail(req, "Could not take the fallback snapshot, so nothing was changed: "+err.Error())
	}

	// ── download ──────────────────────────────────────────────────────────
	h.status(req, PhaseDownloading, "Downloading "+req.Version+".", 3, 6)
	staged, err := h.download(ctx, rel, req.Version)
	if err != nil {
		return h.fail(req, "Download failed, so nothing was changed: "+err.Error())
	}

	// One fork/exec that eliminates the whole class of artifact that cannot
	// reach main() at all — a corrupt binary that matched its digest because
	// the digest was corrupt too, a wrong-architecture build, a truncated file
	// the filesystem happened to pad.
	if out, err := h.o.Runner.Run(ctx, staged, "version"); err != nil || !strings.Contains(string(out), req.Version) {
		_ = os.Remove(staged)
		return h.fail(req, fmt.Sprintf("The downloaded binary does not report %s when run, so it was not installed.", req.Version))
	}

	// ── migrate ───────────────────────────────────────────────────────────
	// Its own step, with the NEW binary, before the service starts. A migration
	// failure becomes attributable ("migration 42 failed", not "the panel did
	// not come back"), and the count is honest progress rather than a spinner.
	h.status(req, PhaseMigrating, "Applying database migrations.", 4, 6)
	if out, err := h.o.Runner.Run(ctx, staged, "migrate"); err != nil {
		_ = os.Remove(staged)
		return h.fail(req, "Migration failed, so the new version was not installed: "+lastLine(string(out)))
	}

	// ── swap ──────────────────────────────────────────────────────────────
	h.status(req, PhaseRestarting, "Installing and restarting.", 5, 6)
	if err := h.swap(staged); err != nil {
		return h.fail(req, "Could not install the new binary: "+err.Error())
	}
	if out, err := h.o.Runner.Run(ctx, "systemctl", "restart", h.o.Unit); err != nil {
		log.Error("upgrade: restarting", "error", err, "output", string(out))
	}

	// ── gate ──────────────────────────────────────────────────────────────
	h.status(req, PhaseHealthGate, "Waiting for the new version to answer.", 6, 6)
	if err := h.gate(ctx); err != nil {
		return h.rollback(ctx, req, snapshot, err)
	}

	_ = h.o.Dir.WriteStatus(Status{
		RequestID: req.ID, Version: req.Version, Phase: PhaseSucceeded,
		Detail:       "Running " + req.Version + ".",
		SnapshotPath: snapshot, SnapshotSize: size, FromVersion: h.o.FromVersion,
		Step: 6, Steps: 6,
	})
	return nil
}

// gate is /readyz answering 200 AND the unit not having restarted during the
// window. BOTH, because a crash-looping service answers 200 in between crashes.
func (h *Helper) gate(ctx context.Context) error {
	deadline := h.o.Now().Add(h.o.Probation)
	before := h.restarts(ctx)
	var lastErr error
	for h.o.Now().Before(deadline) {
		code, err := h.o.Probe(ctx, h.o.ReadyURL)
		if err == nil && code == http.StatusOK {
			if after := h.restarts(ctx); after > before {
				lastErr = fmt.Errorf("the service restarted %d times during the health gate", after-before)
			} else {
				return nil
			}
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("/readyz answered %d", code)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("the new version did not become ready within the probation window")
	}
	return lastErr
}

func (h *Helper) restarts(ctx context.Context) int {
	out, err := h.o.Runner.Run(ctx, "systemctl", "show", "-p", "NRestarts", "--value", h.o.Unit)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// rollback is §6's ordered recovery, and the ORDER is the whole point: the
// cheap, non-destructive path is attempted first and the destructive one is a
// fallback, never the routine.
func (h *Helper) rollback(ctx context.Context, req Request, snapshot string, cause error) error {
	log := h.o.Log
	h.status(req, PhaseRestarting, "The new version did not become healthy. Putting the previous one back.", 0, 0)

	// 1. Rename the previous binary back. If the old build comes up, STOP HERE
	// — this is the common case, nothing is lost, and everything written on the
	// new version in the last minute is still there.
	prev := filepath.Join(h.o.Dir.Slots(), "cypherd.prev")
	if err := os.Rename(prev, h.o.BinaryPath); err != nil {
		log.Error("upgrade: restoring the previous binary", "error", err)
	}
	if out, err := h.o.Runner.Run(ctx, "systemctl", "restart", h.o.Unit); err != nil {
		log.Error("upgrade: restarting the previous version", "error", err, "output", string(out))
	}
	if err := h.gate(ctx); err == nil {
		_ = h.o.Dir.WriteStatus(Status{
			RequestID: req.ID, Version: h.o.FromVersion, Phase: PhaseRolledBack,
			Detail:       "The upgrade to " + req.Version + " did not pass its health check, so " + h.o.FromVersion + " was put back. Nothing was lost: " + cause.Error(),
			SnapshotPath: snapshot, FromVersion: h.o.FromVersion,
		})
		return nil
	}

	// 2. Only now — because the schema moved past what the old binary can read,
	// or a migration stopped half-way — restore the snapshot.
	log.Warn("upgrade: the previous binary could not start; restoring the snapshot")
	if err := h.restore(ctx, snapshot); err != nil {
		// 3. Stop. A helper that keeps trying things at this point is a helper
		// making an incident worse.
		_ = h.o.Dir.WriteStatus(Status{
			RequestID: req.ID, Version: h.o.FromVersion, Phase: PhaseFailed,
			Detail: "The upgrade failed, the previous version could not start, and restoring the snapshot also failed. " +
				"The snapshot is still at " + snapshot + " and nothing further was attempted. Original failure: " + cause.Error() +
				". Restore failure: " + err.Error(),
			SnapshotPath: snapshot, FromVersion: h.o.FromVersion,
		})
		return err
	}
	if out, err := h.o.Runner.Run(ctx, "systemctl", "restart", h.o.Unit); err != nil {
		log.Error("upgrade: restarting after the restore", "error", err, "output", string(out))
	}
	_ = h.o.Dir.WriteStatus(Status{
		RequestID: req.ID, Version: h.o.FromVersion, Phase: PhaseRolledBack,
		Detail: "The upgrade to " + req.Version + " failed and the previous version could not start on the new schema, " +
			"so the database was restored from the snapshot taken at the start. Anything written during the upgrade window is gone. " + cause.Error(),
		SnapshotPath: snapshot, FromVersion: h.o.FromVersion,
	})
	return nil
}

func (h *Helper) restoreOnly(ctx context.Context, req Request) error {
	h.status(req, PhaseSnapshotting, "Restoring the snapshot.", 1, 1)
	if err := h.restore(ctx, req.RestoreSnapshot); err != nil {
		return h.fail(req, "The restore failed and nothing was changed: "+err.Error())
	}
	if out, err := h.o.Runner.Run(ctx, "systemctl", "restart", h.o.Unit); err != nil {
		h.o.Log.Error("upgrade: restarting after the restore", "error", err, "output", string(out))
	}
	_ = h.o.Dir.WriteStatus(Status{
		RequestID: req.ID, Phase: PhaseSucceeded,
		Detail: "The database was restored from the snapshot.", FromVersion: h.o.FromVersion,
	})
	return nil
}

// download stages the release binary and holds it against the SIGNED digest —
// not against a checksum file that whoever replaced the binary could also have
// replaced.
func (h *Helper) download(ctx context.Context, rel VerifiedRelease, version string) (string, error) {
	name := AssetName(runtime.GOARCH)
	want, ok := rel.Digests[name]
	if !ok {
		return "", fmt.Errorf("the signed manifest does not cover %s", name)
	}
	base := strings.TrimSuffix(fmt.Sprintf(h.o.BaseURL, version), "/")
	body, err := h.o.Fetcher.Fetch(ctx, base+"/"+name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("the downloaded binary does not match its signed digest")
	}

	// Staged on the SAME FILESYSTEM as the target so the swap is a rename
	// rather than a copy — a copy can be interrupted half-way and leave a
	// binary that is neither version.
	if err := os.MkdirAll(h.o.Dir.Slots(), 0o770); err != nil {
		return "", err
	}
	staged := filepath.Join(h.o.Dir.Slots(), "cypherd.new")
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return staged, nil
}

// swap is the two-slot rename, the same shape the agents use.
func (h *Helper) swap(staged string) error {
	prev := filepath.Join(h.o.Dir.Slots(), "cypherd.prev")
	if err := os.Rename(h.o.BinaryPath, prev); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staged, h.o.BinaryPath); err != nil {
		// Put the old one back immediately: a moment with no binary at all is
		// the one state from which nothing can recover.
		_ = os.Rename(prev, h.o.BinaryPath)
		return err
	}
	return nil
}

// ranBefore reports whether this host has a record of running a version, which
// is what bounds a downgrade to releases this install actually had.
func (h *Helper) ranBefore(version string) bool {
	entries, err := os.ReadDir(h.o.Dir.Slots())
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), version) {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

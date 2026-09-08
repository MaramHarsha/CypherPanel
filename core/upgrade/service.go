package upgrade

// The plane's half of a guided upgrade (panel-updates.md §§4, 6, 7): the lock,
// the request, and mirroring the helper's status back into Postgres so history
// survives the restart the upgrade itself causes.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence a guided upgrade needs (consumer-defined).
type Store interface {
	CreatePanelUpgrade(ctx context.Context, u domain.PanelUpgrade) (domain.PanelUpgrade, error)
	GetActivePanelUpgrade(ctx context.Context) (domain.PanelUpgrade, error)
	SetPanelUpgradePhase(ctx context.Context, id, phase, detail string) error
	AttachPanelUpgradeSnapshot(ctx context.Context, id, snapshotID string) error
	ExpireStalePanelUpgrades(ctx context.Context, now time.Time) error
	ListPanelUpgrades(ctx context.Context, limit int) ([]domain.PanelUpgrade, error)
	CreatePanelSnapshot(ctx context.Context, sn domain.PanelSnapshot) (domain.PanelSnapshot, error)
	GetPanelSnapshot(ctx context.Context, id string) (domain.PanelSnapshot, error)
	ListPanelSnapshots(ctx context.Context) ([]domain.PanelSnapshot, error)
	SetPanelSnapshotRetention(ctx context.Context, id string, expiresAt *time.Time, pinned bool) (domain.PanelSnapshot, error)
	DeletePanelSnapshot(ctx context.Context, id string) error
	ListExpiredPanelSnapshots(ctx context.Context, now time.Time) ([]domain.PanelSnapshot, error)
	// The pre-flight's inputs.
	DatabaseSizeBytes(ctx context.Context) (int64, error)
	ListServers(ctx context.Context) ([]domain.Server, error)
	CountRunningWork(ctx context.Context) (int, error)
}

// LockTTL bounds the read-only window. A helper that dies between phases must
// not leave the panel read-only forever, so the lock expires and any request
// finding an expired one clears it.
const LockTTL = 30 * time.Minute

// ErrActive is returned when an upgrade already holds the lock.
var ErrActive = errors.New("upgrade: an upgrade is already running")

// ErrUnavailable is returned when this install cannot upgrade itself.
var ErrUnavailable = errors.New("upgrade: this panel runs as a container and cannot upgrade itself")

// Options wires the service.
type Options struct {
	Store          Store
	Dir            Dir
	SnapshotDir    string
	BaseURL        string
	Fetcher        Fetcher
	CurrentVersion string
	MinFreeBytes   int64
	Log            *slog.Logger
	Now            func() time.Time
	// FreeBytes reports free space on the snapshot directory's filesystem.
	FreeBytes func(path string) (int64, error)
}

// Service is the plane side. It never performs a swap; its entire power is to
// write a request file and to read what the helper wrote back.
type Service struct {
	o Options
}

func NewService(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Mode reports whether this install can be helped. A panel that pretends to a
// capability it does not have is worse than one that hands over cleanly.
func (s *Service) Mode() string {
	if s.o.Dir.Available() {
		return ModeAssisted
	}
	return ModeManual
}

// Locked reports whether a guided upgrade currently holds the read-only lock.
// It clears an expired one on the way past, which is what stops a dead helper
// from freezing the panel.
func (s *Service) Locked(ctx context.Context) bool {
	now := s.o.Now()
	if err := s.o.Store.ExpireStalePanelUpgrades(ctx, now); err != nil {
		s.o.Log.Debug("upgrade: expiring stale locks", "error", err)
	}
	u, err := s.o.Store.GetActivePanelUpgrade(ctx)
	if err != nil {
		return false
	}
	return u.Active(now)
}

// Active returns the running upgrade, if any.
func (s *Service) Active(ctx context.Context) (domain.PanelUpgrade, bool) {
	u, err := s.o.Store.GetActivePanelUpgrade(ctx)
	if err != nil {
		return domain.PanelUpgrade{}, false
	}
	return u, true
}

// Preflight runs the five checks. It changes nothing, which is why the route
// that calls it is a GET.
func (s *Service) Preflight(ctx context.Context, version string) (Preflight, error) {
	in := PreflightInput{
		Mode: s.Mode(), FromVersion: s.o.CurrentVersion,
		MinFreeBytes: s.o.MinFreeBytes, Now: s.o.Now(),
	}
	rel, verr := VerifyRelease(ctx, s.o.Fetcher, s.o.BaseURL, version)
	in.Release, in.VerifyErr = rel, verr
	if verr != nil {
		// The requested version still names the answer, so the screen has a
		// heading even when nothing verified.
		in.Release.Manifest.Version = version
	}

	if n, err := s.o.Store.DatabaseSizeBytes(ctx); err == nil {
		in.DatabaseBytes = n
	}
	if s.o.FreeBytes != nil {
		if n, err := s.o.FreeBytes(s.o.SnapshotDir); err == nil {
			in.FreeBytes = n
		}
	}
	servers, err := s.o.Store.ListServers(ctx)
	if err == nil {
		for _, srv := range servers {
			in.Agents = append(in.Agents, AgentVersion{
				Name: srv.Name, Version: srv.AgentVersion,
				Online: srv.Status != domain.StatusUnknown && srv.Enrolled(),
			})
		}
	}
	if n, err := s.o.Store.CountRunningWork(ctx); err == nil {
		in.RunningWork = n
	}
	if _, _, err := ResolvePgDump(os.Getenv("CYPHERD_DATABASE_URL")); err != nil {
		in.SnapshotErr = err
	}
	return RunPreflight(in), nil
}

// Start takes the lock and asks the helper. It is the whole of the plane's
// power over its own version.
func (s *Service) Start(ctx context.Context, version, actor string, retentionDays int, rollback bool) (domain.PanelUpgrade, error) {
	if s.Mode() != ModeAssisted {
		return domain.PanelUpgrade{}, ErrUnavailable
	}
	now := s.o.Now()
	if err := s.o.Store.ExpireStalePanelUpgrades(ctx, now); err != nil {
		s.o.Log.Debug("upgrade: expiring stale locks", "error", err)
	}
	// The partial unique index makes this a database invariant; the read is
	// only here so the refusal names the running upgrade rather than a
	// constraint.
	if existing, ok := s.Active(ctx); ok && existing.Active(now) {
		return existing, ErrActive
	}

	u := domain.PanelUpgrade{
		ID: ids.New(ids.PrefixPanelUpgrade), FromVersion: s.o.CurrentVersion,
		ToVersion: version, Actor: actor, Rollback: rollback,
		ExpiresAt: now.Add(LockTTL),
	}
	saved, err := s.o.Store.CreatePanelUpgrade(ctx, u)
	if err != nil {
		return domain.PanelUpgrade{}, ErrActive
	}

	req := Request{
		ID: saved.ID, Version: version, Rollback: rollback,
		SnapshotRetentionDays: retentionDays,
		Actor:                 actor, Nonce: ids.New("non"),
		RequestedAt: now, ExpiresAt: now.Add(LockTTL),
	}
	if err := s.o.Dir.WriteRequest(req); err != nil {
		// The lock is released immediately: a request that was never written is
		// an upgrade that will never run, and holding the panel read-only for
		// it would be a self-inflicted outage.
		_ = s.o.Store.SetPanelUpgradePhase(ctx, saved.ID, PhaseFailed, "The upgrade request could not be handed to the helper: "+err.Error())
		return domain.PanelUpgrade{}, err
	}
	return saved, nil
}

// Cancel releases the lock before the swap. After it, the helper owns the host
// and the panel has nothing to cancel with — so the caller refuses.
func (s *Service) Cancel(ctx context.Context, id string) error {
	return s.o.Store.SetPanelUpgradePhase(ctx, id, PhaseFailed, "Cancelled before anything was changed.")
}

// Sync mirrors the helper's status file into Postgres. It runs on a tick AND on
// boot, because the restart the upgrade itself causes happens in the middle:
// the plane that started the upgrade is not the plane that records its outcome.
func (s *Service) Sync(ctx context.Context) {
	st, ok, err := s.o.Dir.ReadStatus()
	if err != nil || !ok || st.RequestID == "" {
		return
	}
	if err := s.o.Store.SetPanelUpgradePhase(ctx, st.RequestID, st.Phase, st.Detail); err != nil {
		s.o.Log.Debug("upgrade: mirroring status", "error", err)
		return
	}
	if st.SnapshotPath == "" {
		return
	}
	// Record the snapshot once. A repeat Sync of the same terminal status must
	// not create a second row for one file.
	existing, err := s.o.Store.ListPanelSnapshots(ctx)
	if err == nil {
		for _, sn := range existing {
			if sn.Path == st.SnapshotPath {
				return
			}
		}
	}
	sn, err := s.o.Store.CreatePanelSnapshot(ctx, domain.PanelSnapshot{
		ID: ids.New(ids.PrefixPanelSnapshot), Version: st.FromVersion,
		Path: st.SnapshotPath, SizeBytes: st.SnapshotSize,
	})
	if err != nil {
		s.o.Log.Error("upgrade: recording the snapshot", "error", err)
		return
	}
	if err := s.o.Store.AttachPanelUpgradeSnapshot(ctx, st.RequestID, sn.ID); err != nil {
		s.o.Log.Debug("upgrade: attaching the snapshot", "error", err)
	}
}

// RecordExternalUpgrade writes a row when the running version differs from the
// last one recorded — which is what a compose install's upgrade looks like from
// in here, and what a hand-run binary swap looks like too. Version history has
// to record what RAN, not only what this panel performed.
func (s *Service) RecordExternalUpgrade(ctx context.Context) {
	history, err := s.o.Store.ListPanelUpgrades(ctx, 1)
	if err != nil {
		return
	}
	last := ""
	if len(history) > 0 {
		last = history[0].ToVersion
	}
	if last == s.o.CurrentVersion || s.o.CurrentVersion == "" || s.o.CurrentVersion == "dev" {
		return
	}
	now := s.o.Now()
	u := domain.PanelUpgrade{
		ID: ids.New(ids.PrefixPanelUpgrade), FromVersion: last, ToVersion: s.o.CurrentVersion,
		Phase: PhaseSucceeded, Actor: domain.UpgradeActorExternal,
		Detail:    "Observed on boot: this version differs from the last one recorded, so it was installed outside the panel.",
		ExpiresAt: now,
	}
	saved, err := s.o.Store.CreatePanelUpgrade(ctx, u)
	if err != nil {
		return
	}
	// Immediately terminal: it is a record, not a lock.
	if err := s.o.Store.SetPanelUpgradePhase(ctx, saved.ID, PhaseSucceeded, u.Detail); err != nil {
		s.o.Log.Debug("upgrade: closing the external record", "error", err)
	}
}

// RunRetention prunes snapshots past their expiry and the files behind them. A
// PINNED snapshot is never swept whatever its expiry says: pinning is the
// operator saying "keep this one", and a sweep that overrode it would be the
// panel deciding for them.
func (s *Service) RunRetention(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

func (s *Service) sweep(ctx context.Context) {
	expired, err := s.o.Store.ListExpiredPanelSnapshots(ctx, s.o.Now())
	if err != nil {
		return
	}
	for _, sn := range expired {
		// The file first, the row second: a row with no file is a broken
		// restore button, and a file with no row is only wasted disk.
		if err := os.Remove(sn.Path); err != nil && !os.IsNotExist(err) {
			s.o.Log.Warn("upgrade: removing an expired snapshot", "path", sn.Path, "error", err)
			continue
		}
		if err := s.o.Store.DeletePanelSnapshot(ctx, sn.ID); err != nil {
			s.o.Log.Warn("upgrade: forgetting an expired snapshot", "id", sn.ID, "error", err)
		}
	}
}

// SnapshotPath is where a snapshot lives, used by the restore request.
func (s *Service) SnapshotPath(name string) string {
	return filepath.Join(s.o.SnapshotDir, name)
}

// Restore asks the helper to put a snapshot back. The last resort, and it
// carries the same lock as an upgrade because it is the same kind of window.
func (s *Service) Restore(ctx context.Context, snapshotID, actor string) error {
	if s.Mode() != ModeAssisted {
		return ErrUnavailable
	}
	sn, err := s.o.Store.GetPanelSnapshot(ctx, snapshotID)
	if err != nil {
		return err
	}
	now := s.o.Now()
	u, err := s.o.Store.CreatePanelUpgrade(ctx, domain.PanelUpgrade{
		ID: ids.New(ids.PrefixPanelUpgrade), FromVersion: s.o.CurrentVersion,
		ToVersion: sn.Version, Actor: actor, Rollback: true,
		ExpiresAt: now.Add(LockTTL),
	})
	if err != nil {
		return ErrActive
	}
	req := Request{
		ID: u.ID, Version: sn.Version, Rollback: true, RestoreSnapshot: sn.Path,
		Actor: actor, Nonce: ids.New("non"), RequestedAt: now, ExpiresAt: now.Add(LockTTL),
	}
	if err := s.o.Dir.WriteRequest(req); err != nil {
		_ = s.o.Store.SetPanelUpgradePhase(ctx, u.ID, PhaseFailed, "The restore request could not be handed to the helper: "+err.Error())
		return err
	}
	return nil
}

// History is the version timeline, upgrades joined to what they saved.
func (s *Service) History(ctx context.Context, limit int) ([]domain.PanelUpgrade, error) {
	return s.o.Store.ListPanelUpgrades(ctx, limit)
}

func (s *Service) Snapshots(ctx context.Context) ([]domain.PanelSnapshot, error) {
	return s.o.Store.ListPanelSnapshots(ctx)
}

func (s *Service) SetSnapshotRetention(ctx context.Context, id string, expiresAt *time.Time, pinned bool) (domain.PanelSnapshot, error) {
	return s.o.Store.SetPanelSnapshotRetention(ctx, id, expiresAt, pinned)
}

// DeleteSnapshot removes the row and the file behind it. The file first: a row
// with no file is a broken restore button, and a file with no row is only
// wasted disk.
func (s *Service) DeleteSnapshot(ctx context.Context, id string) error {
	sn, err := s.o.Store.GetPanelSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if err := os.Remove(sn.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.o.Store.DeletePanelSnapshot(ctx, id)
}

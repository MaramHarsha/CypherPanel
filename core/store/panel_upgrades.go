package store

// Guided panel upgrades and their snapshots (panel-updates.md §§6, 7).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func upgradeFromRow(r db.PanelUpgrade) domain.PanelUpgrade {
	return domain.PanelUpgrade{
		ID: r.ID, FromVersion: r.FromVersion, ToVersion: r.ToVersion,
		Phase: r.Phase, Detail: r.Detail, Actor: r.Actor, Rollback: r.Rollback,
		SnapshotID: r.SnapshotID.String, StartedAt: r.StartedAt.Time,
		FinishedAt: ptrTime(r.FinishedAt), ExpiresAt: r.ExpiresAt.Time,
	}
}

func snapshotFromRow(r db.PanelSnapshot) domain.PanelSnapshot {
	return domain.PanelSnapshot{
		ID: r.ID, Version: r.Version, Path: r.Path, SizeBytes: r.SizeBytes,
		CreatedAt: r.CreatedAt.Time, ExpiresAt: ptrTime(r.ExpiresAt), Pinned: r.Pinned,
	}
}

// CreatePanelUpgrade takes the lock. The partial unique index refuses a second
// active row, so a double-trigger fails at the database rather than racing
// through two handlers.
func (s *Store) CreatePanelUpgrade(ctx context.Context, u domain.PanelUpgrade) (domain.PanelUpgrade, error) {
	row, err := s.q.CreatePanelUpgrade(ctx, db.CreatePanelUpgradeParams{
		ID: u.ID, FromVersion: u.FromVersion, ToVersion: u.ToVersion,
		Actor: u.Actor, Rollback: u.Rollback, ExpiresAt: tsFromTime(u.ExpiresAt),
	})
	if err != nil {
		return domain.PanelUpgrade{}, wrap("starting the upgrade", err)
	}
	return upgradeFromRow(row), nil
}

func (s *Store) GetActivePanelUpgrade(ctx context.Context) (domain.PanelUpgrade, error) {
	row, err := s.q.GetActivePanelUpgrade(ctx)
	if err != nil {
		return domain.PanelUpgrade{}, wrap("reading the active upgrade", err)
	}
	return upgradeFromRow(row), nil
}

func (s *Store) GetPanelUpgrade(ctx context.Context, id string) (domain.PanelUpgrade, error) {
	row, err := s.q.GetPanelUpgrade(ctx, id)
	if err != nil {
		return domain.PanelUpgrade{}, wrap("reading the upgrade", err)
	}
	return upgradeFromRow(row), nil
}

func (s *Store) SetPanelUpgradePhase(ctx context.Context, id, phase, detail string) error {
	if err := s.q.SetPanelUpgradePhase(ctx, db.SetPanelUpgradePhaseParams{ID: id, Phase: phase, Detail: detail}); err != nil {
		return wrap("recording the upgrade phase", err)
	}
	return nil
}

func (s *Store) AttachPanelUpgradeSnapshot(ctx context.Context, id, snapshotID string) error {
	if err := s.q.AttachPanelUpgradeSnapshot(ctx, db.AttachPanelUpgradeSnapshotParams{
		ID: id, SnapshotID: pgtype.Text{String: snapshotID, Valid: snapshotID != ""},
	}); err != nil {
		return wrap("attaching the snapshot", err)
	}
	return nil
}

// ExpireStalePanelUpgrades releases a lock whose helper stopped reporting.
func (s *Store) ExpireStalePanelUpgrades(ctx context.Context, now time.Time) error {
	if err := s.q.ExpireStalePanelUpgrades(ctx, tsFromTime(now)); err != nil {
		return wrap("expiring stale upgrades", err)
	}
	return nil
}

func (s *Store) ListPanelUpgrades(ctx context.Context, limit int) ([]domain.PanelUpgrade, error) {
	rows, err := s.q.ListPanelUpgrades(ctx, int32(limit))
	if err != nil {
		return nil, wrap("listing upgrades", err)
	}
	out := make([]domain.PanelUpgrade, 0, len(rows))
	for _, r := range rows {
		out = append(out, upgradeFromRow(r))
	}
	return out, nil
}

func (s *Store) CreatePanelSnapshot(ctx context.Context, sn domain.PanelSnapshot) (domain.PanelSnapshot, error) {
	row, err := s.q.CreatePanelSnapshot(ctx, db.CreatePanelSnapshotParams{
		ID: sn.ID, Version: sn.Version, Path: sn.Path, SizeBytes: sn.SizeBytes,
		ExpiresAt: tsFromPtr(sn.ExpiresAt),
	})
	if err != nil {
		return domain.PanelSnapshot{}, wrap("recording the snapshot", err)
	}
	return snapshotFromRow(row), nil
}

func (s *Store) GetPanelSnapshot(ctx context.Context, id string) (domain.PanelSnapshot, error) {
	row, err := s.q.GetPanelSnapshot(ctx, id)
	if err != nil {
		return domain.PanelSnapshot{}, wrap("reading the snapshot", err)
	}
	return snapshotFromRow(row), nil
}

func (s *Store) ListPanelSnapshots(ctx context.Context) ([]domain.PanelSnapshot, error) {
	rows, err := s.q.ListPanelSnapshots(ctx)
	if err != nil {
		return nil, wrap("listing snapshots", err)
	}
	out := make([]domain.PanelSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, snapshotFromRow(r))
	}
	return out, nil
}

func (s *Store) SetPanelSnapshotRetention(ctx context.Context, id string, expiresAt *time.Time, pinned bool) (domain.PanelSnapshot, error) {
	row, err := s.q.SetPanelSnapshotRetention(ctx, db.SetPanelSnapshotRetentionParams{
		ID: id, ExpiresAt: tsFromPtr(expiresAt), Pinned: pinned,
	})
	if err != nil {
		return domain.PanelSnapshot{}, wrap("changing the snapshot's retention", err)
	}
	return snapshotFromRow(row), nil
}

func (s *Store) DeletePanelSnapshot(ctx context.Context, id string) error {
	if err := s.q.DeletePanelSnapshot(ctx, id); err != nil {
		return wrap("deleting the snapshot", err)
	}
	return nil
}

func (s *Store) ListExpiredPanelSnapshots(ctx context.Context, now time.Time) ([]domain.PanelSnapshot, error) {
	rows, err := s.q.ListExpiredPanelSnapshots(ctx, tsFromTime(now))
	if err != nil {
		return nil, wrap("listing expired snapshots", err)
	}
	out := make([]domain.PanelSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, snapshotFromRow(r))
	}
	return out, nil
}

// DatabaseSizeBytes is the pre-flight's disk input.
func (s *Store) DatabaseSizeBytes(ctx context.Context) (int64, error) {
	n, err := s.q.DatabaseSizeBytes(ctx)
	if err != nil {
		return 0, wrap("reading the database size", err)
	}
	return n, nil
}

// CountRunningWork is the quiescence count.
func (s *Store) CountRunningWork(ctx context.Context) (int, error) {
	n, err := s.q.CountRunningWork(ctx)
	if err != nil {
		return 0, wrap("counting running work", err)
	}
	return int(n), nil
}

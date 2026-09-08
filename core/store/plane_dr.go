package store

// Plane disaster recovery persistence (plane-disaster-recovery.md §9.2).

import (
	"context"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func drConfigFromRow(r db.PlaneDrConfig) domain.PlaneDRConfig {
	return domain.PlaneDRConfig{
		TargetID: r.TargetID, PathPrefix: r.PathPrefix, Schedule: r.Schedule,
		RetentionCount: int(r.RetentionCount), Recipient: r.Recipient,
		RecipientMode:       r.RecipientMode,
		RecipientVerifiedAt: ptrTime(r.RecipientVerifiedAt),
		LastRunAt:           ptrTime(r.LastRunAt),
		LastStatus:          r.LastStatus, LastDetail: r.LastDetail,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func planeSnapshotFromRow(r db.PlaneSnapshot) domain.PlaneSnapshot {
	return domain.PlaneSnapshot{
		ID: r.ID, ObjectKey: r.ObjectKey, PanelVersion: r.PanelVersion,
		SchemaVersion: r.SchemaVersion, SizeBytes: r.SizeBytes, SHA256: r.Sha256,
		RowCount: r.RowCount, Recipient: r.Recipient, Status: r.Status, Detail: r.Detail,
		StartedAt: r.StartedAt.Time, FinishedAt: ptrTime(r.FinishedAt), PrunedAt: ptrTime(r.PrunedAt),
	}
}

func (s *Store) GetPlaneDRConfig(ctx context.Context) (domain.PlaneDRConfig, error) {
	row, err := s.q.GetPlaneDRConfig(ctx)
	if err != nil {
		return domain.PlaneDRConfig{}, wrap("reading the disaster recovery config", err)
	}
	return drConfigFromRow(row), nil
}

func (s *Store) SetPlaneDRConfig(ctx context.Context, c domain.PlaneDRConfig) (domain.PlaneDRConfig, error) {
	row, err := s.q.SetPlaneDRConfig(ctx, db.SetPlaneDRConfigParams{
		TargetID: c.TargetID, PathPrefix: c.PathPrefix, Schedule: c.Schedule,
		RetentionCount: int32(c.RetentionCount), Recipient: c.Recipient, RecipientMode: c.RecipientMode,
	})
	if err != nil {
		return domain.PlaneDRConfig{}, wrap("saving the disaster recovery config", err)
	}
	return drConfigFromRow(row), nil
}

func (s *Store) DeletePlaneDRConfig(ctx context.Context) error {
	if err := s.q.DeletePlaneDRConfig(ctx); err != nil {
		return wrap("disarming disaster recovery", err)
	}
	return nil
}

func (s *Store) SetPlaneDRRun(ctx context.Context, at time.Time, status, detail string) error {
	if err := s.q.SetPlaneDRRun(ctx, db.SetPlaneDRRunParams{
		LastRunAt: tsFromTime(at), LastStatus: status, LastDetail: detail,
	}); err != nil {
		return wrap("recording the run", err)
	}
	return nil
}

func (s *Store) MarkPlaneDRRecipientVerified(ctx context.Context, at time.Time) error {
	if err := s.q.MarkPlaneDRRecipientVerified(ctx, tsFromTime(at)); err != nil {
		return wrap("recording the verification", err)
	}
	return nil
}

func (s *Store) CreatePlaneSnapshot(ctx context.Context, sn domain.PlaneSnapshot) (domain.PlaneSnapshot, error) {
	row, err := s.q.CreatePlaneSnapshot(ctx, db.CreatePlaneSnapshotParams{
		ID: sn.ID, ObjectKey: sn.ObjectKey, PanelVersion: sn.PanelVersion,
		SchemaVersion: sn.SchemaVersion, Recipient: sn.Recipient,
	})
	if err != nil {
		return domain.PlaneSnapshot{}, wrap("recording the snapshot", err)
	}
	return planeSnapshotFromRow(row), nil
}

func (s *Store) FinishPlaneSnapshot(ctx context.Context, id string, size int64, sha string, rows int64, status, detail string) error {
	if err := s.q.FinishPlaneSnapshot(ctx, db.FinishPlaneSnapshotParams{
		ID: id, SizeBytes: size, Sha256: sha, RowCount: rows, Status: status, Detail: detail,
	}); err != nil {
		return wrap("finishing the snapshot", err)
	}
	return nil
}

func (s *Store) ListPlaneSnapshots(ctx context.Context, limit int) ([]domain.PlaneSnapshot, error) {
	rows, err := s.q.ListPlaneSnapshots(ctx, int32(limit))
	if err != nil {
		return nil, wrap("listing snapshots", err)
	}
	return mapPlaneSnapshots(rows), nil
}

// ListPlaneSnapshotsBeyondRetention is a COUNT, not an age: an operator who
// backs up nightly and keeps fourteen has two weeks, and one who backs up
// hourly has fourteen hours — which is what they asked for either way.
func (s *Store) ListPlaneSnapshotsBeyondRetention(ctx context.Context, keep int) ([]domain.PlaneSnapshot, error) {
	rows, err := s.q.ListPlaneSnapshotsBeyondRetention(ctx, int32(keep))
	if err != nil {
		return nil, wrap("listing snapshots past retention", err)
	}
	return mapPlaneSnapshots(rows), nil
}

func mapPlaneSnapshots(rows []db.PlaneSnapshot) []domain.PlaneSnapshot {
	out := make([]domain.PlaneSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, planeSnapshotFromRow(r))
	}
	return out
}

func (s *Store) MarkPlaneSnapshotPruned(ctx context.Context, id string) error {
	if err := s.q.MarkPlaneSnapshotPruned(ctx, id); err != nil {
		return wrap("marking the snapshot pruned", err)
	}
	return nil
}

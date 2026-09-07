package store

// Volume backup schedules and their history (volume-backups.md §3).

import (
	"context"
	"fmt"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func volumeBackupFromRow(r db.VolumeBackup) domain.VolumeBackup {
	return domain.VolumeBackup{
		ID: r.ID, ApplicationID: r.ApplicationID, TargetID: r.TargetID,
		Schedule: r.Schedule, RetentionCount: int(r.RetentionCount), Enabled: r.Enabled,
		LastRunAt: ptrTime(r.LastRunAt), LastStatus: r.LastStatus,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func volumeRecordFromRow(r db.VolumeBackupRecord) domain.VolumeBackupRecord {
	return domain.VolumeBackupRecord{
		ID: r.ID, VolumeBackupID: r.VolumeBackupID, VolumeName: r.VolumeName,
		ObjectKey: r.ObjectKey, SizeBytes: r.SizeBytes, Status: r.Status, Detail: r.Detail,
		StartedAt: r.StartedAt.Time, FinishedAt: ptrTime(r.FinishedAt), CreatedAt: r.CreatedAt.Time,
	}
}

func (s *Store) UpsertVolumeBackup(ctx context.Context, v domain.VolumeBackup) (domain.VolumeBackup, error) {
	row, err := s.q.UpsertVolumeBackup(ctx, db.UpsertVolumeBackupParams{
		ID: v.ID, ApplicationID: v.ApplicationID, TargetID: v.TargetID,
		Schedule: v.Schedule, RetentionCount: int32(v.RetentionCount), Enabled: v.Enabled,
	})
	if err != nil {
		return domain.VolumeBackup{}, fmt.Errorf("store: upserting volume backup: %w", err)
	}
	return volumeBackupFromRow(row), nil
}

func (s *Store) GetVolumeBackupByApplication(ctx context.Context, appID string) (domain.VolumeBackup, error) {
	row, err := s.q.GetVolumeBackupByApplication(ctx, appID)
	if err != nil {
		return domain.VolumeBackup{}, wrap("getting volume backup", err)
	}
	return volumeBackupFromRow(row), nil
}

func (s *Store) GetVolumeBackup(ctx context.Context, id string) (domain.VolumeBackup, error) {
	row, err := s.q.GetVolumeBackup(ctx, id)
	if err != nil {
		return domain.VolumeBackup{}, wrap("getting volume backup", err)
	}
	return volumeBackupFromRow(row), nil
}

func (s *Store) DeleteVolumeBackup(ctx context.Context, appID string) error {
	if err := s.q.DeleteVolumeBackup(ctx, appID); err != nil {
		return fmt.Errorf("store: deleting volume backup: %w", err)
	}
	return nil
}

func (s *Store) ListEnabledVolumeBackupSchedules(ctx context.Context) ([]domain.VolumeBackup, error) {
	rows, err := s.q.ListEnabledVolumeBackupSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing volume backup schedules: %w", err)
	}
	out := make([]domain.VolumeBackup, 0, len(rows))
	for _, r := range rows {
		out = append(out, volumeBackupFromRow(r))
	}
	return out, nil
}

func (s *Store) SetVolumeBackupLastRun(ctx context.Context, id string, at *time.Time, status string) error {
	if err := s.q.SetVolumeBackupLastRun(ctx, db.SetVolumeBackupLastRunParams{
		ID: id, LastRunAt: tsFromPtr(at), LastStatus: status,
	}); err != nil {
		return fmt.Errorf("store: setting volume backup last run: %w", err)
	}
	return nil
}

func (s *Store) CreateVolumeBackupRecord(ctx context.Context, id, scheduleID, volumeName string) (domain.VolumeBackupRecord, error) {
	row, err := s.q.CreateVolumeBackupRecord(ctx, db.CreateVolumeBackupRecordParams{
		ID: id, VolumeBackupID: scheduleID, VolumeName: volumeName,
	})
	if err != nil {
		return domain.VolumeBackupRecord{}, fmt.Errorf("store: creating volume backup record: %w", err)
	}
	return volumeRecordFromRow(row), nil
}

func (s *Store) GetVolumeBackupRecord(ctx context.Context, id string) (domain.VolumeBackupRecord, error) {
	row, err := s.q.GetVolumeBackupRecord(ctx, id)
	if err != nil {
		return domain.VolumeBackupRecord{}, wrap("getting volume backup record", err)
	}
	return volumeRecordFromRow(row), nil
}

func (s *Store) UpdateVolumeBackupRecord(ctx context.Context, id, objectKey string, size int64, status, detail string) error {
	if err := s.q.UpdateVolumeBackupRecord(ctx, db.UpdateVolumeBackupRecordParams{
		ID: id, ObjectKey: objectKey, SizeBytes: size, Status: status, Detail: detail,
	}); err != nil {
		return fmt.Errorf("store: updating volume backup record: %w", err)
	}
	return nil
}

func (s *Store) ListVolumeBackupRecords(ctx context.Context, scheduleID string, limit int) ([]domain.VolumeBackupRecord, error) {
	rows, err := s.q.ListVolumeBackupRecords(ctx, db.ListVolumeBackupRecordsParams{
		VolumeBackupID: scheduleID, Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing volume backup records: %w", err)
	}
	out := make([]domain.VolumeBackupRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, volumeRecordFromRow(r))
	}
	return out, nil
}

// ListVolumeRecordsBeyondRetention returns the successful records past the keep
// count FOR ONE VOLUME. Per volume rather than per schedule: keeping "the last
// 7" across a two-volume application would keep three of one and four of the
// other, which is not what the number on the screen says.
func (s *Store) ListVolumeRecordsBeyondRetention(ctx context.Context, scheduleID, volumeName string, keep int) ([]domain.VolumeBackupRecord, error) {
	rows, err := s.q.ListVolumeRecordsBeyondRetention(ctx, db.ListVolumeRecordsBeyondRetentionParams{
		VolumeBackupID: scheduleID, VolumeName: volumeName, Offset: int32(keep),
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing volume records beyond retention: %w", err)
	}
	out := make([]domain.VolumeBackupRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, volumeRecordFromRow(r))
	}
	return out, nil
}

func (s *Store) DeleteVolumeBackupRecords(ctx context.Context, ids []string) error {
	if err := s.q.DeleteVolumeBackupRecords(ctx, ids); err != nil {
		return fmt.Errorf("store: deleting volume backup records: %w", err)
	}
	return nil
}

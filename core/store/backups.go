package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── Backup Targets ─────────────────────────────────────────────────────────

func (s *Store) CreateBackupTarget(ctx context.Context, t domain.BackupTarget) (domain.BackupTarget, error) {
	row, err := s.q.CreateBackupTarget(ctx, db.CreateBackupTargetParams{
		ID:             t.ID,
		Name:           t.Name,
		Endpoint:       t.Endpoint,
		Bucket:         t.Bucket,
		Region:         t.Region,
		AccessKeyCt:    t.AccessKeyCT,
		AccessKeyNonce: t.AccessKeyNonce,
		SecretKeyCt:    t.SecretKeyCT,
		SecretKeyNonce: t.SecretKeyNonce,
		PathPrefix:     t.PathPrefix,
	})
	if err != nil {
		return domain.BackupTarget{}, wrapCreate("creating backup target", err)
	}
	return backupTargetFromRow(row), nil
}

func (s *Store) GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error) {
	row, err := s.q.GetBackupTarget(ctx, id)
	if err != nil {
		return domain.BackupTarget{}, wrap("getting backup target", err)
	}
	return backupTargetFromRow(row), nil
}

func (s *Store) ListBackupTargets(ctx context.Context) ([]domain.BackupTarget, error) {
	rows, err := s.q.ListBackupTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing backup targets: %w", err)
	}
	out := make([]domain.BackupTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, backupTargetFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateBackupTarget(ctx context.Context, t domain.BackupTarget) (domain.BackupTarget, error) {
	row, err := s.q.UpdateBackupTarget(ctx, db.UpdateBackupTargetParams{
		ID:             t.ID,
		Name:           t.Name,
		Endpoint:       t.Endpoint,
		Bucket:         t.Bucket,
		Region:         t.Region,
		AccessKeyCt:    t.AccessKeyCT,
		AccessKeyNonce: t.AccessKeyNonce,
		SecretKeyCt:    t.SecretKeyCT,
		SecretKeyNonce: t.SecretKeyNonce,
		PathPrefix:     t.PathPrefix,
	})
	if err != nil {
		return domain.BackupTarget{}, wrapUpdate("updating backup target", err)
	}
	return backupTargetFromRow(row), nil
}

func (s *Store) DeleteBackupTarget(ctx context.Context, id string) error {
	if err := s.q.DeleteBackupTarget(ctx, id); err != nil {
		return wrapDelete("deleting backup target", err)
	}
	return nil
}

// ─── Database Backups & Records ──────────────────────────────────────────────

func (s *Store) CreateDatabaseBackup(ctx context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	row, err := s.q.CreateDatabaseBackup(ctx, db.CreateDatabaseBackupParams{
		ID:             b.ID,
		DatabaseID:     b.DatabaseID,
		TargetID:       b.TargetID,
		Schedule:       b.Schedule,
		RetentionCount: int32(b.RetentionCount),
		Enabled:        b.Enabled,
	})
	if err != nil {
		return domain.DatabaseBackup{}, wrapCreate("creating database backup schedule", err)
	}
	return databaseBackupFromRow(row), nil
}

func (s *Store) GetDatabaseBackup(ctx context.Context, id string) (domain.DatabaseBackup, error) {
	row, err := s.q.GetDatabaseBackup(ctx, id)
	if err != nil {
		return domain.DatabaseBackup{}, wrap("getting database backup schedule", err)
	}
	return databaseBackupFromRow(row), nil
}

func (s *Store) ListDatabaseBackups(ctx context.Context, dbID string) ([]domain.DatabaseBackup, error) {
	rows, err := s.q.ListDatabaseBackups(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("store: listing database backup schedules: %w", err)
	}
	out := make([]domain.DatabaseBackup, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseBackupFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateDatabaseBackup(ctx context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	row, err := s.q.UpdateDatabaseBackup(ctx, db.UpdateDatabaseBackupParams{
		ID:             b.ID,
		Schedule:       b.Schedule,
		RetentionCount: int32(b.RetentionCount),
		Enabled:        b.Enabled,
	})
	if err != nil {
		return domain.DatabaseBackup{}, wrapUpdate("updating database backup schedule", err)
	}
	return databaseBackupFromRow(row), nil
}

func (s *Store) DeleteDatabaseBackup(ctx context.Context, id string) error {
	if err := s.q.DeleteDatabaseBackup(ctx, id); err != nil {
		return wrapDelete("deleting database backup schedule", err)
	}
	return nil
}

func (s *Store) SetDatabaseBackupLastRun(ctx context.Context, id string, lastRunAt *time.Time, lastStatus string) error {
	err := s.q.SetDatabaseBackupLastRun(ctx, db.SetDatabaseBackupLastRunParams{
		ID:         id,
		LastRunAt:  tsFromPtr(lastRunAt),
		LastStatus: lastStatus,
	})
	if err != nil {
		return wrapUpdate("setting database backup last run", err)
	}
	return nil
}

func (s *Store) ListEnabledBackupSchedules(ctx context.Context) ([]domain.DatabaseBackup, error) {
	rows, err := s.q.ListEnabledBackupSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing enabled backup schedules: %w", err)
	}
	out := make([]domain.DatabaseBackup, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseBackupFromRow(r))
	}
	return out, nil
}

func (s *Store) CreateBackupRecord(ctx context.Context, r domain.BackupRecord) (domain.BackupRecord, error) {
	row, err := s.q.CreateBackupRecord(ctx, db.CreateBackupRecordParams{
		ID:               r.ID,
		DatabaseBackupID: r.DatabaseBackupID,
		Status:           r.Status,
		StartedAt:        tsFromTime(r.StartedAt),
	})
	if err != nil {
		return domain.BackupRecord{}, wrapCreate("creating backup record", err)
	}
	return backupRecordFromRow(row), nil
}

func (s *Store) GetBackupRecord(ctx context.Context, id string) (domain.BackupRecord, error) {
	row, err := s.q.GetBackupRecord(ctx, id)
	if err != nil {
		return domain.BackupRecord{}, wrap("getting backup record", err)
	}
	return backupRecordFromRow(row), nil
}

func (s *Store) ListBackupRecords(ctx context.Context, backupID string) ([]domain.BackupRecord, error) {
	rows, err := s.q.ListBackupRecords(ctx, backupID)
	if err != nil {
		return nil, fmt.Errorf("store: listing backup records: %w", err)
	}
	out := make([]domain.BackupRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, backupRecordFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateBackupRecord(ctx context.Context, id string, objectKey string, sizeBytes int64, status, detail string, finishedAt *time.Time) error {
	err := s.q.UpdateBackupRecord(ctx, db.UpdateBackupRecordParams{
		ID:         id,
		ObjectKey:  objectKey,
		SizeBytes:  sizeBytes,
		Status:     status,
		Detail:     detail,
		FinishedAt: tsFromPtr(finishedAt),
	})
	if err != nil {
		return wrapUpdate("updating backup record", err)
	}
	return nil
}

// PrunableBackupRecord is a succeeded backup beyond the retention window: its
// row and S3 object are both due for deletion.
type PrunableBackupRecord struct {
	ID        string
	ObjectKey string
}

// ListBackupRecordsBeyondRetention returns the succeeded backups older than the
// newest `keep` for a schedule — the retention sweep set, each carrying an S3
// object to delete.
func (s *Store) ListBackupRecordsBeyondRetention(ctx context.Context, backupID string, keep int32) ([]PrunableBackupRecord, error) {
	rows, err := s.q.ListBackupRecordsBeyondRetention(ctx, db.ListBackupRecordsBeyondRetentionParams{
		DatabaseBackupID: backupID,
		Limit:            keep,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing prunable backup records: %w", err)
	}
	out := make([]PrunableBackupRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, PrunableBackupRecord{ID: r.ID, ObjectKey: r.ObjectKey})
	}
	return out, nil
}

// DeleteBackupRecordsByObjectKeys removes the rows for objects the agent has
// confirmed deleted from S3.
func (s *Store) DeleteBackupRecordsByObjectKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.q.DeleteBackupRecordsByObjectKeys(ctx, keys); err != nil {
		return fmt.Errorf("store: deleting pruned backup records: %w", err)
	}
	return nil
}

func backupTargetFromRow(r db.BackupTarget) domain.BackupTarget {
	return domain.BackupTarget{
		ID:             r.ID,
		Name:           r.Name,
		Endpoint:       r.Endpoint,
		Bucket:         r.Bucket,
		Region:         r.Region,
		AccessKeyCT:    r.AccessKeyCt,
		AccessKeyNonce: r.AccessKeyNonce,
		SecretKeyCT:    r.SecretKeyCt,
		SecretKeyNonce: r.SecretKeyNonce,
		PathPrefix:     r.PathPrefix,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

func databaseBackupFromRow(r db.DatabaseBackup) domain.DatabaseBackup {
	return domain.DatabaseBackup{
		ID:             r.ID,
		DatabaseID:     r.DatabaseID,
		TargetID:       r.TargetID,
		Schedule:       r.Schedule,
		RetentionCount: int(r.RetentionCount),
		Enabled:        r.Enabled,
		LastRunAt:      ptrTime(r.LastRunAt),
		LastStatus:     r.LastStatus,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

func backupRecordFromRow(r db.BackupRecord) domain.BackupRecord {
	return domain.BackupRecord{
		ID:               r.ID,
		DatabaseBackupID: r.DatabaseBackupID,
		ObjectKey:        r.ObjectKey,
		SizeBytes:        r.SizeBytes,
		Status:           r.Status,
		Detail:           r.Detail,
		StartedAt:        r.StartedAt.Time,
		FinishedAt:       ptrTime(r.FinishedAt),
		CreatedAt:        r.CreatedAt.Time,
	}
}

// tsFromPtr converts an optional time to a pg timestamp (NULL when nil).
func tsFromPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return tsFromTime(*t)
}

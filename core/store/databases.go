package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreateDatabaseWithRevision inserts a database and its first revision inside a
// single transaction.
func (s *Store) CreateDatabaseWithRevision(ctx context.Context, d domain.Database, rev domain.DatabaseRevision) (domain.Database, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Database{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)

	_, err = qtx.CreateDatabase(ctx, db.CreateDatabaseParams{
		ID:                d.ID,
		EnvironmentID:     d.EnvironmentID,
		Name:              d.Name,
		Engine:            string(d.Engine),
		Version:           d.Version,
		ServerID:          d.ServerID,
		CpuLimit:          float4FromPtr(d.CPULimit),
		MemoryLimitMb:     int4FromPtr(d.MemoryLimitMB),
		VolumeName:        d.VolumeName,
		DataPath:          d.DataPath,
		ExposePort:        int4FromPtr(d.ExposePort),
		Network:           d.Network,
		RootUser:          d.RootUser,
		RootPasswordCt:    d.RootPasswordCT,
		RootPasswordNonce: d.RootPasswordNonce,
		RequirePassword:   d.RequirePassword,
		Status:            d.Status,
		StatusDetail:      d.StatusDetail,
	})
	if err != nil {
		return domain.Database{}, wrapCreate("creating database", err)
	}

	_, err = qtx.CreateDatabaseRevision(ctx, db.CreateDatabaseRevisionParams{
		ID:             rev.ID,
		DatabaseID:     rev.DatabaseID,
		ConfigSnapshot: rev.ConfigSnapshot,
	})
	if err != nil {
		return domain.Database{}, wrapCreate("creating initial database revision", err)
	}

	// Update the database to point to the desired revision.
	row, err := qtx.SetDatabaseDesiredRevision(ctx, db.SetDatabaseDesiredRevisionParams{
		ID:                d.ID,
		DesiredRevisionID: textFromPtr(&rev.ID),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired revision", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Database{}, fmt.Errorf("store: committing tx: %w", err)
	}

	return databaseFromRow(row), nil
}

func (s *Store) GetDatabase(ctx context.Context, id string) (domain.Database, error) {
	row, err := s.q.GetDatabase(ctx, id)
	if err != nil {
		return domain.Database{}, wrap("getting database", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) ListDatabasesByEnvironment(ctx context.Context, envID string) ([]domain.Database, error) {
	rows, err := s.q.ListDatabasesByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("store: listing databases by environment: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

func (s *Store) ListDatabasesByServer(ctx context.Context, serverID string) ([]domain.Database, error) {
	rows, err := s.q.ListDatabasesByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: listing databases by server: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

func (s *Store) ListPendingDeleteDatabases(ctx context.Context) ([]domain.Database, error) {
	rows, err := s.q.ListPendingDeleteDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing pending delete databases: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateDatabaseConfig(ctx context.Context, d domain.Database) (domain.Database, error) {
	row, err := s.q.UpdateDatabaseConfig(ctx, db.UpdateDatabaseConfigParams{
		ID:            d.ID,
		Name:          d.Name,
		Version:       d.Version,
		CpuLimit:      float4FromPtr(d.CPULimit),
		MemoryLimitMb: int4FromPtr(d.MemoryLimitMB),
		ExposePort:    int4FromPtr(d.ExposePort),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("updating database config", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) UpdateDatabasePassword(ctx context.Context, id string, ct, nonce []byte) error {
	err := s.q.UpdateDatabasePassword(ctx, db.UpdateDatabasePasswordParams{
		ID:                id,
		RootPasswordCt:    ct,
		RootPasswordNonce: nonce,
	})
	if err != nil {
		return wrapUpdate("updating database password", err)
	}
	return nil
}

func (s *Store) SetDatabaseDesiredRevision(ctx context.Context, id string, revID string) (domain.Database, error) {
	row, err := s.q.SetDatabaseDesiredRevision(ctx, db.SetDatabaseDesiredRevisionParams{
		ID:                id,
		DesiredRevisionID: textFromPtr(&revID),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired revision", err)
	}
	return databaseFromRow(row), nil
}

// SetDatabaseDesiredState records the operator's run/stop intent (authoritative
// for the scheduler; distinct from the observed status).
func (s *Store) SetDatabaseDesiredState(ctx context.Context, id, desiredState string) (domain.Database, error) {
	row, err := s.q.SetDatabaseDesiredState(ctx, db.SetDatabaseDesiredStateParams{
		ID:           id,
		DesiredState: desiredState,
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired state", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) SetDatabaseStatus(ctx context.Context, id, status, detail string) error {
	err := s.q.SetDatabaseStatus(ctx, db.SetDatabaseStatusParams{
		ID:           id,
		Status:       status,
		StatusDetail: detail,
	})
	if err != nil {
		return wrapUpdate("setting database status", err)
	}
	return nil
}

func (s *Store) SetDatabaseObservedStatus(ctx context.Context, id, status, detail, observedRevisionID string, observedAt time.Time) error {
	err := s.q.SetDatabaseObservedStatus(ctx, db.SetDatabaseObservedStatusParams{
		ID:                 id,
		Status:             status,
		StatusDetail:       detail,
		ObservedRevisionID: observedRevisionID,
		StatusObservedAt:   tsFromTime(observedAt),
	})
	if err != nil {
		return wrapUpdate("setting database observed status", err)
	}
	return nil
}

func (s *Store) SetDatabasePendingDelete(ctx context.Context, id string, deleteVolume bool) error {
	err := s.q.SetDatabasePendingDelete(ctx, db.SetDatabasePendingDeleteParams{
		ID:           id,
		DeleteVolume: deleteVolume,
	})
	if err != nil {
		return wrapUpdate("setting database pending delete", err)
	}
	return nil
}

func (s *Store) DeleteDatabase(ctx context.Context, id string) error {
	if err := s.q.DeleteDatabase(ctx, id); err != nil {
		return wrapDelete("deleting database", err)
	}
	return nil
}

func (s *Store) CreateDatabaseRevision(ctx context.Context, rev domain.DatabaseRevision) (domain.DatabaseRevision, error) {
	row, err := s.q.CreateDatabaseRevision(ctx, db.CreateDatabaseRevisionParams{
		ID:             rev.ID,
		DatabaseID:     rev.DatabaseID,
		ConfigSnapshot: rev.ConfigSnapshot,
	})
	if err != nil {
		return domain.DatabaseRevision{}, wrapCreate("creating database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) GetDatabaseRevision(ctx context.Context, id string) (domain.DatabaseRevision, error) {
	row, err := s.q.GetDatabaseRevision(ctx, id)
	if err != nil {
		return domain.DatabaseRevision{}, wrap("getting database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) GetLatestDatabaseRevision(ctx context.Context, dbID string) (domain.DatabaseRevision, error) {
	row, err := s.q.GetLatestDatabaseRevision(ctx, dbID)
	if err != nil {
		return domain.DatabaseRevision{}, wrap("getting latest database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) ListDatabaseRevisions(ctx context.Context, dbID string) ([]domain.DatabaseRevision, error) {
	rows, err := s.q.ListDatabaseRevisions(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("store: listing database revisions: %w", err)
	}
	out := make([]domain.DatabaseRevision, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseRevisionFromRow(r))
	}
	return out, nil
}

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

func (s *Store) SetDatabaseBackupLastRun(ctx context.Context, id string, lastRunAt pgtype.Timestamptz, lastStatus string) error {
	err := s.q.SetDatabaseBackupLastRun(ctx, db.SetDatabaseBackupLastRunParams{
		ID:         id,
		LastRunAt:  lastRunAt,
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

func (s *Store) UpdateBackupRecord(ctx context.Context, id string, objectKey string, sizeBytes int64, status, detail string, finishedAt pgtype.Timestamptz) error {
	err := s.q.UpdateBackupRecord(ctx, db.UpdateBackupRecordParams{
		ID:         id,
		ObjectKey:  objectKey,
		SizeBytes:  sizeBytes,
		Status:     status,
		Detail:     detail,
		FinishedAt: finishedAt,
	})
	if err != nil {
		return wrapUpdate("updating backup record", err)
	}
	return nil
}

func (s *Store) DeleteOldBackupRecords(ctx context.Context, backupID string, limit int32) error {
	err := s.q.DeleteOldBackupRecords(ctx, db.DeleteOldBackupRecordsParams{
		DatabaseBackupID: backupID,
		Limit:            limit,
	})
	if err != nil {
		return fmt.Errorf("store: deleting old backup records: %w", err)
	}
	return nil
}

// ─── mapping helpers ────────────────────────────────────────────────────────

func float4FromPtr(f *float64) pgtype.Float4 {
	if f == nil {
		return pgtype.Float4{}
	}
	return pgtype.Float4{Float32: float32(*f), Valid: true}
}

func ptrFromFloat4(f pgtype.Float4) *float64 {
	if !f.Valid {
		return nil
	}
	v := float64(f.Float32)
	return &v
}

func int4FromPtr(i *int) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*i), Valid: true}
}

func ptrFromInt4(i pgtype.Int4) *int {
	if !i.Valid {
		return nil
	}
	v := int(i.Int32)
	return &v
}

func databaseFromRow(r db.Database) domain.Database {
	return domain.Database{
		ID:                 r.ID,
		EnvironmentID:      r.EnvironmentID,
		Name:               r.Name,
		Engine:             domain.DbEngine(r.Engine),
		Version:            r.Version,
		ServerID:           r.ServerID,
		CPULimit:           ptrFromFloat4(r.CpuLimit),
		MemoryLimitMB:      ptrFromInt4(r.MemoryLimitMb),
		VolumeName:         r.VolumeName,
		DataPath:           r.DataPath,
		ExposePort:         ptrFromInt4(r.ExposePort),
		Network:            r.Network,
		RootUser:           r.RootUser,
		RootPasswordCT:     r.RootPasswordCt,
		RootPasswordNonce:  r.RootPasswordNonce,
		RequirePassword:    r.RequirePassword,
		DesiredRevisionID:  ptrFromText(r.DesiredRevisionID),
		DesiredState:       r.DesiredState,
		Status:             r.Status,
		StatusDetail:       r.StatusDetail,
		ObservedRevisionID: r.ObservedRevisionID,
		StatusObservedAt:   ptrTime(r.StatusObservedAt),
		PendingDelete:      r.PendingDelete,
		DeleteVolume:       r.DeleteVolume,
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

func databaseRevisionFromRow(r db.DatabaseRevision) domain.DatabaseRevision {
	return domain.DatabaseRevision{
		ID:             r.ID,
		DatabaseID:     r.DatabaseID,
		ConfigSnapshot: r.ConfigSnapshot,
		CreatedAt:      r.CreatedAt.Time,
	}
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

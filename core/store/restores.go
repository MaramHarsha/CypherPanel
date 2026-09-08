package store

// Database restores, recorded as operations rather than as a status flag
// (managed-databases.md §"Restoring").

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreateDatabaseRestore opens a restore record. It is written before the work
// item is published, so a plane that dies between the two restarts with a
// restore it knows about rather than one it has forgotten (ENGINEERING rule 15).
func (s *Store) CreateDatabaseRestore(ctx context.Context, id, databaseID, backupRecordID, step string) (domain.DatabaseRestore, error) {
	var rec pgtype.Text
	if backupRecordID != "" {
		rec = pgtype.Text{String: backupRecordID, Valid: true}
	}
	row, err := s.q.CreateDatabaseRestore(ctx, db.CreateDatabaseRestoreParams{
		ID:             id,
		DatabaseID:     databaseID,
		BackupRecordID: rec,
		Step:           step,
	})
	if err != nil {
		return domain.DatabaseRestore{}, wrapCreate("creating database restore", err)
	}
	return restoreFromRow(row), nil
}

func (s *Store) GetDatabaseRestore(ctx context.Context, id string) (domain.DatabaseRestore, error) {
	row, err := s.q.GetDatabaseRestore(ctx, id)
	if err != nil {
		return domain.DatabaseRestore{}, wrap("getting database restore", err)
	}
	return restoreFromRow(row), nil
}

// ListDatabaseRestores returns a database's restore history, newest first.
func (s *Store) ListDatabaseRestores(ctx context.Context, databaseID string, limit int32) ([]domain.DatabaseRestore, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListDatabaseRestores(ctx, db.ListDatabaseRestoresParams{DatabaseID: databaseID, Limit: limit})
	if err != nil {
		return nil, wrap("listing database restores", err)
	}
	out := make([]domain.DatabaseRestore, 0, len(rows))
	for _, r := range rows {
		out = append(out, restoreFromRow(r))
	}
	return out, nil
}

// RunningDatabaseRestore returns the restore a database is in the middle of, or
// ErrNotFound. What a reopened page needs to show the popup again.
func (s *Store) RunningDatabaseRestore(ctx context.Context, databaseID string) (domain.DatabaseRestore, error) {
	row, err := s.q.RunningDatabaseRestore(ctx, databaseID)
	if err != nil {
		return domain.DatabaseRestore{}, wrap("getting running database restore", err)
	}
	return restoreFromRow(row), nil
}

// AdvanceDatabaseRestore records progress. ErrNotFound means the restore is no
// longer running — a late progress event for one that already finished, which
// is ignored rather than allowed to reopen it.
func (s *Store) AdvanceDatabaseRestore(ctx context.Context, id, step string, done, total int64) (domain.DatabaseRestore, error) {
	row, err := s.q.AdvanceDatabaseRestore(ctx, db.AdvanceDatabaseRestoreParams{
		ID: id, Step: step, BytesDone: done, BytesTotal: total,
	})
	if err != nil {
		return domain.DatabaseRestore{}, wrap("advancing database restore", err)
	}
	return restoreFromRow(row), nil
}

// FinishDatabaseRestore closes a restore. Also ErrNotFound when it was already
// closed, so a redelivered terminal event is a no-op rather than a second
// finish with a different answer.
func (s *Store) FinishDatabaseRestore(ctx context.Context, id, status, detail string) (domain.DatabaseRestore, error) {
	row, err := s.q.FinishDatabaseRestore(ctx, db.FinishDatabaseRestoreParams{
		ID: id, Status: status, Detail: detail,
	})
	if err != nil {
		return domain.DatabaseRestore{}, wrap("finishing database restore", err)
	}
	return restoreFromRow(row), nil
}

func restoreFromRow(r db.DatabaseRestore) domain.DatabaseRestore {
	return domain.DatabaseRestore{
		ID:             r.ID,
		DatabaseID:     r.DatabaseID,
		BackupRecordID: textOrEmpty(r.BackupRecordID),
		Status:         r.Status,
		Step:           r.Step,
		BytesDone:      r.BytesDone,
		BytesTotal:     r.BytesTotal,
		Detail:         r.Detail,
		StartedAt:      r.StartedAt.Time,
		FinishedAt:     ptrTime(r.FinishedAt),
	}
}

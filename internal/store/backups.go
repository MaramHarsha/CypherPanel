package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupDestination is a restic repository the fleet backs accounts up into.
// CredentialsEncrypted is the AES-GCM blob; it is never serialised to an API
// response — only decrypted when handing credentials to an agent.
type BackupDestination struct {
	ID                   string
	Name                 string
	Kind                 string
	Repository           string
	CredentialsEncrypted []byte
	Schedule             string
	RetentionDaily       int
	RetentionWeekly      int
	RetentionMonthly     int
	LastRunAt            *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// AccountBackup is one backup (or restore) attempt against a destination.
type AccountBackup struct {
	ID            string
	AccountID     string
	DestinationID string
	TaskID        string
	SnapshotID    string
	Kind          string
	Status        string
	SizeBytes     int64
	Error         string
	StartedAt     time.Time
	CompletedAt   *time.Time
}

type Backups struct{ pool *pgxpool.Pool }

func NewBackups(pool *pgxpool.Pool) *Backups { return &Backups{pool: pool} }

const destColumns = `id, name, kind, repository, credentials_encrypted, schedule,
	retention_daily, retention_weekly, retention_monthly, last_run_at, created_at, updated_at`

func scanDestination(row pgx.Row) (*BackupDestination, error) {
	var d BackupDestination
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Repository, &d.CredentialsEncrypted,
		&d.Schedule, &d.RetentionDaily, &d.RetentionWeekly, &d.RetentionMonthly,
		&d.LastRunAt, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning backup destination: %w", err)
	}
	return &d, nil
}

func (s *Backups) CreateDestination(ctx context.Context, d BackupDestination) (*BackupDestination, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO backup_destinations
			(name, kind, repository, credentials_encrypted, schedule,
			 retention_daily, retention_weekly, retention_monthly)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+destColumns,
		d.Name, d.Kind, d.Repository, d.CredentialsEncrypted, d.Schedule,
		d.RetentionDaily, d.RetentionWeekly, d.RetentionMonthly)
	return scanDestination(row)
}

func (s *Backups) ListDestinations(ctx context.Context) ([]BackupDestination, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+destColumns+` FROM backup_destinations ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: listing backup destinations: %w", err)
	}
	defer rows.Close()
	var out []BackupDestination
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Backups) GetDestination(ctx context.Context, id string) (*BackupDestination, error) {
	return scanDestination(s.pool.QueryRow(ctx, `SELECT `+destColumns+` FROM backup_destinations WHERE id = $1`, id))
}

// ListScheduledDestinations returns destinations whose schedule is due, i.e.
// never run or last run before the cutoff for their cadence.
func (s *Backups) ListScheduledDestinations(ctx context.Context, now time.Time) ([]BackupDestination, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+destColumns+`
		FROM backup_destinations
		WHERE schedule <> 'off'
		  AND (last_run_at IS NULL
		       OR (schedule = 'daily'  AND last_run_at < $1 - interval '1 day')
		       OR (schedule = 'weekly' AND last_run_at < $1 - interval '7 days'))
		ORDER BY created_at`, now)
	if err != nil {
		return nil, fmt.Errorf("store: listing due backup destinations: %w", err)
	}
	defer rows.Close()
	var out []BackupDestination
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Backups) UpdateDestinationSchedule(ctx context.Context, id, schedule string, daily, weekly, monthly int) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE backup_destinations
		SET schedule = $2, retention_daily = $3, retention_weekly = $4,
		    retention_monthly = $5, updated_at = now()
		WHERE id = $1`, id, schedule, daily, weekly, monthly)
	if err != nil {
		return fmt.Errorf("store: updating backup schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Backups) MarkDestinationRun(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE backup_destinations SET last_run_at = now(), updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: marking destination run: %w", err)
	}
	return nil
}

func (s *Backups) DeleteDestination(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM backup_destinations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: deleting backup destination: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const backupColumns = `id, account_id, destination_id, COALESCE(task_id::text, ''),
	snapshot_id, kind, status, size_bytes, error, started_at, completed_at`

func scanBackup(row pgx.Row) (*AccountBackup, error) {
	var b AccountBackup
	err := row.Scan(&b.ID, &b.AccountID, &b.DestinationID, &b.TaskID, &b.SnapshotID,
		&b.Kind, &b.Status, &b.SizeBytes, &b.Error, &b.StartedAt, &b.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning account backup: %w", err)
	}
	return &b, nil
}

func (s *Backups) CreateRun(ctx context.Context, accountID, destinationID, taskID, kind string) (*AccountBackup, error) {
	var task any
	if taskID != "" {
		task = taskID
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO account_backups (account_id, destination_id, task_id, kind)
		VALUES ($1, $2, $3, $4)
		RETURNING `+backupColumns, accountID, destinationID, task, kind)
	return scanBackup(row)
}

func (s *Backups) ListByAccount(ctx context.Context, accountID string, limit int) ([]AccountBackup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+backupColumns+`
		FROM account_backups WHERE account_id = $1
		ORDER BY started_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing account backups: %w", err)
	}
	defer rows.Close()
	var out []AccountBackup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Backups) GetRun(ctx context.Context, id string) (*AccountBackup, error) {
	return scanBackup(s.pool.QueryRow(ctx, `SELECT `+backupColumns+` FROM account_backups WHERE id = $1`, id))
}

// GetRunByTask resolves the backup row a task belongs to, so the task-result
// path can close it out without the agent knowing the backup row's id.
func (s *Backups) GetRunByTask(ctx context.Context, taskID string) (*AccountBackup, error) {
	return scanBackup(s.pool.QueryRow(ctx, `SELECT `+backupColumns+` FROM account_backups WHERE task_id = $1`, taskID))
}

// CompleteRun records a terminal outcome. Success and failure share one call so
// a caller can never accidentally record only the happy path.
func (s *Backups) CompleteRun(ctx context.Context, id, snapshotID string, sizeBytes int64, runErr string) error {
	status := "completed"
	if runErr != "" {
		status = "failed"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE account_backups
		SET status = $2, snapshot_id = $3, size_bytes = $4, error = $5, completed_at = now()
		WHERE id = $1`, id, status, snapshotID, sizeBytes, runErr)
	if err != nil {
		return fmt.Errorf("store: completing backup run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

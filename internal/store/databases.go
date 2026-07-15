package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Database is a hosted-account MariaDB database record. The password is stored
// only in encrypted form (PasswordEnc); it is never persisted in plaintext.
type Database struct {
	ID          string
	AccountID   string
	Name        string
	DBUser      string
	DBHost      string
	Status      string
	PasswordEnc []byte
	CreatedAt   time.Time
}

type Databases struct {
	pool *pgxpool.Pool
}

func NewDatabases(pool *pgxpool.Pool) *Databases { return &Databases{pool: pool} }

// Create records a new database in 'creating' state before the agent task runs.
func (s *Databases) Create(ctx context.Context, accountID, name, dbUser, dbHost string) (*Database, error) {
	var d Database
	err := s.pool.QueryRow(ctx, `
		INSERT INTO account_databases (account_id, name, db_user, db_host)
		VALUES ($1, $2, $3, $4)
		RETURNING id, account_id, name, db_user, db_host, status, created_at`,
		accountID, name, dbUser, dbHost).
		Scan(&d.ID, &d.AccountID, &d.Name, &d.DBUser, &d.DBHost, &d.Status, &d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: creating database: %w", err)
	}
	return &d, nil
}

// ListByAccount returns an account's databases (no password material).
func (s *Databases) ListByAccount(ctx context.Context, accountID string) ([]Database, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, account_id, name, db_user, db_host, status, created_at
		FROM account_databases WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: listing databases: %w", err)
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.ID, &d.AccountID, &d.Name, &d.DBUser, &d.DBHost, &d.Status, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scanning database: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Databases) GetByID(ctx context.Context, id string) (*Database, error) {
	var d Database
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, name, db_user, db_host, status, password_enc, created_at
		FROM account_databases WHERE id = $1`, id).
		Scan(&d.ID, &d.AccountID, &d.Name, &d.DBUser, &d.DBHost, &d.Status, &d.PasswordEnc, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting database: %w", err)
	}
	return &d, nil
}

// GetByAccountAndName finds a database record by its owning account and name —
// used to apply an async task result back to the right row (the task carries
// the account + name, not the record id).
func (s *Databases) GetByAccountAndName(ctx context.Context, accountID, name string) (*Database, error) {
	var d Database
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, name, db_user, db_host, status, password_enc, created_at
		FROM account_databases WHERE account_id = $1 AND name = $2`, accountID, name).
		Scan(&d.ID, &d.AccountID, &d.Name, &d.DBUser, &d.DBHost, &d.Status, &d.PasswordEnc, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting database by name: %w", err)
	}
	return &d, nil
}

// CountByAccount is used to enforce the package's databases limit.
func (s *Databases) CountByAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM account_databases WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting databases: %w", err)
	}
	return n, nil
}

// SetActive marks a database active and stores its encrypted password.
func (s *Databases) SetActive(ctx context.Context, id string, passwordEnc []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE account_databases SET status = 'active', password_enc = $2, updated_at = now()
		WHERE id = $1`, id, passwordEnc)
	if err != nil {
		return fmt.Errorf("store: activating database: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus updates only the status (e.g. 'failed', 'deleting').
func (s *Databases) SetStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE account_databases SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: setting database status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes the database record (after the agent has dropped it).
func (s *Databases) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM account_databases WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: deleting database record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FTPAccount is a per-account FTP virtual user. Password stored encrypted only.
type FTPAccount struct {
	ID          string
	AccountID   string
	Username    string
	HomeDir     string
	Status      string
	PasswordEnc []byte
	CreatedAt   time.Time
}

type FTPAccounts struct {
	pool *pgxpool.Pool
}

func NewFTPAccounts(pool *pgxpool.Pool) *FTPAccounts { return &FTPAccounts{pool: pool} }

func (s *FTPAccounts) Create(ctx context.Context, accountID, username, homeDir string) (*FTPAccount, error) {
	var f FTPAccount
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ftp_accounts (account_id, username, home_dir)
		VALUES ($1, $2, $3)
		RETURNING id, account_id, username, home_dir, status, created_at`,
		accountID, username, homeDir).
		Scan(&f.ID, &f.AccountID, &f.Username, &f.HomeDir, &f.Status, &f.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: creating ftp account: %w", err)
	}
	return &f, nil
}

func (s *FTPAccounts) ListByAccount(ctx context.Context, accountID string) ([]FTPAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, account_id, username, home_dir, status, created_at
		FROM ftp_accounts WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: listing ftp accounts: %w", err)
	}
	defer rows.Close()
	var out []FTPAccount
	for rows.Next() {
		var f FTPAccount
		if err := rows.Scan(&f.ID, &f.AccountID, &f.Username, &f.HomeDir, &f.Status, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scanning ftp account: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *FTPAccounts) GetByID(ctx context.Context, id string) (*FTPAccount, error) {
	var f FTPAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, username, home_dir, status, password_enc, created_at
		FROM ftp_accounts WHERE id = $1`, id).
		Scan(&f.ID, &f.AccountID, &f.Username, &f.HomeDir, &f.Status, &f.PasswordEnc, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting ftp account: %w", err)
	}
	return &f, nil
}

func (s *FTPAccounts) GetByUsername(ctx context.Context, username string) (*FTPAccount, error) {
	var f FTPAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, username, home_dir, status, password_enc, created_at
		FROM ftp_accounts WHERE username = $1`, username).
		Scan(&f.ID, &f.AccountID, &f.Username, &f.HomeDir, &f.Status, &f.PasswordEnc, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting ftp account by username: %w", err)
	}
	return &f, nil
}

func (s *FTPAccounts) CountByAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ftp_accounts WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting ftp accounts: %w", err)
	}
	return n, nil
}

func (s *FTPAccounts) SetActive(ctx context.Context, id, homeDir string, passwordEnc []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ftp_accounts SET status = 'active', home_dir = $2, password_enc = $3, updated_at = now()
		WHERE id = $1`, id, homeDir, passwordEnc)
	if err != nil {
		return fmt.Errorf("store: activating ftp account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *FTPAccounts) SetStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE ftp_accounts SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: setting ftp status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *FTPAccounts) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ftp_accounts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: deleting ftp account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

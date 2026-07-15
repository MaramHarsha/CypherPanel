package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MailAccount is a hosted-account email mailbox record (control-plane view).
type MailAccount struct {
	ID        string
	AccountID string
	Address   string
	QuotaMB   int
	Status    string
	CreatedAt time.Time
}

type MailAccounts struct {
	pool *pgxpool.Pool
}

func NewMailAccounts(pool *pgxpool.Pool) *MailAccounts { return &MailAccounts{pool: pool} }

func (s *MailAccounts) Create(ctx context.Context, accountID, address string, quotaMB int) (*MailAccount, error) {
	var m MailAccount
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mail_accounts (account_id, address, quota_mb)
		VALUES ($1, $2, $3)
		RETURNING id, account_id, address, quota_mb, status, created_at`,
		accountID, address, quotaMB).
		Scan(&m.ID, &m.AccountID, &m.Address, &m.QuotaMB, &m.Status, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: creating mailbox: %w", err)
	}
	return &m, nil
}

func (s *MailAccounts) ListByAccount(ctx context.Context, accountID string) ([]MailAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, account_id, address, quota_mb, status, created_at
		FROM mail_accounts WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: listing mailboxes: %w", err)
	}
	defer rows.Close()
	var out []MailAccount
	for rows.Next() {
		var m MailAccount
		if err := rows.Scan(&m.ID, &m.AccountID, &m.Address, &m.QuotaMB, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scanning mailbox: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *MailAccounts) GetByID(ctx context.Context, id string) (*MailAccount, error) {
	var m MailAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, address, quota_mb, status, created_at
		FROM mail_accounts WHERE id = $1`, id).
		Scan(&m.ID, &m.AccountID, &m.Address, &m.QuotaMB, &m.Status, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting mailbox: %w", err)
	}
	return &m, nil
}

func (s *MailAccounts) GetByAddress(ctx context.Context, address string) (*MailAccount, error) {
	var m MailAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, account_id, address, quota_mb, status, created_at
		FROM mail_accounts WHERE address = $1`, address).
		Scan(&m.ID, &m.AccountID, &m.Address, &m.QuotaMB, &m.Status, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting mailbox by address: %w", err)
	}
	return &m, nil
}

func (s *MailAccounts) CountByAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mail_accounts WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting mailboxes: %w", err)
	}
	return n, nil
}

func (s *MailAccounts) SetStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mail_accounts SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: setting mailbox status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MailAccounts) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mail_accounts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: deleting mailbox: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

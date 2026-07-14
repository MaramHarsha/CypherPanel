package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Reseller struct {
	ID           string
	Username     string
	Email        string
	MaxAccounts  int
	MaxDiskMB    int
	AccountCount int // current accounts in the pool (derived)
	CreatedAt    time.Time
}

// Pool is a reseller's allocation limits (0 = unlimited).
type Pool struct {
	MaxAccounts int
	MaxDiskMB   int
}

type Resellers struct {
	pool *pgxpool.Pool
}

func NewResellers(pool *pgxpool.Pool) *Resellers {
	return &Resellers{pool: pool}
}

// Create atomically creates the reseller user (role=reseller) and its pool.
func (s *Resellers) Create(ctx context.Context, username, email, passwordHash string, maxAccounts, maxDiskMB int) (*Reseller, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, $3, 'reseller') RETURNING id, created_at`,
		username, email, passwordHash).Scan(&id, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("store: creating reseller user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO reseller_pools (user_id, max_accounts, max_disk_mb)
		VALUES ($1, $2, $3)`, id, maxAccounts, maxDiskMB); err != nil {
		return nil, fmt.Errorf("store: creating reseller pool: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: committing reseller: %w", err)
	}
	return &Reseller{ID: id, Username: username, Email: email,
		MaxAccounts: maxAccounts, MaxDiskMB: maxDiskMB, CreatedAt: createdAt}, nil
}

func (s *Resellers) List(ctx context.Context) ([]Reseller, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.username, u.email, rp.max_accounts, rp.max_disk_mb, u.created_at,
		       (SELECT count(*) FROM accounts a JOIN users eu ON eu.id = a.user_id
		        WHERE eu.reseller_id = u.id) AS account_count
		FROM users u JOIN reseller_pools rp ON rp.user_id = u.id
		WHERE u.role = 'reseller'
		ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: listing resellers: %w", err)
	}
	defer rows.Close()

	var out []Reseller
	for rows.Next() {
		var r Reseller
		if err := rows.Scan(&r.ID, &r.Username, &r.Email, &r.MaxAccounts, &r.MaxDiskMB, &r.CreatedAt, &r.AccountCount); err != nil {
			return nil, fmt.Errorf("store: scanning reseller: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPool returns a reseller's allocation limits.
func (s *Resellers) GetPool(ctx context.Context, userID string) (*Pool, error) {
	var p Pool
	err := s.pool.QueryRow(ctx, `
		SELECT max_accounts, max_disk_mb FROM reseller_pools WHERE user_id = $1`, userID).
		Scan(&p.MaxAccounts, &p.MaxDiskMB)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting reseller pool: %w", err)
	}
	return &p, nil
}

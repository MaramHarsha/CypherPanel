// Package store holds hand-written SQL data access (pgx, no ORM — plan.md
// Section 3). One file per aggregate as the schema grows.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	ResellerID   string // empty when not reseller-owned
	SuspendedAt  *time.Time
	CreatedAt    time.Time
}

type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

func (s *Users) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, email, password_hash, role, COALESCE(reseller_id::text, ''), suspended_at, created_at
		FROM users WHERE username = $1`, username)
	return scanUser(row)
}

func (s *Users) GetByID(ctx context.Context, id string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, email, password_hash, role, COALESCE(reseller_id::text, ''), suspended_at, created_at
		FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func (s *Users) Create(ctx context.Context, username, email, passwordHash, role string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, username, email, password_hash, role, COALESCE(reseller_id::text, ''), suspended_at, created_at`,
		username, email, passwordHash, role)
	return scanUser(row)
}

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.ResellerID, &u.SuspendedAt, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning user: %w", err)
	}
	return &u, nil
}

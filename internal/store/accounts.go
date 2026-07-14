package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Account struct {
	ID             string
	UserID         string
	Username       string // joined from users
	Email          string // joined from users
	ResellerID     string // end user's owning reseller ("" if admin-created)
	ServerID       string
	ServerName     string // joined from servers
	PackageID      string
	PackageName    string // joined from packages
	SystemUsername string
	PrimaryDomain  string
	Status         string
	PHPVersion     string
	PHPSettings    map[string]string
	SSLStatus      string
	SSLExpiresAt   *time.Time
	CreatedAt      time.Time
}

type Accounts struct {
	pool *pgxpool.Pool
}

func NewAccounts(pool *pgxpool.Pool) *Accounts {
	return &Accounts{pool: pool}
}

const accountSelect = `
	SELECT a.id, a.user_id, u.username, u.email, COALESCE(u.reseller_id::text, ''),
	       a.server_id, s.name, a.package_id, p.name,
	       a.system_username, a.primary_domain, a.status, a.php_version, a.php_settings,
	       a.ssl_status, a.ssl_expires_at, a.created_at
	FROM accounts a
	JOIN users u ON u.id = a.user_id
	JOIN servers s ON s.id = a.server_id
	JOIN packages p ON p.id = a.package_id`

// CreateWithUser atomically creates the panel user (end_user role) and the
// hosting account in one transaction — an account without a login or a login
// without an account are both invalid states. resellerID owns the end user
// when a reseller provisions the account ("" when a root admin does).
func (s *Accounts) CreateWithUser(ctx context.Context, username, email, passwordHash, resellerID, serverID, packageID, systemUsername, domain, phpVersion string) (*Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var userID string
	err = tx.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, reseller_id)
		VALUES ($1, $2, $3, 'end_user', NULLIF($4, '')::uuid) RETURNING id`,
		username, email, passwordHash, resellerID).Scan(&userID)
	if err != nil {
		return nil, fmt.Errorf("store: creating account user: %w", err)
	}

	var accountID string
	err = tx.QueryRow(ctx, `
		INSERT INTO accounts (user_id, server_id, package_id, system_username, primary_domain, php_version)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, serverID, packageID, systemUsername, domain, phpVersion).Scan(&accountID)
	if err != nil {
		return nil, fmt.Errorf("store: creating account: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: committing account: %w", err)
	}
	return s.GetByID(ctx, accountID)
}

func (s *Accounts) GetByID(ctx context.Context, id string) (*Account, error) {
	return scanAccount(s.pool.QueryRow(ctx, accountSelect+` WHERE a.id = $1`, id))
}

// List returns accounts visible to a caller: all when resellerID is empty
// (root admin), or only the reseller's own pool when set.
func (s *Accounts) List(ctx context.Context, resellerID string) ([]Account, error) {
	rows, err := s.pool.Query(ctx, accountSelect+`
		WHERE ($1 = '' OR u.reseller_id = $1::uuid)
		ORDER BY a.created_at DESC`, resellerID)
	if err != nil {
		return nil, fmt.Errorf("store: listing accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// CountByReseller returns how many accounts a reseller currently owns (for
// pool-quota enforcement).
func (s *Accounts) CountByReseller(ctx context.Context, resellerID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM accounts a JOIN users u ON u.id = a.user_id
		WHERE u.reseller_id = $1::uuid`, resellerID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: counting reseller accounts: %w", err)
	}
	return n, nil
}

// SetStatus transitions an account and keeps the panel user's suspension flag
// in sync (suspended accounts cannot log in; anything else can).
func (s *Accounts) SetStatus(ctx context.Context, id, status string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE accounts SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: setting account status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	suspend := "NULL"
	if status == "suspended" {
		suspend = "now()"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET suspended_at = `+suspend+`, updated_at = now()
		WHERE id = (SELECT user_id FROM accounts WHERE id = $1)`, id); err != nil {
		return fmt.Errorf("store: syncing user suspension: %w", err)
	}
	return tx.Commit(ctx)
}

// Delete removes a terminated account and its panel user.
func (s *Accounts) Delete(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var userID string
	if err := tx.QueryRow(ctx, `DELETE FROM accounts WHERE id = $1 RETURNING user_id`, id).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: deleting account: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1 AND role = 'end_user'`, userID); err != nil {
		return fmt.Errorf("store: deleting account user: %w", err)
	}
	return tx.Commit(ctx)
}

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	var phpSettings []byte
	err := row.Scan(&a.ID, &a.UserID, &a.Username, &a.Email, &a.ResellerID, &a.ServerID, &a.ServerName,
		&a.PackageID, &a.PackageName, &a.SystemUsername, &a.PrimaryDomain, &a.Status,
		&a.PHPVersion, &phpSettings, &a.SSLStatus, &a.SSLExpiresAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning account: %w", err)
	}
	if len(phpSettings) > 0 {
		_ = json.Unmarshal(phpSettings, &a.PHPSettings)
	}
	return &a, nil
}

// SetSSL updates an account's certificate status and expiry.
func (s *Accounts) SetSSL(ctx context.Context, id, status string, expiresAt *time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET ssl_status = $2, ssl_expires_at = $3, updated_at = now() WHERE id = $1`,
		id, status, expiresAt)
	if err != nil {
		return fmt.Errorf("store: setting ssl status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePHPSettings replaces an account's php.ini override map.
func (s *Accounts) UpdatePHPSettings(ctx context.Context, id string, settings map[string]string) error {
	blob, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("store: encoding php settings: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET php_settings = $2, updated_at = now() WHERE id = $1`, id, blob)
	if err != nil {
		return fmt.Errorf("store: updating php settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

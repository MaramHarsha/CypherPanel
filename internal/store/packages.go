package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInUse is returned when deleting a resource that others still reference.
var ErrInUse = errors.New("store: resource is in use")

// PackageLimits is the validated shape of the packages.limits jsonb document.
// Zero means "unlimited" for counts and "no cap" for resources (MVP).
type PackageLimits struct {
	DiskMB        int `json:"disk_mb"`
	// Inodes caps total files+directories. Separate from DiskMB because the
	// two exhaust independently: a tiny-file flood fills the inode table long
	// before it fills the disk.
	Inodes        int `json:"inodes"`
	BandwidthMB   int `json:"bandwidth_mb"`
	Domains       int `json:"domains"`
	Databases     int `json:"databases"`
	EmailAccounts int `json:"email_accounts"`
	CPUQuotaPct   int `json:"cpu_quota_pct"`
	MemoryMaxMB   int `json:"memory_max_mb"`
}

type Package struct {
	ID        string
	Name      string
	OwnerID   string
	Limits    PackageLimits
	CreatedAt time.Time
}

type Packages struct {
	pool *pgxpool.Pool
}

func NewPackages(pool *pgxpool.Pool) *Packages {
	return &Packages{pool: pool}
}

func (s *Packages) Create(ctx context.Context, name, ownerID string, limits PackageLimits) (*Package, error) {
	blob, err := json.Marshal(limits)
	if err != nil {
		return nil, fmt.Errorf("store: encoding limits: %w", err)
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO packages (name, owner_id, limits)
		VALUES ($1, $2, $3)
		RETURNING id, name, owner_id, limits, created_at`, name, ownerID, blob)
	return scanPackage(row)
}

// List returns packages visible to a caller: all packages when ownerID is
// empty (root admin), or only the caller's own when set (reseller).
func (s *Packages) List(ctx context.Context, ownerID string) ([]Package, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, owner_id, limits, created_at FROM packages
		WHERE ($1 = '' OR owner_id = $1::uuid)
		ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: listing packages: %w", err)
	}
	defer rows.Close()

	var out []Package
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Packages) GetByID(ctx context.Context, id string) (*Package, error) {
	return scanPackage(s.pool.QueryRow(ctx, `
		SELECT id, name, owner_id, limits, created_at FROM packages WHERE id = $1`, id))
}

func (s *Packages) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM packages WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return ErrInUse
	}
	if err != nil {
		return fmt.Errorf("store: deleting package: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanPackage(row pgx.Row) (*Package, error) {
	var p Package
	var blob []byte
	err := row.Scan(&p.ID, &p.Name, &p.OwnerID, &blob, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning package: %w", err)
	}
	if err := json.Unmarshal(blob, &p.Limits); err != nil {
		return nil, fmt.Errorf("store: decoding package limits: %w", err)
	}
	return &p, nil
}

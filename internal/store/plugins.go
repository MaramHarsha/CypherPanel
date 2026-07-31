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

// Plugin is an installed plugin record. Manifest holds the validated
// plugin.yaml, which is what the UI renders its declared surfaces from.
type Plugin struct {
	ID          string
	Name        string
	Version     string
	Kind        string
	Enabled     bool
	Manifest    json.RawMessage
	InstalledAt time.Time
}

type Plugins struct {
	pool *pgxpool.Pool
}

func NewPlugins(pool *pgxpool.Pool) *Plugins {
	return &Plugins{pool: pool}
}

const pluginColumns = `id, name, version, kind, enabled, manifest, installed_at`

func scanPlugin(row pgx.Row) (*Plugin, error) {
	var p Plugin
	err := row.Scan(&p.ID, &p.Name, &p.Version, &p.Kind, &p.Enabled, &p.Manifest, &p.InstalledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning plugin: %w", err)
	}
	return &p, nil
}

func (s *Plugins) List(ctx context.Context) ([]Plugin, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pluginColumns+` FROM plugins ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: listing plugins: %w", err)
	}
	defer rows.Close()

	var out []Plugin
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListEnabled returns only enabled plugins — the set whose declared UI
// surfaces the panel actually renders.
func (s *Plugins) ListEnabled(ctx context.Context) ([]Plugin, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE enabled ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: listing enabled plugins: %w", err)
	}
	defer rows.Close()

	var out []Plugin
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Plugins) GetByName(ctx context.Context, name string) (*Plugin, error) {
	return scanPlugin(s.pool.QueryRow(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE name = $1`, name))
}

// Install records a validated manifest. Re-installing the same plugin name is
// an upgrade in place: the version and manifest are replaced while the
// enabled state is preserved, so upgrading does not silently turn a plugin off
// (or on) behind the operator's back.
func (s *Plugins) Install(ctx context.Context, name, version, kind string, manifest []byte) (*Plugin, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO plugins (name, version, kind, manifest)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE
		SET version = EXCLUDED.version, kind = EXCLUDED.kind, manifest = EXCLUDED.manifest
		RETURNING `+pluginColumns, name, version, kind, manifest)
	return scanPlugin(row)
}

func (s *Plugins) SetEnabled(ctx context.Context, name string, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE plugins SET enabled = $2 WHERE name = $1`, name, enabled)
	if err != nil {
		return fmt.Errorf("store: setting plugin enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Plugins) Delete(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM plugins WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("store: deleting plugin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

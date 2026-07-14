package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Plugin is an installed plugin record. The loader/runtime is post-MVP; for
// now this backs the reserved read-only listing endpoint.
type Plugin struct {
	Name        string
	Version     string
	Kind        string
	Enabled     bool
	InstalledAt time.Time
}

type Plugins struct {
	pool *pgxpool.Pool
}

func NewPlugins(pool *pgxpool.Pool) *Plugins {
	return &Plugins{pool: pool}
}

func (s *Plugins) List(ctx context.Context) ([]Plugin, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, version, kind, enabled, installed_at FROM plugins ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: listing plugins: %w", err)
	}
	defer rows.Close()

	var out []Plugin
	for rows.Next() {
		var p Plugin
		if err := rows.Scan(&p.Name, &p.Version, &p.Kind, &p.Enabled, &p.InstalledAt); err != nil {
			return nil, fmt.Errorf("store: scanning plugin: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

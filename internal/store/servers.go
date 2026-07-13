package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	ID          string
	Name        string
	Hostname    string
	IPAddress   string
	AgentStatus string
	LastSeenAt  *time.Time
	CreatedAt   time.Time
}

type Servers struct {
	pool *pgxpool.Pool
}

func NewServers(pool *pgxpool.Pool) *Servers {
	return &Servers{pool: pool}
}

// UpsertByHostname registers a server or refreshes an existing one (agent
// re-registration after reinstall/restart is normal, not an error).
func (s *Servers) UpsertByHostname(ctx context.Context, hostname, ip string) (*Server, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO servers (name, hostname, ip_address, agent_status, last_seen_at)
		VALUES ($1, $1, $2::inet, 'online', now())
		ON CONFLICT (name) DO UPDATE
			SET hostname = EXCLUDED.hostname,
			    ip_address = EXCLUDED.ip_address,
			    agent_status = 'online',
			    last_seen_at = now()
		RETURNING id, name, hostname, ip_address::text, agent_status, last_seen_at, created_at`,
		hostname, ip)

	var srv Server
	if err := row.Scan(&srv.ID, &srv.Name, &srv.Hostname, &srv.IPAddress, &srv.AgentStatus, &srv.LastSeenAt, &srv.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: upserting server %s: %w", hostname, err)
	}
	return &srv, nil
}

// Heartbeat marks a server online and refreshes last_seen_at.
func (s *Servers) Heartbeat(ctx context.Context, serverID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE servers SET agent_status = 'online', last_seen_at = now() WHERE id = $1`, serverID)
	if err != nil {
		return fmt.Errorf("store: heartbeat for %s: %w", serverID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

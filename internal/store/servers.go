package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceStatus is a managed system service's latest reported state.
type ServiceStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Server struct {
	ID          string
	Name        string
	Hostname    string
	IPAddress   string
	AgentStatus string
	LastSeenAt  *time.Time
	CreatedAt   time.Time
	Stats       HostStats
	Services    []ServiceStatus
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
		RETURNING id, name, hostname, host(ip_address), agent_status, last_seen_at, created_at`,
		hostname, ip)

	var srv Server
	if err := row.Scan(&srv.ID, &srv.Name, &srv.Hostname, &srv.IPAddress, &srv.AgentStatus, &srv.LastSeenAt, &srv.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: upserting server %s: %w", hostname, err)
	}
	return &srv, nil
}

// List returns all registered servers, most recently seen first.
func (s *Servers) List(ctx context.Context) ([]Server, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, hostname, host(ip_address), agent_status, last_seen_at, created_at,
		       load_1m, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, services
		FROM servers ORDER BY last_seen_at DESC NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("store: listing servers: %w", err)
	}
	defer rows.Close()

	var out []Server
	for rows.Next() {
		var srv Server
		var memTotal, memUsed, diskTotal, diskUsed int64
		var svcBlob []byte
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.Hostname, &srv.IPAddress, &srv.AgentStatus,
			&srv.LastSeenAt, &srv.CreatedAt,
			&srv.Stats.Load1m, &memTotal, &memUsed, &diskTotal, &diskUsed, &svcBlob); err != nil {
			return nil, fmt.Errorf("store: scanning server: %w", err)
		}
		srv.Stats.MemoryTotalBytes = uint64(memTotal)
		srv.Stats.MemoryUsedBytes = uint64(memUsed)
		srv.Stats.DiskTotalBytes = uint64(diskTotal)
		srv.Stats.DiskUsedBytes = uint64(diskUsed)
		if len(svcBlob) > 0 {
			_ = json.Unmarshal(svcBlob, &srv.Services)
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// HostStats is the latest host snapshot reported with a heartbeat.
type HostStats struct {
	Load1m           float64
	MemoryTotalBytes uint64
	MemoryUsedBytes  uint64
	DiskTotalBytes   uint64
	DiskUsedBytes    uint64
}

// Heartbeat marks a server online, refreshes last_seen_at, and stores the
// latest host snapshot and managed-service states.
func (s *Servers) Heartbeat(ctx context.Context, serverID string, stats HostStats, svcs []ServiceStatus) error {
	if svcs == nil {
		svcs = []ServiceStatus{}
	}
	svcBlob, err := json.Marshal(svcs)
	if err != nil {
		return fmt.Errorf("store: encoding services: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE servers SET
			agent_status = 'online',
			last_seen_at = now(),
			load_1m = $2,
			memory_total_bytes = $3,
			memory_used_bytes = $4,
			disk_total_bytes = $5,
			disk_used_bytes = $6,
			services = $7
		WHERE id = $1`,
		serverID, stats.Load1m,
		int64(stats.MemoryTotalBytes), int64(stats.MemoryUsedBytes),
		int64(stats.DiskTotalBytes), int64(stats.DiskUsedBytes), svcBlob)
	if err != nil {
		return fmt.Errorf("store: heartbeat for %s: %w", serverID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

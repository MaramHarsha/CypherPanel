// Package audit records every privileged action (who did what to which
// resource, from where). Writes are synchronous: an unrecorded privileged
// action is worse than a slower one.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	ActorID    string         // user UUID; empty for system/unauthenticated actions
	ActorRole  string
	Action     string         // e.g. "auth.login", "account.suspend"
	TargetType string         // e.g. "user", "account", "server"
	TargetID   string
	Detail     map[string]any // action-specific context; must not contain secrets
	IP         string
}

type Logger struct {
	pool *pgxpool.Pool
}

func NewLogger(pool *pgxpool.Pool) *Logger {
	return &Logger{pool: pool}
}

func (l *Logger) Record(ctx context.Context, e Entry) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return fmt.Errorf("audit: encoding detail: %w", err)
	}
	if e.Detail == nil {
		detail = []byte("{}")
	}

	_, err = l.pool.Exec(ctx, `
		INSERT INTO audit_log (actor_id, actor_role, action, target_type, target_id, detail, ip_address)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, ''), $3, $4, $5, $6, NULLIF($7, '')::inet)`,
		e.ActorID, e.ActorRole, e.Action, e.TargetType, e.TargetID, detail, e.IP,
	)
	if err != nil {
		return fmt.Errorf("audit: recording %q: %w", e.Action, err)
	}
	return nil
}

// Record is a stored audit entry for the dashboard (append-only history).
type Record struct {
	ID         string         `json:"id"`
	ActorName  string         `json:"actor_name"`
	ActorRole  string         `json:"actor_role"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
	IP         string         `json:"ip_address"`
	CreatedAt  time.Time      `json:"created_at"`
}

// List returns audit entries newest-first, optionally filtered by action prefix.
func (l *Logger) List(ctx context.Context, action string, limit, offset int) ([]Record, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := l.pool.Query(ctx, `
		SELECT a.id, COALESCE(u.username, ''), COALESCE(a.actor_role, ''), a.action,
		       a.target_type, a.target_id, a.detail, COALESCE(host(a.ip_address), ''), a.created_at
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.actor_id
		WHERE ($1 = '' OR a.action LIKE $1 || '%')
		ORDER BY a.created_at DESC
		LIMIT $2 OFFSET $3`, action, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("audit: listing: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		var detail []byte
		if err := rows.Scan(&r.ID, &r.ActorName, &r.ActorRole, &r.Action,
			&r.TargetType, &r.TargetID, &detail, &r.IP, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("audit: scanning: %w", err)
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &r.Detail)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Prune deletes audit entries older than the cutoff (retention policy). Audit
// rows are append-only; retention is age-based pruning, never in-place edits.
func (l *Logger) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := l.pool.Exec(ctx, `DELETE FROM audit_log WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("audit: pruning: %w", err)
	}
	return tag.RowsAffected(), nil
}

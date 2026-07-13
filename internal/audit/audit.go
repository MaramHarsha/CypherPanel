// Package audit records every privileged action (who did what to which
// resource, from where). Writes are synchronous: an unrecorded privileged
// action is worse than a slower one.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

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

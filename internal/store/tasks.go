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

type Task struct {
	ID        string
	ServerID  string
	Type      string
	Payload   json.RawMessage
	Status    string
	Error     string
	CreatedBy string
	AccountID string // set when the task acts on a hosting account
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Tasks struct {
	pool *pgxpool.Pool
}

func NewTasks(pool *pgxpool.Pool) *Tasks {
	return &Tasks{pool: pool}
}

func (s *Tasks) Create(ctx context.Context, serverID, taskType string, payload json.RawMessage, createdBy, accountID string) (*Task, error) {
	if payload == nil {
		payload = json.RawMessage("{}")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO tasks (server_id, type, payload, created_by, account_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid)
		RETURNING id, server_id, type, payload, status, error, COALESCE(created_by::text, ''), COALESCE(account_id::text, ''), created_at, updated_at`,
		serverID, taskType, payload, createdBy, accountID)
	return scanTask(row)
}

func (s *Tasks) GetByID(ctx context.Context, id string) (*Task, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, server_id, type, payload, status, error, COALESCE(created_by::text, ''), COALESCE(account_id::text, ''), created_at, updated_at
		FROM tasks WHERE id = $1`, id)
	return scanTask(row)
}

// SetResult records the agent-reported outcome. Results are idempotent: a
// redelivered report for an already-finished task is a no-op, not an error.
func (s *Tasks) SetResult(ctx context.Context, id, status, errMsg string) error {
	if status != "succeeded" && status != "failed" {
		return fmt.Errorf("store: invalid task status %q", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tasks SET status = $2, error = $3, updated_at = now()
		WHERE id = $1 AND status = 'pending'`, id, status, errMsg)
	if err != nil {
		return fmt.Errorf("store: setting task result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either unknown ID or already finalized; distinguish for the caller.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("store: checking task %s: %w", id, err)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ServerID, &t.Type, &t.Payload, &t.Status, &t.Error, &t.CreatedBy, &t.AccountID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scanning task: %w", err)
	}
	return &t, nil
}

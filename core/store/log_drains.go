package store

// Log drain persistence (log-drains.md §3).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func logDrainFromRow(r db.LogDrain) domain.LogDrain {
	return domain.LogDrain{
		ID: r.ID, Name: r.Name, Kind: r.Kind,
		ProjectID: r.ProjectID.String, TargetID: r.TargetID.String,
		ConfigCT: r.ConfigCt, ConfigNonce: r.ConfigNonce, Enabled: r.Enabled,
		LastShippedAt: ptrTime(r.LastShippedAt), LastError: r.LastError,
		LastErrorAt: ptrTime(r.LastErrorAt), DroppedLines: r.DroppedLines,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func nullText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func (s *Store) CreateLogDrain(ctx context.Context, d domain.LogDrain) (domain.LogDrain, error) {
	row, err := s.q.CreateLogDrain(ctx, db.CreateLogDrainParams{
		ID: d.ID, Name: d.Name, Kind: d.Kind,
		ProjectID: nullText(d.ProjectID), TargetID: nullText(d.TargetID),
		ConfigCt: d.ConfigCT, ConfigNonce: d.ConfigNonce, Enabled: d.Enabled,
	})
	if err != nil {
		return domain.LogDrain{}, wrap("creating the log drain", err)
	}
	return logDrainFromRow(row), nil
}

func (s *Store) GetLogDrain(ctx context.Context, id string) (domain.LogDrain, error) {
	row, err := s.q.GetLogDrain(ctx, id)
	if err != nil {
		return domain.LogDrain{}, wrap("reading the log drain", err)
	}
	return logDrainFromRow(row), nil
}

func (s *Store) ListLogDrains(ctx context.Context) ([]domain.LogDrain, error) {
	rows, err := s.q.ListLogDrains(ctx)
	if err != nil {
		return nil, wrap("listing log drains", err)
	}
	return mapLogDrains(rows), nil
}

func (s *Store) ListEnabledLogDrains(ctx context.Context) ([]domain.LogDrain, error) {
	rows, err := s.q.ListEnabledLogDrains(ctx)
	if err != nil {
		return nil, wrap("listing enabled log drains", err)
	}
	return mapLogDrains(rows), nil
}

func mapLogDrains(rows []db.LogDrain) []domain.LogDrain {
	out := make([]domain.LogDrain, 0, len(rows))
	for _, r := range rows {
		out = append(out, logDrainFromRow(r))
	}
	return out
}

func (s *Store) UpdateLogDrain(ctx context.Context, d domain.LogDrain) (domain.LogDrain, error) {
	row, err := s.q.UpdateLogDrain(ctx, db.UpdateLogDrainParams{
		ID: d.ID, Name: d.Name,
		ProjectID: nullText(d.ProjectID), TargetID: nullText(d.TargetID),
		ConfigCt: d.ConfigCT, ConfigNonce: d.ConfigNonce, Enabled: d.Enabled,
	})
	if err != nil {
		return domain.LogDrain{}, wrap("updating the log drain", err)
	}
	return logDrainFromRow(row), nil
}

func (s *Store) SetLogDrainEnabled(ctx context.Context, id string, enabled bool) (domain.LogDrain, error) {
	row, err := s.q.SetLogDrainEnabled(ctx, db.SetLogDrainEnabledParams{ID: id, Enabled: enabled})
	if err != nil {
		return domain.LogDrain{}, wrap("updating the log drain", err)
	}
	return logDrainFromRow(row), nil
}

func (s *Store) DeleteLogDrain(ctx context.Context, id string) error {
	if err := s.q.DeleteLogDrain(ctx, id); err != nil {
		return wrap("deleting the log drain", err)
	}
	return nil
}

// RecordLogDrainShipped clears the error too: a drain that recovered must not
// keep showing why it once failed.
func (s *Store) RecordLogDrainShipped(ctx context.Context, id string, at time.Time) error {
	if err := s.q.RecordLogDrainShipped(ctx, db.RecordLogDrainShippedParams{ID: id, LastShippedAt: tsFromTime(at)}); err != nil {
		return wrap("recording a shipped batch", err)
	}
	return nil
}

func (s *Store) RecordLogDrainError(ctx context.Context, id, detail string, at time.Time) error {
	if err := s.q.RecordLogDrainError(ctx, db.RecordLogDrainErrorParams{
		ID: id, LastError: detail, LastErrorAt: tsFromTime(at),
	}); err != nil {
		return wrap("recording a drain failure", err)
	}
	return nil
}

func (s *Store) AddLogDrainDropped(ctx context.Context, id string, n int64) error {
	if err := s.q.AddLogDrainDropped(ctx, db.AddLogDrainDroppedParams{ID: id, DroppedLines: n}); err != nil {
		return wrap("recording dropped lines", err)
	}
	return nil
}

// ListLogDrainsByTarget answers the 409 that NAMES what blocks a backup
// target's delete, rather than counting it.
func (s *Store) ListLogDrainsByTarget(ctx context.Context, targetID string) ([]domain.LogDrain, error) {
	rows, err := s.q.ListLogDrainsByTarget(ctx, nullText(targetID))
	if err != nil {
		return nil, wrap("listing drains by target", err)
	}
	return mapLogDrains(rows), nil
}

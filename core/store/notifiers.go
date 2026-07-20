package store

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreateNotifier inserts a notifier row (notifications.md §2).
func (s *Store) CreateNotifier(ctx context.Context, n domain.Notifier) (domain.Notifier, error) {
	row, err := s.q.CreateNotifier(ctx, db.CreateNotifierParams{
		ID:          n.ID,
		ProjectID:   n.ProjectID,
		Name:        n.Name,
		Channel:     n.Channel,
		ConfigCt:    n.ConfigCT,
		ConfigNonce: n.ConfigNonce,
		Events:      n.Events,
		Enabled:     n.Enabled,
	})
	if err != nil {
		return domain.Notifier{}, wrapCreate("creating notifier", err)
	}
	return notifierFromRow(row), nil
}

func (s *Store) GetNotifier(ctx context.Context, id string) (domain.Notifier, error) {
	row, err := s.q.GetNotifier(ctx, id)
	if err != nil {
		return domain.Notifier{}, wrap("getting notifier", err)
	}
	return notifierFromRow(row), nil
}

func (s *Store) ListNotifiersByProject(ctx context.Context, projectID string) ([]domain.Notifier, error) {
	rows, err := s.q.ListNotifiersByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: listing notifiers: %w", err)
	}
	out := make([]domain.Notifier, 0, len(rows))
	for _, r := range rows {
		out = append(out, notifierFromRow(r))
	}
	return out, nil
}

// ListEnabledNotifiersForEvent returns the enabled notifiers in a project
// subscribed to eventType — the dispatcher's input (notifications.md §4).
func (s *Store) ListEnabledNotifiersForEvent(ctx context.Context, projectID, eventType string) ([]domain.Notifier, error) {
	rows, err := s.q.ListEnabledNotifiersForEvent(ctx, db.ListEnabledNotifiersForEventParams{
		ProjectID: projectID,
		EventType: eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing notifiers for event: %w", err)
	}
	out := make([]domain.Notifier, 0, len(rows))
	for _, r := range rows {
		out = append(out, notifierFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateNotifier(ctx context.Context, n domain.Notifier) (domain.Notifier, error) {
	row, err := s.q.UpdateNotifier(ctx, db.UpdateNotifierParams{
		ID:          n.ID,
		Name:        n.Name,
		ConfigCt:    n.ConfigCT,
		ConfigNonce: n.ConfigNonce,
		Events:      n.Events,
		Enabled:     n.Enabled,
	})
	if err != nil {
		return domain.Notifier{}, wrapUpdate("updating notifier", err)
	}
	return notifierFromRow(row), nil
}

func (s *Store) DeleteNotifier(ctx context.Context, id string) error {
	if err := s.q.DeleteNotifier(ctx, id); err != nil {
		return wrapDelete("deleting notifier", err)
	}
	return nil
}

func notifierFromRow(r db.Notifier) domain.Notifier {
	return domain.Notifier{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		Name:        r.Name,
		Channel:     r.Channel,
		ConfigCT:    r.ConfigCt,
		ConfigNonce: r.ConfigNonce,
		Events:      r.Events,
		Enabled:     r.Enabled,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}

package store

// Status pages, their components and the interval time series (status-pages.md
// §5).

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func statusPageFromRow(r db.StatusPage) domain.StatusPage {
	return domain.StatusPage{
		ID: r.ID, ProjectID: r.ProjectID, Slug: r.Slug, Title: r.Title,
		Enabled: r.Enabled, Domain: r.Domain, HTTPS: r.Https,
		RouteServerID: r.RouteServerID.String,
		CreatedAt:     r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func statusComponentFromRow(r db.StatusPageComponent) domain.StatusPageComponent {
	return domain.StatusPageComponent{
		ID: r.ID, StatusPageID: r.StatusPageID, ResourceKind: r.ResourceKind,
		ResourceID: r.ResourceID, Label: r.Label, Position: int(r.Position),
		TrackingSince: r.TrackingSince.Time,
	}
}

func statusIntervalFromRow(r db.StatusInterval) domain.StatusInterval {
	return domain.StatusInterval{
		ID: r.ID, ComponentID: r.ComponentID, State: r.State,
		StartedAt: r.StartedAt.Time, EndedAt: ptrTime(r.EndedAt), Message: r.Message,
	}
}

func (s *Store) UpsertStatusPage(ctx context.Context, p domain.StatusPage) (domain.StatusPage, error) {
	server := pgtype.Text{}
	if p.RouteServerID != "" {
		server = pgText(p.RouteServerID)
	}
	row, err := s.q.UpsertStatusPage(ctx, db.UpsertStatusPageParams{
		ID: p.ID, ProjectID: p.ProjectID, Slug: p.Slug, Title: p.Title,
		Enabled: p.Enabled, Domain: p.Domain, Https: p.HTTPS, RouteServerID: server,
	})
	if err != nil {
		return domain.StatusPage{}, wrap("upserting status page", err)
	}
	return statusPageFromRow(row), nil
}

func (s *Store) GetStatusPageByProject(ctx context.Context, projectID string) (domain.StatusPage, error) {
	row, err := s.q.GetStatusPageByProject(ctx, projectID)
	if err != nil {
		return domain.StatusPage{}, wrap("reading status page", err)
	}
	return statusPageFromRow(row), nil
}

func (s *Store) GetStatusPage(ctx context.Context, id string) (domain.StatusPage, error) {
	row, err := s.q.GetStatusPage(ctx, id)
	if err != nil {
		return domain.StatusPage{}, wrap("reading status page", err)
	}
	return statusPageFromRow(row), nil
}

func (s *Store) GetStatusPageBySlug(ctx context.Context, slug string) (domain.StatusPage, error) {
	row, err := s.q.GetStatusPageBySlug(ctx, slug)
	if err != nil {
		return domain.StatusPage{}, wrap("reading status page", err)
	}
	return statusPageFromRow(row), nil
}

func (s *Store) DeleteStatusPage(ctx context.Context, projectID string) error {
	if err := s.q.DeleteStatusPage(ctx, projectID); err != nil {
		return wrap("deleting status page", err)
	}
	return nil
}

func (s *Store) ListEnabledStatusPages(ctx context.Context) ([]domain.StatusPage, error) {
	rows, err := s.q.ListEnabledStatusPages(ctx)
	if err != nil {
		return nil, wrap("listing enabled status pages", err)
	}
	out := make([]domain.StatusPage, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusPageFromRow(r))
	}
	return out, nil
}

func (s *Store) ListRoutableStatusPages(ctx context.Context) ([]domain.StatusPage, error) {
	rows, err := s.q.ListRoutableStatusPages(ctx)
	if err != nil {
		return nil, wrap("listing routable status pages", err)
	}
	out := make([]domain.StatusPage, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusPageFromRow(r))
	}
	return out, nil
}

func (s *Store) UpsertStatusPageComponent(ctx context.Context, c domain.StatusPageComponent) (domain.StatusPageComponent, error) {
	row, err := s.q.CreateStatusPageComponent(ctx, db.CreateStatusPageComponentParams{
		ID: c.ID, StatusPageID: c.StatusPageID, ResourceKind: c.ResourceKind,
		ResourceID: c.ResourceID, Label: c.Label, Position: int32(c.Position),
	})
	if err != nil {
		return domain.StatusPageComponent{}, wrap("saving status page component", err)
	}
	return statusComponentFromRow(row), nil
}

func (s *Store) ListStatusPageComponents(ctx context.Context, pageID string) ([]domain.StatusPageComponent, error) {
	rows, err := s.q.ListStatusPageComponents(ctx, pageID)
	if err != nil {
		return nil, wrap("listing status page components", err)
	}
	out := make([]domain.StatusPageComponent, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusComponentFromRow(r))
	}
	return out, nil
}

// DeleteStatusPageComponentsNotIn is how the wholesale PUT removes what the
// operator dropped. An empty keep list removes every component, which is what
// "the list is now empty" means.
func (s *Store) DeleteStatusPageComponentsNotIn(ctx context.Context, pageID string, keep []string) error {
	if keep == nil {
		keep = []string{}
	}
	if err := s.q.DeleteStatusPageComponentsNotIn(ctx, db.DeleteStatusPageComponentsNotInParams{
		StatusPageID: pageID, KeepIds: keep,
	}); err != nil {
		return wrap("pruning status page components", err)
	}
	return nil
}

func (s *Store) ListAllTrackedComponents(ctx context.Context) ([]domain.StatusPageComponent, error) {
	rows, err := s.q.ListAllTrackedComponents(ctx)
	if err != nil {
		return nil, wrap("listing tracked components", err)
	}
	out := make([]domain.StatusPageComponent, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusComponentFromRow(r))
	}
	return out, nil
}

func (s *Store) GetOpenStatusInterval(ctx context.Context, componentID string) (domain.StatusInterval, error) {
	row, err := s.q.GetOpenStatusInterval(ctx, componentID)
	if err != nil {
		return domain.StatusInterval{}, wrap("reading open status interval", err)
	}
	return statusIntervalFromRow(row), nil
}

func (s *Store) OpenStatusInterval(ctx context.Context, id, componentID, state string, at time.Time) (domain.StatusInterval, error) {
	row, err := s.q.OpenStatusInterval(ctx, db.OpenStatusIntervalParams{
		ID: id, ComponentID: componentID, State: state, StartedAt: tsFromTime(at),
	})
	if err != nil {
		return domain.StatusInterval{}, wrap("opening status interval", err)
	}
	return statusIntervalFromRow(row), nil
}

func (s *Store) CloseStatusInterval(ctx context.Context, id string, at time.Time) error {
	if err := s.q.CloseStatusInterval(ctx, db.CloseStatusIntervalParams{ID: id, EndedAt: tsFromTime(at)}); err != nil {
		return wrap("closing status interval", err)
	}
	return nil
}

func (s *Store) GetStatusInterval(ctx context.Context, id string) (domain.StatusInterval, error) {
	row, err := s.q.GetStatusInterval(ctx, id)
	if err != nil {
		return domain.StatusInterval{}, wrap("reading status interval", err)
	}
	return statusIntervalFromRow(row), nil
}

func (s *Store) SetStatusIntervalMessage(ctx context.Context, id, message string) error {
	if err := s.q.SetStatusIntervalMessage(ctx, db.SetStatusIntervalMessageParams{ID: id, Message: message}); err != nil {
		return wrap("annotating status interval", err)
	}
	return nil
}

func (s *Store) ListStatusIntervalsSince(ctx context.Context, componentID string, since time.Time) ([]domain.StatusInterval, error) {
	rows, err := s.q.ListStatusIntervalsSince(ctx, db.ListStatusIntervalsSinceParams{
		ComponentID: componentID, EndedAt: tsFromTime(since),
	})
	if err != nil {
		return nil, wrap("listing status intervals", err)
	}
	out := make([]domain.StatusInterval, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusIntervalFromRow(r))
	}
	return out, nil
}

// LastEvaluation is the panel-wide marker the evaluator stamps every tick. A
// zero time means it has never run, which on boot is not a gap — there is no
// "before" to have missed.
func (s *Store) LastEvaluation(ctx context.Context) (time.Time, error) {
	row, err := s.q.GetLastEvaluation(ctx)
	if err != nil {
		return time.Time{}, wrap("reading last status evaluation", err)
	}
	return row.EvaluatedAt.Time, nil
}

func (s *Store) StampEvaluation(ctx context.Context, at time.Time) error {
	if err := s.q.StampEvaluation(ctx, tsFromTime(at)); err != nil {
		return wrap("stamping status evaluation", err)
	}
	return nil
}

func (s *Store) DeleteStatusIntervalsBefore(ctx context.Context, cutoff time.Time, limit int) error {
	if err := s.q.DeleteStatusIntervalsBefore(ctx, db.DeleteStatusIntervalsBeforeParams{
		EndedAt: tsFromTime(cutoff), Limit: int32(limit),
	}); err != nil {
		return fmt.Errorf("store: sweeping status intervals: %w", err)
	}
	return nil
}

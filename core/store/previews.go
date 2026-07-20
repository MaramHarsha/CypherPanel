package store

import (
	"context"
	"fmt"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreatePreview inserts a preview row (preview-environments.md §3).
func (s *Store) CreatePreview(ctx context.Context, p domain.Preview) (domain.Preview, error) {
	row, err := s.q.CreatePreview(ctx, db.CreatePreviewParams{
		ID:            p.ID,
		SourceAppID:   p.SourceAppID,
		EnvironmentID: p.EnvironmentID,
		PreviewAppID:  textFromPtr(p.PreviewAppID),
		PrNumber:      int32(p.PRNumber),
		PrBranch:      p.PRBranch,
		Domain:        p.Domain,
		Status:        p.Status,
		ExpiresAt:     tsFromPtr(p.ExpiresAt),
	})
	if err != nil {
		return domain.Preview{}, wrapCreate("creating preview", err)
	}
	return previewFromRow(row), nil
}

func (s *Store) GetPreview(ctx context.Context, id string) (domain.Preview, error) {
	row, err := s.q.GetPreview(ctx, id)
	if err != nil {
		return domain.Preview{}, wrap("getting preview", err)
	}
	return previewFromRow(row), nil
}

// GetPreviewByPR returns the live preview for a (source app, PR) pair, or
// ErrNotFound if none exists yet.
func (s *Store) GetPreviewByPR(ctx context.Context, sourceAppID string, prNumber int) (domain.Preview, error) {
	row, err := s.q.GetPreviewByPR(ctx, db.GetPreviewByPRParams{SourceAppID: sourceAppID, PrNumber: int32(prNumber)})
	if err != nil {
		return domain.Preview{}, wrap("getting preview by pr", err)
	}
	return previewFromRow(row), nil
}

func (s *Store) ListPreviewsBySourceApp(ctx context.Context, sourceAppID string) ([]domain.Preview, error) {
	rows, err := s.q.ListPreviewsBySourceApp(ctx, sourceAppID)
	if err != nil {
		return nil, fmt.Errorf("store: listing previews: %w", err)
	}
	out := make([]domain.Preview, 0, len(rows))
	for _, r := range rows {
		out = append(out, previewFromRow(r))
	}
	return out, nil
}

func (s *Store) SetPreviewStatus(ctx context.Context, id, status string) error {
	if err := s.q.SetPreviewStatus(ctx, db.SetPreviewStatusParams{ID: id, Status: status}); err != nil {
		return wrapUpdate("setting preview status", err)
	}
	return nil
}

// ListExpiredPreviews returns previews past cutoff not already tearing down —
// the TTL sweeper's input (preview-environments.md §4).
func (s *Store) ListExpiredPreviews(ctx context.Context, cutoff time.Time) ([]domain.Preview, error) {
	rows, err := s.q.ListExpiredPreviews(ctx, tsFromTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("store: listing expired previews: %w", err)
	}
	out := make([]domain.Preview, 0, len(rows))
	for _, r := range rows {
		out = append(out, previewFromRow(r))
	}
	return out, nil
}

func (s *Store) DeletePreview(ctx context.Context, id string) error {
	if err := s.q.DeletePreview(ctx, id); err != nil {
		return wrapDelete("deleting preview", err)
	}
	return nil
}

func previewFromRow(r db.Preview) domain.Preview {
	return domain.Preview{
		ID:            r.ID,
		SourceAppID:   r.SourceAppID,
		EnvironmentID: r.EnvironmentID,
		PreviewAppID:  ptrFromText(r.PreviewAppID),
		PRNumber:      int(r.PrNumber),
		PRBranch:      r.PrBranch,
		Domain:        r.Domain,
		Status:        r.Status,
		ExpiresAt:     ptrTime(r.ExpiresAt),
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

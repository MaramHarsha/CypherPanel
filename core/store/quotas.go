package store

// Resource quota persistence and the meter (resource-quotas.md §8).
//
// THE METER IS COMPUTED, NEVER MATERIALISED. Three bounded queries at
// admission: memory over the declared limits, disk over the latest bucket per
// resource, previews as a count. A materialised meter is a second answer to a
// question the tables already answer, and the two would drift.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func quotaFromRow(r db.ResourceQuota) domain.ResourceQuota {
	q := domain.ResourceQuota{
		ID: r.ID, ProjectID: r.ProjectID.String, TeamID: r.TeamID.String,
		UpdatedBy: r.UpdatedBy, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.MemoryLimitBytes.Valid {
		v := r.MemoryLimitBytes.Int64
		q.MemoryLimitBytes = &v
	}
	if r.DiskLimitBytes.Valid {
		v := r.DiskLimitBytes.Int64
		q.DiskLimitBytes = &v
	}
	if r.PreviewLimit.Valid {
		v := int(r.PreviewLimit.Int32)
		q.PreviewLimit = &v
	}
	return q
}

func nullInt64(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func nullInt32(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func (s *Store) UpsertProjectQuota(ctx context.Context, id, projectID string, q domain.ResourceQuota) (domain.ResourceQuota, error) {
	row, err := s.q.UpsertProjectQuota(ctx, db.UpsertProjectQuotaParams{
		ID: id, ProjectID: pgText(projectID),
		MemoryLimitBytes: nullInt64(q.MemoryLimitBytes),
		DiskLimitBytes:   nullInt64(q.DiskLimitBytes),
		PreviewLimit:     nullInt32(q.PreviewLimit),
		UpdatedBy:        q.UpdatedBy,
	})
	if err != nil {
		return domain.ResourceQuota{}, wrap("saving the quota", err)
	}
	return quotaFromRow(row), nil
}

func (s *Store) UpsertTeamQuota(ctx context.Context, id, teamID string, q domain.ResourceQuota) (domain.ResourceQuota, error) {
	row, err := s.q.UpsertTeamQuota(ctx, db.UpsertTeamQuotaParams{
		ID: id, TeamID: pgText(teamID),
		MemoryLimitBytes: nullInt64(q.MemoryLimitBytes),
		DiskLimitBytes:   nullInt64(q.DiskLimitBytes),
		PreviewLimit:     nullInt32(q.PreviewLimit),
		UpdatedBy:        q.UpdatedBy,
	})
	if err != nil {
		return domain.ResourceQuota{}, wrap("saving the quota", err)
	}
	return quotaFromRow(row), nil
}

func (s *Store) GetProjectQuota(ctx context.Context, projectID string) (domain.ResourceQuota, error) {
	row, err := s.q.GetProjectQuota(ctx, pgText(projectID))
	if err != nil {
		return domain.ResourceQuota{}, wrap("reading the quota", err)
	}
	return quotaFromRow(row), nil
}

func (s *Store) GetTeamQuota(ctx context.Context, teamID string) (domain.ResourceQuota, error) {
	row, err := s.q.GetTeamQuota(ctx, pgText(teamID))
	if err != nil {
		return domain.ResourceQuota{}, wrap("reading the quota", err)
	}
	return quotaFromRow(row), nil
}

func (s *Store) ListResourceQuotas(ctx context.Context) ([]domain.ResourceQuota, error) {
	rows, err := s.q.ListResourceQuotas(ctx)
	if err != nil {
		return nil, wrap("listing quotas", err)
	}
	out := make([]domain.ResourceQuota, 0, len(rows))
	for _, r := range rows {
		out = append(out, quotaFromRow(r))
	}
	return out, nil
}

func (s *Store) DeleteProjectQuota(ctx context.Context, projectID string) error {
	if err := s.q.DeleteProjectQuota(ctx, pgText(projectID)); err != nil {
		return wrap("removing the quota", err)
	}
	return nil
}

func (s *Store) DeleteTeamQuota(ctx context.Context, teamID string) error {
	if err := s.q.DeleteTeamQuota(ctx, pgText(teamID)); err != nil {
		return wrap("removing the quota", err)
	}
	return nil
}

// ProjectDeclaredMemory is the meter's memory dimension, in BYTES. The query
// sums megabytes because that is the column's unit; the conversion is here so
// every reader downstream deals in one unit.
func (s *Store) ProjectDeclaredMemory(ctx context.Context, projectID string) (int64, error) {
	mb, err := s.q.ProjectDeclaredMemory(ctx, projectID)
	if err != nil {
		return 0, wrap("metering declared memory", err)
	}
	return int64(mb) * 1024 * 1024, nil
}

// ProjectUnlimitedResources names the resources with no declared memory limit.
// They are NAMED rather than counted as zero: a resource with no limit makes a
// memory quota a fiction, and the runaway project this feature exists to stop
// is precisely the one that never set one.
func (s *Store) ProjectUnlimitedResources(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.q.ProjectUnlimitedResources(ctx, projectID)
	if err != nil {
		return nil, wrap("finding unlimited resources", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Kind+" "+r.Name)
	}
	return out, nil
}

func (s *Store) ProjectComposeStackCount(ctx context.Context, projectID string) (int, error) {
	n, err := s.q.ProjectComposeStackCount(ctx, projectID)
	if err != nil {
		return 0, wrap("counting compose stacks", err)
	}
	return int(n), nil
}

func (s *Store) ProjectObservedDisk(ctx context.Context, projectID string) (int64, error) {
	n, err := s.q.ProjectObservedDisk(ctx, projectID)
	if err != nil {
		return 0, wrap("metering disk", err)
	}
	return n, nil
}

func (s *Store) ProjectLivePreviews(ctx context.Context, projectID string) (int, error) {
	n, err := s.q.ProjectLivePreviews(ctx, projectID)
	if err != nil {
		return 0, wrap("counting previews", err)
	}
	return int(n), nil
}

func (s *Store) ListProjectsInTeam(ctx context.Context, teamID string) ([]string, error) {
	rows, err := s.q.ListProjectsInTeam(ctx, teamID)
	if err != nil {
		return nil, wrap("listing the team's projects", err)
	}
	return rows, nil
}

// GetQuotaState is the last ANNOUNCED state, so the warning fires on the
// transition rather than on every deploy.
func (s *Store) GetQuotaState(ctx context.Context, kind, id, dimension string) (string, time.Time, error) {
	row, err := s.q.GetQuotaState(ctx, db.GetQuotaStateParams{ScopeKind: kind, ScopeID: id, Dimension: dimension})
	if err != nil {
		return "", time.Time{}, wrap("reading the quota state", err)
	}
	return row.State, row.ChangedAt.Time, nil
}

func (s *Store) SetQuotaState(ctx context.Context, kind, id, dimension, state string) error {
	if err := s.q.SetQuotaState(ctx, db.SetQuotaStateParams{
		ScopeKind: kind, ScopeID: id, Dimension: dimension, State: state,
	}); err != nil {
		return wrap("recording the quota state", err)
	}
	return nil
}

package store

// Metrics and usage persistence (metrics-and-usage.md §6).

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t.UTC().Truncate(24 * time.Hour), Valid: true}
}

// WriteMetricsReport applies one server's whole bucket. It is deliberately not
// a transaction: a report is a gauge, not a ledger, and a single bad row must
// not cost the other forty resources on the node their bucket. Each statement
// is idempotent on the bucket identity, so a redelivery is a no-op.
func (s *Store) WriteMetricsReport(
	ctx context.Context,
	resources []domain.ResourceMetricBucket,
	requests []domain.RequestMetricBucket,
	paths map[string][]domain.RequestPathCount,
	disk []domain.ResourceDiskBucket,
) error {
	for _, r := range resources {
		if err := s.q.UpsertResourceMetric(ctx, db.UpsertResourceMetricParams{
			ResourceKind: r.ResourceKind, ResourceID: r.ResourceID,
			BucketStart: tsFromTime(r.BucketStart), ServerID: r.ServerID,
			CpuCoreMs: r.CPUCoreMs, CpuPercentPeak: r.CPUPercentPeak,
			MemoryByteSeconds: r.MemoryByteSeconds, MemoryBytesPeak: r.MemoryBytesPeak,
			MemoryLimitBytes: r.MemoryLimitBytes,
			SampleCount:      int32(r.SampleCount), CoveredSeconds: int32(r.CoveredSeconds),
		}); err != nil {
			return wrap("writing resource metrics", err)
		}
	}
	for _, r := range requests {
		if err := s.q.UpsertRequestMetric(ctx, db.UpsertRequestMetricParams{
			ResourceKind: r.ResourceKind, ResourceID: r.ResourceID,
			BucketStart: tsFromTime(r.BucketStart), ServerID: r.ServerID,
			Requests: r.Requests, RedirectCount: r.RedirectCount,
			Status2xx: r.Status2xx, Status3xx: r.Status3xx,
			Status4xx: r.Status4xx, Status5xx: r.Status5xx,
			ResponseBytes: r.ResponseBytes, LatencyBuckets: r.LatencyBuckets,
			HistogramVersion: int32(r.HistogramVersion), SampleRate: int32(r.SampleRate),
		}); err != nil {
			return wrap("writing request metrics", err)
		}
	}
	for key, rows := range paths {
		kind, id, at, ok := splitPathKey(key)
		if !ok {
			continue
		}
		for _, p := range rows {
			if err := s.q.UpsertRequestPath(ctx, db.UpsertRequestPathParams{
				ResourceKind: kind, ResourceID: id, BucketStart: tsFromTime(at),
				Path: p.Path, Requests: p.Requests, Status5xx: p.Status5xx,
				HistogramVersion: domain.HistogramVersion,
			}); err != nil {
				return wrap("writing request paths", err)
			}
		}
	}
	for _, d := range disk {
		if err := s.q.UpsertResourceDisk(ctx, db.UpsertResourceDiskParams{
			ResourceKind: d.ResourceKind, ResourceID: d.ResourceID,
			BucketStart: tsFromTime(d.BucketStart), ServerID: d.ServerID,
			ImageBytes: d.ImageBytes, VolumeBytes: d.VolumeBytes, ContainerBytes: d.ContainerBytes,
		}); err != nil {
			return wrap("writing resource disk", err)
		}
	}
	return nil
}

// The path map is keyed by kind, id and bucket because paths are hourly while
// the buckets around them are not.
func PathKey(kind, id string, at time.Time) string {
	return kind + "\x00" + id + "\x00" + at.UTC().Format(time.RFC3339)
}

func splitPathKey(key string) (kind, id string, at time.Time, ok bool) {
	parts := splitN(key, '\x00', 3)
	if len(parts) != 3 {
		return "", "", time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, parts[2])
	if err != nil {
		return "", "", time.Time{}, false
	}
	return parts[0], parts[1], t, true
}

func splitN(s string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func resourceMetricFromRow(r db.ResourceMetric) domain.ResourceMetricBucket {
	return domain.ResourceMetricBucket{
		ResourceKind: r.ResourceKind, ResourceID: r.ResourceID,
		BucketStart: r.BucketStart.Time, ServerID: r.ServerID,
		CPUCoreMs: r.CpuCoreMs, CPUPercentPeak: r.CpuPercentPeak,
		MemoryByteSeconds: r.MemoryByteSeconds, MemoryBytesPeak: r.MemoryBytesPeak,
		MemoryLimitBytes: r.MemoryLimitBytes,
		SampleCount:      int(r.SampleCount), CoveredSeconds: int(r.CoveredSeconds),
	}
}

func requestMetricFromRow(r db.RequestMetric) domain.RequestMetricBucket {
	return domain.RequestMetricBucket{
		ResourceKind: r.ResourceKind, ResourceID: r.ResourceID,
		BucketStart: r.BucketStart.Time, ServerID: r.ServerID,
		Requests: r.Requests, RedirectCount: r.RedirectCount,
		Status2xx: r.Status2xx, Status3xx: r.Status3xx,
		Status4xx: r.Status4xx, Status5xx: r.Status5xx,
		ResponseBytes: r.ResponseBytes, LatencyBuckets: r.LatencyBuckets,
		HistogramVersion: int(r.HistogramVersion), SampleRate: int(r.SampleRate),
	}
}

func (s *Store) ListResourceMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.ResourceMetricBucket, error) {
	rows, err := s.q.ListResourceMetrics(ctx, db.ListResourceMetricsParams{
		ResourceKind: kind, ResourceID: id, BucketStart: tsFromTime(since),
	})
	if err != nil {
		return nil, wrap("listing resource metrics", err)
	}
	out := make([]domain.ResourceMetricBucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, resourceMetricFromRow(r))
	}
	return out, nil
}

func (s *Store) ListRequestMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.RequestMetricBucket, error) {
	rows, err := s.q.ListRequestMetrics(ctx, db.ListRequestMetricsParams{
		ResourceKind: kind, ResourceID: id, BucketStart: tsFromTime(since),
	})
	if err != nil {
		return nil, wrap("listing request metrics", err)
	}
	out := make([]domain.RequestMetricBucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, requestMetricFromRow(r))
	}
	return out, nil
}

func (s *Store) ListRequestPaths(ctx context.Context, kind, id string, since time.Time, limit int) ([]domain.RequestPathCount, error) {
	rows, err := s.q.ListRequestPaths(ctx, db.ListRequestPathsParams{
		ResourceKind: kind, ResourceID: id, BucketStart: tsFromTime(since), Limit: int32(limit),
	})
	if err != nil {
		return nil, wrap("listing request paths", err)
	}
	out := make([]domain.RequestPathCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.RequestPathCount{Path: r.Path, Requests: r.Requests, Status5xx: r.Status5xx})
	}
	return out, nil
}

func (s *Store) LatestResourceDisk(ctx context.Context, kind, id string) (domain.ResourceDiskBucket, error) {
	r, err := s.q.LatestResourceDisk(ctx, db.LatestResourceDiskParams{ResourceKind: kind, ResourceID: id})
	if err != nil {
		return domain.ResourceDiskBucket{}, wrap("reading resource disk", err)
	}
	return domain.ResourceDiskBucket{
		ResourceKind: r.ResourceKind, ResourceID: r.ResourceID,
		BucketStart: r.BucketStart.Time, ServerID: r.ServerID,
		ImageBytes: r.ImageBytes, VolumeBytes: r.VolumeBytes, ContainerBytes: r.ContainerBytes,
	}, nil
}

// ListServerMetrics sums the managed containers on a node. There is no
// server-level sampler: a node's figure IS its resources' figures, and a second
// source would be a second answer to the same question.
func (s *Store) ListServerMetrics(ctx context.Context, serverID string, since time.Time) ([]domain.ResourceMetricBucket, error) {
	rows, err := s.q.ListServerMetrics(ctx, db.ListServerMetricsParams{
		ServerID: serverID, BucketStart: tsFromTime(since),
	})
	if err != nil {
		return nil, wrap("listing server metrics", err)
	}
	out := make([]domain.ResourceMetricBucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ResourceMetricBucket{
			ResourceKind: domain.MetricResourceServer, ResourceID: serverID,
			BucketStart: r.BucketStart.Time, ServerID: serverID,
			CPUCoreMs: r.CpuCoreMs, CPUPercentPeak: r.CpuPercentPeak,
			MemoryByteSeconds: r.MemoryByteSeconds, MemoryBytesPeak: r.MemoryBytesPeak,
			CoveredSeconds: int(r.CoveredSeconds),
		})
	}
	return out, nil
}

// ─── rollup ────────────────────────────────────────────────────────────────

func (s *Store) ListUnrolledMetricDays(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	rows, err := s.q.ListUnrolledMetricDays(ctx, db.ListUnrolledMetricDaysParams{
		BucketStart: tsFromTime(from), BucketStart_2: tsFromTime(to),
	})
	if err != nil {
		return nil, wrap("listing unrolled metric days", err)
	}
	out := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Time)
	}
	return out, nil
}

// RollupDay writes one UTC day's daily rows and then stamps the ownership
// chain onto them. The order matters: the numbers come from buckets that carry
// no names, and the names are snapshotted afterwards from the live rows — so a
// resource deleted later keeps the name it had on the day.
func (s *Store) RollupDay(ctx context.Context, day time.Time) error {
	d := pgDate(day)
	if err := s.q.RollupResourceUsageDay(ctx, d); err != nil {
		return wrap("rolling up usage", err)
	}
	if err := s.q.StampApplicationUsageOwnership(ctx, d); err != nil {
		return wrap("stamping application usage", err)
	}
	if err := s.q.StampComposeUsageOwnership(ctx, d); err != nil {
		return wrap("stamping compose usage", err)
	}
	if err := s.q.StampDatabaseUsageOwnership(ctx, d); err != nil {
		return wrap("stamping database usage", err)
	}
	if err := s.q.RollupDeployMinutes(ctx, d); err != nil {
		return wrap("rolling up deploy minutes", err)
	}
	return nil
}

func (s *Store) ListUsageForMonth(ctx context.Context, from, to time.Time) ([]domain.ResourceUsageDay, error) {
	rows, err := s.q.ListUsageForMonth(ctx, db.ListUsageForMonthParams{Day: pgDate(from), Day_2: pgDate(to)})
	if err != nil {
		return nil, wrap("listing usage", err)
	}
	out := make([]domain.ResourceUsageDay, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ResourceUsageDay{
			ResourceKind: r.ResourceKind, ResourceID: r.ResourceID, Day: r.Day.Time,
			TeamID: r.TeamID, ProjectID: r.ProjectID, ProjectName: r.ProjectName,
			EnvironmentID: r.EnvironmentID, EnvironmentName: r.EnvironmentName,
			ResourceName:      r.ResourceName,
			CPUCoreSeconds:    r.CpuCoreSeconds,
			MemoryByteSeconds: r.MemoryByteSeconds,
			MemoryBytesPeak:   r.MemoryBytesPeak,
			DiskBytes:         r.DiskBytes,
			Requests:          r.Requests, Status5xx: r.Status5xx,
			DeployCount: int(r.DeployCount), DeploySeconds: r.DeploySeconds,
		})
	}
	return out, nil
}

// SweepMetrics drops buckets past the metrics horizon and daily rows past the
// usage horizon. A zero retention keeps forever, which — said plainly — is how
// a busy fleet fills a disk, and the config help text says so.
func (s *Store) SweepMetrics(ctx context.Context, bucketCutoff, usageCutoff time.Time, sweepBuckets, sweepUsage bool) error {
	if sweepBuckets {
		ts := tsFromTime(bucketCutoff)
		if err := s.q.DeleteResourceMetricsBefore(ctx, ts); err != nil {
			return wrap("sweeping resource metrics", err)
		}
		if err := s.q.DeleteRequestMetricsBefore(ctx, ts); err != nil {
			return wrap("sweeping request metrics", err)
		}
		if err := s.q.DeleteRequestPathsBefore(ctx, ts); err != nil {
			return wrap("sweeping request paths", err)
		}
		if err := s.q.DeleteResourceDiskBefore(ctx, ts); err != nil {
			return wrap("sweeping resource disk", err)
		}
	}
	if sweepUsage {
		if err := s.q.DeleteUsageDailyBefore(ctx, pgDate(usageCutoff)); err != nil {
			return wrap("sweeping usage", err)
		}
	}
	return nil
}

// GetMetricsSettings answers the defaults when the row is missing rather than
// an error: a panel that has never been configured collects, which is the
// behaviour the feature ships with.
func (s *Store) GetMetricsSettings(ctx context.Context) (domain.MetricsSettings, error) {
	row, err := s.q.GetMetricsSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return domain.DefaultMetricsSettings(), nil
		}
		return domain.DefaultMetricsSettings(), wrap("reading metrics settings", err)
	}
	return domain.MetricsSettings{
		Enabled:          row.Enabled,
		RequestAnalytics: row.RequestAnalytics,
		BucketSeconds:    int(row.BucketSeconds),
	}, nil
}

func (s *Store) SetMetricsSettings(ctx context.Context, m domain.MetricsSettings) (domain.MetricsSettings, error) {
	row, err := s.q.SetMetricsSettings(ctx, db.SetMetricsSettingsParams{
		Enabled: m.Enabled, RequestAnalytics: m.RequestAnalytics, BucketSeconds: int32(m.BucketSeconds),
	})
	if err != nil {
		return domain.MetricsSettings{}, wrap("saving metrics settings", err)
	}
	return domain.MetricsSettings{
		Enabled:          row.Enabled,
		RequestAnalytics: row.RequestAnalytics,
		BucketSeconds:    int(row.BucketSeconds),
	}, nil
}

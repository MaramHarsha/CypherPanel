// Package usage ingests agent metrics reports, rolls them up nightly, and
// answers the questions the Metrics and Usage screens ask
// (metrics-and-usage.md §§4.6, 7, 8).
//
// The line this feature walks, stated where the code is rather than only in the
// spec: vision.md puts "SaaS billing and metering" explicitly out of scope. What
// is banned is the panel doing COMMERCE — prices, currency, rate cards,
// invoices, plans, payment integrations, and quota enforcement. None of that is
// here, and the boundary is enforceable by three rules:
//
//  1. No monetary concept exists anywhere in this package — no price, no rate,
//     no currency, in the schema, the API or the UI.
//  2. NOTHING HERE EVER REFUSES AN ACTION. There is no quota, no cap, no soft
//     limit. Usage is reported; it never gates a deploy. Metering with teeth is
//     what "metering" means in that vision line, and this has none.
//  3. The CSV ends where a spreadsheet begins. The panel produces
//     measurements; whatever an operator multiplies them by is theirs.
package usage

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Store is the persistence this package needs (consumer-defined).
type Store interface {
	WriteMetricsReport(ctx context.Context,
		resources []domain.ResourceMetricBucket,
		requests []domain.RequestMetricBucket,
		paths map[string][]domain.RequestPathCount,
		disk []domain.ResourceDiskBucket) error
	ListUnrolledMetricDays(ctx context.Context, from, to time.Time) ([]time.Time, error)
	RollupDay(ctx context.Context, day time.Time) error
	SweepMetrics(ctx context.Context, bucketCutoff, usageCutoff time.Time, sweepBuckets, sweepUsage bool) error
}

// Config is §7's two knobs.
type Config struct {
	// MetricsRetention bounds the 5-minute and hourly buckets. Zero keeps them
	// forever — which, said plainly, is how a busy fleet fills a disk.
	MetricsRetention time.Duration
	// UsageRetention bounds the daily rows. 400 days by default rather than
	// 365, so "the same month last year" is always still there.
	UsageRetention time.Duration
}

// Recorder applies incoming reports and owns the nightly rollup.
type Recorder struct {
	store Store
	cfg   Config
	log   *slog.Logger
	now   func() time.Time
}

func New(st Store, cfg Config, log *slog.Logger) *Recorder {
	return &Recorder{store: st, cfg: cfg, log: log, now: time.Now}
}

// SetClock injects the clock (ENGINEERING rule 9).
func (r *Recorder) SetClock(now func() time.Time) { r.now = now }

// Record applies one agent's report. A MALFORMED REPORT IS DROPPED WITH A
// WARNING, never partially applied — the same stance status.Recorder takes on a
// malformed heartbeat. One bad agent must not corrupt a month.
func (r *Recorder) Record(ctx context.Context, serverID string, data []byte) {
	var report agentv1.MetricsReport
	if err := proto.Unmarshal(data, &report); err != nil {
		r.log.Warn("metrics: dropping a malformed report", "server_id", serverID, "error", err)
		return
	}
	if report.GetBucketStart() == nil {
		r.log.Warn("metrics: dropping a report with no bucket", "server_id", serverID)
		return
	}
	// The subject's server id is the authenticated one; the body's is not.
	if serverID == "" {
		serverID = report.GetServerId()
	}
	bucket := report.GetBucketStart().AsTime().UTC()

	resources := make([]domain.ResourceMetricBucket, 0, len(report.GetResources()))
	for _, m := range report.GetResources() {
		if m.GetResourceId() == "" || m.GetResourceKind() == "" {
			continue
		}
		resources = append(resources, domain.ResourceMetricBucket{
			ResourceKind: m.GetResourceKind(), ResourceID: m.GetResourceId(),
			BucketStart: bucket, ServerID: serverID,
			CPUCoreMs: int64(m.GetCpuCoreMs()), CPUPercentPeak: m.GetCpuPercentPeak(),
			MemoryByteSeconds: int64(m.GetMemoryByteSeconds()),
			MemoryBytesPeak:   int64(m.GetMemoryBytesPeak()),
			MemoryLimitBytes:  int64(m.GetMemoryLimitBytes()),
			SampleCount:       int(m.GetSampleCount()), CoveredSeconds: int(m.GetCoveredSeconds()),
		})
	}

	requests := make([]domain.RequestMetricBucket, 0, len(report.GetRequests()))
	for _, q := range report.GetRequests() {
		if q.GetResourceId() == "" || q.GetResourceKind() == "" {
			continue
		}
		rate := int(q.GetSampleRate())
		if rate < 1 {
			rate = 1
		}
		requests = append(requests, domain.RequestMetricBucket{
			ResourceKind: q.GetResourceKind(), ResourceID: q.GetResourceId(),
			BucketStart: bucket, ServerID: serverID,
			Requests: int64(q.GetRequests()), RedirectCount: int64(q.GetRedirectCount()),
			Status2xx: int64(q.GetStatus_2Xx()), Status3xx: int64(q.GetStatus_3Xx()),
			Status4xx: int64(q.GetStatus_4Xx()), Status5xx: int64(q.GetStatus_5Xx()),
			ResponseBytes:    int64(q.GetResponseBytes()),
			LatencyBuckets:   toInt64(q.GetLatencyBuckets()),
			HistogramVersion: int(q.GetHistogramVersion()), SampleRate: rate,
		})
	}

	paths := map[string][]domain.RequestPathCount{}
	for _, p := range report.GetPaths() {
		if p.GetResourceId() == "" || p.GetPath() == "" {
			continue
		}
		key := store.PathKey(p.GetResourceKind(), p.GetResourceId(), bucket)
		paths[key] = append(paths[key], domain.RequestPathCount{
			Path: p.GetPath(), Requests: int64(p.GetRequests()), Status5xx: int64(p.GetStatus_5Xx()),
		})
	}

	disk := make([]domain.ResourceDiskBucket, 0, len(report.GetDisk()))
	for _, d := range report.GetDisk() {
		if d.GetResourceId() == "" {
			continue
		}
		disk = append(disk, domain.ResourceDiskBucket{
			ResourceKind: d.GetResourceKind(), ResourceID: d.GetResourceId(),
			BucketStart: bucket, ServerID: serverID,
			ImageBytes: int64(d.GetImageBytes()), VolumeBytes: int64(d.GetVolumeBytes()),
			ContainerBytes: int64(d.GetContainerBytes()),
		})
	}

	if err := r.store.WriteMetricsReport(ctx, resources, requests, paths, disk); err != nil {
		r.log.Error("metrics: writing report", "server_id", serverID, "bucket", bucket, "error", err)
	}
}

func toInt64(in []uint64) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}

// RunRollup is the one owned goroutine (ENGINEERING rule 7). It rolls up EVERY
// UTC day that has bucket rows and no complete daily row — not merely
// yesterday — so a plane that was down for three days catches up on boot
// instead of leaving a permanent hole. It is bounded to the metrics retention
// window, because beyond it the source rows are gone and no amount of catching
// up invents them.
func (r *Recorder) RunRollup(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	r.rollupAndSweep(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.rollupAndSweep(ctx)
		}
	}
}

func (r *Recorder) rollupAndSweep(ctx context.Context) {
	now := r.now().UTC()
	window := r.cfg.MetricsRetention
	if window <= 0 {
		window = 14 * 24 * time.Hour
	}
	days, err := r.store.ListUnrolledMetricDays(ctx, now.Add(-window), now.Add(24*time.Hour))
	if err != nil {
		r.log.Error("usage: listing days to roll up", "error", err)
		return
	}
	for _, day := range days {
		if err := r.store.RollupDay(ctx, day); err != nil {
			r.log.Error("usage: rolling up a day", "day", day.Format("2006-01-02"), "error", err)
		}
	}

	// Sweep AFTER the rollup, always: dropping the buckets a day was never
	// rolled up from would delete that day's numbers before they were summed.
	sweepBuckets := r.cfg.MetricsRetention > 0
	sweepUsage := r.cfg.UsageRetention > 0
	if !sweepBuckets && !sweepUsage {
		return
	}
	if err := r.store.SweepMetrics(ctx,
		now.Add(-r.cfg.MetricsRetention), now.Add(-r.cfg.UsageRetention),
		sweepBuckets, sweepUsage); err != nil {
		r.log.Error("usage: sweeping", "error", err)
	}
}

// RollupResource writes a resource's unrolled days BEFORE its row disappears,
// so a preview environment that lived for six hours still appears in the month
// it lived in. The rollup is an idempotent upsert, so calling it here and again
// that night is harmless.
func (r *Recorder) RollupResource(ctx context.Context) {
	r.rollupAndSweep(ctx)
}

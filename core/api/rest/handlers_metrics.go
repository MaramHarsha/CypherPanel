package rest

// Metrics, traffic and usage (metrics-and-usage.md §10).
//
// FIXED ENDPOINTS, NOT A QUERY LANGUAGE. No arbitrary group-by, no filter DSL,
// no PromQL. These answer the questions the screens ask; anything richer is a
// reason to export, not a reason to build a query engine into a control plane
// with a 300 MB budget.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const (
	maxMetricsWindow  = 30 * 24 * time.Hour
	defaultMetricsWin = 6 * time.Hour
)

// MetricsStore is the read surface the metrics routes need (consumer-defined).
type MetricsStore interface {
	ListResourceMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.ResourceMetricBucket, error)
	ListRequestMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.RequestMetricBucket, error)
	ListRequestPaths(ctx context.Context, kind, id string, since time.Time, limit int) ([]domain.RequestPathCount, error)
	LatestResourceDisk(ctx context.Context, kind, id string) (domain.ResourceDiskBucket, error)
	ListServerMetrics(ctx context.Context, serverID string, since time.Time) ([]domain.ResourceMetricBucket, error)
	ListUsageForMonth(ctx context.Context, from, to time.Time) ([]domain.ResourceUsageDay, error)
	GetMetricsSettings(ctx context.Context) (domain.MetricsSettings, error)
	SetMetricsSettings(ctx context.Context, m domain.MetricsSettings) (domain.MetricsSettings, error)
}

// ─── DTOs ──────────────────────────────────────────────────────────────────

type metricPointDTO struct {
	At string `json:"at"`
	// CPUPercent and MemoryBytes are DERIVED from accumulators over the covered
	// time, never stored. Storing a mean would make a longer window's mean an
	// average-of-averages, which is wrong whenever the buckets are not equally
	// covered — and after an agent restart they are not.
	CPUPercent      float64 `json:"cpu_percent"`
	CPUPercentPeak  float64 `json:"cpu_percent_peak"`
	MemoryBytes     int64   `json:"memory_bytes"`
	MemoryBytesPeak int64   `json:"memory_bytes_peak"`
	// CoveredSeconds says how much of the bucket was observed, so a chart can
	// draw a partial bucket as partial rather than as a dip.
	CoveredSeconds int `json:"covered_seconds"`
	BucketSeconds  int `json:"bucket_seconds"`
}

type metricsResponse struct {
	Resolution string           `json:"resolution"`
	AsOf       *string          `json:"as_of"`
	Points     []metricPointDTO `json:"points"`
	Summary    metricsSummary   `json:"summary"`
	Disk       *diskDTO         `json:"disk"`
	// Collecting is false when the panel has metrics turned off. A resource
	// with no data then reads "collection is off", not "this application is
	// idle" — nothing is unknown, never zero (ADR-010).
	Collecting bool `json:"collecting"`
}

type metricsSummary struct {
	CPUPercentMean   float64 `json:"cpu_percent_mean"`
	CPUPercentPeak   float64 `json:"cpu_percent_peak"`
	MemoryBytesMean  int64   `json:"memory_bytes_mean"`
	MemoryBytesPeak  int64   `json:"memory_bytes_peak"`
	MemoryLimitBytes int64   `json:"memory_limit_bytes"`
	CoveredSeconds   int64   `json:"covered_seconds"`
}

type diskDTO struct {
	ImageBytes     int64  `json:"image_bytes"`
	VolumeBytes    int64  `json:"volume_bytes"`
	ContainerBytes int64  `json:"container_bytes"`
	MeasuredAt     string `json:"measured_at"`
}

type trafficPointDTO struct {
	At            string `json:"at"`
	Requests      int64  `json:"requests"`
	Status2xx     int64  `json:"status_2xx"`
	Status3xx     int64  `json:"status_3xx"`
	Status4xx     int64  `json:"status_4xx"`
	Status5xx     int64  `json:"status_5xx"`
	ResponseBytes int64  `json:"response_bytes"`
	BucketSeconds int    `json:"bucket_seconds"`
}

type trafficPathDTO struct {
	Path      string `json:"path"`
	Requests  int64  `json:"requests"`
	Status5xx int64  `json:"status_5xx"`
}

type trafficResponse struct {
	Resolution string            `json:"resolution"`
	AsOf       *string           `json:"as_of"`
	Points     []trafficPointDTO `json:"points"`
	Paths      []trafficPathDTO  `json:"paths"`
	Summary    trafficSummary    `json:"summary"`
	Collecting bool              `json:"collecting"`
}

type trafficSummary struct {
	Requests      int64   `json:"requests"`
	Redirects     int64   `json:"redirects"`
	Status2xx     int64   `json:"status_2xx"`
	Status3xx     int64   `json:"status_3xx"`
	Status4xx     int64   `json:"status_4xx"`
	Status5xx     int64   `json:"status_5xx"`
	ResponseBytes int64   `json:"response_bytes"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
	// Sampled says the counters are ESTIMATES because the node fell back to
	// 1-in-N past its line-rate ceiling. A number the panel is not sure of is
	// labelled (ui-principles §10), never quietly presented as exact.
	Sampled    bool `json:"sampled"`
	SampleRate int  `json:"sample_rate"`
}

// ─── window parsing ────────────────────────────────────────────────────────

// metricsWindow takes the same forms `?since=` already takes on the log
// streams. Anything else is a 400 naming both, for the same reason: a client
// that asked for the last hour and silently got a fortnight has been answered
// confidently and wrongly.
func (a *API) metricsWindow(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := r.URL.Query().Get("window")
	if raw == "" {
		return time.Now().Add(-defaultMetricsWin), true
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 || d > maxMetricsWindow {
			writeError(w, http.StatusBadRequest, "window must be a positive duration of at most 720h")
			return time.Time{}, false
		}
		return time.Now().Add(-d), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	writeError(w, http.StatusBadRequest, `window must be a duration such as "6h" or an RFC 3339 timestamp`)
	return time.Time{}, false
}

// ─── shaping ───────────────────────────────────────────────────────────────

func buildMetricsResponse(buckets []domain.ResourceMetricBucket, disk *domain.ResourceDiskBucket, collecting bool) metricsResponse {
	out := metricsResponse{
		Resolution: "bucket",
		Points:     make([]metricPointDTO, 0, len(buckets)),
		Collecting: collecting,
	}
	var cpuMs, memByteSeconds, covered int64
	for _, b := range buckets {
		bucketSeconds := b.CoveredSeconds
		if bucketSeconds <= 0 {
			bucketSeconds = 300
		}
		p := metricPointDTO{
			At:              b.BucketStart.UTC().Format(time.RFC3339),
			CPUPercentPeak:  b.CPUPercentPeak,
			MemoryBytesPeak: b.MemoryBytesPeak,
			CoveredSeconds:  b.CoveredSeconds,
			BucketSeconds:   bucketSeconds,
		}
		if b.CoveredSeconds > 0 {
			p.CPUPercent = float64(b.CPUCoreMs) / float64(b.CoveredSeconds*1000) * 100
			p.MemoryBytes = b.MemoryByteSeconds / int64(b.CoveredSeconds)
		}
		out.Points = append(out.Points, p)

		cpuMs += b.CPUCoreMs
		memByteSeconds += b.MemoryByteSeconds
		covered += int64(b.CoveredSeconds)
		if b.CPUPercentPeak > out.Summary.CPUPercentPeak {
			out.Summary.CPUPercentPeak = b.CPUPercentPeak
		}
		if b.MemoryBytesPeak > out.Summary.MemoryBytesPeak {
			out.Summary.MemoryBytesPeak = b.MemoryBytesPeak
		}
		if b.MemoryLimitBytes > 0 {
			out.Summary.MemoryLimitBytes = b.MemoryLimitBytes
		}
	}
	out.Summary.CoveredSeconds = covered
	if covered > 0 {
		out.Summary.CPUPercentMean = float64(cpuMs) / float64(covered*1000) * 100
		out.Summary.MemoryBytesMean = memByteSeconds / covered
	}
	if n := len(out.Points); n > 0 {
		asOf := out.Points[n-1].At
		out.AsOf = &asOf
	}
	if disk != nil {
		out.Disk = &diskDTO{
			ImageBytes: disk.ImageBytes, VolumeBytes: disk.VolumeBytes,
			ContainerBytes: disk.ContainerBytes,
			MeasuredAt:     disk.BucketStart.UTC().Format(time.RFC3339),
		}
	}
	return out
}

func buildTrafficResponse(buckets []domain.RequestMetricBucket, paths []domain.RequestPathCount, collecting bool) trafficResponse {
	out := trafficResponse{
		Resolution: "bucket",
		Points:     make([]trafficPointDTO, 0, len(buckets)),
		Paths:      make([]trafficPathDTO, 0, len(paths)),
		Collecting: collecting,
		Summary:    trafficSummary{SampleRate: 1},
	}
	// ONE summed histogram, then one percentile read from it. Averaging the
	// buckets' own percentiles would be arithmetic on numbers that do not add.
	merged := make([]int64, domain.LatencySlots)
	for _, b := range buckets {
		rate := int64(b.SampleRate)
		if rate < 1 {
			rate = 1
		}
		if rate > 1 {
			out.Summary.Sampled = true
			if int(rate) > out.Summary.SampleRate {
				out.Summary.SampleRate = int(rate)
			}
		}
		out.Points = append(out.Points, trafficPointDTO{
			At:            b.BucketStart.UTC().Format(time.RFC3339),
			Requests:      b.Requests * rate,
			Status2xx:     b.Status2xx * rate,
			Status3xx:     b.Status3xx * rate,
			Status4xx:     b.Status4xx * rate,
			Status5xx:     b.Status5xx * rate,
			ResponseBytes: b.ResponseBytes * rate,
			BucketSeconds: 300,
		})
		out.Summary.Requests += b.Requests * rate
		out.Summary.Redirects += b.RedirectCount * rate
		out.Summary.Status2xx += b.Status2xx * rate
		out.Summary.Status3xx += b.Status3xx * rate
		out.Summary.Status4xx += b.Status4xx * rate
		out.Summary.Status5xx += b.Status5xx * rate
		out.Summary.ResponseBytes += b.ResponseBytes * rate
		merged = domain.MergeHistograms(merged, b.LatencyBuckets)
	}
	out.Summary.P50Ms = domain.Percentile(merged, 0.50)
	out.Summary.P95Ms = domain.Percentile(merged, 0.95)
	out.Summary.P99Ms = domain.Percentile(merged, 0.99)
	for _, p := range paths {
		out.Paths = append(out.Paths, trafficPathDTO{Path: p.Path, Requests: p.Requests, Status5xx: p.Status5xx})
	}
	if n := len(out.Points); n > 0 {
		asOf := out.Points[n-1].At
		out.AsOf = &asOf
	}
	return out
}

// ─── handlers ──────────────────────────────────────────────────────────────

func (a *API) metricsReady(w http.ResponseWriter) bool {
	if a.deps.Metrics == nil {
		writeError(w, http.StatusNotImplemented, "metrics are not enabled on this panel")
		return false
	}
	return true
}

func (a *API) collecting(ctx context.Context) bool {
	s, err := a.deps.Metrics.GetMetricsSettings(ctx)
	return err == nil && s.Enabled
}

func (a *API) serveResourceMetrics(w http.ResponseWriter, r *http.Request, kind, id string) {
	since, ok := a.metricsWindow(w, r)
	if !ok {
		return
	}
	buckets, err := a.deps.Metrics.ListResourceMetrics(r.Context(), kind, id, since)
	if err != nil {
		a.deps.Log.Error("reading metrics", "kind", kind, "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the metrics")
		return
	}
	var disk *domain.ResourceDiskBucket
	if d, derr := a.deps.Metrics.LatestResourceDisk(r.Context(), kind, id); derr == nil {
		disk = &d
	} else if !errors.Is(derr, store.ErrNotFound) {
		a.deps.Log.Debug("reading resource disk", "kind", kind, "id", id, "error", derr)
	}
	writeJSON(w, http.StatusOK, buildMetricsResponse(buckets, disk, a.collecting(r.Context())))
}

func (a *API) handleApplicationMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	a.serveResourceMetrics(w, r, domain.MetricResourceApplication, r.PathValue("id"))
}

func (a *API) handleComposeStackMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForComposeStack(ctx, r.PathValue("id"))
	}) {
		return
	}
	a.serveResourceMetrics(w, r, domain.MetricResourceComposeStack, r.PathValue("id"))
}

func (a *API) handleDatabaseMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	a.serveResourceMetrics(w, r, domain.MetricResourceDatabase, r.PathValue("id"))
}

// handleServerMetrics sums the managed containers on a node. A server belongs
// to no project, so this is panel admin — the same rank every other
// fleet-level read takes.
func (a *API) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	since, ok := a.metricsWindow(w, r)
	if !ok {
		return
	}
	buckets, err := a.deps.Metrics.ListServerMetrics(r.Context(), r.PathValue("id"), since)
	if err != nil {
		a.deps.Log.Error("reading server metrics", "server_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the metrics")
		return
	}
	writeJSON(w, http.StatusOK, buildMetricsResponse(buckets, nil, a.collecting(r.Context())))
}

// serveTraffic answers the whole screen in one response — summary numbers, the
// series behind the sparkline, and the top-paths rows — because it is one
// screen, and three round trips for one card is how a panel starts feeling
// slow.
func (a *API) serveTraffic(w http.ResponseWriter, r *http.Request, kind, id string) {
	since, ok := a.metricsWindow(w, r)
	if !ok {
		return
	}
	buckets, err := a.deps.Metrics.ListRequestMetrics(r.Context(), kind, id, since)
	if err != nil {
		a.deps.Log.Error("reading traffic", "kind", kind, "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the traffic")
		return
	}
	paths, err := a.deps.Metrics.ListRequestPaths(r.Context(), kind, id, since, 20)
	if err != nil {
		a.deps.Log.Error("reading traffic paths", "kind", kind, "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the traffic")
		return
	}
	collecting := false
	if s, serr := a.deps.Metrics.GetMetricsSettings(r.Context()); serr == nil {
		collecting = s.Enabled && s.RequestAnalytics
	}
	writeJSON(w, http.StatusOK, buildTrafficResponse(buckets, paths, collecting))
}

func (a *API) handleApplicationTraffic(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	a.serveTraffic(w, r, domain.MetricResourceApplication, r.PathValue("id"))
}

func (a *API) handleComposeStackTraffic(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForComposeStack(ctx, r.PathValue("id"))
	}) {
		return
	}
	a.serveTraffic(w, r, domain.MetricResourceComposeStack, r.PathValue("id"))
}

// ─── usage ─────────────────────────────────────────────────────────────────

type usageProjectDTO struct {
	ProjectID         string  `json:"project_id"`
	ProjectName       string  `json:"project_name"`
	TeamID            string  `json:"team_id"`
	CPUCoreSeconds    int64   `json:"cpu_core_seconds"`
	CPUShare          float64 `json:"cpu_share"`
	MemoryBytesMean   int64   `json:"memory_bytes_mean"`
	MemoryBytesPeak   int64   `json:"memory_bytes_peak"`
	DiskBytes         int64   `json:"disk_bytes"`
	Requests          int64   `json:"requests"`
	Status5xx         int64   `json:"status_5xx"`
	DeployCount       int     `json:"deploy_count"`
	DeploySeconds     int64   `json:"deploy_seconds"`
	MemoryByteSeconds int64   `json:"memory_byte_seconds"`
}

type usageResponse struct {
	Month    string            `json:"month"`
	Projects []usageProjectDTO `json:"projects"`
	// Denominator names what the CPU share is a share OF, in words. Showing "38%
	// of fleet CPU" to a member of one team silently discloses the size of
	// every other team's load, so the denominator is the total of what THIS
	// VIEWER can see and the page says which.
	Denominator string `json:"denominator"`
	Collecting  bool   `json:"collecting"`
}

// usageMonth parses ?month=YYYY-MM, defaulting to the current UTC month.
func usageMonth(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := r.URL.Query().Get("month")
	if raw == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse("2006-01", raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "month must be YYYY-MM")
		return time.Time{}, false
	}
	return t.UTC(), true
}

// visibleUsage reads the month and keeps only the rows whose project the caller
// can see. A project the viewer cannot see contributes to NEITHER the numerator
// NOR the denominator — that is what keeps the share from being a tenancy leak.
func (a *API) visibleUsage(w http.ResponseWriter, r *http.Request) ([]domain.ResourceUsageDay, time.Time, bool) {
	month, ok := usageMonth(w, r)
	if !ok {
		return nil, time.Time{}, false
	}
	rows, err := a.deps.Metrics.ListUsageForMonth(r.Context(), month, month.AddDate(0, 1, 0))
	if err != nil {
		a.deps.Log.Error("reading usage", "month", month, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read usage")
		return nil, time.Time{}, false
	}
	user, _ := userFromContext(r.Context())
	wanted := r.URL.Query().Get("team_id")

	visible := make([]domain.ResourceUsageDay, 0, len(rows))
	allowed := map[string]bool{}
	for _, row := range rows {
		if row.ProjectID == "" {
			continue
		}
		if wanted != "" && row.TeamID != wanted {
			continue
		}
		seen, known := allowed[row.ProjectID]
		if !known {
			role, rerr := a.deps.Teams.RoleForProject(r.Context(), user, row.ProjectID)
			seen = rerr == nil && role != ""
			allowed[row.ProjectID] = seen
		}
		if seen {
			visible = append(visible, row)
		}
	}
	return visible, month, true
}

func (a *API) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	rows, month, ok := a.visibleUsage(w, r)
	if !ok {
		return
	}

	byProject := map[string]*usageProjectDTO{}
	order := []string{}
	var fleetCPU int64
	days := map[string]map[string]bool{}
	for _, row := range rows {
		p := byProject[row.ProjectID]
		if p == nil {
			p = &usageProjectDTO{ProjectID: row.ProjectID, ProjectName: row.ProjectName, TeamID: row.TeamID}
			byProject[row.ProjectID] = p
			order = append(order, row.ProjectID)
			days[row.ProjectID] = map[string]bool{}
		}
		p.CPUCoreSeconds += row.CPUCoreSeconds
		p.MemoryByteSeconds += row.MemoryByteSeconds
		p.DiskBytes += row.DiskBytes
		p.Requests += row.Requests
		p.Status5xx += row.Status5xx
		p.DeployCount += row.DeployCount
		p.DeploySeconds += row.DeploySeconds
		if row.MemoryBytesPeak > p.MemoryBytesPeak {
			p.MemoryBytesPeak = row.MemoryBytesPeak
		}
		days[row.ProjectID][row.Day.Format("2006-01-02")] = true
		fleetCPU += row.CPUCoreSeconds
	}

	out := usageResponse{
		Month:       month.Format("2006-01"),
		Projects:    make([]usageProjectDTO, 0, len(order)),
		Denominator: "the projects you can see",
		Collecting:  a.collecting(r.Context()),
	}
	if user, _ := userFromContext(r.Context()); domain.RoleRank(user.Role) >= domain.RoleRank(domain.RoleAdmin) {
		out.Denominator = "the whole fleet"
	}
	for _, id := range order {
		p := byProject[id]
		if n := len(days[id]); n > 0 {
			p.MemoryBytesMean = p.MemoryByteSeconds / int64(n*86400)
		}
		if fleetCPU > 0 {
			p.CPUShare = float64(p.CPUCoreSeconds) / float64(fleetCPU)
		}
		out.Projects = append(out.Projects, *p)
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		return out.Projects[i].CPUCoreSeconds > out.Projects[j].CPUCoreSeconds
	})
	writeJSON(w, http.StatusOK, out)
}

// handleUsageExport writes one row per resource. Same scope as the page: the
// export can never contain a row the caller could not already read.
func (a *API) handleUsageExport(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	rows, month, ok := a.visibleUsage(w, r)
	if !ok {
		return
	}

	type key struct{ kind, id string }
	agg := map[key]*domain.ResourceUsageDay{}
	order := []key{}
	for _, row := range rows {
		k := key{row.ResourceKind, row.ResourceID}
		cur := agg[k]
		if cur == nil {
			copied := row
			agg[k] = &copied
			order = append(order, k)
			continue
		}
		cur.CPUCoreSeconds += row.CPUCoreSeconds
		cur.MemoryByteSeconds += row.MemoryByteSeconds
		cur.DiskBytes += row.DiskBytes
		cur.Requests += row.Requests
		cur.Status5xx += row.Status5xx
		cur.DeployCount += row.DeployCount
		cur.DeploySeconds += row.DeploySeconds
		if row.MemoryBytesPeak > cur.MemoryBytesPeak {
			cur.MemoryBytesPeak = row.MemoryBytesPeak
		}
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "usage-"+month.Format("2006-01")+".csv"))
	cw := csv.NewWriter(w)
	// No price, no rate, no currency: the CSV ends where a spreadsheet begins.
	_ = cw.Write([]string{
		"month", "team_id", "project", "environment", "resource_kind", "resource",
		"cpu_core_seconds", "memory_byte_seconds", "memory_bytes_peak", "disk_bytes",
		"requests", "status_5xx", "deploy_count", "deploy_seconds",
	})
	for _, k := range order {
		row := agg[k]
		_ = cw.Write([]string{
			month.Format("2006-01"), row.TeamID, row.ProjectName, row.EnvironmentName,
			row.ResourceKind, row.ResourceName,
			strconv.FormatInt(row.CPUCoreSeconds, 10),
			strconv.FormatInt(row.MemoryByteSeconds, 10),
			strconv.FormatInt(row.MemoryBytesPeak, 10),
			strconv.FormatInt(row.DiskBytes, 10),
			strconv.FormatInt(row.Requests, 10),
			strconv.FormatInt(row.Status5xx, 10),
			strconv.Itoa(row.DeployCount),
			strconv.FormatInt(row.DeploySeconds, 10),
		})
	}
	cw.Flush()
}

// ─── settings ──────────────────────────────────────────────────────────────

type metricsSettingsDTO struct {
	Enabled          bool `json:"enabled"`
	RequestAnalytics bool `json:"request_analytics"`
	BucketSeconds    int  `json:"bucket_seconds"`
}

func (a *API) handleGetMetricsSettings(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	s, err := a.deps.Metrics.GetMetricsSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the settings")
		return
	}
	writeJSON(w, http.StatusOK, metricsSettingsDTO{s.Enabled, s.RequestAnalytics, s.BucketSeconds})
}

func (a *API) handleSetMetricsSettings(w http.ResponseWriter, r *http.Request) {
	if !a.metricsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req metricsSettingsDTO
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.BucketSeconds <= 0 {
		req.BucketSeconds = 300
	}
	if 3600%req.BucketSeconds != 0 {
		writeError(w, http.StatusBadRequest, "the bucket must divide an hour evenly — 60, 300, 900 or 1800 seconds")
		return
	}
	saved, err := a.deps.Metrics.SetMetricsSettings(r.Context(), domain.MetricsSettings{
		Enabled: req.Enabled, RequestAnalytics: req.RequestAnalytics, BucketSeconds: req.BucketSeconds,
	})
	if err != nil {
		a.deps.Log.Error("saving metrics settings", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the settings")
		return
	}
	// Turning request analytics on or off changes the Proxy's static config,
	// which recreates cypher-proxy on each node — a few seconds with no
	// routing there. The nudge makes that happen now rather than at the next
	// drift pass, so an operator who just flipped it sees the effect.
	if a.deps.Scheduler != nil {
		if err := a.deps.Scheduler.RequestResync(r.Context(), "metrics settings changed"); err != nil {
			a.deps.Log.Warn("metrics: resync nudge failed", "error", err)
		}
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMetricsSettingsChanged,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "metrics"),
		Detail: map[string]any{
			"enabled": saved.Enabled, "request_analytics": saved.RequestAnalytics,
			"bucket_seconds": saved.BucketSeconds,
		},
	})
	writeJSON(w, http.StatusOK, metricsSettingsDTO{saved.Enabled, saved.RequestAnalytics, saved.BucketSeconds})
}

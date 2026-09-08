package domain

import "time"

// Metrics and usage (metrics-and-usage.md).
//
// EVERYTHING HERE MERGES. Counters merge by sum, peaks by max, histograms
// element-wise — which is what makes the answer for any window exact rather
// than dependent on the bucket size somebody picked. It is also why there is no
// average and no percentile stored anywhere: a mean is derived by dividing an
// accumulator by the covered time, and a percentile is computed once from a
// summed histogram. Storing either would make a longer window wrong.

// Resource kinds metrics are attributed to. `server` exists for requests that
// matched no router — "traffic is arriving at this node and hitting nothing" is
// a question worth being able to answer, and it costs one row per node.
const (
	MetricResourceApplication  = "application"
	MetricResourceComposeStack = "compose_stack"
	MetricResourceDatabase     = "database"
	MetricResourceServer       = "server"
)

// LatencyBoundsMs is histogram version 1: fixed upper bounds in milliseconds,
// with an implicit +∞ slot at the end. The version travels with every row so
// these constants can change later without silently reinterpreting old data.
var LatencyBoundsMs = [15]float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

// HistogramVersion is the boundary set above.
const HistogramVersion = 1

// LatencySlots is the number of histogram slots — the bounds plus the overflow.
const LatencySlots = len(LatencyBoundsMs) + 1

// ResourceMetricBucket is one resource's CPU and memory over one bucket.
type ResourceMetricBucket struct {
	ResourceKind      string
	ResourceID        string
	BucketStart       time.Time
	ServerID          string
	CPUCoreMs         int64
	CPUPercentPeak    float64
	MemoryByteSeconds int64
	MemoryBytesPeak   int64
	MemoryLimitBytes  int64
	SampleCount       int
	// CoveredSeconds is how much of the bucket was actually observed. A chart
	// draws a partial bucket as partial rather than as a dip in traffic.
	CoveredSeconds int
}

// RequestMetricBucket is one routed resource's traffic over one bucket.
type RequestMetricBucket struct {
	ResourceKind     string
	ResourceID       string
	BucketStart      time.Time
	ServerID         string
	Requests         int64
	RedirectCount    int64
	Status2xx        int64
	Status3xx        int64
	Status4xx        int64
	Status5xx        int64
	ResponseBytes    int64
	LatencyBuckets   []int64
	HistogramVersion int
	// SampleRate is 1 normally, and N when the node fell back to 1-in-N
	// sampling past its line-rate ceiling. A number the panel is not sure of is
	// labelled as an estimate, never quietly presented as exact.
	SampleRate int
}

// RequestPathCount is one normalised path's share of a window.
type RequestPathCount struct {
	Path      string
	Requests  int64
	Status5xx int64
}

// ResourceDiskBucket is a resource's disk footprint at one hourly reading.
//
// These figures DO NOT SUM to the host's usage: image layers are shared, and a
// base layer used by four applications is counted for each of them, because the
// question the number answers is "what is this resource responsible for", not
// "how would the host shrink if I deleted it". The fleet figure an operator
// should trust for capacity is the one on the Server.
type ResourceDiskBucket struct {
	ResourceKind   string
	ResourceID     string
	BucketStart    time.Time
	ServerID       string
	ImageBytes     int64
	VolumeBytes    int64
	ContainerBytes int64
}

// ResourceUsageDay is one resource's day, with the ownership chain SNAPSHOTTED
// rather than joined — a project renamed in March must still read as it did in
// February's report, and a resource deleted on the 28th must still appear in
// that month.
type ResourceUsageDay struct {
	ResourceKind      string
	ResourceID        string
	Day               time.Time
	TeamID            string
	ProjectID         string
	ProjectName       string
	EnvironmentID     string
	EnvironmentName   string
	ResourceName      string
	CPUCoreSeconds    int64
	MemoryByteSeconds int64
	MemoryBytesPeak   int64
	DiskBytes         int64
	Requests          int64
	Status5xx         int64
	DeployCount       int
	DeploySeconds     int64
}

// MetricsSettings is the panel-wide collection policy, carried to every node in
// desired state.
type MetricsSettings struct {
	Enabled          bool
	RequestAnalytics bool
	BucketSeconds    int
}

// DefaultMetricsSettings is collection on, request analytics OFF. Analytics is
// opt-in because paths are application-authored data: normalisation removes
// id-shaped segments and truncation removes depth, which covers the common
// /reset/<token> shape but not every shape, and an operator for whom URLs are
// themselves sensitive must not have that decision made for them.
func DefaultMetricsSettings() MetricsSettings {
	return MetricsSettings{Enabled: true, RequestAnalytics: false, BucketSeconds: 300}
}

// Percentile reads a percentile off a summed histogram, in milliseconds.
// Accurate to the bucket boundary, which is stated rather than hidden: the
// answer is the upper bound of the slot the percentile falls in.
//
// The overflow slot answers with the last finite bound, because "at least 60
// seconds" is what the data supports and inventing a larger number would be
// making one up.
func Percentile(buckets []int64, p float64) float64 {
	var total int64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return 0
	}
	target := float64(total) * p
	var running float64
	for i, n := range buckets {
		running += float64(n)
		if running >= target {
			if i >= len(LatencyBoundsMs) {
				return LatencyBoundsMs[len(LatencyBoundsMs)-1]
			}
			return LatencyBoundsMs[i]
		}
	}
	return LatencyBoundsMs[len(LatencyBoundsMs)-1]
}

// MergeHistograms sums two histograms element-wise. This is the operation the
// whole design rests on: it is why a window's percentile is computed once from
// one summed histogram instead of averaged from several, which would be wrong.
func MergeHistograms(dst, src []int64) []int64 {
	if len(dst) < LatencySlots {
		grown := make([]int64, LatencySlots)
		copy(grown, dst)
		dst = grown
	}
	for i, n := range src {
		if i < len(dst) {
			dst[i] += n
		}
	}
	return dst
}

package metrics

// The collector: one owned goroutine per node that samples containers, folds
// access-log lines, seals buckets on the wall clock and publishes one report
// per bucket (metrics-and-usage.md §4).
//
// Buckets are cut on WALL-CLOCK boundaries, not on an interval since start, so
// every node in a fleet cuts at the same instants and a fleet chart lines up.
//
// The backlog is the honest bound of this whole feature: up to twelve sealed
// buckets are held when the bus is unreachable, and past that the oldest are
// dropped. METRICS ARE LOSSY BY DESIGN. The audit log is a ledger and must not
// lose a row; this is a gauge and may.

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

const (
	// DefaultBucketSeconds is the write budget's central constant.
	DefaultBucketSeconds = 300
	// DefaultSampleInterval is how often a container's counters are read.
	DefaultSampleInterval = 15 * time.Second
	// DefaultDiskInterval is hourly because the daemon's verbose disk call
	// walks the graph driver and can take seconds on a busy host.
	DefaultDiskInterval = time.Hour
	// maxBacklog is one hour of five-minute buckets.
	maxBacklog = 12
	// accessLogMaxRate is where the node switches to deterministic 1-in-N
	// sampling. An agent-side constant: the node is where the cost is known.
	accessLogMaxRate = 5000
)

// Sampler is the driver seam (ENGINEERING rule 11): nothing Docker-shaped
// crosses it, so a Swarm or k8s driver implements the same two methods and the
// rest of this package does not change.
type Sampler interface {
	SampleContainers(ctx context.Context) ([]ContainerSample, error)
	SampleDisk(ctx context.Context) ([]DiskSample, error)
	StreamProxyLog(ctx context.Context, w io.Writer) error
}

// ContainerSample is one container's cumulative counters plus the labels that
// say whose it is.
type ContainerSample struct {
	ResourceKind     string
	ResourceID       string
	CPUTotalNanos    uint64
	OnlineCPUs       int
	MemoryBytes      uint64
	MemoryLimitBytes uint64
}

type DiskSample struct {
	ResourceKind   string
	ResourceID     string
	ImageBytes     uint64
	VolumeBytes    uint64
	ContainerBytes uint64
}

// Publisher is the bus (consumer-defined).
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Settings is the panel-wide policy, replaced wholesale on every desired-state
// sync the way TLS settings are.
type Settings struct {
	Enabled          bool
	RequestAnalytics bool
	BucketSeconds    int
}

// Collector owns the open bucket, the backlog and the sampling loop.
type Collector struct {
	sampler  Sampler
	bus      Publisher
	serverID string
	log      *slog.Logger
	now      func() time.Time

	sampleInterval time.Duration
	diskInterval   time.Duration

	mu       sync.Mutex
	settings Settings
	open     *Bucket
	backlog  []*Bucket
	// prevCPU is the previous cumulative counter per resource. Deltas are
	// computed here rather than trusting the daemon's own precpu window, so a
	// slow or retried read shifts nothing.
	prevCPU map[string]uint64
	// lineRate counts access-log lines in the current second, for the ceiling.
	lineWindow time.Time
	lineCount  int
	sampleRate uint32
}

func New(sampler Sampler, bus Publisher, serverID string, log *slog.Logger) *Collector {
	return &Collector{
		sampler: sampler, bus: bus, serverID: serverID, log: log, now: time.Now,
		sampleInterval: DefaultSampleInterval,
		diskInterval:   DefaultDiskInterval,
		settings:       Settings{Enabled: true, BucketSeconds: DefaultBucketSeconds},
		prevCPU:        map[string]uint64{},
		sampleRate:     1,
	}
}

// SetClock injects the clock (ENGINEERING rule 9).
func (c *Collector) SetClock(now func() time.Time) { c.now = now }

// Apply replaces the panel-wide settings. An empty bucket_seconds is the
// default rather than "no bucketing", and a value that does not divide an hour
// is refused in favour of the default — a bucket that straddles an hour makes
// the hourly path and disk rows land in two different buckets.
func (c *Collector) Apply(s Settings) {
	if s.BucketSeconds <= 0 || 3600%s.BucketSeconds != 0 {
		s.BucketSeconds = DefaultBucketSeconds
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = s
}

func (c *Collector) enabled() (Settings, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings, c.settings.Enabled
}

// Run is the owned goroutine.
func (c *Collector) Run(ctx context.Context) {
	sampleTick := time.NewTicker(c.sampleInterval)
	defer sampleTick.Stop()
	// The seal tick is deliberately finer than a bucket: it checks whether the
	// wall clock has crossed a boundary rather than assuming its own cadence
	// stayed true across a suspend or a clock step.
	sealTick := time.NewTicker(10 * time.Second)
	defer sealTick.Stop()
	diskTick := time.NewTicker(c.diskInterval)
	defer diskTick.Stop()

	go c.consumeAccessLog(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTick.C:
			c.sample(ctx)
		case <-sealTick.C:
			c.sealIfDue(ctx)
		case <-diskTick.C:
			c.sampleDisk(ctx)
		}
	}
}

// bucketFor returns the open bucket for `at`, sealing the previous one when the
// wall clock has crossed a boundary.
func (c *Collector) bucketFor(at time.Time, seconds int) *Bucket {
	start := at.UTC().Truncate(time.Duration(seconds) * time.Second)
	if c.open == nil {
		c.open = newBucket(start, seconds)
		return c.open
	}
	if c.open.Start.Equal(start) && c.open.Seconds == seconds {
		return c.open
	}
	c.backlog = append(c.backlog, c.open)
	if len(c.backlog) > maxBacklog {
		// Dropping the OLDEST: a chart with a hole an hour ago is better than
		// one missing the last five minutes, which is what someone is looking
		// at right now.
		c.backlog = c.backlog[len(c.backlog)-maxBacklog:]
	}
	c.open = newBucket(start, seconds)
	return c.open
}

func (c *Collector) sample(ctx context.Context) {
	settings, on := c.enabled()
	if !on {
		return
	}
	samples, err := c.sampler.SampleContainers(ctx)
	if err != nil {
		c.log.Debug("metrics: sampling containers", "error", err)
		return
	}
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bucketFor(now, settings.BucketSeconds)
	live := make(map[string]bool, len(samples))
	for _, s := range samples {
		live[s.ResourceID] = true
		prev, seen := c.prevCPU[s.ResourceID]
		c.prevCPU[s.ResourceID] = s.CPUTotalNanos
		if !seen || s.CPUTotalNanos < prev {
			// First sight, or a counter that went backwards because the
			// container was recreated. There is no delta to attribute, and
			// inventing one would put a container's whole lifetime of CPU into
			// one bucket.
			continue
		}
		deltaNanos := s.CPUTotalNanos - prev
		cpuCoreMs := deltaNanos / 1e6
		interval := int(c.sampleInterval / time.Second)
		var percent float64
		if interval > 0 {
			percent = float64(cpuCoreMs) / float64(interval*1000) * 100
		}
		b.AddSample(s.ResourceKind, s.ResourceID, cpuCoreMs, percent, s.MemoryBytes, s.MemoryLimitBytes, interval)
	}
	// Forget counters for resources that are gone, so the map cannot grow with
	// every container this node ever ran.
	for id := range c.prevCPU {
		if !live[id] {
			delete(c.prevCPU, id)
		}
	}
}

func (c *Collector) sampleDisk(ctx context.Context) {
	settings, on := c.enabled()
	if !on || c.diskInterval <= 0 {
		return
	}
	samples, err := c.sampler.SampleDisk(ctx)
	if err != nil {
		c.log.Debug("metrics: sampling disk", "error", err)
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bucketFor(now, settings.BucketSeconds)
	for _, s := range samples {
		b.AddDisk(s.ResourceKind, s.ResourceID, s.ImageBytes, s.VolumeBytes, s.ContainerBytes)
	}
}

// consumeAccessLog follows the Proxy container's stdout, counts every line and
// throws it away. It reconnects with backoff: the Proxy is recreated on
// upgrades and on route changes, and a metrics reader that gives up on the
// first EOF stops counting for the rest of the agent's life.
func (c *Collector) consumeAccessLog(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if _, on := c.enabled(); !on {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		if s, _ := c.enabled(); !s.RequestAnalytics {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}

		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			err := c.sampler.StreamProxyLog(ctx, pw)
			_ = pw.CloseWithError(err)
		}()
		c.scanAccessLog(pr)
		_ = pr.Close()
		<-done

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Collector) scanAccessLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		// Docker's multiplexed stream prefixes each frame with 8 bytes when the
		// container has no TTY. The JSON starts at the first '{'.
		if i := indexByte(raw, '{'); i > 0 {
			raw = raw[i:]
		}
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		if !c.admit() {
			continue
		}
		line, ok := ParseAccessLine(raw)
		if !ok {
			continue
		}
		c.fold(line)
	}
}

func indexByte(b []byte, target byte) int {
	for i, v := range b {
		if v == target {
			return i
		}
	}
	return -1
}

// admit is the rate ceiling. Past accessLogMaxRate lines/second the collector
// switches to deterministic 1-in-N: it counts every Nth line and records the
// rate on the bucket, and the plane scales the counters back up on read AND
// says the number is an estimate. Filtering by status or duration was rejected
// — it destroys the counts, and the counts are the point. Sampling preserves
// the shape; filtering biases it.
func (c *Collector) admit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Truncate(time.Second)
	if !now.Equal(c.lineWindow) {
		// A second that stayed under the ceiling returns the bucket to exact
		// counting, so a brief spike does not label a whole day an estimate.
		if c.lineCount <= accessLogMaxRate {
			c.sampleRate = 1
		}
		c.lineWindow, c.lineCount = now, 0
	}
	c.lineCount++
	if c.lineCount > accessLogMaxRate {
		c.sampleRate = uint32(c.lineCount/accessLogMaxRate) + 1
		return c.lineCount%int(c.sampleRate) == 0
	}
	return true
}

func (c *Collector) fold(line ParsedLine) {
	settings, on := c.enabled()
	if !on || !settings.RequestAnalytics {
		return
	}
	kind, id := "application", line.ResourceID
	switch {
	case line.Unrouted:
		kind, id = "server", c.serverID
	case strings.HasPrefix(line.ResourceID, "cs_"):
		kind = "compose_stack"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bucketFor(c.now(), settings.BucketSeconds)
	path := line.Path
	if kind == "server" {
		// An unrouted request's path is somebody else's scanner probing for
		// /wp-admin. Counting it is useful; keeping a table of it is not.
		path = ""
	}
	b.AddRequest(kind, id, line.Status, line.DurationMs, line.Bytes, path, line.Redirect, c.sampleRate)
}

// sealIfDue publishes every sealed bucket. Publishing happens here rather than
// at seal time so a bus outage costs a retry rather than a lost bucket.
func (c *Collector) sealIfDue(ctx context.Context) {
	settings, on := c.enabled()
	if !on {
		return
	}
	c.mu.Lock()
	c.bucketFor(c.now(), settings.BucketSeconds) // rotates when the boundary passed
	pending := c.backlog
	c.backlog = nil
	c.mu.Unlock()

	var unsent []*Bucket
	for _, b := range pending {
		if err := c.publish(b); err != nil {
			c.log.Debug("metrics: publishing bucket", "bucket", b.Start, "error", err)
			unsent = append(unsent, b)
		}
	}
	if len(unsent) == 0 {
		return
	}
	c.mu.Lock()
	c.backlog = append(unsent, c.backlog...)
	if len(c.backlog) > maxBacklog {
		c.backlog = c.backlog[len(c.backlog)-maxBacklog:]
	}
	c.mu.Unlock()
}

// Flush publishes whatever is sealed right now. Used on shutdown so the last
// bucket is not lost to a restart.
func (c *Collector) Flush(ctx context.Context) { c.sealIfDue(ctx) }

func (c *Collector) publish(b *Bucket) error {
	report := b.report(c.serverID)
	if len(report.Resources) == 0 && len(report.Requests) == 0 && len(report.Disk) == 0 {
		return nil
	}
	data, err := proto.Marshal(report)
	if err != nil {
		return err
	}
	return c.bus.Publish(subjects.Metrics(c.serverID), data)
}

// report turns a sealed bucket into the wire message. The path rows are cut to
// the top N here, with everything below the cut summed into "(other)" — so the
// rows always reconcile with the request count for the same window.
func (b *Bucket) report(serverID string) *agentv1.MetricsReport {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := &agentv1.MetricsReport{
		ServerId:      serverID,
		BucketStart:   timestamppb.New(b.Start),
		BucketSeconds: uint32(b.Seconds),
	}
	for id, r := range b.resources {
		out.Resources = append(out.Resources, &agentv1.ResourceMetricBucket{
			ResourceKind: r.kind, ResourceId: id,
			CpuCoreMs: r.cpuCoreMs, CpuPercentPeak: r.cpuPercentPeak,
			MemoryByteSeconds: r.memByteSeconds, MemoryBytesPeak: r.memBytesPeak,
			MemoryLimitBytes: r.memoryLimitBytes,
			SampleCount:      r.samples, CoveredSeconds: r.coveredSeconds,
		})
	}
	for id, q := range b.requests {
		out.Requests = append(out.Requests, &agentv1.RequestBucket{
			ResourceKind: q.kind, ResourceId: id,
			Requests: q.requests, RedirectCount: q.redirects,
			Status_2Xx: q.s2xx, Status_3Xx: q.s3xx, Status_4Xx: q.s4xx, Status_5Xx: q.s5xx,
			ResponseBytes: q.responseBytes, LatencyBuckets: q.latency[:],
			HistogramVersion: HistogramVersion, SampleRate: q.sampleRate,
		})
		for _, p := range topPaths(q) {
			out.Paths = append(out.Paths, p.toProto(q.kind, id))
		}
	}
	for id, d := range b.disk {
		out.Disk = append(out.Disk, &agentv1.ResourceDiskBucket{
			ResourceKind: d.kind, ResourceId: id,
			ImageBytes: d.imageBytes, VolumeBytes: d.volumeBytes, ContainerBytes: d.containerBytes,
		})
	}
	return out
}

type namedPath struct {
	path string
	pathCounters
}

func (p namedPath) toProto(kind, id string) *agentv1.RequestPathBucket {
	buckets := make([]uint64, LatencySlots)
	copy(buckets, p.latency[:])
	return &agentv1.RequestPathBucket{
		ResourceKind: kind, ResourceId: id, Path: p.path,
		Requests: p.requests, Status_5Xx: p.s5xx,
		LatencyBuckets: buckets, HistogramVersion: HistogramVersion,
	}
}

func topPaths(q *requestCounters) []namedPath {
	all := make([]namedPath, 0, len(q.paths))
	for path, p := range q.paths {
		all = append(all, namedPath{path: path, pathCounters: *p})
	}
	// Partial selection rather than a full sort: TopPaths is 20 and the map is
	// capped at 200, so this is bounded either way.
	for i := 0; i < len(all) && i < TopPaths; i++ {
		best := i
		for j := i + 1; j < len(all); j++ {
			if all[j].requests > all[best].requests {
				best = j
			}
		}
		all[i], all[best] = all[best], all[i]
	}
	if len(all) <= TopPaths {
		return all
	}
	kept := all[:TopPaths]
	// Everything below the cut — including an existing "(other)" row from the
	// cap — is summed into one, so the rows always reconcile with the bucket's
	// own request count.
	other := namedPath{path: OtherPath}
	for _, p := range all[TopPaths:] {
		other.requests += p.requests
		other.s5xx += p.s5xx
		for i := range p.latency {
			other.latency[i] += p.latency[i]
		}
	}
	return append(kept, other)
}

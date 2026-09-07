// Package metrics aggregates a node's container and request activity into
// fixed wall-clock buckets and publishes one report per bucket
// (metrics-and-usage.md §4).
//
// THE WHOLE DESIGN IS ONE SENTENCE: nothing is stored per sample and nothing is
// stored per request. The agent folds samples and log lines into an open bucket
// and publishes the bucket. That is what turns 8.6 million rows a day into
// twelve thousand, and it is why this package holds counters rather than
// samples.
//
// Everything here merges — counters by sum, peaks by max, histograms
// element-wise — so a redelivered or re-sealed bucket is harmless and a window
// of any length is exact.
package metrics

import (
	"sync"
	"time"
)

// LatencyBoundsMs is histogram version 1, in milliseconds, with an implicit
// +∞ slot at the end. It matches core/domain's copy; the version travels on
// every bucket so the two can diverge across a rolling upgrade without
// silently reinterpreting anything.
var LatencyBoundsMs = [15]float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

const (
	HistogramVersion = 1
	LatencySlots     = len(LatencyBoundsMs) + 1
	// PathCap bounds the cardinality problem: /api/contacts/8f3ac1d0 is a
	// distinct path for every contact, so at most this many normalised paths
	// are tracked per routed resource per bucket and everything past it folds
	// into one "(other)" row.
	PathCap = 200
	// TopPaths is how many are published. Everything below the cut is summed
	// into "(other)" too, so the rows always reconcile with the request count.
	TopPaths  = 20
	OtherPath = "(other)"
)

// resourceCounters is one resource's CPU and memory over one bucket.
type resourceCounters struct {
	kind             string
	cpuCoreMs        uint64
	cpuPercentPeak   float64
	memByteSeconds   uint64
	memBytesPeak     uint64
	memoryLimitBytes uint64
	samples          uint32
	coveredSeconds   uint32
}

// requestCounters is one routed resource's traffic over one bucket.
type requestCounters struct {
	kind          string
	requests      uint64
	redirects     uint64
	s2xx          uint64
	s3xx          uint64
	s4xx          uint64
	s5xx          uint64
	responseBytes uint64
	latency       [LatencySlots]uint64
	sampleRate    uint32
	paths         map[string]*pathCounters
	pathCapHit    bool
}

type pathCounters struct {
	requests uint64
	s5xx     uint64
	latency  [LatencySlots]uint64
}

// Bucket is one sealed or open interval of a node's activity.
type Bucket struct {
	Start   time.Time
	Seconds int

	mu        sync.Mutex
	resources map[string]*resourceCounters
	requests  map[string]*requestCounters
	disk      map[string]*diskCounters
}

type diskCounters struct {
	kind           string
	imageBytes     uint64
	volumeBytes    uint64
	containerBytes uint64
}

func newBucket(start time.Time, seconds int) *Bucket {
	return &Bucket{
		Start: start, Seconds: seconds,
		resources: map[string]*resourceCounters{},
		requests:  map[string]*requestCounters{},
		disk:      map[string]*diskCounters{},
	}
}

// AddSample folds one container observation into the bucket. cpuCoreMs is the
// DELTA since the previous sample, not a cumulative counter — the collector
// owns that subtraction so a missed read costs nothing.
func (b *Bucket) AddSample(kind, id string, cpuCoreMs uint64, cpuPercent float64, memBytes, memLimit uint64, intervalSeconds int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.resources[id]
	if c == nil {
		c = &resourceCounters{kind: kind}
		b.resources[id] = c
	}
	c.cpuCoreMs += cpuCoreMs
	if cpuPercent > c.cpuPercentPeak {
		c.cpuPercentPeak = cpuPercent
	}
	c.memByteSeconds += memBytes * uint64(intervalSeconds)
	if memBytes > c.memBytesPeak {
		c.memBytesPeak = memBytes
	}
	c.memoryLimitBytes = memLimit
	c.samples++
	c.coveredSeconds += uint32(intervalSeconds)
}

// AddRequest folds one access-log line in. A redirect from the `-http` sibling
// router is counted and then returns: including it would double every visit and
// drag p95 toward the 1 ms redirect.
func (b *Bucket) AddRequest(kind, id string, status int, durationMs float64, responseBytes uint64, path string, redirect bool, sampleRate uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.requests[id]
	if c == nil {
		c = &requestCounters{kind: kind, paths: map[string]*pathCounters{}, sampleRate: 1}
		b.requests[id] = c
	}
	if sampleRate > c.sampleRate {
		c.sampleRate = sampleRate
	}
	if redirect {
		c.redirects++
		return
	}
	c.requests++
	c.responseBytes += responseBytes
	switch {
	case status >= 500:
		c.s5xx++
	case status >= 400:
		c.s4xx++
	case status >= 300:
		c.s3xx++
	case status >= 200:
		c.s2xx++
	}
	slot := latencySlot(durationMs)
	c.latency[slot]++

	if path == "" {
		return
	}
	p := c.paths[path]
	if p == nil {
		if len(c.paths) >= PathCap {
			c.pathCapHit = true
			path = OtherPath
			p = c.paths[OtherPath]
		}
		if p == nil {
			p = &pathCounters{}
			c.paths[path] = p
		}
	}
	p.requests++
	if status >= 500 {
		p.s5xx++
	}
	p.latency[slot]++
}

// AddDisk records one resource's hourly disk reading.
func (b *Bucket) AddDisk(kind, id string, image, volume, container uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.disk[id]
	if d == nil {
		d = &diskCounters{kind: kind}
		b.disk[id] = d
	}
	d.imageBytes += image
	d.volumeBytes += volume
	d.containerBytes += container
}

// latencySlot is the histogram index for a duration. The last slot is the
// overflow, which is what makes "at least 60 seconds" expressible.
func latencySlot(ms float64) int {
	for i, bound := range LatencyBoundsMs {
		if ms <= bound {
			return i
		}
	}
	return LatencySlots - 1
}

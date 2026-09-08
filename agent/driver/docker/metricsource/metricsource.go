package metricsource

// Package metricsource is the Docker side of metrics collection (metrics-and-usage.md §4.1–4.2).
//
// Attribution needs no new mechanism, because the containers are already
// labelled: cypherpanel.app-id for an Application, cypherpanel.db-id for a
// Managed Database, and Compose's own com.docker.compose.project =
// cypher-<stack id> for a Compose Stack. A container carrying none of those is
// NOT OURS and is not sampled — the same rule disk management follows, so an
// operator's own containers on a shared box never appear in a project's
// figures.

import (
	"context"
	"io"
	"strings"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
	"github.com/MaramHarsha/cypherpanel/agent/metrics"
)

// composeProjectLabel is Compose's own label, and composeProjectPrefix is what
// the reconciler names its projects.
const (
	composeProjectLabel  = "com.docker.compose.project"
	composeProjectPrefix = "cypher-"
	// ProxyContainer is the managed Proxy, whose stdout carries the access log.
	ProxyContainer = "cypher-proxy"
)

// MetricsSource adapts the engine to the collector's Sampler seam. It holds no
// state: everything cumulative lives in the collector, so a driver swap loses
// nothing but one interval.
type MetricsSource struct {
	engine *engine.Client
}

func New(e *engine.Client) *MetricsSource { return &MetricsSource{engine: e} }

// attribute maps a container's labels to the resource it belongs to. The empty
// kind means "not ours".
func attribute(labels map[string]string) (kind, id string) {
	if id := labels[driver.LabelAppID]; id != "" {
		return "application", id
	}
	if id := labels[docker.LabelDbID]; id != "" {
		return "database", id
	}
	if p := labels[composeProjectLabel]; strings.HasPrefix(p, composeProjectPrefix) {
		return "compose_stack", strings.TrimPrefix(p, composeProjectPrefix)
	}
	return "", ""
}

func (m *MetricsSource) SampleContainers(ctx context.Context) ([]metrics.ContainerSample, error) {
	stats, err := m.engine.SampleContainers(ctx)
	if err != nil {
		return nil, err
	}
	// A Compose Stack is several containers, so its samples are SUMMED into
	// one resource: the operator asked about the stack, not about its
	// sidecars, and a per-container breakdown of somebody else's compose file
	// is a level of detail this product does not model.
	byResource := map[string]*metrics.ContainerSample{}
	order := make([]string, 0, len(stats))
	for _, s := range stats {
		kind, id := attribute(s.Labels)
		if kind == "" {
			continue
		}
		agg := byResource[id]
		if agg == nil {
			agg = &metrics.ContainerSample{ResourceKind: kind, ResourceID: id, OnlineCPUs: s.OnlineCPUs}
			byResource[id] = agg
			order = append(order, id)
		}
		agg.CPUTotalNanos += s.CPUTotalNanos
		agg.MemoryBytes += s.MemoryBytes
		// The limit is the container's own; summing them across a stack would
		// invent a ceiling nothing enforces, so the largest one stands in.
		if s.MemoryLimitBytes > agg.MemoryLimitBytes {
			agg.MemoryLimitBytes = s.MemoryLimitBytes
		}
	}
	out := make([]metrics.ContainerSample, 0, len(order))
	for _, id := range order {
		out = append(out, *byResource[id])
	}
	return out, nil
}

// SampleDisk attributes image, writable-layer and volume bytes by the same
// labels.
//
// These figures DO NOT SUM to the host's usage, and that is deliberate: image
// layers are shared, and a base layer used by four applications is counted for
// each of them, because the question the number answers is "what is this
// resource responsible for", not "how would the host shrink if I deleted it".
func (m *MetricsSource) SampleDisk(ctx context.Context) ([]metrics.DiskSample, error) {
	du, err := m.engine.DiskUsage(ctx)
	if err != nil {
		return nil, err
	}

	volumeSize := make(map[string]int64, len(du.Volumes))
	for _, v := range du.Volumes {
		volumeSize[v.Name] = v.Size
	}
	imageSize := make(map[string]int64, len(du.Images))
	imageByID := make(map[string]map[string]string, len(du.Images))
	for _, i := range du.Images {
		imageSize[i.ID] = i.Size
		imageByID[i.ID] = i.Labels
		for _, tag := range i.RepoTags {
			imageSize[tag] = i.Size
		}
	}

	byResource := map[string]*metrics.DiskSample{}
	order := make([]string, 0)
	get := func(kind, id string) *metrics.DiskSample {
		s := byResource[id]
		if s == nil {
			s = &metrics.DiskSample{ResourceKind: kind, ResourceID: id}
			byResource[id] = s
			order = append(order, id)
		}
		return s
	}

	for _, ct := range du.Containers {
		kind, id := attribute(ct.Labels)
		if kind == "" {
			continue
		}
		s := get(kind, id)
		if ct.SizeRw > 0 {
			s.ContainerBytes += uint64(ct.SizeRw)
		}
		if sz, ok := imageSize[ct.Image]; ok && sz > 0 {
			s.ImageBytes += uint64(sz)
		}
		for _, vol := range ct.Mounts {
			if sz, ok := volumeSize[vol]; ok && sz > 0 {
				s.VolumeBytes += uint64(sz)
			}
		}
	}

	// A built image carries our labels; a PULLED one cannot, which is why the
	// container's own image reference above is the second route. Adding
	// labelled images here catches the ones retained for a rollback, which no
	// running container references.
	for id, labels := range imageByID {
		kind, rid := attribute(labels)
		if kind == "" {
			continue
		}
		if sz := imageSize[id]; sz > 0 {
			get(kind, rid).ImageBytes += uint64(sz)
		}
	}

	out := make([]metrics.DiskSample, 0, len(order))
	for _, id := range order {
		out = append(out, *byResource[id])
	}
	return out, nil
}

// StreamProxyLog follows the Proxy container's stdout. Every line is discarded
// after counting: access lines never reach logs.*, never reach the plane, and
// never reach an operator's log pane.
func (m *MetricsSource) StreamProxyLog(ctx context.Context, w io.Writer) error {
	return m.engine.StreamLogsFrom(ctx, ProxyContainer, w)
}

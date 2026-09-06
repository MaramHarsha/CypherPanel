// Package heartbeat publishes the agent's periodic liveness signal on the bus.
// The control plane derives observed status from these (state.* — ADR-003); the
// agent never asserts its own reachability, it just keeps beating.
package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// Health is a concurrency-safe holder for "is some subsystem of this agent
// broken right now". Subsystems that fail in a loop rather than crashing — the
// Proxy is the one that matters, since it cannot bind :80 if anything else on
// the host already has it — record their last error here so the heartbeat can
// carry the fact upward. Without this the plane only ever hears READY, and a
// server whose Proxy can never start still shows green while every routed
// deploy silently fails (ui-principles §10).
//
// The zero value is healthy and usable.
type Health struct {
	mu  sync.RWMutex
	err error
}

// Set records the subsystem's latest outcome; nil clears it.
func (h *Health) Set(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
}

// Err reports the last recorded failure, or nil when healthy.
func (h *Health) Err() error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

// Publisher emits heartbeats for one server at a fixed interval.
type Publisher struct {
	nc       *nats.Conn
	serverID string
	version  string
	driver   string
	role     string
	interval time.Duration
	health   *Health
	// dataRoot is the filesystem whose free space is reported
	// (disk-management.md §4). Empty reports nothing, which the plane reads as
	// unknown — a node that cannot answer is silent rather than alarming.
	dataRoot string
	log      *slog.Logger
}

// NewPublisher wires the publisher. role is the agent's --role value
// (builder-role-and-relay.md §1), reported so the plane can route builds.
// health may be nil, in which case the agent always reports READY.
//
// SetDataRoot adds the disk report; without it the heartbeat carries zeros,
// exactly as it did before disk management existed.
func NewPublisher(nc *nats.Conn, serverID, version, driver, role string, interval time.Duration, health *Health, log *slog.Logger) *Publisher {
	return &Publisher{
		nc: nc, serverID: serverID, version: version, driver: driver,
		role: role, interval: interval, health: health, log: log,
	}
}

// SetDataRoot records the filesystem to report free space for — the daemon's
// own DockerRootDir, read from /info rather than assumed to be
// /var/lib/docker, because an operator who moved it is exactly the one who will
// not have moved an alert with it (disk-management.md §4).
func (p *Publisher) SetDataRoot(path string) { p.dataRoot = path }

// Run publishes one heartbeat immediately, then every interval until ctx is
// cancelled. It owns its ticker's lifecycle (ENGINEERING rule 7).
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	p.publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.publish()
		}
	}
}

// status reports READY unless a subsystem has recorded a failure. The plane
// maps DEGRADED straight onto the server's amber status (core/status), which
// is the whole point: a subsystem stuck in a retry loop is invisible
// otherwise.
func (p *Publisher) status() agentv1.AgentStatus {
	if p.health.Err() != nil {
		return agentv1.AgentStatus_AGENT_STATUS_DEGRADED
	}
	return agentv1.AgentStatus_AGENT_STATUS_READY
}

func (p *Publisher) publish() {
	hb := &agentv1.Heartbeat{
		ServerId:     p.serverID,
		EmittedAt:    timestamppb.Now(),
		AgentVersion: p.version,
		Driver:       p.driver,
		Status:       p.status(),
		Role:         p.role,
	}
	// Measured per heartbeat, not cached: the number's whole value is that it
	// is current, and statfs is a single syscall.
	if p.dataRoot != "" {
		if total, free, ok := diskUsage(p.dataRoot); ok {
			hb.DiskTotalBytes, hb.DiskFreeBytes = total, free
		}
	}
	data, err := proto.Marshal(hb)
	if err != nil {
		p.log.Error("marshaling heartbeat", "error", err)
		return
	}
	if err := p.nc.Publish(subjects.Heartbeat(p.serverID), data); err != nil {
		p.log.Warn("publishing heartbeat", "error", err)
	}
}

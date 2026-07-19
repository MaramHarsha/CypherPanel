// Package heartbeat publishes the agent's periodic liveness signal on the bus.
// The control plane derives observed status from these (state.* — ADR-003); the
// agent never asserts its own reachability, it just keeps beating.
package heartbeat

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// Publisher emits heartbeats for one server at a fixed interval.
type Publisher struct {
	nc       *nats.Conn
	serverID string
	version  string
	driver   string
	role     string
	interval time.Duration
	log      *slog.Logger
}

// NewPublisher wires the publisher. role is the agent's --role value
// (builder-role-and-relay.md §1), reported so the plane can route builds.
func NewPublisher(nc *nats.Conn, serverID, version, driver, role string, interval time.Duration, log *slog.Logger) *Publisher {
	return &Publisher{nc: nc, serverID: serverID, version: version, driver: driver, role: role, interval: interval, log: log}
}

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

func (p *Publisher) publish() {
	data, err := proto.Marshal(&agentv1.Heartbeat{
		ServerId:     p.serverID,
		EmittedAt:    timestamppb.Now(),
		AgentVersion: p.version,
		Driver:       p.driver,
		Status:       agentv1.AgentStatus_AGENT_STATUS_READY,
		Role:         p.role,
	})
	if err != nil {
		p.log.Error("marshaling heartbeat", "error", err)
		return
	}
	if err := p.nc.Publish(subjects.Heartbeat(p.serverID), data); err != nil {
		p.log.Warn("publishing heartbeat", "error", err)
	}
}

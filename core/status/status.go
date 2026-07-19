// Package status turns agent heartbeats into observed server state, and marks
// silent servers Unknown. It is the concrete implementation of ui-principles
// §10: the control plane never shows a status it cannot currently verify.
package status

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Store is the persistence status needs (consumer-defined).
type Store interface {
	RecordHeartbeat(ctx context.Context, id string, status domain.ServerStatus, agentVersion, driver, role string) (domain.Server, error)
	MarkStaleServersUnknown(ctx context.Context, cutoff time.Time) ([]string, error)
}

// Recorder applies incoming heartbeats to observed state.
type Recorder struct {
	store Store
	log   *slog.Logger
}

// NewRecorder wires the recorder.
func NewRecorder(s Store, log *slog.Logger) *Recorder {
	return &Recorder{store: s, log: log}
}

// Record parses a heartbeat payload and updates the server's observed status.
// Malformed or identity-less heartbeats are dropped with a warning rather than
// failing — a bad message from one agent must not disrupt the bus. Safe for
// concurrent use.
func (r *Recorder) Record(ctx context.Context, data []byte) {
	var hb agentv1.Heartbeat
	if err := proto.Unmarshal(data, &hb); err != nil {
		r.log.Warn("dropping malformed heartbeat", "error", err)
		return
	}
	if hb.GetServerId() == "" {
		r.log.Warn("dropping heartbeat with empty server id")
		return
	}
	st := mapStatus(hb.GetStatus())
	// Older agents (pre-role) send no role; they behave as "all" and are
	// recorded as such (builder-role-and-relay.md §1 default). Anything
	// outside the vocabulary is dropped un-persisted, like any other
	// malformed heartbeat — the role column only ever holds known values.
	role := hb.GetRole()
	switch role {
	case "":
		role = domain.RoleAll
	case domain.RoleAll, domain.RoleBuilder, domain.RoleWorker:
	default:
		r.log.Warn("dropping heartbeat with unknown role", "server_id", hb.GetServerId(), "role", role)
		return
	}
	if _, err := r.store.RecordHeartbeat(ctx, hb.GetServerId(), st, hb.GetAgentVersion(), hb.GetDriver(), role); err != nil {
		r.log.Error("recording heartbeat", "server_id", hb.GetServerId(), "error", err)
	}
}

// mapStatus translates the agent's self-reported liveness into the server
// status vocabulary. The plane never maps anything to Running that the agent
// did not explicitly report Ready.
func mapStatus(s agentv1.AgentStatus) domain.ServerStatus {
	switch s {
	case agentv1.AgentStatus_AGENT_STATUS_READY:
		return domain.StatusRunning
	case agentv1.AgentStatus_AGENT_STATUS_DEGRADED:
		return domain.StatusDegraded
	default:
		return domain.StatusUnknown
	}
}

// Sweeper periodically flips enrolled-but-silent servers to Unknown.
type Sweeper struct {
	store    Store
	stale    time.Duration
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time
}

// NewSweeper wires the sweeper. A server not heard from within stale is marked
// Unknown; the check runs every interval.
func NewSweeper(s Store, stale, interval time.Duration, log *slog.Logger) *Sweeper {
	return &Sweeper{store: s, stale: stale, interval: interval, log: log, now: time.Now}
}

// Run sweeps on each tick until ctx is cancelled. It owns its ticker's
// lifecycle (ENGINEERING rule 7) and returns when ctx is done.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) {
	cutoff := s.now().Add(-s.stale)
	ids, err := s.store.MarkStaleServersUnknown(ctx, cutoff)
	if err != nil {
		s.log.Error("stale server sweep failed", "error", err)
		return
	}
	for _, id := range ids {
		s.log.Info("server marked unknown: no recent heartbeat", "server_id", id)
	}
}

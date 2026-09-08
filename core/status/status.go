// Package status turns agent heartbeats into observed server state, and marks
// silent servers Unknown. It is the concrete implementation of ui-principles
// §10: the control plane never shows a status it cannot currently verify.
package status

import (
	"context"
	"log/slog"
	"time"

	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Store is the persistence status needs (consumer-defined).
type Store interface {
	RecordHeartbeat(ctx context.Context, id string, status domain.ServerStatus, agentVersion, driver, role string, diskTotal, diskFree uint64) (domain.Server, error)
	MarkStaleServersUnknown(ctx context.Context, cutoff time.Time) ([]string, error)
	// SetServerDiskLow records the transition, so the alert fires once
	// (disk-management.md §5).
	SetServerDiskLow(ctx context.Context, id string, low bool) error
}

// DiskSink receives a server's disk-pressure transitions (disk-management.md
// §5). Consumer-defined; the wiring satisfies it with the notification inbox,
// because a Server belongs to no project and a Notifier is scoped to one.
// No sink announces nothing, which is what a panel without an inbox does.
type DiskSink interface {
	AnnounceServerDisk(ctx context.Context, server domain.Server, kind, detail string) error
}

// Recorder applies incoming heartbeats to observed state.
type Recorder struct {
	store Store
	// warnPercent is the used-percentage at which a server reports low disk;
	// zero disables the alert entirely (disk-management.md §7).
	warnPercent int
	sinks       []DiskSink
	log         *slog.Logger
}

// NewRecorder wires the recorder.
func NewRecorder(s Store, log *slog.Logger) *Recorder {
	return &Recorder{store: s, log: log}
}

// WatchDisk turns on disk alerting. Kept out of NewRecorder so it stays an
// opt-in add-on: a panel that never calls it records the numbers and announces
// nothing, which is exactly how it behaved before.
func (r *Recorder) WatchDisk(warnPercent int, sinks ...DiskSink) {
	r.warnPercent = warnPercent
	r.sinks = append(r.sinks, sinks...)
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
	server, err := r.store.RecordHeartbeat(ctx, hb.GetServerId(), st, hb.GetAgentVersion(), hb.GetDriver(), role,
		hb.GetDiskTotalBytes(), hb.GetDiskFreeBytes())
	if err != nil {
		r.log.Error("recording heartbeat", "server_id", hb.GetServerId(), "error", err)
		return
	}
	r.checkDisk(ctx, server)
}

// checkDisk announces a server crossing the disk threshold, and crossing back.
//
// It fires on the TRANSITION, never on the heartbeat: one arrives every few
// seconds, and a channel that repeats itself gets muted — taking the next real
// alert with it. `server` carries the state as it was BEFORE this heartbeat's
// measurement was compared, because RecordHeartbeat writes the numbers and
// leaves disk_low alone.
func (r *Recorder) checkDisk(ctx context.Context, server domain.Server) {
	if r.warnPercent <= 0 || server.DiskTotalBytes == 0 {
		return // disabled, or a host that could not answer — never read as full
	}
	used := server.DiskTotalBytes - server.DiskFreeBytes
	usedPercent := int(used * 100 / server.DiskTotalBytes)
	low := usedPercent >= r.warnPercent
	if low == server.DiskLow {
		return
	}
	if err := r.store.SetServerDiskLow(ctx, server.ID, low); err != nil {
		// Not announced: a transition we could not record would be announced
		// again on the very next heartbeat, which is the flood this exists to
		// prevent.
		r.log.Error("recording server disk state", "server_id", server.ID, "error", err)
		return
	}
	kind := domain.InboxKindServerDiskRecovered
	detail := fmt.Sprintf("%d%% of the disk is used, %s free.", usedPercent, humanBytes(server.DiskFreeBytes))
	if low {
		kind = domain.InboxKindServerDiskLow
	}
	r.log.Warn("server disk state changed", "server_id", server.ID, "used_percent", usedPercent, "low", low)
	for _, sink := range r.sinks {
		if err := sink.AnnounceServerDisk(ctx, server, kind, detail); err != nil {
			// The transition is already recorded, so this is not repeated on
			// the next heartbeat. Losing the announcement is the lesser cost.
			r.log.Error("announcing server disk state", "server_id", server.ID, "error", err)
		}
	}
}

// humanBytes renders a size an operator can act on. Exact bytes in an alert is
// a number nobody converts under pressure.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
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

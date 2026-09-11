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
	// SetServerAgentUpdate records what the agent last said about its own
	// binary (agent-updates.md §7). Separate from RecordHeartbeat because the
	// plane compares the PREVIOUS phase to decide whether a rollback is a
	// transition worth announcing.
	SetServerAgentUpdate(ctx context.Context, id, phase, target, detail string) error
	// SetServerSubsystemHealth records WHICH subsystems the agent reported
	// unhealthy. Separate from RecordHeartbeat for the same reason: it changes
	// rarely, and the plane compares the previous value to avoid a write per
	// heartbeat.
	SetServerSubsystemHealth(ctx context.Context, id string, health []domain.SubsystemHealth) error
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
	r.checkAgentUpdate(ctx, server, hb.GetAgentUpdate())
	r.recordSubsystemHealth(ctx, server, st, hb.GetSubsystemHealth())
}

// recordSubsystemHealth stores which parts of the agent are unhealthy, so a
// degraded server can say what is wrong with it instead of only that something
// is (ADR-002 leaves no SSH to go and look).
//
// `repeated` has NO presence on the wire, so an empty list means both "healthy"
// and "an agent older than this field". The status word resolves it, and both
// readings land on the same write: a READY agent has nothing wrong with it
// either way, and a DEGRADED agent that names nothing is one that cannot — the
// stored detail is cleared and the screen says so, rather than keeping a
// finding from ten minutes ago beside a status that has since changed.
func (r *Recorder) recordSubsystemHealth(ctx context.Context, server domain.Server, st domain.ServerStatus, reported []*agentv1.SubsystemHealth) {
	health := make([]domain.SubsystemHealth, 0, len(reported))
	if st == domain.StatusDegraded {
		for _, h := range reported {
			if h.GetSubsystem() == "" {
				continue
			}
			health = append(health, domain.SubsystemHealth{Subsystem: h.GetSubsystem(), Message: h.GetMessage()})
		}
	}
	if sameHealth(server.SubsystemHealth, health) {
		return // no change: a heartbeat every few seconds must not be a write
	}
	if err := r.store.SetServerSubsystemHealth(ctx, server.ID, health); err != nil {
		r.log.Error("recording subsystem health", "server_id", server.ID, "error", err)
	}
}

func sameHealth(a, b []domain.SubsystemHealth) bool {
	if len(a) != len(b) {
		return false
	}
	// Both sides are ordered: the agent sorts by subsystem before publishing,
	// so equal findings compare equal without a set on either end.
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// agentPhases maps the wire enum onto the stored vocabulary. An agent NEWER
// than this plane can report a phase this plane does not know; it is stored as
// the empty string rather than dropping the whole heartbeat, because a fleet
// mid-upgrade is exactly when the plane must keep hearing from its hosts.
var agentPhases = map[agentv1.AgentUpdateStatus_Phase]string{
	agentv1.AgentUpdateStatus_PHASE_IDLE:        domain.AgentPhaseIdle,
	agentv1.AgentUpdateStatus_PHASE_PENDING:     domain.AgentPhasePending,
	agentv1.AgentUpdateStatus_PHASE_DOWNLOADING: domain.AgentPhaseDownloading,
	agentv1.AgentUpdateStatus_PHASE_VERIFYING:   domain.AgentPhaseVerifying,
	agentv1.AgentUpdateStatus_PHASE_SWAPPING:    domain.AgentPhaseSwapping,
	agentv1.AgentUpdateStatus_PHASE_ROLLED_BACK: domain.AgentPhaseRolledBack,
	agentv1.AgentUpdateStatus_PHASE_FAILED:      domain.AgentPhaseFailed,
	agentv1.AgentUpdateStatus_PHASE_DISABLED:    domain.AgentPhaseDisabled,
}

// checkAgentUpdate records the observed phase and announces a rollback once.
//
// An agent that carries NO update status at all — every agent before ADR-010 —
// leaves the stored columns alone rather than clearing them: absence is silence,
// not "idle", and overwriting a rolled_back row with a blank because one old
// agent heartbeat arrived would erase the amber row an operator has to act on.
func (r *Recorder) checkAgentUpdate(ctx context.Context, server domain.Server, st *agentv1.AgentUpdateStatus) {
	if st == nil {
		return
	}
	phase := agentPhases[st.GetPhase()]
	if phase == server.AgentUpdatePhase &&
		st.GetTargetVersion() == server.AgentUpdateTarget &&
		st.GetDetail() == server.AgentUpdateDetail {
		return // no change: a heartbeat every few seconds must not be a write
	}
	if err := r.store.SetServerAgentUpdate(ctx, server.ID, phase, st.GetTargetVersion(), st.GetDetail()); err != nil {
		// Not announced: a transition we could not record would be announced
		// again on the very next heartbeat, which is the flood this avoids.
		r.log.Error("recording the agent update phase", "server_id", server.ID, "error", err)
		return
	}
	if phase != domain.AgentPhaseRolledBack || server.AgentUpdatePhase == domain.AgentPhaseRolledBack {
		return
	}
	detail := st.GetDetail()
	if detail == "" {
		detail = "The agent rolled its own update back and is running its previous version."
	}
	r.log.Warn("agent update rolled back", "server_id", server.ID, "target", st.GetTargetVersion())
	for _, sink := range r.sinks {
		if err := sink.AnnounceServerDisk(ctx, server, domain.InboxAgentUpdateFailed,
			fmt.Sprintf("Update to %s was rolled back. %s", st.GetTargetVersion(), detail)); err != nil {
			r.log.Error("announcing an agent rollback", "server_id", server.ID, "error", err)
		}
	}
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

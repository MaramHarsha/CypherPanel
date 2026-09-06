package status

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

type recordCall struct {
	id     string
	status domain.ServerStatus
	driver string
	role   string
}

type fakeStore struct {
	records []recordCall
	staleIn []string
	// server is what RecordHeartbeat returns — the row as it stands AFTER the
	// measurement is written and BEFORE disk_low is compared, which is what the
	// transition check reads (disk-management.md §5).
	server   domain.Server
	diskLow  []bool
	diskErr  error
	lastDisk [2]uint64
}

func (f *fakeStore) RecordHeartbeat(_ context.Context, id string, st domain.ServerStatus, _, driver, role string, diskTotal, diskFree uint64) (domain.Server, error) {
	f.records = append(f.records, recordCall{id: id, status: st, driver: driver, role: role})
	f.lastDisk = [2]uint64{diskTotal, diskFree}
	srv := f.server
	srv.ID = id
	srv.DiskTotalBytes, srv.DiskFreeBytes = diskTotal, diskFree
	return srv, nil
}

func (f *fakeStore) SetServerDiskLow(_ context.Context, _ string, low bool) error {
	if f.diskErr != nil {
		return f.diskErr
	}
	f.diskLow = append(f.diskLow, low)
	return nil
}

func (f *fakeStore) MarkStaleServersUnknown(_ context.Context, _ time.Time) ([]string, error) {
	return f.staleIn, nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecordAppliesHeartbeat(t *testing.T) {
	fs := &fakeStore{}
	r := NewRecorder(fs, quietLog())

	data, err := proto.Marshal(&agentv1.Heartbeat{
		ServerId:     "srv_1",
		AgentVersion: "0.1.0",
		Driver:       "docker",
		Status:       agentv1.AgentStatus_AGENT_STATUS_READY,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	r.Record(context.Background(), data)

	if len(fs.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(fs.records))
	}
	got := fs.records[0]
	if got.id != "srv_1" || got.status != domain.StatusRunning || got.driver != "docker" {
		t.Fatalf("unexpected record: %+v", got)
	}
	// A pre-role heartbeat (no role field) persists as "all" — the
	// backward-compatible default (builder-role-and-relay.md §1).
	if got.role != domain.RoleAll {
		t.Fatalf("role = %q, want %q for a role-less heartbeat", got.role, domain.RoleAll)
	}
}

// A role outside the vocabulary is dropped un-persisted, like any other
// malformed heartbeat: the role column must only ever hold known values.
func TestRecordDropsUnknownRole(t *testing.T) {
	fs := &fakeStore{}
	r := NewRecorder(fs, quietLog())
	data, err := proto.Marshal(&agentv1.Heartbeat{
		ServerId: "srv_1",
		Status:   agentv1.AgentStatus_AGENT_STATUS_READY,
		Role:     "root-of-all-builds",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	r.Record(context.Background(), data)
	if len(fs.records) != 0 {
		t.Fatalf("unknown role should be dropped, got %d records", len(fs.records))
	}
}

func TestRecordDropsGarbage(t *testing.T) {
	fs := &fakeStore{}
	r := NewRecorder(fs, quietLog())
	r.Record(context.Background(), []byte("not a protobuf"))
	if len(fs.records) != 0 {
		t.Fatalf("garbage should be dropped, got %d records", len(fs.records))
	}
}

func TestRecordDropsEmptyServerID(t *testing.T) {
	fs := &fakeStore{}
	r := NewRecorder(fs, quietLog())
	data, _ := proto.Marshal(&agentv1.Heartbeat{Status: agentv1.AgentStatus_AGENT_STATUS_READY})
	r.Record(context.Background(), data)
	if len(fs.records) != 0 {
		t.Fatalf("empty server id should be dropped, got %d records", len(fs.records))
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[agentv1.AgentStatus]domain.ServerStatus{
		agentv1.AgentStatus_AGENT_STATUS_READY:       domain.StatusRunning,
		agentv1.AgentStatus_AGENT_STATUS_DEGRADED:    domain.StatusDegraded,
		agentv1.AgentStatus_AGENT_STATUS_UNSPECIFIED: domain.StatusUnknown,
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestSweeperMarksStale(t *testing.T) {
	fs := &fakeStore{staleIn: []string{"srv_1", "srv_2"}}
	sw := NewSweeper(fs, time.Minute, time.Second, quietLog())
	sw.sweep(context.Background()) // exercise one sweep directly
	// No assertion beyond "does not panic and calls the store"; the store fake
	// returns the ids it was seeded with. The Run loop is covered by wiring.
}

// ─── disk alerting (disk-management.md §5) ──────────────────────────────────

// recordingSink captures the transitions announced.
type recordingSink struct {
	kinds   []string
	details []string
	err     error
}

func (s *recordingSink) AnnounceServerDisk(_ context.Context, _ domain.Server, kind, detail string) error {
	s.kinds = append(s.kinds, kind)
	s.details = append(s.details, detail)
	return s.err
}

// heartbeatWithDisk builds a heartbeat carrying a filesystem measurement.
func heartbeatWithDisk(t *testing.T, total, free uint64) []byte {
	t.Helper()
	data, err := proto.Marshal(&agentv1.Heartbeat{
		ServerId: "srv_1", AgentVersion: "v1", Driver: "docker", Role: domain.RoleAll,
		Status:         agentv1.AgentStatus_AGENT_STATUS_READY,
		DiskTotalBytes: total, DiskFreeBytes: free,
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	return data
}

func TestDiskMeasurementIsRecorded(t *testing.T) {
	fs := &fakeStore{}
	r := NewRecorder(fs, quietLog())

	r.Record(context.Background(), heartbeatWithDisk(t, 100, 40))
	if fs.lastDisk != [2]uint64{100, 40} {
		t.Fatalf("recorded %v, want the measurement carried through", fs.lastDisk)
	}
}

// Crossing the threshold announces once; staying past it announces nothing —
// a heartbeat arrives every few seconds, and a channel that repeats itself
// gets muted, taking the next real alert with it.
func TestCrossingTheThresholdAnnouncesOnce(t *testing.T) {
	fs := &fakeStore{}
	sink := &recordingSink{}
	r := NewRecorder(fs, quietLog())
	r.WatchDisk(85, sink)

	// 90% used: past the threshold.
	r.Record(context.Background(), heartbeatWithDisk(t, 100, 10))
	if len(sink.kinds) != 1 || sink.kinds[0] != domain.InboxKindServerDiskLow {
		t.Fatalf("announced %v, want one disk_low", sink.kinds)
	}
	if !strings.Contains(sink.details[0], "90%") {
		t.Fatalf("detail = %q, want the number in it", sink.details[0])
	}

	// Still past it, and now the row says so.
	fs.server.DiskLow = true
	r.Record(context.Background(), heartbeatWithDisk(t, 100, 8))
	if len(sink.kinds) != 1 {
		t.Fatalf("announced %v, want no repeat while it stays low", sink.kinds)
	}
}

func TestCrossingBackAnnouncesRecovery(t *testing.T) {
	fs := &fakeStore{server: domain.Server{DiskLow: true}}
	sink := &recordingSink{}
	r := NewRecorder(fs, quietLog())
	r.WatchDisk(85, sink)

	r.Record(context.Background(), heartbeatWithDisk(t, 100, 50))
	if len(sink.kinds) != 1 || sink.kinds[0] != domain.InboxKindServerDiskRecovered {
		t.Fatalf("announced %v, want one disk_recovered", sink.kinds)
	}
	if len(fs.diskLow) != 1 || fs.diskLow[0] {
		t.Fatalf("recorded %v, want the row cleared", fs.diskLow)
	}
}

// A host that could not answer reports zeros. That is unknown, and must never
// read as full — a silent node is far better than a false page.
func TestAnUnreportedDiskNeverAlerts(t *testing.T) {
	fs := &fakeStore{}
	sink := &recordingSink{}
	r := NewRecorder(fs, quietLog())
	r.WatchDisk(85, sink)

	r.Record(context.Background(), heartbeatWithDisk(t, 0, 0))
	if len(sink.kinds) != 0 {
		t.Fatalf("announced %v for a host that reported nothing", sink.kinds)
	}
}

// Zero disables the alert, and a panel that never wires a sink records the
// numbers and announces nothing — how it behaved before this existed.
func TestDiskAlertingIsOptIn(t *testing.T) {
	fs := &fakeStore{}
	sink := &recordingSink{}

	off := NewRecorder(fs, quietLog())
	off.WatchDisk(0, sink)
	off.Record(context.Background(), heartbeatWithDisk(t, 100, 1))

	unwired := NewRecorder(fs, quietLog())
	unwired.Record(context.Background(), heartbeatWithDisk(t, 100, 1))

	if len(sink.kinds) != 0 {
		t.Fatalf("announced %v, want nothing when disabled or unwired", sink.kinds)
	}
}

// A transition that could not be recorded is not announced: it would be
// announced again on the very next heartbeat, which is the flood this exists
// to prevent.
func TestAnUnrecordedTransitionIsNotAnnounced(t *testing.T) {
	fs := &fakeStore{diskErr: errors.New("database is gone")}
	sink := &recordingSink{}
	r := NewRecorder(fs, quietLog())
	r.WatchDisk(85, sink)

	r.Record(context.Background(), heartbeatWithDisk(t, 100, 5))
	if len(sink.kinds) != 0 {
		t.Fatalf("announced %v despite failing to record the transition", sink.kinds)
	}
}

// Exactly at the threshold counts as low: 85% used with a warn of 85 is the
// case an operator set the number for.
func TestTheThresholdIsInclusive(t *testing.T) {
	fs := &fakeStore{}
	sink := &recordingSink{}
	r := NewRecorder(fs, quietLog())
	r.WatchDisk(85, sink)

	r.Record(context.Background(), heartbeatWithDisk(t, 100, 15))
	if len(sink.kinds) != 1 {
		t.Fatalf("announced %v at exactly the threshold, want one", sink.kinds)
	}
}

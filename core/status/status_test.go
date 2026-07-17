package status

import (
	"context"
	"io"
	"log/slog"
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
}

type fakeStore struct {
	records []recordCall
	staleIn []string
}

func (f *fakeStore) RecordHeartbeat(_ context.Context, id string, st domain.ServerStatus, _, driver string) (domain.Server, error) {
	f.records = append(f.records, recordCall{id: id, status: st, driver: driver})
	return domain.Server{ID: id}, nil
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

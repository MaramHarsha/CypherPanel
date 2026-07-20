package worker

// Managed-database work routing (managed-databases.md §6). The nastiest bug
// this guards: the work subject ".db.remove" also ends with ".remove" (the
// app-remove suffix), so a naive switch routes database removes into the app
// reconciler and the database container is never touched. These tests pin the
// routing and the reconciler dispatch.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

type fakeDbReconciler struct {
	mu        sync.Mutex
	provision [][]*agentv1.DbSpec
	removes   []string
}

func (f *fakeDbReconciler) ReconcileDatabases(_ context.Context, desired []*agentv1.DbSpec) ([]*agentv1.DbStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provision = append(f.provision, desired)
	out := make([]*agentv1.DbStatus, 0, len(desired))
	for _, s := range desired {
		out = append(out, &agentv1.DbStatus{DbId: s.DbId, RevisionId: s.RevisionId, State: "running"})
	}
	return out, nil
}

func (f *fakeDbReconciler) RemoveDatabase(_ context.Context, dbID string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, dbID)
	return nil
}

func (f *fakeDbReconciler) Exec(context.Context, string, []string) (io.Reader, error) {
	return nil, nil
}
func (f *fakeDbReconciler) StartContainer(context.Context, string) error { return nil }
func (f *fakeDbReconciler) StopContainer(context.Context, string, time.Duration) error {
	return nil
}
func (f *fakeDbReconciler) WaitHealthy(context.Context, string, time.Duration) error { return nil }

func dbRemoveMsg(t *testing.T, serverID, dbID string, deleteVolume bool) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.DbRemoveWork{DbId: dbID, DeleteVolume: deleteVolume})
	if err != nil {
		t.Fatalf("marshal db remove: %v", err)
	}
	return &fakeMessage{subject: subjects.DbRemove(serverID), data: data}
}

// A ".db.remove" work item must dispatch to the database reconciler's
// RemoveDatabase — never be swallowed by the app-remove case whose ".remove"
// suffix it shares.
func TestDbRemoveRoutesToDatabaseReconciler(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{}
	dbRec := &fakeDbReconciler{}
	w := New(bus, "srv1", drv, dbRec, nil, nil, nil, quietLog())

	msg := dbRemoveMsg(t, "srv1", "db_1", true)
	w.handleMsg(context.Background(), msg)

	if len(dbRec.removes) != 1 || dbRec.removes[0] != "db_1" {
		t.Fatalf("RemoveDatabase calls = %v, want [db_1]", dbRec.removes)
	}
	// The app reconciler must not have been driven by a database remove.
	if drv.calls != 0 {
		t.Fatalf("app reconciler ran %d times on a db.remove; routing collision", drv.calls)
	}
	if !msg.acked {
		t.Fatal("db.remove not acked")
	}
}

func dbProvisionMsg(t *testing.T, serverID string, spec *agentv1.DbSpec) *fakeMessage {
	t.Helper()
	data, err := proto.Marshal(&agentv1.DbProvisionWork{IdempotencyKey: spec.DbId, Spec: spec})
	if err != nil {
		t.Fatalf("marshal db provision: %v", err)
	}
	return &fakeMessage{subject: subjects.DbProvision(serverID), data: data}
}

// Database provisioning must work on a node with no app driver (a
// database-only role): the driver-nil guard must not swallow db.provision.
func TestDbProvisionWorksWithoutAppDriver(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	dbRec := &fakeDbReconciler{}
	w := New(bus, "srv1", nil, dbRec, nil, nil, nil, quietLog()) // drv == nil

	spec := &agentv1.DbSpec{DbId: "db_1", RevisionId: "dbr_1", Engine: "postgresql", Image: "postgres:16"}
	msg := dbProvisionMsg(t, "srv1", spec)
	w.handleMsg(context.Background(), msg)

	if len(dbRec.provision) != 1 {
		t.Fatalf("ReconcileDatabases called %d times, want 1", len(dbRec.provision))
	}
	got := dbRec.provision[0]
	if len(got) != 1 || got[0].DbId != "db_1" || got[0].Image != "postgres:16" {
		t.Fatalf("ReconcileDatabases desired = %+v, want the db_1/postgres:16 spec", got)
	}
	if !msg.acked || msg.termed {
		t.Fatalf("db.provision on a driverless node: acked=%v termed=%v, want clean ack", msg.acked, msg.termed)
	}
}

// An app ".remove" must still reach the app reconciler (the exclusion guard
// must not over-match).
func TestAppRemoveStillRoutesToAppReconciler(t *testing.T) {
	bus := newFakeBus(desiredStateBytes(t))
	drv := &recordingDriver{}
	dbRec := &fakeDbReconciler{}
	w := New(bus, "srv1", drv, dbRec, nil, nil, nil, quietLog())

	data, _ := proto.Marshal(&agentv1.RemoveWork{DeploymentId: "dep1", AppId: "app_1"})
	w.handleMsg(context.Background(), &fakeMessage{subject: subjects.Remove("srv1"), data: data})

	if len(dbRec.removes) != 0 {
		t.Fatalf("db reconciler saw an app remove: %v", dbRec.removes)
	}
	if drv.calls == 0 {
		t.Fatal("app reconciler never ran for an app .remove")
	}
}

package worker_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/agent/worker"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

type fakeDriver struct {
	mu   sync.Mutex
	apps []*agentv1.AppSpec
}

func (f *fakeDriver) Name() string { return "fake" }

func (f *fakeDriver) Reconcile(ctx context.Context, desired []*agentv1.AppSpec) ([]*agentv1.AppStatus, error) {
	f.mu.Lock()
	f.apps = desired
	f.mu.Unlock()
	var statuses []*agentv1.AppStatus
	for _, app := range desired {
		statuses = append(statuses, &agentv1.AppStatus{
			AppId:      app.AppId,
			RevisionId: app.RevisionId,
			State:      "running",
		})
	}
	return statuses, nil
}

func (f *fakeDriver) getApps() []*agentv1.AppSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps
}

func startServer(t *testing.T) *server.Server {
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func TestWorkerSync(t *testing.T) {
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	// Mock the control plane sync response
	syncSub, _ := nc.Subscribe(subjects.Sync("srv1"), func(msg *nats.Msg) {
		ds := &agentv1.DesiredState{
			Specs: []*agentv1.AppSpec{
				{AppId: "app1", RevisionId: "rev1"},
			},
		}
		data, _ := proto.Marshal(ds)
		msg.Respond(data)
	})
	defer syncSub.Unsubscribe()

	// Create Work consumer on the server
	js, _ := nc.JetStream()
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "WORK",
		Subjects: []string{"work.*.>"},
	})
	if err != nil {
		t.Fatalf("add stream: %v", err)
	}
	_, err = js.AddConsumer("WORK", &nats.ConsumerConfig{
		Durable:       subjects.WorkConsumer("srv1"),
		FilterSubject: subjects.WorkForServer("srv1"),
	})
	if err != nil {
		t.Fatalf("add consumer: %v", err)
	}

	drv := &fakeDriver{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := worker.New(nc, "srv1", drv, nil, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker
	go func() {
		_ = w.Run(ctx)
	}()

	// Wait for sync to complete and first reconcile to be called
	time.Sleep(1 * time.Second)

	apps := drv.getApps()
	if len(apps) != 1 || apps[0].AppId != "app1" {
		t.Fatalf("expected 1 app, got %v", apps)
	}
}

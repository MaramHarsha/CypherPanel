package docker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

type fakeDbClient struct {
	networks   map[string]map[string]string
	volumes    map[string]map[string]string
	containers map[string]*DbContainer
	specs      map[string]DbContainerSpec
	pulled     map[string]bool
	healthy    map[string]bool
	execCalls  []struct {
		containerID string
		cmd         []string
	}
}

func newFakeDbClient() *fakeDbClient {
	return &fakeDbClient{
		networks:   make(map[string]map[string]string),
		volumes:    make(map[string]map[string]string),
		containers: make(map[string]*DbContainer),
		specs:      make(map[string]DbContainerSpec),
		pulled:     make(map[string]bool),
		healthy:    make(map[string]bool),
	}
}

func (f *fakeDbClient) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	f.networks[name] = labels
	return nil
}

func (f *fakeDbClient) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	f.volumes[name] = labels
	return nil
}

func (f *fakeDbClient) RemoveVolume(ctx context.Context, name string) error {
	delete(f.volumes, name)
	return nil
}

func (f *fakeDbClient) ListManagedDb(ctx context.Context) ([]DbContainer, error) {
	var list []DbContainer
	for _, c := range f.containers {
		list = append(list, *c)
	}
	return list, nil
}

func (f *fakeDbClient) CreateDbContainer(ctx context.Context, spec DbContainerSpec) (string, error) {
	id := "c_" + spec.Name
	f.specs[id] = spec
	f.containers[id] = &DbContainer{
		ID:         id,
		Name:       spec.Name,
		DbID:       spec.Labels[LabelDbID],
		RevisionID: spec.Labels[LabelDbRevisionID],
		Running:    false,
		Healthy:    false,
	}
	return id, nil
}

func (f *fakeDbClient) StartContainer(ctx context.Context, id string) error {
	if c, ok := f.containers[id]; ok {
		c.Running = true
		c.Healthy = f.healthy[id]
	}
	return nil
}

func (f *fakeDbClient) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	if c, ok := f.containers[id]; ok {
		c.Running = false
		c.Healthy = false
	}
	return nil
}

func (f *fakeDbClient) RemoveContainer(ctx context.Context, id string) error {
	delete(f.containers, id)
	delete(f.specs, id)
	return nil
}

func (f *fakeDbClient) PullImage(ctx context.Context, image string) error {
	f.pulled[image] = true
	return nil
}

func (f *fakeDbClient) WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	if c, ok := f.containers[containerID]; ok {
		c.Healthy = true
	}
	return nil
}

func (f *fakeDbClient) Exec(ctx context.Context, containerID string, cmd []string) (io.Reader, error) {
	f.execCalls = append(f.execCalls, struct {
		containerID string
		cmd         []string
	}{containerID, cmd})
	return strings.NewReader("fake exec output"), nil
}

func TestDatabaseReconcilerLifecycle(t *testing.T) {
	client := newFakeDbClient()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewDatabaseReconciler(client, log)

	spec := &agentv1.DbSpec{
		DbId:          "db1",
		RevisionId:    "rev1",
		EnvironmentId: "env1",
		Engine:        "postgresql",
		Image:         "postgres:16",
		VolumeName:    "cypher-db-db1",
		DataPath:      "/var/lib/postgresql/data",
		Network:       "cypher-env1",
		Env:           map[string]string{"POSTGRES_PASSWORD": "fake-password"},
		HealthCmd:     "pg_isready",
		CpuLimit:      1.5,
		MemoryLimitMb: 1024,
		ExposePort:    5432,
	}

	ctx := context.Background()

	// 1. Reconcile Database (Create)
	statuses, err := r.ReconcileDatabases(ctx, []*agentv1.DbSpec{spec})
	if err != nil {
		t.Fatalf("ReconcileDatabases failed: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	status := statuses[0]
	if status.DbId != "db1" || status.State != "running" {
		t.Errorf("unexpected status: %+v", status)
	}

	// Verify volume creation
	volName := "cypher-db-db1"
	if _, ok := client.volumes[volName]; !ok {
		t.Errorf("expected volume %s to be created", volName)
	}

	// Verify container spec details
	containerID := "c_cypher-db-db1"
	cSpec, ok := client.specs[containerID]
	if !ok {
		t.Fatalf("expected container %s to be created", containerID)
	}
	if cSpec.Image != "postgres:16" {
		t.Errorf("image = %q, want postgres:16", cSpec.Image)
	}
	if cSpec.ExposePort != 5432 {
		t.Errorf("ExposePort = %d, want 5432", cSpec.ExposePort)
	}
	if cSpec.CPULimit != 1.5 || cSpec.MemoryLimitMB != 1024 {
		t.Errorf("limits = cpu:%v, mem:%v, want 1.5, 1024", cSpec.CPULimit, cSpec.MemoryLimitMB)
	}

	// 2. Remove Database
	err = r.RemoveDatabase(ctx, "db1", true)
	if err != nil {
		t.Fatalf("RemoveDatabase failed: %v", err)
	}

	if _, ok := client.containers[containerID]; ok {
		t.Errorf("expected container to be removed")
	}
	if _, ok := client.volumes[volName]; ok {
		t.Errorf("expected volume to be removed when deleteVolume=true")
	}
}

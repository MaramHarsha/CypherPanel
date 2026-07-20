// Package docker — database reconciler (managed-databases.md §6).
//
// The database reconciler lives alongside the Application reconciler and
// reuses the same Docker client, label conventions, and network-creation
// logic. Key differences from Application reconciliation:
//
//   - No build stage — images are public registry tags pulled by the daemon.
//   - Named volumes — created if absent, never removed unless explicitly
//     instructed by a DbRemoveWork with delete_volume=true.
//   - Health check — engine-specific shell command via Docker HEALTHCHECK.
//   - Resource limits — NanoCPUs + Memory in HostConfig.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Database status vocabulary (same values as the plane's domain constants;
// the agent must not import core, so we duplicate the strings here).
const (
	dbStateRunning      = "running"
	dbStateProvisioning = "provisioning"
	dbStateStopped      = "stopped"
	dbStateError        = "error"
)

// Database management label — distinct from Application labels so the two
// resource types never collide in ListManaged queries.
const (
	LabelDbManaged    = "cypherpanel.db.managed"
	LabelDbID         = "cypherpanel.db-id"
	LabelDbRevisionID = "cypherpanel.db.revision-id"
)

// DbContainer is a managed database container as the driver observes it.
type DbContainer struct {
	ID         string
	Name       string
	DbID       string
	RevisionID string
	Running    bool
	Healthy    bool // Docker HEALTHCHECK status
}

// DbContainerSpec is the create request the driver builds from a DbSpec.
type DbContainerSpec struct {
	Name          string
	Image         string
	Env           map[string]string
	Network       string
	VolumeName    string
	DataPath      string // mount target inside the container
	ExposePort    uint32 // 0 = no host publishing
	Labels        map[string]string
	HealthCmd     string  // shell command for HEALTHCHECK
	CPULimit      float64 // fractional cores; 0 = no limit
	MemoryLimitMB uint32  // 0 = no limit
}

// DbClient extends the base Client with database-specific operations.
type DbClient interface {
	// EnsureNetwork creates the named network if absent (idempotent).
	EnsureNetwork(ctx context.Context, name string, labels map[string]string) error
	// EnsureVolume creates a named volume if absent (idempotent).
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	// RemoveVolume removes a named volume.
	RemoveVolume(ctx context.Context, name string) error
	// ListManagedDb returns every container carrying the database managed label.
	ListManagedDb(ctx context.Context) ([]DbContainer, error)
	// CreateDbContainer creates a database container with volumes, health
	// checks, and resource limits.
	CreateDbContainer(ctx context.Context, spec DbContainerSpec) (id string, err error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	// PullImage pulls an image from a registry.
	PullImage(ctx context.Context, image string) error
	// WaitHealthy blocks until the container's HEALTHCHECK reports healthy,
	// or the context expires.
	WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error
}

// DatabaseReconciler converges database containers toward desired state.
type DatabaseReconciler struct {
	client DbClient
	log    *slog.Logger
}

// NewDatabaseReconciler wires the database reconciler.
func NewDatabaseReconciler(client DbClient, log *slog.Logger) *DatabaseReconciler {
	return &DatabaseReconciler{client: client, log: log}
}

// Exec executes a command inside a running container.
func (r *DatabaseReconciler) Exec(ctx context.Context, containerID string, cmd []string) (io.Reader, error) {
	if execer, ok := r.client.(DockerExecer); ok {
		return execer.Exec(ctx, containerID, cmd)
	}
	return nil, fmt.Errorf("docker engine client does not support Exec")
}

// StartContainer starts a created container.
func (r *DatabaseReconciler) StartContainer(ctx context.Context, id string) error {
	return r.client.StartContainer(ctx, id)
}

// StopContainer stops a container.
func (r *DatabaseReconciler) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	return r.client.StopContainer(ctx, id, timeout)
}

// WaitHealthy blocks until container health check passes.
func (r *DatabaseReconciler) WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	return r.client.WaitHealthy(ctx, containerID, timeout)
}

// ReconcileDatabases converges the local Docker daemon toward the desired set
// of database containers and reports the observed status of each.
//
// Convergence contract (rule 13): reconciling twice equals reconciling once.
// A DbProvisionWork for a container that already matches the desired spec
// reports success without recreation. A container whose image or config
// differs is stopped, removed, and recreated with the same named volume.
func (r *DatabaseReconciler) ReconcileDatabases(ctx context.Context, desired []*agentv1.DbSpec) ([]*agentv1.DbStatus, error) {
	managed, err := r.client.ListManagedDb(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker: listing managed db containers: %w", err)
	}

	byDb := make(map[string]DbContainer)
	for _, c := range managed {
		byDb[c.DbID] = c
	}

	desiredSet := make(map[string]bool)
	var statuses []*agentv1.DbStatus

	// Converge desired databases.
	for _, spec := range desired {
		desiredSet[spec.DbId] = true
		status := r.convergeDatabase(ctx, spec, byDb[spec.DbId])
		statuses = append(statuses, status)
	}

	// Remove databases absent from desired (absence-means-remove contract).
	for dbID, existing := range byDb {
		if desiredSet[dbID] {
			continue
		}
		r.log.Info("removing absent database", "db_id", dbID, "container", existing.Name)
		if err := r.removeDbContainer(ctx, existing, false); err != nil {
			r.log.Error("removing absent database container", "db_id", dbID, "error", err)
			statuses = append(statuses, &agentv1.DbStatus{
				DbId:       dbID,
				RevisionId: existing.RevisionID,
				State:      dbStateError,
				Detail:     fmt.Sprintf("failed to remove: %v", err),
				ObservedAt: timestamppb.Now(),
			})
		}
	}

	return statuses, nil
}

// convergeDatabase brings one database container to its desired state.
func (r *DatabaseReconciler) convergeDatabase(ctx context.Context, spec *agentv1.DbSpec, existing DbContainer) *agentv1.DbStatus {
	containerName := "cypher-db-" + spec.DbId
	now := timestamppb.Now()

	// If the container exists and matches the desired revision, check health.
	if existing.DbID == spec.DbId && existing.RevisionID == spec.RevisionId {
		if existing.Running && existing.Healthy {
			return &agentv1.DbStatus{
				DbId: spec.DbId, RevisionId: spec.RevisionId,
				State: dbStateRunning, ObservedAt: now,
			}
		}
		if existing.Running {
			// Running but not healthy yet — provisioning.
			return &agentv1.DbStatus{
				DbId: spec.DbId, RevisionId: spec.RevisionId,
				State: dbStateProvisioning, Detail: "waiting for health check",
				ObservedAt: now,
			}
		}
		// Not running — try to start it.
		if err := r.client.StartContainer(ctx, existing.ID); err != nil {
			return &agentv1.DbStatus{
				DbId: spec.DbId, RevisionId: spec.RevisionId,
				State: dbStateError, Detail: fmt.Sprintf("failed to start: %v", err),
				ObservedAt: now,
			}
		}
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateProvisioning, Detail: "started, waiting for health check",
			ObservedAt: now,
		}
	}

	// Revision differs or container doesn't exist — (re)provision.
	r.log.Info("provisioning database", "db_id", spec.DbId, "revision", spec.RevisionId,
		"engine", spec.Engine, "image", spec.Image)

	// Remove existing container if it's a revision change (volume preserved).
	if existing.DbID != "" {
		r.log.Info("replacing database container", "db_id", spec.DbId,
			"old_revision", existing.RevisionID, "new_revision", spec.RevisionId)
		if err := r.removeDbContainer(ctx, existing, false); err != nil {
			return &agentv1.DbStatus{
				DbId: spec.DbId, RevisionId: spec.RevisionId,
				State: dbStateError, Detail: fmt.Sprintf("failed to remove old container: %v", err),
				ObservedAt: now,
			}
		}
	}

	// Ensure network.
	netLabels := map[string]string{driver.LabelManaged: driverName}
	if err := r.client.EnsureNetwork(ctx, spec.Network, netLabels); err != nil {
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateError, Detail: fmt.Sprintf("network: %v", err),
			ObservedAt: now,
		}
	}

	// Ensure volume.
	volLabels := map[string]string{
		LabelDbManaged: driverName,
		LabelDbID:      spec.DbId,
	}
	if err := r.client.EnsureVolume(ctx, spec.VolumeName, volLabels); err != nil {
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateError, Detail: fmt.Sprintf("volume: %v", err),
			ObservedAt: now,
		}
	}

	// Pull image.
	if err := r.client.PullImage(ctx, spec.Image); err != nil {
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateError, Detail: fmt.Sprintf("pull image: %v", err),
			ObservedAt: now,
		}
	}

	// Create container.
	labels := map[string]string{
		LabelDbManaged:    driverName,
		LabelDbID:         spec.DbId,
		LabelDbRevisionID: spec.RevisionId,
	}
	cSpec := DbContainerSpec{
		Name:          containerName,
		Image:         spec.Image,
		Env:           spec.Env,
		Network:       spec.Network,
		VolumeName:    spec.VolumeName,
		DataPath:      spec.DataPath,
		ExposePort:    spec.ExposePort,
		Labels:        labels,
		HealthCmd:     spec.HealthCmd,
		CPULimit:      spec.CpuLimit,
		MemoryLimitMB: spec.MemoryLimitMb,
	}
	cID, err := r.client.CreateDbContainer(ctx, cSpec)
	if err != nil {
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateError, Detail: fmt.Sprintf("create container: %v", err),
			ObservedAt: now,
		}
	}

	// Start container.
	if err := r.client.StartContainer(ctx, cID); err != nil {
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateError, Detail: fmt.Sprintf("start container: %v", err),
			ObservedAt: now,
		}
	}

	// Wait for health check.
	healthTimeout := 60 * time.Second
	if err := r.client.WaitHealthy(ctx, cID, healthTimeout); err != nil {
		r.log.Warn("database health check not yet passing", "db_id", spec.DbId, "error", err)
		return &agentv1.DbStatus{
			DbId: spec.DbId, RevisionId: spec.RevisionId,
			State: dbStateProvisioning, Detail: "health check pending",
			ObservedAt: now,
		}
	}

	r.log.Info("database provisioned", "db_id", spec.DbId, "revision", spec.RevisionId)
	return &agentv1.DbStatus{
		DbId: spec.DbId, RevisionId: spec.RevisionId,
		State: dbStateRunning, ObservedAt: now,
	}
}

// removeDbContainer stops and removes a database container. The volume is only
// removed when deleteVolume is true (explicit operator opt-in).
func (r *DatabaseReconciler) removeDbContainer(ctx context.Context, c DbContainer, deleteVolume bool) error {
	if c.Running {
		if err := r.client.StopContainer(ctx, c.ID, defaultDrainTimeout); err != nil {
			r.log.Warn("stopping db container", "db_id", c.DbID, "error", err)
		}
	}
	if err := r.client.RemoveContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("removing db container: %w", err)
	}
	return nil
}

// RemoveDatabase handles a DbRemoveWork: stops the container, removes it, and
// optionally removes the volume.
func (r *DatabaseReconciler) RemoveDatabase(ctx context.Context, dbID string, deleteVolume bool) error {
	managed, err := r.client.ListManagedDb(ctx)
	if err != nil {
		return fmt.Errorf("docker: listing managed db containers: %w", err)
	}

	for _, c := range managed {
		if c.DbID == dbID {
			if err := r.removeDbContainer(ctx, c, false); err != nil {
				return err
			}
			break
		}
	}

	if deleteVolume {
		volName := "cypher-db-" + dbID
		r.log.Info("deleting database volume", "db_id", dbID, "volume", volName)
		if err := r.client.RemoveVolume(ctx, volName); err != nil {
			return fmt.Errorf("removing volume %s: %w", volName, err)
		}
	}
	return nil
}

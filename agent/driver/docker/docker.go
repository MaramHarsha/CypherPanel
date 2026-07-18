// Package docker is the standalone-Docker reconciler — the only orchestrator
// driver at v1 launch (ADR-006). It converges a server's containers toward the
// desired set of Applications with the zero-downtime sequence, and reports what
// is actually running (ADR-005; docs/features/application-deploy.md).
//
// Everything Docker-specific is behind the Client interface (consumer-defined,
// ENGINEERING rule 6): the real implementation wraps the Docker Engine API; the
// tests use a recording fake, so the convergence logic — the part that must be
// correct — is verified without a daemon. The route flip and health probe are
// likewise injected (Router, HealthProber), keeping the reconciler pure logic.
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// driverName identifies this driver in labels, heartbeats, and work routing.
const driverName = "docker"

// Status vocabulary (ui-principles §5) as reported in AppStatus.State. Defined
// locally because the agent must not import core; the plane maps these onto the
// Application unchanged.
const (
	stateRunning = "running"
	stateError   = "error"
	stateStopped = "stopped"
)

const defaultDrainTimeout = 10 * time.Second

// Container is a managed container as the driver observes it. Identity comes
// from labels the driver itself stamped, never from in-memory bookkeeping — so
// a freshly-constructed driver can converge a host it has never seen (the
// crash-recovery path is the same as a normal deploy).
type Container struct {
	ID         string
	Name       string
	AppID      string
	RevisionID string
	Running    bool
}

// ContainerSpec is the create request the driver builds from an AppSpec.
type ContainerSpec struct {
	Name    string
	Image   string
	Env     map[string]string
	Network string
	Port    uint32
	Labels  map[string]string
}

// Image is a managed image, identified by the labels the build stamped.
type Image struct {
	ID         string
	AppID      string
	RevisionID string
}

// Client is the subset of the Docker Engine API the reconciler needs.
type Client interface {
	// EnsureNetwork creates the named network if absent (idempotent).
	EnsureNetwork(ctx context.Context, name string, labels map[string]string) error
	// ListManaged returns every container carrying this driver's managed label.
	ListManaged(ctx context.Context) ([]Container, error)
	CreateContainer(ctx context.Context, spec ContainerSpec) (id string, err error)
	StartContainer(ctx context.Context, id string) error
	// StopContainer stops with a drain timeout, then RemoveContainer deletes it.
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	// ContainerIP returns the container's address on the given network.
	ContainerIP(ctx context.Context, id, network string) (string, error)
	// ListManagedImages returns every image carrying this driver's managed label.
	ListManagedImages(ctx context.Context) ([]Image, error)
	RemoveImage(ctx context.Context, id string) error
}

// Router applies (or removes) an Application's route on the local proxy. The
// agent/proxy Traefik writer satisfies it structurally (ADR-004).
type Router interface {
	SetRoute(ctx context.Context, appID string, route *agentv1.RouteSpec, upstream string) error
	RemoveRoute(ctx context.Context, appID string) error
}

// HealthProber checks that an upstream is serving before the route flips. The
// real prober performs an HTTP GET; the fake returns a configured result.
type HealthProber interface {
	Probe(ctx context.Context, upstream string, hc *agentv1.HealthCheck) error
}

// Driver reconciles standalone-Docker containers. Construct with New.
type Driver struct {
	client       Client
	router       Router
	prober       HealthProber
	drainTimeout time.Duration
	log          *slog.Logger
}

// New wires the driver with its collaborators.
func New(client Client, router Router, prober HealthProber, log *slog.Logger) *Driver {
	return &Driver{
		client:       client,
		router:       router,
		prober:       prober,
		drainTimeout: defaultDrainTimeout,
		log:          log,
	}
}

// Name reports the driver identity.
func (d *Driver) Name() string { return driverName }

// Reconcile converges local containers toward desired and reports observed
// status. A total inability to reconcile (daemon unreachable) is returned as an
// error; a single app's failure is captured in its AppStatus and does not stop
// the others (reconciler-development skill).
func (d *Driver) Reconcile(ctx context.Context, desired []*agentv1.AppSpec) ([]*agentv1.AppStatus, error) {
	managed, err := d.client.ListManaged(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker: listing managed containers: %w", err)
	}

	byApp := make(map[string][]Container)
	for _, c := range managed {
		byApp[c.AppID] = append(byApp[c.AppID], c)
	}
	desiredApps := make(map[string]struct{}, len(desired))

	statuses := make([]*agentv1.AppStatus, 0, len(desired))
	for _, spec := range desired {
		desiredApps[spec.GetAppId()] = struct{}{}
		statuses = append(statuses, d.convergeApp(ctx, spec, byApp[spec.GetAppId()]))
	}

	// Absence means removal: any managed app not in desired is torn down.
	for appID, containers := range byApp {
		if _, wanted := desiredApps[appID]; !wanted {
			d.removeApp(ctx, appID, containers)
		}
	}

	// Desired-state image GC: images of fully-removed apps are prunable
	// (threat-model §5.9). Revision-window GC within a still-desired app needs
	// the plane's retain-set and lands with the deployment store.
	d.gcRemovedAppImages(ctx, desiredApps)

	return statuses, nil
}

// convergeApp rolls one Application onto its desired revision with the
// zero-downtime sequence, or reports why it could not.
func (d *Driver) convergeApp(ctx context.Context, spec *agentv1.AppSpec, existing []Container) *agentv1.AppStatus {
	// Already converged? The desired revision is running → no mutation at all
	// (this is what makes converging twice equal converging once, rule 13).
	for _, c := range existing {
		if c.RevisionID == spec.GetRevisionId() && c.Running {
			return status(spec.GetAppId(), spec.GetRevisionId(), stateRunning, "")
		}
	}

	if err := d.client.EnsureNetwork(ctx, spec.GetNetwork(), managedLabels(spec)); err != nil {
		return status(spec.GetAppId(), "", stateError, "network: "+err.Error())
	}

	// Start the new revision alongside the old one.
	newID, err := d.client.CreateContainer(ctx, ContainerSpec{
		Name:    containerName(spec.GetAppId(), spec.GetRevisionId()),
		Image:   spec.GetImage(),
		Env:     spec.GetEnv(),
		Network: spec.GetNetwork(),
		Port:    spec.GetPort(),
		Labels:  managedLabels(spec),
	})
	if err != nil {
		return status(spec.GetAppId(), "", stateError, "create: "+err.Error())
	}
	if err := d.client.StartContainer(ctx, newID); err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), "", stateError, "start: "+err.Error())
	}

	// Health-gate before the old revision stops serving.
	ip, err := d.client.ContainerIP(ctx, newID, spec.GetNetwork())
	if err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), "", stateError, "address: "+err.Error())
	}
	upstream := net.JoinHostPort(ip, strconv.Itoa(int(spec.GetPort())))
	if err := d.prober.Probe(ctx, upstream, spec.GetHealth()); err != nil {
		// New revision never became healthy: discard it; the old container is
		// untouched and still serving. This is the anti-stale-container property.
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "health check failed: "+err.Error())
	}

	// New revision healthy: flip the route to it, then drain the old.
	if err := d.router.SetRoute(ctx, spec.GetAppId(), spec.GetRoute(), upstream); err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "route: "+err.Error())
	}
	for _, c := range existing {
		d.drain(ctx, c.ID)
	}
	return status(spec.GetAppId(), spec.GetRevisionId(), stateRunning, "")
}

// removeApp tears down every container for an app that is no longer desired and
// removes its route.
func (d *Driver) removeApp(ctx context.Context, appID string, containers []Container) {
	if err := d.router.RemoveRoute(ctx, appID); err != nil {
		d.log.Warn("removing route for absent app", "app_id", appID, "error", err)
	}
	for _, c := range containers {
		d.drain(ctx, c.ID)
	}
}

// gcRemovedAppImages removes images belonging to apps absent from desired.
func (d *Driver) gcRemovedAppImages(ctx context.Context, desiredApps map[string]struct{}) {
	images, err := d.client.ListManagedImages(ctx)
	if err != nil {
		d.log.Warn("listing managed images for GC", "error", err)
		return
	}
	for _, img := range images {
		if _, wanted := desiredApps[img.AppID]; wanted {
			continue
		}
		if err := d.client.RemoveImage(ctx, img.ID); err != nil {
			d.log.Warn("removing image", "image", img.ID, "app_id", img.AppID, "error", err)
		}
	}
}

// drain stops (with the drain timeout) and removes a container.
func (d *Driver) drain(ctx context.Context, id string) {
	if err := d.client.StopContainer(ctx, id, d.drainTimeout); err != nil {
		d.log.Warn("stopping container", "container", id, "error", err)
	}
	if err := d.client.RemoveContainer(ctx, id); err != nil {
		d.log.Warn("removing container", "container", id, "error", err)
	}
}

// discard removes a container that failed to come up (best-effort cleanup).
func (d *Driver) discard(ctx context.Context, id string) {
	_ = d.client.StopContainer(ctx, id, d.drainTimeout)
	if err := d.client.RemoveContainer(ctx, id); err != nil {
		d.log.Warn("discarding failed container", "container", id, "error", err)
	}
}

func managedLabels(spec *agentv1.AppSpec) map[string]string {
	return map[string]string{
		driver.LabelManaged:    driverName,
		driver.LabelAppID:      spec.GetAppId(),
		driver.LabelRevisionID: spec.GetRevisionId(),
	}
}

func containerName(appID, revisionID string) string {
	return "cypher-" + appID + "-" + revisionID
}

// currentRevision returns the revision of the first running container, or "".
// Used to report which revision is still serving after a failed rollout.
func currentRevision(existing []Container) string {
	for _, c := range existing {
		if c.Running {
			return c.RevisionID
		}
	}
	return ""
}

func status(appID, revisionID, state, detail string) *agentv1.AppStatus {
	return &agentv1.AppStatus{
		AppId:      appID,
		RevisionId: revisionID,
		State:      state,
		Detail:     detail,
	}
}

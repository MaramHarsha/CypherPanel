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
	"io"
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
// Application unchanged. ("stopped" — an app with no desired revision — never
// reaches the driver; the plane reports it directly.)
const (
	stateRunning  = "running"
	stateError    = "error"
	stateDegraded = "degraded"
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
	Name          string
	Image         string
	Env           map[string]string
	Network       string
	Port          uint32
	Labels        map[string]string
	CPULimit      float64       // fractional cores; 0 = no limit
	MemoryLimitMB uint32        // 0 = no limit
	Binds         []string      // "<volume>:<path>" mounts
	Ports         []PortBinding // raw host-port publishes (tcp/udp)
}

// PortBinding publishes a container port to a host port on one protocol. The
// engine maps it onto the container's ExposedPorts + HostConfig.PortBindings.
type PortBinding struct {
	HostPort      uint32
	ContainerPort uint32
	Protocol      string // "tcp" or "udp"
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
	// EnsureVolume creates a named volume if absent (idempotent). App volumes
	// persist across container recreation and are never touched by GC.
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	// ListManaged returns every container carrying this driver's managed label.
	ListManaged(ctx context.Context) ([]Container, error)
	CreateContainer(ctx context.Context, spec ContainerSpec) (id string, err error)
	StartContainer(ctx context.Context, id string) error
	// StopContainer stops with a drain timeout, then RemoveContainer deletes it.
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	// ContainerIP returns the container's address on the given network.
	ContainerIP(ctx context.Context, id, network string) (string, error)
	StreamLogs(ctx context.Context, id string, out io.Writer) error
	// ListManagedImages returns every image carrying this driver's managed label.
	ListManagedImages(ctx context.Context) ([]Image, error)
	RemoveImage(ctx context.Context, id string) error
	// ExecAndWait runs argv in a running container to completion, returning its
	// exit code and captured output (scheduled tasks, backups). A non-zero exit
	// is not an error — the caller interprets it.
	ExecAndWait(ctx context.Context, containerID string, cmd []string) (exitCode int, output []byte, err error)
}

// Router applies (or removes) an Application's route on the local proxy, and —
// because convergence must observe route state, not remember it — reports the
// route currently applied. The agent/proxy Traefik driver satisfies it
// structurally (ADR-004): the fragment file on disk is the observable truth.
type Router interface {
	// EnsureProxy makes the node's Proxy exist and run (routing-and-tls.md).
	// Idempotent; a converged Proxy is a no-op.
	EnsureProxy(ctx context.Context) error
	// AttachNetwork connects the Proxy to an environment network so it can
	// reach that environment's upstreams. Idempotent.
	AttachNetwork(ctx context.Context, network string) error
	SetRoute(ctx context.Context, appID string, route *agentv1.RouteSpec, upstream string) error
	RemoveRoute(ctx context.Context, appID string) error
	// Route returns the upstream the app's route currently points at, or
	// ok=false when no route is applied. Used by the converged fast path to
	// re-assert a route lost to a crash between start and flip.
	Route(ctx context.Context, appID string) (upstream string, ok bool, err error)
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
	// reportProxy, when set, receives the outcome of every EnsureProxy attempt
	// so the agent's heartbeat can report DEGRADED while routing is broken.
	// The driver stays ignorant of the heartbeat package — it just hands the
	// result to whoever asked (ENGINEERING: consumer-defined interfaces).
	reportProxy func(error)
	log         *slog.Logger
}

// OnProxyHealth registers a sink for Proxy reconciliation outcomes. Passing nil
// (or never calling it) leaves the driver silent, which is what the unit tests
// and builder-role agents want.
func (d *Driver) OnProxyHealth(fn func(error)) { d.reportProxy = fn }

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
// the others (reconciler-development skill). Apps absent from desired are torn
// down; a teardown that fails is itself reported as an observed error status —
// the plane must see that removal has not actually converged.
func (d *Driver) Reconcile(ctx context.Context, desired []*agentv1.AppSpec) ([]*agentv1.AppStatus, error) {
	managed, err := d.client.ListManaged(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker: listing managed containers: %w", err)
	}

	// Ensure the node's Proxy is up before converging apps. Routing
	// convergence is best-effort: a Proxy hiccup must not stop container
	// convergence (fragments persist and Traefik picks them up once it
	// recovers), so failures are logged, not returned.
	proxyErr := d.router.EnsureProxy(ctx)
	if proxyErr != nil {
		d.log.Warn("ensuring proxy", "error", proxyErr)
	}
	if d.reportProxy != nil {
		// Reported on success too: this is what clears a degraded server once
		// the operator frees the port.
		d.reportProxy(proxyErr)
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

	// Attach the Proxy to every desired environment network — after
	// convergeApp, which creates each network via EnsureNetwork, so a
	// brand-new app's network exists by now and the Proxy can reach its
	// upstream. Idempotent and best-effort.
	seenNet := make(map[string]struct{})
	for _, spec := range desired {
		net := spec.GetNetwork()
		if _, ok := seenNet[net]; ok || net == "" {
			continue
		}
		seenNet[net] = struct{}{}
		if err := d.router.AttachNetwork(ctx, net); err != nil {
			d.log.Warn("attaching proxy to network", "network", net, "error", err)
		}
	}

	// Absence means removal: any managed app not in desired is torn down. A
	// failed teardown is an observation the plane needs (the app still exists
	// here), so it joins the status report.
	for appID, containers := range byApp {
		if _, wanted := desiredApps[appID]; !wanted {
			if err := d.removeApp(ctx, appID, containers); err != nil {
				statuses = append(statuses, status(appID, currentRevision(containers), stateError, "teardown: "+err.Error()))
			}
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
//
// The converged check is an observation of *all* the app's state, not just
// "is the new revision running": a crash can leave the desired revision
// running with the route still on the old revision, or leave old containers
// undrained, or leave a created-but-never-started container squatting on the
// deterministic name. Each of those must converge with no manual step.
func (d *Driver) convergeApp(ctx context.Context, spec *agentv1.AppSpec, existing []Container) *agentv1.AppStatus {
	var current *Container    // desired revision, running
	var leftovers []Container // everything else: old revisions, dead duplicates
	for i := range existing {
		c := existing[i]
		if c.RevisionID == spec.GetRevisionId() && c.Running && current == nil {
			current = &existing[i]
			continue
		}
		leftovers = append(leftovers, c)
	}

	if current != nil {
		return d.convergedApp(ctx, spec, current, leftovers)
	}

	if err := d.client.EnsureNetwork(ctx, spec.GetNetwork(), networkLabels()); err != nil {
		return status(spec.GetAppId(), currentRevision(existing), stateError, "network: "+err.Error())
	}
	// Ensure the app's persistent volumes exist before binding them. Idempotent,
	// and only in the create path — the converged (no-change) path skips it, so
	// converge-twice stays zero-mutation.
	binds := make([]string, 0, len(spec.GetVolumes()))
	for _, v := range spec.GetVolumes() {
		if err := d.client.EnsureVolume(ctx, v.GetVolumeName(), volumeLabels(spec)); err != nil {
			return status(spec.GetAppId(), currentRevision(existing), stateError, "volume: "+err.Error())
		}
		binds = append(binds, v.GetVolumeName()+":"+v.GetPath())
	}

	// A dead container of the desired revision (crash between create and
	// start) holds the deterministic name; clear it so create cannot collide.
	remaining := leftovers[:0]
	for _, c := range leftovers {
		if c.RevisionID == spec.GetRevisionId() {
			d.discard(ctx, c.ID)
			continue
		}
		remaining = append(remaining, c)
	}
	leftovers = remaining

	// Start the new revision alongside the old one.
	newID, err := d.client.CreateContainer(ctx, ContainerSpec{
		Name:          containerName(spec.GetAppId(), spec.GetRevisionId()),
		Image:         spec.GetImage(),
		Env:           spec.GetEnv(),
		Network:       spec.GetNetwork(),
		Port:          spec.GetPort(),
		Labels:        managedLabels(spec),
		CPULimit:      spec.GetCpuLimit(),
		MemoryLimitMB: spec.GetMemoryLimitMb(),
		Binds:         binds,
		Ports:         portBindings(spec),
	})
	if err != nil {
		return status(spec.GetAppId(), currentRevision(existing), stateError, "create: "+err.Error())
	}
	if err := d.client.StartContainer(ctx, newID); err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "start: "+err.Error())
	}

	// Health-gate before the old revision stops serving.
	upstream, err := d.upstreamOf(ctx, newID, spec)
	if err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "address: "+err.Error())
	}
	if err := d.prober.Probe(ctx, upstream, spec.GetHealth()); err != nil {
		// New revision never became healthy: discard it; the old container is
		// untouched and still serving. This is the anti-stale-container property.
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "health check failed: "+err.Error())
	}

	// New revision healthy: point the route at it (or ensure no route for a raw
	// app), then drain the old.
	if err := d.applyDesiredRoute(ctx, spec, upstream); err != nil {
		d.discard(ctx, newID)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "route: "+err.Error())
	}
	return d.finishConverge(ctx, spec, leftovers)
}

// applyDesiredRoute reconciles the proxy fragment to the app's desired route:
// an HTTP app (non-empty domain) gets its fragment pointed at upstream; a raw
// app (no domain) gets any fragment removed. Both are idempotent.
func (d *Driver) applyDesiredRoute(ctx context.Context, spec *agentv1.AppSpec, upstream string) error {
	if spec.GetRoute().GetDomain() == "" {
		return d.router.RemoveRoute(ctx, spec.GetAppId())
	}
	return d.router.SetRoute(ctx, spec.GetAppId(), spec.GetRoute(), upstream)
}

// convergedApp handles the app whose desired revision is already running:
// verify the route actually points at it (a crash between start and flip
// leaves it on the old revision) and drain any leftover containers (a crash
// between flip and drain leaves the old revision running unrouted). When
// reality fully matches desired, this makes zero mutating calls.
func (d *Driver) convergedApp(ctx context.Context, spec *agentv1.AppSpec, current *Container, leftovers []Container) *agentv1.AppStatus {
	upstream, err := d.upstreamOf(ctx, current.ID, spec)
	if err != nil {
		return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "address: "+err.Error())
	}
	applied, ok, err := d.router.Route(ctx, spec.GetAppId())
	if err != nil {
		return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "route: "+err.Error())
	}
	if spec.GetRoute().GetDomain() == "" {
		// Raw (routeless) app: the desired state is no fragment. Remove a stale
		// one left by a prior HTTP config; if none exists this makes zero calls,
		// preserving the converge-twice invariant.
		if ok {
			if err := d.router.RemoveRoute(ctx, spec.GetAppId()); err != nil {
				return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "route: "+err.Error())
			}
		}
	} else {
		// Probing is what must stay gated — it costs a network round trip per
		// cycle. The route itself is written every time: the fragment on disk is
		// the observable truth (see the Router doc above), and comparing only
		// the upstream meant any change to the fragment's *shape* never reached
		// a stable app. A middleware added to the template stayed invisible
		// until something unrelated moved the container's IP. SetRoute skips the
		// write when the bytes already match, so this stays a no-op in the
		// common case and does not churn the proxy's file watcher.
		if !ok || applied != upstream {
			// The running revision passed its health gate when it was started; a
			// re-observed flip still gates on health so a container that has
			// since died can never capture the route.
			if err := d.prober.Probe(ctx, upstream, spec.GetHealth()); err != nil {
				return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "health check failed: "+err.Error())
			}
		}
		if err := d.router.SetRoute(ctx, spec.GetAppId(), spec.GetRoute(), upstream); err != nil {
			return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "route: "+err.Error())
		}
	}
	return d.finishConverge(ctx, spec, leftovers)
}

// finishConverge drains the leftover containers of an app whose desired
// revision is serving and routed. A drain failure is not a rollout failure —
// the desired revision holds the traffic — but it is not convergence either:
// the app is reported degraded and the next reconcile retries the drain.
func (d *Driver) finishConverge(ctx context.Context, spec *agentv1.AppSpec, leftovers []Container) *agentv1.AppStatus {
	for _, c := range leftovers {
		if err := d.drain(ctx, c.ID); err != nil {
			return status(spec.GetAppId(), spec.GetRevisionId(), stateDegraded, "draining old revision: "+err.Error())
		}
	}
	return status(spec.GetAppId(), spec.GetRevisionId(), stateRunning, "")
}

// removeApp tears down every container for an app that is no longer desired
// and removes its route. It returns an error when any part of the teardown
// failed, so the caller can report the app as still (erroneously) present.
func (d *Driver) removeApp(ctx context.Context, appID string, containers []Container) error {
	var failed error
	if err := d.router.RemoveRoute(ctx, appID); err != nil {
		d.log.Warn("removing route for absent app", "app_id", appID, "error", err)
		failed = fmt.Errorf("removing route: %w", err)
	}
	for _, c := range containers {
		if err := d.drain(ctx, c.ID); err != nil && failed == nil {
			failed = err
		}
	}
	return failed
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
func (d *Driver) drain(ctx context.Context, id string) error {
	if err := d.client.StopContainer(ctx, id, d.drainTimeout); err != nil {
		d.log.Warn("stopping container", "container", id, "error", err)
		return fmt.Errorf("stopping container %s: %w", id, err)
	}
	if err := d.client.RemoveContainer(ctx, id); err != nil {
		d.log.Warn("removing container", "container", id, "error", err)
		return fmt.Errorf("removing container %s: %w", id, err)
	}
	return nil
}

// discard removes a container that failed to come up (best-effort cleanup).
func (d *Driver) discard(ctx context.Context, id string) {
	_ = d.client.StopContainer(ctx, id, d.drainTimeout)
	if err := d.client.RemoveContainer(ctx, id); err != nil {
		d.log.Warn("discarding failed container", "container", id, "error", err)
	}
}

func (d *Driver) upstreamOf(ctx context.Context, containerID string, spec *agentv1.AppSpec) (string, error) {
	ip, err := d.client.ContainerIP(ctx, containerID, spec.GetNetwork())
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, strconv.Itoa(int(spec.GetPort()))), nil
}

func managedLabels(spec *agentv1.AppSpec) map[string]string {
	return map[string]string{
		driver.LabelManaged:    driverName,
		driver.LabelAppID:      spec.GetAppId(),
		driver.LabelRevisionID: spec.GetRevisionId(),
	}
}

// networkLabels marks the network as managed without app or revision labels:
// networks are environment-scoped (cypher-<environment_id>) and shared by
// every app in the environment, so per-app labels would be wrong on them.
func networkLabels() map[string]string {
	return map[string]string{driver.LabelManaged: driverName}
}

// volumeLabels marks a persistent app volume with the app id (no revision — the
// volume outlives revisions) so it is discoverable and, later, reclaimable.
func volumeLabels(spec *agentv1.AppSpec) map[string]string {
	return map[string]string{
		driver.LabelManaged: driverName,
		driver.LabelAppID:   spec.GetAppId(),
	}
}

func containerName(appID, revisionID string) string {
	return "cypher-" + appID + "-" + revisionID
}

// portBindings maps the app's raw host-port publishes onto the container spec.
func portBindings(spec *agentv1.AppSpec) []PortBinding {
	ports := spec.GetPorts()
	if len(ports) == 0 {
		return nil
	}
	out := make([]PortBinding, 0, len(ports))
	for _, p := range ports {
		out = append(out, PortBinding{
			HostPort:      p.GetHostPort(),
			ContainerPort: p.GetContainerPort(),
			Protocol:      p.GetProtocol(),
		})
	}
	return out
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

// RunningContainerForApp resolves the app's currently-running container by its
// app-id label — the exec target for a scheduled task (ADR-011: the app's own
// container, nothing else). ok is false when no container is running (the task
// run is skipped, scheduled-tasks.md §5).
func (d *Driver) RunningContainerForApp(ctx context.Context, appID string) (containerID string, ok bool, err error) {
	managed, err := d.client.ListManaged(ctx)
	if err != nil {
		return "", false, fmt.Errorf("docker: listing containers: %w", err)
	}
	for _, c := range managed {
		if c.AppID == appID && c.Running {
			return c.ID, true, nil
		}
	}
	return "", false, nil
}

// ExecAndWait runs argv in a container to completion (ADR-011: argv straight to
// exec, never a shell), returning its exit code and captured output.
func (d *Driver) ExecAndWait(ctx context.Context, containerID string, argv []string) (exitCode int, output []byte, err error) {
	return d.client.ExecAndWait(ctx, containerID, argv)
}

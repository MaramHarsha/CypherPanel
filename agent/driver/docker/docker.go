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
	"slices"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/registryauth"
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
	// RestartToken is the token this container was created under. A container
	// whose token differs from the spec's is not the desired container, even at
	// the desired revision (deployment-control.md §3).
	RestartToken string
	// ReplicaIndex is 1-based and stable; 1 for every container created before
	// replicas existed (app-scaling.md §2). uint32 rather than int on purpose:
	// it is read from a container label and used as a map key and a wire field,
	// so the narrowing has to happen at the bounded parse rather than at every
	// use.
	ReplicaIndex uint32
	Running      bool
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

// Image is a managed image as garbage collection sees it. Identity is the
// image itself, not one of its names, because reclaiming disk means dropping
// *every* reference the daemon holds — a pulled image keeps its layers for as
// long as the registry reference it arrived under still exists, however many
// managed aliases were removed.
type Image struct {
	ID string
	// AppIDs is every application with a managed association to this image:
	// the label a build stamped, and/or the cypher/<app>:<revision> aliases a
	// pull was tagged with. More than one when two apps run the same image.
	AppIDs []string
	// References is every managed name the daemon holds for it. Only ours: an
	// image can also carry tags an operator or another tool made, and deleting
	// an application must never untag those.
	References []string
	// Pending is the tidy-up an earlier rollout could not finish — a registry
	// reference our own pull created and failed to drop.
	Pending []PendingRef
	// Managed pairs each managed reference with the application and revision it
	// names, so garbage collection can reclaim ONE revision's reference without
	// touching another's on a shared image (disk-management.md §2).
	Managed []ManagedRef
}

// ManagedRef is one reference this driver created, with what it names.
type ManagedRef struct {
	Reference  string
	AppID      string
	RevisionID string
}

// PendingRef pairs a registry reference this driver's pull created with the
// marker reference recording it as ours (driver.PullMarkerRef).
//
// The pair is what makes the removal retryable. Drop the marker first and the
// reference becomes indistinguishable from one the operator made — which is to
// say permanently unreclaimable, since GC may never touch what is not ours.
// So the marker always outlives the reference it names.
type PendingRef struct {
	Source string
	Marker string
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
	// ListManagedImages returns every image this driver has an association with,
	// each carrying all of its references so GC can drop them together.
	ListManagedImages(ctx context.Context) ([]Image, error)
	RemoveImage(ctx context.Context, id string) error
	// EnsureImage makes the local daemon hold the bits this reference currently
	// designates. A digest is immutable, so a local copy satisfies it; a tag is
	// mutable and is re-fetched, because a redeploy of `acme/web:latest` must
	// pick up whatever that tag points at now rather than silently reusing the
	// cached image. Only pull-marked specs reach it (AppSpec.pull — deploy from
	// container image); built images keep the ADR-008 local/relay contract.
	// registryAuth is the encoded credential for a private registry
	// (pkg/registryauth); empty is the anonymous pull every public image does.
	EnsureImage(ctx context.Context, image, registryAuth string) error
	// ImageDigest returns the immutable digest reference (repo@sha256:…) of a
	// local image, or "" when it has none (a locally-built image never pushed
	// anywhere). This is what lets the plane pin a revision to the artifact it
	// actually ran instead of to a tag that can move underneath it.
	ImageDigest(ctx context.Context, image string) (string, error)
	// TagImage points a managed reference at an existing local image. Pulled
	// images cannot carry our labels, so this is how they become visible to
	// desired-state GC. Idempotent.
	TagImage(ctx context.Context, source, target string) error
	// HasImage reports whether a reference already exists locally. Used before
	// a pull to learn whether the reference is one we are about to create — the
	// only way to know later whether it is ours to remove.
	HasImage(ctx context.Context, image string) (bool, error)
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
	SetRoute(ctx context.Context, appID string, route *agentv1.RouteSpec, upstreams []string) error
	RemoveRoute(ctx context.Context, appID string) error
	// Route returns the upstream the app's route currently points at, or
	// ok=false when no route is applied. Used by the converged fast path to
	// re-assert a route lost to a crash between start and flip.
	Route(ctx context.Context, appID string) (upstreams []string, ok bool, err error)
	// EnsureMaintenance makes the node's maintenance responder serve and
	// reports the upstream a route in maintenance points at
	// (app-access-control.md §7). An error means it is NOT serving, and the
	// caller leaves the route alone.
	EnsureMaintenance(ctx context.Context) (upstream string, err error)
	// RemoveMaintenance takes the responder down once nothing on this node is
	// in maintenance. Idempotent.
	RemoveMaintenance(ctx context.Context) error
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
func (d *Driver) Reconcile(ctx context.Context, desired []*agentv1.AppSpec, retain []*agentv1.RetainSpec) ([]*agentv1.AppStatus, error) {
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
	inMaintenance := false
	for _, spec := range desired {
		desiredApps[spec.GetAppId()] = struct{}{}
		if underMaintenance(spec) {
			inMaintenance = true
		}
		statuses = append(statuses, d.convergeApp(ctx, spec, byApp[spec.GetAppId()]))
	}

	// The responder is shared, so it goes away only when the LAST resource on
	// this node leaves maintenance — a node that never uses the feature pays
	// nothing for it. Best-effort like the rest of proxy convergence: a
	// responder left running routes no traffic, and the next cycle retries.
	if !inMaintenance {
		if err := d.router.RemoveMaintenance(ctx); err != nil {
			d.log.Warn("removing the maintenance responder", "error", err)
		}
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
	d.gcImages(ctx, desiredApps, retainSet(retain))

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
	indexes := desiredIndexes(spec)

	// Partition what is here into "the desired container for index N" and
	// everything else. A restart changes only the token, which is exactly what
	// makes it a difference the ordinary rollout path closes.
	current := make(map[uint32]*Container, len(indexes))
	wanted := make(map[uint32]bool, len(indexes))
	for _, idx := range indexes {
		wanted[idx] = true
	}
	var leftovers []Container
	for i := range existing {
		c := existing[i]
		idx := c.ReplicaIndex
		if idx == 0 {
			idx = 1
		}
		if wanted[idx] && current[idx] == nil &&
			c.RevisionID == spec.GetRevisionId() && c.RestartToken == spec.GetRestartToken() && c.Running {
			current[idx] = &existing[i]
			continue
		}
		leftovers = append(leftovers, c)
	}

	missing := make([]uint32, 0, len(indexes))
	for _, idx := range indexes {
		if current[idx] == nil {
			missing = append(missing, idx)
		}
	}

	// Everything below this point is skipped entirely when nothing is missing,
	// which is what keeps converge-twice a zero-mutation operation.
	if len(missing) > 0 {
		st := d.startMissingReplicas(ctx, spec, existing, missing, current, &leftovers)
		if st != nil {
			return st
		}
	}

	// Health-gate every replica before the old revision stops serving, then
	// point the route at all of them at once. Per node a rollout is
	// surge-then-flip: every new replica starts alongside the old ones, all of
	// them pass their gate, the fragment is rewritten ONCE, and only then are
	// the old containers drained. Replacing replicas one at a time and letting
	// the fragment carry a mix would make two revisions serve one node's
	// traffic for the whole roll, so a client could see an old and a new API
	// response in the same session.
	newly := make(map[uint32]bool, len(missing))
	for _, idx := range missing {
		newly[idx] = true
	}
	// Under maintenance the APPLIED upstream is the responder's, so it can
	// never equal an app container's address. Without this the comparison below
	// reads as "the fragment disagrees" and the agent health-probes every
	// replica once per cycle for the whole outage window
	// (app-access-control.md §8).
	maintenance := underMaintenance(spec)
	applied, routed, err := d.router.Route(ctx, spec.GetAppId())
	if err != nil {
		return status(spec.GetAppId(), spec.GetRevisionId(), stateError, "route: "+err.Error())
	}

	upstreams := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		c := current[idx]
		up, err := d.upstreamOf(ctx, c.ID, spec)
		if err != nil {
			d.discardNew(ctx, current, newly)
			return status(spec.GetAppId(), currentRevision(existing), stateError, "address: "+err.Error())
		}
		// Probing is what must stay gated — it costs a network round trip per
		// cycle. A newly started replica is always probed; a running one is
		// re-probed only when the fragment disagrees with it, so a container
		// that has since died can never capture the route.
		if newly[idx] || (!maintenance && (!routed || !slices.Contains(applied, up))) {
			if err := d.prober.Probe(ctx, up, spec.GetHealth()); err != nil {
				d.discardNew(ctx, current, newly)
				return status(spec.GetAppId(), currentRevision(existing), stateError, "health check failed: "+err.Error())
			}
		}
		upstreams = append(upstreams, up)
	}

	// A raw (routeless) app's desired state is NO fragment. Removing one that
	// was never written would be a mutation on a converged app, so the removal
	// is conditional on a fragment actually being there — that is what keeps
	// converge-twice at zero mutations.
	if spec.GetRoute().GetDomain() == "" && !routed {
		return d.finishConverge(ctx, spec, indexes, current, leftovers)
	}
	// Maintenance is a SERVICE SWAP, not a middleware: the rule, the TLS, the
	// allowlist and the basic auth are all unchanged and only the load
	// balancer's server moves (app-access-control.md §7). The app containers
	// keep running and keep passing their gate above — this flips a route that
	// currently points elsewhere.
	if maintenance {
		up, mErr := d.router.EnsureMaintenance(ctx)
		if mErr != nil {
			// Never take an application down as a side effect of failing to
			// take it down politely: the route is untouched, so the app keeps
			// serving, and the leftovers are deliberately NOT drained — they
			// may still be the ones holding the traffic. Degraded is precisely
			// "serving, with something wrong", and app.crashed fires only on
			// running → error, so nobody is paged for a page that did not
			// appear (§8).
			return status(spec.GetAppId(), currentRevision(existing), stateDegraded, "maintenance page: "+mErr.Error())
		}
		upstreams = []string{up}
	}
	if err := d.applyDesiredRoute(ctx, spec, upstreams); err != nil {
		d.discardNew(ctx, current, newly)
		return status(spec.GetAppId(), currentRevision(existing), stateError, "route: "+err.Error())
	}
	return d.finishConverge(ctx, spec, indexes, current, leftovers)
}

// underMaintenance reports whether this spec wants the responder in front of it.
// A routeless app is never in maintenance: there is no front door to hold shut,
// and a holding page for a raw port would be a fragment nothing reads.
func underMaintenance(spec *agentv1.AppSpec) bool {
	return spec.GetRoute().GetDomain() != "" && spec.GetRoute().GetAccess().GetMaintenance()
}

// desiredIndexes is the replica set this node runs. Empty means [1], which is
// every application that exists today.
func desiredIndexes(spec *agentv1.AppSpec) []uint32 {
	raw := spec.GetReplicaIndexes()
	if len(raw) == 0 {
		return []uint32{1}
	}
	out := make([]uint32, 0, len(raw))
	seen := make(map[uint32]bool, len(raw))
	for _, i := range raw {
		if i == 0 || seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	if len(out) == 0 {
		return []uint32{1}
	}
	slices.Sort(out)
	return out
}

// discardNew removes only the containers THIS pass created. A replica that was
// already serving when the pass began is untouched by a failure — that is the
// anti-stale-container property, kept intact for N replicas.
func (d *Driver) discardNew(ctx context.Context, current map[uint32]*Container, newly map[uint32]bool) {
	for idx := range newly {
		if c := current[idx]; c != nil {
			d.discard(ctx, c.ID)
			delete(current, idx)
		}
	}
}

// startMissingReplicas prepares the node (network, volumes, image) and starts
// every replica that is not already running the desired revision. It returns a
// non-nil status only on failure.
func (d *Driver) startMissingReplicas(
	ctx context.Context,
	spec *agentv1.AppSpec,
	existing []Container,
	missing []uint32,
	current map[uint32]*Container,
	leftovers *[]Container,
) *agentv1.AppStatus {
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

	// Registry-sourced spec (source.kind=image): fetch the image before create.
	// Only in the create branch — the converged fast path never gets here, so
	// converge-twice stays zero-mutation — and EnsureImage itself is a no-op
	// when the reference is already local (crash-resume between pull and start).
	image := spec.GetImage()
	if spec.GetPull() {
		// Learn whether this reference is ours *before* the pull creates it.
		hadSource, hasErr := d.client.HasImage(ctx, image)
		if hasErr != nil {
			// Unknown provenance: assume the operator's and leave it alone.
			hadSource = true
			d.log.Warn("checking image provenance", "image", image, "error", hasErr)
		}
		// The credential is assembled per rollout and lives only for this call:
		// nothing about a registry is written to the agent's disk, so revoking
		// one on the plane revokes it here at the next work item.
		auth, authErr := registryauth.Encode(
			spec.GetRegistryAuth().GetServerAddress(),
			spec.GetRegistryAuth().GetUsername(),
			spec.GetRegistryAuth().GetToken(),
		)
		if authErr != nil {
			return status(spec.GetAppId(), currentRevision(existing), stateError, "registry credential: "+authErr.Error())
		}
		if err := d.client.EnsureImage(ctx, image, auth); err != nil {
			return status(spec.GetAppId(), currentRevision(existing), stateError, "pull: "+err.Error())
		}
		// Record that the reference is ours before anything else can fail.
		// Ownership is knowable only here, and every step after this one —
		// tagging, create, start, the health gate, the route flip — can fail,
		// with the last four discarding the container. A record kept on the
		// container is therefore lost in exactly the cases it exists for. The
		// marker rides on the image, which outlives all of them, so GC can
		// finish the job from the daemon alone.
		pending := PendingRef{Source: image}
		if !hadSource {
			if ref, ok := driver.PullMarkerRef(spec.GetAppId(), image); !ok {
				d.log.Warn("reference too long to record for cleanup", "image", image)
			} else if err := d.client.TagImage(ctx, image, ref); err != nil {
				d.log.Warn("recording the reference our pull created", "image", image, "error", err)
			} else {
				pending.Marker = ref
			}
		}
		// Give the pulled image a managed reference and run the container from
		// that. A pulled image cannot carry our labels — they are baked in by
		// whoever built it — so without this it is invisible to desired-state
		// GC and deleting the app would never reclaim it. Tagging also pins the
		// container to the exact image this rollout resolved, so a tag moving
		// mid-rollout cannot swap what starts.
		managed := managedImageTag(spec.GetAppId(), spec.GetRevisionId())
		if err := d.client.TagImage(ctx, image, managed); err != nil {
			return status(spec.GetAppId(), currentRevision(existing), stateError, "tag: "+err.Error())
		}
		// The managed alias now holds the image, so drop the floating reference
		// the pull created: leaving it keeps every layer alive after the app is
		// deleted. A reference that already existed belongs to the operator and
		// is never touched. Best-effort — tidying disk must not fail a rollout —
		// and whatever is left behind is retried by GC on the next reconcile.
		if !hadSource {
			d.dropPullCreated(ctx, pending)
		}
		image = managed
	}

	// A dead container of the desired revision (crash between create and
	// start) holds the deterministic name; clear it so create cannot collide.
	//
	// Matched on the restart token AND the index, because that is what the name
	// now carries: a leftover at the same revision under a DIFFERENT token is
	// the container a restart is replacing, it is very likely still serving,
	// and discarding it here would turn a zero-downtime restart into an outage.
	// It stays a leftover and is drained after the replacement is healthy and
	// routed.
	starting := make(map[uint32]bool, len(missing))
	for _, idx := range missing {
		starting[idx] = true
	}
	remaining := (*leftovers)[:0]
	for _, c := range *leftovers {
		idx := c.ReplicaIndex
		if idx == 0 {
			idx = 1
		}
		if starting[idx] && c.RevisionID == spec.GetRevisionId() && c.RestartToken == spec.GetRestartToken() {
			d.discard(ctx, c.ID)
			continue
		}
		remaining = append(remaining, c)
	}
	*leftovers = remaining

	// Start the new revision alongside the old one, one container per missing
	// index.
	started := map[uint32]bool{}
	fail := func(detail string) *agentv1.AppStatus {
		d.discardNew(ctx, current, started)
		return status(spec.GetAppId(), currentRevision(existing), stateError, detail)
	}
	for _, idx := range missing {
		newID, err := d.client.CreateContainer(ctx, ContainerSpec{
			Name:          containerName(spec.GetAppId(), spec.GetRevisionId(), spec.GetRestartToken(), idx),
			Image:         image,
			Env:           spec.GetEnv(),
			Network:       spec.GetNetwork(),
			Port:          spec.GetPort(),
			Labels:        managedLabels(spec, idx),
			CPULimit:      spec.GetCpuLimit(),
			MemoryLimitMB: spec.GetMemoryLimitMb(),
			Binds:         binds,
			Ports:         portBindings(spec),
		})
		if err != nil {
			return fail("create: " + err.Error())
		}
		current[idx] = &Container{ID: newID, AppID: spec.GetAppId(), RevisionID: spec.GetRevisionId(), ReplicaIndex: idx, Running: true}
		started[idx] = true
		if err := d.client.StartContainer(ctx, newID); err != nil {
			return fail("start: " + err.Error())
		}
	}
	return nil
}

// applyDesiredRoute reconciles the proxy fragment to the app's desired route:
// an HTTP app (non-empty domain) gets its fragment pointed at every healthy
// replica; a raw app (no domain) gets any fragment removed. Both are
// idempotent.
func (d *Driver) applyDesiredRoute(ctx context.Context, spec *agentv1.AppSpec, upstreams []string) error {
	if spec.GetRoute().GetDomain() == "" || len(upstreams) == 0 {
		return d.router.RemoveRoute(ctx, spec.GetAppId())
	}
	return d.router.SetRoute(ctx, spec.GetAppId(), spec.GetRoute(), upstreams)
}

// finishConverge drains the leftover containers of an app whose desired
// revision is serving and routed. A drain failure is not a rollout failure —
// the desired revision holds the traffic — but it is not convergence either:
// the app is reported degraded and the next reconcile retries the drain.
//
// Scale-in drains before it stops, and that ordering is the whole point: the
// departing index has already been left out of the fragment above, so Traefik
// has stopped sending to it and in-flight requests can finish. Stopping first
// and rewriting after would drop them.
func (d *Driver) finishConverge(ctx context.Context, spec *agentv1.AppSpec, indexes []uint32, current map[uint32]*Container, leftovers []Container) *agentv1.AppStatus {
	for _, c := range leftovers {
		if err := d.drain(ctx, c.ID); err != nil {
			st := d.replicaStatus(spec, indexes, current)
			st.State, st.Detail = stateDegraded, "draining old revision: "+err.Error()
			return st
		}
	}
	st := d.runningStatus(ctx, spec)
	st.Replicas = replicaObservations(indexes, current)
	return st
}

// replicaStatus is the aggregate reading of the replica set (app-scaling.md
// §8): running iff every desired index is running, degraded if some but not
// all, error if none. No new vocabulary — this is the same reading a partially
// up Compose Stack already gets.
func (d *Driver) replicaStatus(spec *agentv1.AppSpec, indexes []uint32, current map[uint32]*Container) *agentv1.AppStatus {
	up := 0
	for _, idx := range indexes {
		if c := current[idx]; c != nil && c.Running {
			up++
		}
	}
	state := stateRunning
	switch {
	case up == 0:
		state = stateError
	case up < len(indexes):
		state = stateDegraded
	}
	st := status(spec.GetAppId(), spec.GetRevisionId(), state, "")
	st.Replicas = replicaObservations(indexes, current)
	return st
}

func replicaObservations(indexes []uint32, current map[uint32]*Container) []*agentv1.ReplicaStatus {
	out := make([]*agentv1.ReplicaStatus, 0, len(indexes))
	for _, idx := range indexes {
		c := current[idx]
		if c == nil {
			out = append(out, &agentv1.ReplicaStatus{Index: idx, State: stateError})
			continue
		}
		id := c.ID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, &agentv1.ReplicaStatus{
			Index: idx, ContainerId: id, RevisionId: c.RevisionID, State: stateRunning,
		})
	}
	return out
}

// runningStatus reports a converged app, carrying the immutable digest of what
// it is actually running for registry-sourced revisions. Resolution is
// best-effort: an image with no digest (never pushed) or a daemon hiccup must
// not turn a healthy rollout into a failure — the plane simply keeps the
// reference it already had.
func (d *Driver) runningStatus(ctx context.Context, spec *agentv1.AppSpec) *agentv1.AppStatus {
	st := status(spec.GetAppId(), spec.GetRevisionId(), stateRunning, "")
	if !spec.GetPull() {
		return st
	}
	// Resolve from the managed alias, not the source tag: that tag can be
	// repointed locally (another app pulling the same `ghost:5`), and resolving
	// it would report a digest this container never ran — which the plane would
	// then pin to the revision, so a later rollback would deploy the wrong
	// artifact. The alias is fixed to what this rollout tagged.
	digest, err := d.client.ImageDigest(ctx, managedImageTag(spec.GetAppId(), spec.GetRevisionId()))
	if err != nil {
		d.log.Warn("resolving image digest", "app_id", spec.GetAppId(), "image", spec.GetImage(), "error", err)
		return st
	}
	st.ResolvedImage = digest
	return st
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
	// Images are not reclaimed here: desired-state GC does it from the images
	// themselves, so a rollout that failed before any container existed still
	// gets cleaned up, and a single failed attempt is retried next reconcile
	// rather than lost with the container that recorded it.
	return failed
}

// dropPullCreated removes a registry reference this driver's own pull created,
// then the marker that recorded it as ours.
//
// The order is the whole point: the marker is what makes a failed removal
// retryable, so it must outlive the reference it names. Best-effort in both
// directions — whatever fails is simply found again on the next reconcile,
// because the record is on the image rather than in this process.
func (d *Driver) dropPullCreated(ctx context.Context, ref PendingRef) {
	if err := d.client.RemoveImage(ctx, ref.Source); err != nil {
		d.log.Warn("dropping the reference our pull created", "image", ref.Source, "error", err)
		return
	}
	if ref.Marker == "" {
		return
	}
	if err := d.client.RemoveImage(ctx, ref.Marker); err != nil {
		d.log.Warn("removing a spent pull marker", "marker", ref.Marker, "error", err)
	}
}

// retainSet indexes the plane's instruction: for each application, the
// revisions whose images must survive (disk-management.md §2).
//
// An application ABSENT from the instruction is absent from this map, and every
// caller reads that as "no instruction", never as "remove everything". That is
// the opposite of how the spec list is read, deliberately: the cost of keeping
// an image too long is disk, and the cost of removing one too early is a
// rollback that cannot run.
func retainSet(desired []*agentv1.RetainSpec) map[string]map[string]struct{} {
	if len(desired) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{}, len(desired))
	for _, r := range desired {
		if len(r.GetRevisionIds()) == 0 {
			continue // no instruction, not "keep nothing"
		}
		keep := make(map[string]struct{}, len(r.GetRevisionIds()))
		for _, rev := range r.GetRevisionIds() {
			keep[rev] = struct{}{}
		}
		out[r.GetAppId()] = keep
	}
	return out
}

// gcImages reclaims what desired state does not reference.
//
// Two rules, and nothing else is ever touched. An application absent from
// desired loses every managed reference — that is absence-means-remove, and it
// already existed. An application still desired loses the references naming
// revisions outside its retain set — that is the part a prune job cannot do,
// because it cannot know which stopped image a rollback still needs.
//
// It works on REFERENCES rather than images: two revisions of one app can share
// an image, and one operator's own tag can sit beside ours on it. Removing a
// name we created frees the layers exactly when the last such name goes, and
// never removes one we did not create.
func (d *Driver) gcImages(ctx context.Context, desiredApps map[string]struct{}, retain map[string]map[string]struct{}) {
	images, err := d.client.ListManagedImages(ctx)
	if err != nil {
		d.log.Warn("listing managed images for GC", "error", err)
		return
	}
	for _, img := range images {
		// Retried for desired and removed apps alike: the marker names both the
		// reference and the fact that it is ours, so this needs no spec, no
		// container, and no memory of the rollout that created it. That is what
		// makes it survive a rollout which failed before any container existed —
		// and the app being deleted before the retry ever succeeded.
		for _, p := range img.Pending {
			d.dropPullCreated(ctx, p)
		}
		// An image shared by a still-desired app must survive whole: two apps
		// can run the same pulled image, each under its own managed alias.
		if slices.ContainsFunc(img.AppIDs, func(appID string) bool {
			_, wanted := desiredApps[appID]
			return wanted
		}) {
			d.gcRetainedRevisions(ctx, img, desiredApps, retain)
			continue
		}
		// Drop every managed alias. Together with the pending references above
		// that is every name we ever put on this image, which is what actually
		// frees the layers — untagging the alias alone leaves the registry
		// reference the image arrived under still holding them.
		for _, ref := range img.References {
			if err := d.client.RemoveImage(ctx, ref); err != nil {
				d.log.Warn("removing image reference", "reference", ref, "app_ids", img.AppIDs, "error", err)
			}
		}
	}
}

// gcRetainedRevisions drops the references of a still-desired application that
// name a revision the plane no longer wants to be able to run.
func (d *Driver) gcRetainedRevisions(ctx context.Context, img Image, desiredApps map[string]struct{}, retain map[string]map[string]struct{}) {
	for _, ref := range img.Managed {
		if _, wanted := desiredApps[ref.AppID]; !wanted {
			continue // handled by the absence path, which drops every reference
		}
		keep, instructed := retain[ref.AppID]
		if !instructed {
			continue // no instruction for this app: leave it alone
		}
		if ref.RevisionID == "" {
			continue // a reference that names no revision is not ours to judge
		}
		if _, retained := keep[ref.RevisionID]; retained {
			continue
		}
		if err := d.client.RemoveImage(ctx, ref.Reference); err != nil {
			d.log.Warn("reclaiming an unreferenced revision image",
				"reference", ref.Reference, "app_id", ref.AppID, "revision_id", ref.RevisionID, "error", err)
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

func managedLabels(spec *agentv1.AppSpec, index uint32) map[string]string {
	labels := map[string]string{
		driver.LabelManaged:    driverName,
		driver.LabelAppID:      spec.GetAppId(),
		driver.LabelRevisionID: spec.GetRevisionId(),
	}
	// Stamped only when there is one, so an application that has never
	// restarted keeps exactly the labels it had before this feature existed.
	if t := spec.GetRestartToken(); t != "" {
		labels[driver.LabelRestartToken] = t
	}
	// Stamped only past index 1, for the same reason the name is: a container
	// that predates replicas must keep exactly the labels it had.
	if index > 1 {
		labels[driver.LabelReplicaIndex] = strconv.FormatUint(uint64(index), 10)
	}
	return labels
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

// containerName is the deterministic name a rollout creates under. The restart
// token joins it so a restart's replacement does NOT collide with the container
// it replaces: that is what lets a restart run the ordinary zero-downtime
// sequence — start alongside, health-gate, flip the route, drain the old —
// rather than killing the only container that is serving.
//
// Identity comes from labels, never from this name (see Container), so the
// extra segment is cosmetic to everything except collision avoidance. An
// application that has never restarted keeps the exact name it always had.
// containerName is deterministic, and index 1 keeps EXACTLY the name it has
// always had. That is not cosmetic: without it, upgrading the agent would make
// every existing container in every fleet read as drift and get recreated on
// the next reconcile, turning a version bump into a fleet-wide rolling restart
// (app-scaling.md §2). Identity still comes from labels, never the name.
func containerName(appID, revisionID, restartToken string, index uint32) string {
	name := "cypher-" + appID + "-" + revisionID
	if restartToken != "" {
		name += "-" + restartToken
	}
	if index > 1 {
		name += "-r" + strconv.FormatUint(uint64(index), 10)
	}
	return name
}

// managedImageTag is the deterministic reference every managed image lives
// under — what the build path tags into, and what a pulled image is tagged as
// so both routes are equally visible to desired-state GC.
func managedImageTag(appID, revisionID string) string {
	return "cypher/" + appID + ":" + revisionID
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

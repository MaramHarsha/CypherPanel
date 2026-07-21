package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// ── recording fakes ─────────────────────────────────────────────────────────

type fakeClient struct {
	containers map[string]*Container // managed, by id
	unmanaged  map[string]*Container // never returned by ListManaged
	images     []Image
	ipByID     map[string]string
	nextID     int

	createErrForImage map[string]error
	stopErrForID      map[string]error
	removeErrForID    map[string]error
	listErr           error

	lastCreateSpec ContainerSpec // the most recent CreateContainer argument
	ensuredVolumes []string      // names passed to EnsureVolume
	execCalls      []execCall    // recorded ExecAndWait invocations
	execExit       int           // injected exit code
	execOut        []byte        // injected output
	execErr        error         // injected error

	mutations int // count of state-changing calls
}

type execCall struct {
	containerID string
	cmd         []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		containers:        map[string]*Container{},
		unmanaged:         map[string]*Container{},
		ipByID:            map[string]string{},
		createErrForImage: map[string]error{},
		stopErrForID:      map[string]error{},
		removeErrForID:    map[string]error{},
	}
}

func (f *fakeClient) EnsureNetwork(_ context.Context, _ string, _ map[string]string) error {
	f.mutations++
	return nil
}

func (f *fakeClient) EnsureVolume(_ context.Context, name string, _ map[string]string) error {
	f.mutations++
	f.ensuredVolumes = append(f.ensuredVolumes, name)
	return nil
}

func (f *fakeClient) ListManaged(_ context.Context) ([]Container, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Container, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeClient) ExecAndWait(_ context.Context, containerID string, cmd []string) (int, []byte, error) {
	f.execCalls = append(f.execCalls, execCall{containerID: containerID, cmd: cmd})
	return f.execExit, f.execOut, f.execErr
}

func (f *fakeClient) CreateContainer(_ context.Context, spec ContainerSpec) (string, error) {
	f.lastCreateSpec = spec
	if err := f.createErrForImage[spec.Image]; err != nil {
		return "", err
	}
	// The real daemon refuses a create whose name is taken — the fake must
	// too, or crash-window convergence bugs stay invisible in tests.
	for _, c := range f.containers {
		if c.Name == spec.Name {
			return "", errors.New("name " + spec.Name + " already in use")
		}
	}
	f.mutations++
	f.nextID++
	id := "c" + itoa(f.nextID)
	f.containers[id] = &Container{
		ID:         id,
		Name:       spec.Name,
		AppID:      spec.Labels[driver.LabelAppID],
		RevisionID: spec.Labels[driver.LabelRevisionID],
		Running:    false,
	}
	f.ipByID[id] = "10.1.2.3"
	return id, nil
}

func (f *fakeClient) StartContainer(_ context.Context, id string) error {
	f.mutations++
	if c := f.containers[id]; c != nil {
		c.Running = true
	}
	return nil
}

func (f *fakeClient) StopContainer(_ context.Context, id string, _ time.Duration) error {
	if err := f.stopErrForID[id]; err != nil {
		return err
	}
	f.mutations++
	if c := f.containers[id]; c != nil {
		c.Running = false
	}
	return nil
}

func (f *fakeClient) RemoveContainer(_ context.Context, id string) error {
	if err := f.removeErrForID[id]; err != nil {
		return err
	}
	f.mutations++
	delete(f.containers, id)
	return nil
}

func (f *fakeClient) ContainerIP(_ context.Context, id, _ string) (string, error) {
	return f.ipByID[id], nil
}

func (f *fakeClient) ListManagedImages(_ context.Context) ([]Image, error) {
	return f.images, nil
}

func (f *fakeClient) RemoveImage(_ context.Context, id string) error {
	f.mutations++
	kept := f.images[:0]
	for _, img := range f.images {
		if img.ID != id {
			kept = append(kept, img)
		}
	}
	f.images = kept
	return nil
}

// addContainer seeds a managed container as if a previous driver run created
// it (deterministic name included) — the raw material of crash-window tests.
func (f *fakeClient) addContainer(appID, revID string, running bool) string {
	f.nextID++
	id := "c" + itoa(f.nextID)
	f.containers[id] = &Container{
		ID:         id,
		Name:       containerName(appID, revID),
		AppID:      appID,
		RevisionID: revID,
		Running:    running,
	}
	f.ipByID[id] = "10.1.2." + itoa(f.nextID)
	return id
}

func (f *fakeClient) StreamLogs(ctx context.Context, id string, out io.Writer) error {
	return nil
}

type fakeRouter struct {
	routes    map[string]string // appID → upstream
	setErr    error
	mutations int
	// EnsureProxy/AttachNetwork are idempotent daemon no-ops (real ones only
	// mutate on Proxy recreate / first attach), so they are tracked here but
	// deliberately kept out of `mutations` — the converge-twice invariant is
	// about container/route changes, which these are not.
	proxyEnsured  int
	networksBound []string
}

func newFakeRouter() *fakeRouter { return &fakeRouter{routes: map[string]string{}} }

func (r *fakeRouter) EnsureProxy(context.Context) error { r.proxyEnsured++; return nil }

func (r *fakeRouter) AttachNetwork(_ context.Context, network string) error {
	r.networksBound = append(r.networksBound, network)
	return nil
}

func (r *fakeRouter) SetRoute(_ context.Context, appID string, _ *agentv1.RouteSpec, upstream string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.mutations++
	r.routes[appID] = upstream
	return nil
}

func (r *fakeRouter) RemoveRoute(_ context.Context, appID string) error {
	r.mutations++
	delete(r.routes, appID)
	return nil
}

func (r *fakeRouter) Route(_ context.Context, appID string) (string, bool, error) {
	up, ok := r.routes[appID]
	return up, ok, nil
}

type fakeProber struct{ fail bool }

func (p *fakeProber) Probe(_ context.Context, _ string, _ *agentv1.HealthCheck) error {
	if p.fail {
		return errors.New("not healthy")
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ── harness ─────────────────────────────────────────────────────────────────

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newDriver(c *fakeClient, r *fakeRouter, p *fakeProber) *Driver {
	return New(c, r, p, quietLog())
}

func spec(appID, revID, image string) *agentv1.AppSpec {
	return &agentv1.AppSpec{
		AppId:      appID,
		RevisionId: revID,
		Image:      image,
		Port:       8080,
		Network:    "cypher-env1",
		Route:      &agentv1.RouteSpec{Domain: appID + ".example.com", Https: true},
		Health:     &agentv1.HealthCheck{Path: "/"},
	}
}

func statusOf(statuses []*agentv1.AppStatus, appID string) *agentv1.AppStatus {
	for _, s := range statuses {
		if s.GetAppId() == appID {
			return s
		}
	}
	return nil
}

// runningRev returns the ids of running containers for app/revision.
func runningRev(c *fakeClient, appID, revID string) []string {
	var out []string
	for id, ct := range c.containers {
		if ct.AppID == appID && ct.RevisionID == revID && ct.Running {
			out = append(out, id)
		}
	}
	return out
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestReconcileRollsOutNewApp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st == nil || st.GetState() != stateRunning || st.GetRevisionId() != "rev1" {
		t.Fatalf("status = %+v, want running rev1", st)
	}
	if len(c.containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(c.containers))
	}
	if r.routes["app1"] != "10.1.2.3:8080" {
		t.Fatalf("route = %q, want upstream set", r.routes["app1"])
	}
}

func TestConvergeTwiceIsNoOp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)
	specs := []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	baseline := c.mutations + r.mutations

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if after := c.mutations + r.mutations; after != baseline {
		t.Fatalf("second converge mutated state: %d calls (want %d)", after-baseline, 0)
	}
}

func TestCrashResumeIsNoOp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	specs := []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}

	if _, err := newDriver(c, r, p).Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	baseline := c.mutations + r.mutations

	// A brand-new driver instance over the same state must be a no-op — proving
	// convergence reads reality (labels), not in-memory bookkeeping.
	if _, err := newDriver(c, r, p).Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("resumed Reconcile: %v", err)
	}
	if after := c.mutations + r.mutations; after != baseline {
		t.Fatalf("resumed converge mutated state: %d calls (want 0)", after-baseline)
	}
}

// Crash window 1: create succeeded, start never happened. The dead container
// holds the deterministic name; convergence must clear it and roll out —
// against the real daemon a blind create would fail with a name conflict.
func TestCrashAfterCreateBeforeStartConverges(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	oldID := c.addContainer("app1", "rev1", true)
	r.routes["app1"] = "10.1.2.1:8080"
	c.addContainer("app1", "rev2", false) // the crash leftover

	got, err := newDriver(c, r, p).Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateRunning || st.GetRevisionId() != "rev2" {
		t.Fatalf("status = %+v, want running rev2", st)
	}
	if n := runningRev(c, "app1", "rev2"); len(n) != 1 {
		t.Fatalf("want exactly one running rev2 container, got %d", len(n))
	}
	if _, ok := c.containers[oldID]; ok {
		t.Fatal("old revision was not drained after successful rollout")
	}
}

// Crash window 2: the new revision started but the route never flipped. The
// old code reported running and left traffic on the old revision forever;
// convergence must observe the route, flip it, and drain the old container.
func TestCrashAfterStartBeforeFlipConverges(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	oldID := c.addContainer("app1", "rev1", true)
	newID := c.addContainer("app1", "rev2", true)
	r.routes["app1"] = "10.1.2." + oldID[1:] + ":8080" // route still on rev1

	got, err := newDriver(c, r, p).Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateRunning || st.GetRevisionId() != "rev2" {
		t.Fatalf("status = %+v, want running rev2", st)
	}
	wantUpstream := c.ipByID[newID] + ":8080"
	if r.routes["app1"] != wantUpstream {
		t.Fatalf("route = %q, want flipped to rev2 upstream %q", r.routes["app1"], wantUpstream)
	}
	if _, ok := c.containers[oldID]; ok {
		t.Fatal("old revision was not drained")
	}
}

// Crash window 3: route flipped but the old container never drained.
// Convergence must drain it without touching the route or creating anything.
func TestCrashAfterFlipBeforeDrainConverges(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	oldID := c.addContainer("app1", "rev1", true)
	newID := c.addContainer("app1", "rev2", true)
	r.routes["app1"] = c.ipByID[newID] + ":8080" // route already on rev2

	routeMuts := r.mutations
	got, err := newDriver(c, r, p).Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateRunning || st.GetRevisionId() != "rev2" {
		t.Fatalf("status = %+v, want running rev2", st)
	}
	if _, ok := c.containers[oldID]; ok {
		t.Fatal("old revision was not drained")
	}
	if r.mutations != routeMuts {
		t.Fatal("route mutated although it already pointed at the desired revision")
	}
}

func TestAbsenceRemovesApp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	// A non-managed look-alike the driver must never touch.
	c.unmanaged["x1"] = &Container{ID: "x1", AppID: "someone-else", Running: true}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("rollout: %v", err)
	}
	// Now app1 is no longer desired.
	got, err := d.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("clean teardown reported statuses: %+v", got)
	}
	if len(c.containers) != 0 {
		t.Fatalf("want app container removed, %d remain", len(c.containers))
	}
	if _, ok := r.routes["app1"]; ok {
		t.Fatal("route not removed")
	}
	if _, ok := c.unmanaged["x1"]; !ok {
		t.Fatal("driver removed an unmanaged container")
	}
}

// A teardown that fails is an observation the plane needs: the app has not
// actually converged to absence, so it must appear in the status report.
func TestFailedTeardownIsReported(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	id := c.addContainer("gone", "rev1", true)
	c.stopErrForID[id] = errors.New("daemon busy")

	got, err := newDriver(c, r, p).Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "gone")
	if st == nil || st.GetState() != stateError {
		t.Fatalf("status = %+v, want error for failed teardown", st)
	}
}

// A drain failure after a successful flip is degraded, not error: the desired
// revision holds the traffic, but convergence isn't complete — and the next
// reconcile retries the drain and returns to running.
func TestFailedDrainReportsDegradedThenRecovers(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	oldID := c.addContainer("app1", "rev1", true)
	newID := c.addContainer("app1", "rev2", true)
	r.routes["app1"] = c.ipByID[newID] + ":8080"
	c.stopErrForID[oldID] = errors.New("daemon busy")

	d := newDriver(c, r, p)
	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st := statusOf(got, "app1"); st.GetState() != stateDegraded || st.GetRevisionId() != "rev2" {
		t.Fatalf("status = %+v, want degraded on rev2", st)
	}

	// Drain succeeds on the retry: back to running, old container gone.
	delete(c.stopErrForID, oldID)
	got, err = d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("retry Reconcile: %v", err)
	}
	if st := statusOf(got, "app1"); st.GetState() != stateRunning {
		t.Fatalf("status = %+v, want running after drain retry", st)
	}
	if _, ok := c.containers[oldID]; ok {
		t.Fatal("old revision still present after drain retry")
	}
}

func TestFailedHealthKeepsOldServing(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	// rev1 healthy and serving.
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("rev1 rollout: %v", err)
	}
	rev1Route := r.routes["app1"]

	// rev2 attempted, but health fails.
	p.fail = true
	mutBefore := r.mutations
	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("rev2 rollout: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateError {
		t.Fatalf("state = %q, want error", st.GetState())
	}
	if st.GetRevisionId() != "rev1" {
		t.Fatalf("serving revision = %q, want rev1 still serving", st.GetRevisionId())
	}
	// rev1 container still present and running; route unchanged; rev2 discarded.
	if r.routes["app1"] != rev1Route {
		t.Fatal("route flipped to an unhealthy revision")
	}
	if r.mutations != mutBefore {
		t.Fatal("route mutated despite failed health gate")
	}
	rev1Present := false
	for _, ct := range c.containers {
		if ct.RevisionID == "rev1" {
			rev1Present = true
		}
		if ct.RevisionID == "rev2" {
			t.Fatal("unhealthy rev2 container was left behind")
		}
	}
	if !rev1Present {
		t.Fatal("rev1 was removed despite rev2 failing")
	}
}

func TestPartialFailureConvergesOthers(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.createErrForImage["img:bad"] = errors.New("no such image")
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{
		spec("bad", "rev1", "img:bad"),
		spec("good", "rev1", "img:good"),
	})
	if err != nil {
		t.Fatalf("Reconcile returned a total error for a per-app failure: %v", err)
	}
	if st := statusOf(got, "bad"); st.GetState() != stateError {
		t.Fatalf("bad app state = %q, want error", st.GetState())
	}
	if st := statusOf(got, "good"); st.GetState() != stateRunning {
		t.Fatalf("good app state = %q, want running", st.GetState())
	}
	if r.routes["good"] == "" {
		t.Fatal("good app route not set")
	}
}

// A failed create must report the revision still serving, not an empty one —
// the plane needs to know the old revision kept the traffic.
func TestFailedCreateReportsServingRevision(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.addContainer("app1", "rev1", true)
	r.routes["app1"] = "10.1.2.1:8080"
	c.createErrForImage["img:rev2"] = errors.New("no such image")

	got, err := newDriver(c, r, p).Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev2", "img:rev2")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateError || st.GetRevisionId() != "rev1" {
		t.Fatalf("status = %+v, want error with rev1 still serving", st)
	}
}

func TestListManagedErrorIsReturned(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.listErr = errors.New("daemon unreachable")
	if _, err := newDriver(c, r, p).Reconcile(context.Background(), nil); err == nil {
		t.Fatal("expected a total error when the daemon is unreachable")
	}
}

func TestGCRemovesImagesOfAbsentApps(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.images = []Image{
		{ID: "i1", AppID: "keep", RevisionID: "rev1"},
		{ID: "i2", AppID: "gone", RevisionID: "rev1"},
	}
	d := newDriver(c, r, p)
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("keep", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.images) != 1 || c.images[0].AppID != "keep" {
		t.Fatalf("GC left images = %+v, want only keep", c.images)
	}
}

// RunningContainerForApp resolves the app's running container (the scheduled-
// task exec target, ADR-011) and reports ok=false when none is running.
func TestRunningContainerForApp(t *testing.T) {
	c := newFakeClient()
	d := newDriver(c, newFakeRouter(), &fakeProber{})

	running := c.addContainer("app1", "rev2", true)
	c.addContainer("app1", "rev1", false) // an older, stopped container
	c.addContainer("app2", "rev1", true)  // a different app

	id, ok, err := d.RunningContainerForApp(context.Background(), "app1")
	if err != nil || !ok || id != running {
		t.Fatalf("RunningContainerForApp = (%q, %v, %v), want the running app1 container", id, ok, err)
	}

	// An app with no running container: ok=false, no error (a skip, not a fail).
	c2 := newFakeClient()
	d2 := newDriver(c2, newFakeRouter(), &fakeProber{})
	c2.addContainer("app1", "rev1", false)
	if _, ok, err := d2.RunningContainerForApp(context.Background(), "app1"); ok || err != nil {
		t.Fatalf("want ok=false,nil err when nothing runs; got ok=%v err=%v", ok, err)
	}
}

// ExecAndWait delegates to the client and returns its exit code and output.
func TestDriverExecAndWait(t *testing.T) {
	c := newFakeClient()
	c.execExit, c.execOut = 3, []byte("out")
	d := newDriver(c, newFakeRouter(), &fakeProber{})

	exit, out, err := d.ExecAndWait(context.Background(), "c1", []string{"sh", "-c", "x"})
	if err != nil || exit != 3 || string(out) != "out" {
		t.Fatalf("ExecAndWait = (%d, %q, %v), want (3, out, nil)", exit, out, err)
	}
	if len(c.execCalls) != 1 || c.execCalls[0].containerID != "c1" {
		t.Fatalf("exec not delegated to client: %+v", c.execCalls)
	}
}

// Resource limits on the AppSpec reach the container's create spec (the engine
// then maps them to HostConfig NanoCpus/Memory) — feature-matrix V1.
func TestReconcileAppliesResourceLimits(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	sp := spec("app1", "rev1", "img")
	sp.CpuLimit = 1.5
	sp.MemoryLimitMb = 512
	d.Reconcile(context.Background(), []*agentv1.AppSpec{sp})

	if c.lastCreateSpec.CPULimit != 1.5 || c.lastCreateSpec.MemoryLimitMB != 512 {
		t.Fatalf("container spec limits = %v/%v, want 1.5/512", c.lastCreateSpec.CPULimit, c.lastCreateSpec.MemoryLimitMB)
	}
}

// Declared volumes are ensured and bound into the container on create; a second
// converge (no change) makes no further EnsureVolume call (converge-twice).
func TestReconcileEnsuresAndBindsVolumes(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	sp := spec("app1", "rev1", "img")
	sp.Volumes = []*agentv1.VolumeMount{
		{VolumeName: "cypher-appvol-app1-data", Path: "/data"},
	}
	d.Reconcile(context.Background(), []*agentv1.AppSpec{sp})

	if len(c.ensuredVolumes) != 1 || c.ensuredVolumes[0] != "cypher-appvol-app1-data" {
		t.Fatalf("ensured volumes = %v, want [cypher-appvol-app1-data]", c.ensuredVolumes)
	}
	if len(c.lastCreateSpec.Binds) != 1 || c.lastCreateSpec.Binds[0] != "cypher-appvol-app1-data:/data" {
		t.Fatalf("binds = %v, want [cypher-appvol-app1-data:/data]", c.lastCreateSpec.Binds)
	}

	// Converge again with the same desired state: no new EnsureVolume (the app
	// is already at its desired revision — converge-twice invariant).
	before := len(c.ensuredVolumes)
	d.Reconcile(context.Background(), []*agentv1.AppSpec{sp})
	if len(c.ensuredVolumes) != before {
		t.Fatalf("second converge ensured a volume again (%d → %d)", before, len(c.ensuredVolumes))
	}
}

package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
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

	lastCreateSpec ContainerSpec     // the most recent CreateContainer argument
	ensuredVolumes []string          // names passed to EnsureVolume
	pulledImages   []string          // refs EnsureImage actually fetched
	localImages    map[string]bool   // refs EnsureImage treats as already present
	pullErr        error             // injected EnsureImage failure
	digests        map[string]string // explicit ref → digest overrides
	digestErr      error             // injected ImageDigest failure
	tagged         []string          // "source -> target" per TagImage call
	removedImages  map[string]bool   // refs passed to RemoveImage
	removeImageErr map[string]error  // injected RemoveImage failures by ref
	tagErr         error             // injected TagImage failure
	execCalls      []execCall        // recorded ExecAndWait invocations
	execExit       int               // injected exit code
	execOut        []byte            // injected output
	execErr        error             // injected error

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

// EnsureImage models the real engine's contract: a digest is immutable, so a
// local copy is accepted; a tag is mutable and always re-fetched.
func (f *fakeClient) EnsureImage(_ context.Context, ref string) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	if strings.Contains(ref, "@") && f.localImages[ref] {
		return nil
	}
	f.mutations++
	f.pulledImages = append(f.pulledImages, ref)
	if f.localImages == nil {
		f.localImages = map[string]bool{}
	}
	f.localImages[ref] = true
	return nil
}

func (f *fakeClient) HasImage(_ context.Context, ref string) (bool, error) {
	return f.localImages[ref], nil
}

func (f *fakeClient) TagImage(_ context.Context, source, target string) error {
	if f.tagErr != nil {
		return f.tagErr
	}
	f.mutations++
	f.tagged = append(f.tagged, source+" -> "+target)
	if f.localImages == nil {
		f.localImages = map[string]bool{}
	}
	f.localImages[target] = true
	return nil
}

// ImageDigest models the engine: a pulled reference resolves to an immutable
// digest; a locally-built image has none.
func (f *fakeClient) ImageDigest(_ context.Context, ref string) (string, error) {
	if f.digestErr != nil {
		return "", f.digestErr
	}
	if d, ok := f.digests[ref]; ok {
		return d, nil
	}
	if strings.Contains(ref, "@") {
		return ref, nil // already a digest reference
	}
	if !f.localImages[ref] {
		return "", nil // never pulled here
	}
	name, _ := ref, ""
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name = ref[:i]
	}
	return name + "@sha256:" + strings.Repeat("a", 8), nil
}

func (f *fakeClient) ListManagedImages(_ context.Context) ([]Image, error) {
	return f.images, nil
}

func (f *fakeClient) RemoveImage(_ context.Context, id string) error {
	if err := f.removeImageErr[id]; err != nil {
		return err
	}
	f.mutations++
	if f.removedImages == nil {
		f.removedImages = map[string]bool{}
	}
	f.removedImages[id] = true
	kept := f.images[:0]
	for _, img := range f.images {
		if !slices.Contains(img.References, id) && img.ID != id {
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
	setCalls      int
	proxyErr      error
	networksBound []string
}

func newFakeRouter() *fakeRouter { return &fakeRouter{routes: map[string]string{}} }

func (r *fakeRouter) EnsureProxy(context.Context) error { r.proxyEnsured++; return r.proxyErr }

func (r *fakeRouter) AttachNetwork(_ context.Context, network string) error {
	r.networksBound = append(r.networksBound, network)
	return nil
}

// The reconciler calls SetRoute every cycle so that a change to the fragment's
// shape reaches a stable app, and the real implementation compares the rendered
// bytes and skips an identical write. The fake models that contract: a call
// that changes nothing is not a mutation, which is what the converge-twice
// invariant actually asserts.
func (r *fakeRouter) SetRoute(_ context.Context, appID string, _ *agentv1.RouteSpec, upstream string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.setCalls++
	if cur, ok := r.routes[appID]; ok && cur == upstream {
		return nil
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

type fakeProber struct {
	fail  bool
	calls int
}

func (p *fakeProber) Probe(_ context.Context, _ string, _ *agentv1.HealthCheck) error {
	p.calls++
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
		{ID: "i1", AppIDs: []string{"keep"}, References: []string{"cypher/keep:rev1"}},
		{ID: "i2", AppIDs: []string{"gone"}, References: []string{"cypher/gone:rev1"}},
	}
	d := newDriver(c, r, p)
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("keep", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.images) != 1 || c.images[0].AppIDs[0] != "keep" {
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

// A raw (non-HTTP) service has no route domain: the reconciler publishes its
// ports, writes NO proxy fragment, and still converges idempotently.
func TestReconcileRawServiceNoRouteAndPorts(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	sp := spec("app1", "rev1", "img")
	sp.Route = &agentv1.RouteSpec{} // no domain → raw service
	sp.Health = &agentv1.HealthCheck{Kind: "tcp"}
	sp.Ports = []*agentv1.PortMapping{
		{HostPort: 25565, ContainerPort: 25565, Protocol: "tcp"},
		{HostPort: 25565, ContainerPort: 25565, Protocol: "udp"},
	}

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{sp})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st := statusOf(got, "app1"); st == nil || st.GetState() != stateRunning {
		t.Fatalf("status = %+v, want running", st)
	}
	// Ports flowed onto the container spec.
	if len(c.lastCreateSpec.Ports) != 2 {
		t.Fatalf("container ports = %+v, want 2", c.lastCreateSpec.Ports)
	}
	// No proxy fragment for a routeless app.
	if _, ok := r.routes["app1"]; ok {
		t.Fatalf("a route was written for a raw service: %q", r.routes["app1"])
	}

	// Converge-twice: the second pass makes zero client and router mutations.
	cm, rm := c.mutations, r.mutations
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{sp}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if c.mutations != cm || r.mutations != rm {
		t.Fatalf("second converge mutated (client %d→%d, router %d→%d)", cm, c.mutations, rm, r.mutations)
	}
}

// A Proxy that cannot start is reported upward, not just logged. It fails in a
// retry loop rather than crashing, so without this the agent kept reporting
// READY and the server stayed green while no routed deploy could ever work
// (ui-principles §10). Reconcile must still converge containers — routing
// convergence is best-effort and must not block the rest.
func TestReconcileReportsProxyHealth(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	var reported []error
	d.OnProxyHealth(func(err error) { reported = append(reported, err) })

	r.proxyErr = errors.New("bind :80: address already in use")
	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")})
	if err != nil {
		t.Fatalf("Reconcile must still converge apps when the Proxy is down: %v", err)
	}
	if st := statusOf(got, "app1"); st == nil || st.GetState() != stateRunning {
		t.Errorf("app should still roll out with a broken Proxy, got %+v", st)
	}
	if len(reported) != 1 || reported[0] == nil {
		t.Fatalf("proxy failure not reported: %v", reported)
	}

	// Recovery has to clear it, or the server stays amber forever once the
	// operator frees the port.
	r.proxyErr = nil
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("Reconcile after recovery: %v", err)
	}
	if len(reported) != 2 || reported[1] != nil {
		t.Fatalf("recovery not reported as healthy: %v", reported)
	}
}

// A change to the fragment's shape — a middleware added to the template, say —
// must reach an app whose upstream never moves. The reconciler used to call
// SetRoute only when the upstream changed, so a stable app kept a stale
// fragment indefinitely and the change appeared only after something unrelated
// restarted its container.
func TestReconcileRewritesRouteWhenOnlyTheTemplateChanged(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)
	specs := []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	before := r.setCalls

	// Nothing about the app changed, so no probe should be needed...
	probesBefore := p.calls
	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	// ...but SetRoute is still offered the current desired route, so the
	// renderer can notice its own output has changed.
	if r.setCalls <= before {
		t.Errorf("SetRoute must be called every cycle, got %d then %d", before, r.setCalls)
	}
	if p.calls != probesBefore {
		t.Errorf("an unchanged upstream must not be re-probed: %d -> %d", probesBefore, p.calls)
	}
}

// ── deploy-from-image (AppSpec.pull) ────────────────────────────────────────

func pullSpec(appID, revID, image string) *agentv1.AppSpec {
	s := spec(appID, revID, image)
	s.Pull = true
	return s
}

func TestPullSpecFetchesImageAndRollsOut(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st == nil || st.GetState() != stateRunning || st.GetRevisionId() != "rev1" {
		t.Fatalf("status = %+v, want running rev1", st)
	}
	if len(c.pulledImages) != 1 || c.pulledImages[0] != "ghost:5" {
		t.Fatalf("pulled = %v, want [ghost:5]", c.pulledImages)
	}
}

func TestPullSpecConvergeTwicePullsOnce(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)
	specs := []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	baseline := c.mutations + r.mutations

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if after := c.mutations + r.mutations; after != baseline {
		t.Fatalf("second converge mutated state: %d calls (want 0)", after-baseline)
	}
	if len(c.pulledImages) != 1 {
		t.Fatalf("pull count = %d, want exactly 1 across both converges", len(c.pulledImages))
	}
}

// A mutable tag must be re-fetched for every new revision. Skipping the pull
// because `ghost:5` happens to be cached would start the new container from
// the stale image and then report success — the stale-container failure
// ADR-005 exists to make impossible.
func TestNewRevisionRepullsMutableTag(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	// Same reference, new revision — the operator moved the tag and redeployed.
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev2", "ghost:5")}); err != nil {
		t.Fatalf("rev2: %v", err)
	}
	if len(c.pulledImages) != 2 {
		t.Fatalf("pulled %v, want the tag re-fetched for the new revision", c.pulledImages)
	}
}

// A digest is immutable: once local, those are provably the right bits, so a
// new revision on the same digest needs no registry round trip.
func TestNewRevisionSkipsPullForLocalDigest(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)
	const ref = "ghcr.io/acme/web@sha256:abc"

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", ref)}); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev2", ref)}); err != nil {
		t.Fatalf("rev2: %v", err)
	}
	if len(c.pulledImages) != 1 {
		t.Fatalf("pulled %v, want one fetch for an immutable digest", c.pulledImages)
	}
}

func TestPullFailureLeavesOldServing(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	// rev1 (a pulled image) healthy and serving.
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5.0")}); err != nil {
		t.Fatalf("rev1 rollout: %v", err)
	}
	rev1Route := r.routes["app1"]

	// rev2's registry fetch fails (missing tag, registry down).
	c.pullErr = errors.New("pull failed: manifest unknown")
	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev2", "ghost:9.9")})
	if err != nil {
		t.Fatalf("rev2 rollout: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateError || st.GetRevisionId() != "rev1" {
		t.Fatalf("status = %+v, want error with rev1 still serving", st)
	}
	if !strings.Contains(st.GetDetail(), "pull:") {
		t.Fatalf("detail = %q, want the pull failure surfaced", st.GetDetail())
	}
	if r.routes["app1"] != rev1Route {
		t.Fatal("route moved despite failed pull")
	}
	if len(runningRev(c, "app1", "rev1")) != 1 {
		t.Fatal("rev1 stopped serving despite rev2's failed pull")
	}
}

func TestNonPullSpecNeverPulls(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.pulledImages) != 0 {
		t.Fatalf("pulled = %v, want none for a locally-built image (ADR-008)", c.pulledImages)
	}
}

// A pulled image is tagged into the managed namespace and the container is
// created from that reference — the only way a registry image becomes visible
// to desired-state GC, since labels are baked in by whoever built it.
func TestPulledImageIsTaggedManagedAndUsed(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Ownership is recorded first, then the alias — the marker has to be on the
	// image before any step that could fail while the reference is already
	// there, and the alias tag is the first of those.
	want := []string{
		"ghost:5 -> " + pullMarker(t, "app1", "ghost:5"),
		"ghost:5 -> cypher/app1:rev1",
	}
	if !slices.Equal(c.tagged, want) {
		t.Fatalf("tagged = %v, want %v", c.tagged, want)
	}
	if got := c.lastCreateSpec.Image; got != "cypher/app1:rev1" {
		t.Fatalf("container created from %q, want the managed reference", got)
	}
}

// A locally-built image already lives in the managed namespace and carries our
// labels, so it is neither pulled nor re-tagged (ADR-008).
func TestBuiltImageIsNotTagged(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "cypher/app1:rev1")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.tagged) != 0 || len(c.pulledImages) != 0 {
		t.Fatalf("built image was tagged/pulled: tagged=%v pulled=%v", c.tagged, c.pulledImages)
	}
}

// The digest actually running is reported back, so the plane can pin the
// revision to the artifact rather than to a tag that can move.
func TestPullSpecReportsResolvedDigest(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.digests = map[string]string{"cypher/app1:rev1": "ghost@sha256:deadbeef"}
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st := statusOf(got, "app1"); st.GetResolvedImage() != "ghost@sha256:deadbeef" {
		t.Fatalf("resolved_image = %q, want the observed digest", st.GetResolvedImage())
	}
}

// A built image has no registry digest, and an unresolvable one must not turn a
// healthy rollout into a failure.
func TestResolvedDigestOmittedWhenUnavailable(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.digestErr = errors.New("daemon hiccup")
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := statusOf(got, "app1")
	if st.GetState() != stateRunning {
		t.Fatalf("state = %q, want running despite the digest lookup failing", st.GetState())
	}
	if st.GetResolvedImage() != "" {
		t.Fatalf("resolved_image = %q, want empty", st.GetResolvedImage())
	}
}

// Deleting a registry-sourced app reclaims every reference to its image — the
// managed alias *and* the registry reference it arrived under. Dropping only
// the alias would untag it while the original still held the layers.
//
// Discovery is from the image, not from a container, so this holds even when
// the rollout failed before any container existed.
func TestGCReclaimsAllReferencesOfRemovedApp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.images = []Image{{
		ID:         "sha256:img",
		AppIDs:     []string{"app1"},
		References: []string{"cypher/app1:rev1", "ghost:5"},
	}}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	for _, ref := range []string{"cypher/app1:rev1", "ghost:5"} {
		if !c.removedImages[ref] {
			t.Errorf("reference %q not reclaimed (removed = %v)", ref, c.removedImages)
		}
	}
}

// An image two apps share survives while either is still desired: each app has
// its own managed alias, but the layers are one image.
func TestGCKeepsImageSharedWithDesiredApp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.localImages = map[string]bool{"ghost:5": true} // operator's reference; the rollout leaves it
	c.images = []Image{{
		ID:         "sha256:shared",
		AppIDs:     []string{"gone", "kept"},
		References: []string{"cypher/gone:rev1", "cypher/kept:rev1"},
	}}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("kept", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.removedImages) != 0 {
		t.Fatalf("removed %v — an image a desired app still runs must survive whole", c.removedImages)
	}
}

// The reference our own pull created is dropped once the managed alias holds
// the image: leaving it would keep every layer alive after the app is deleted,
// because GC only reclaims references CypherPanel created.
func TestPullDropsTheReferenceItCreated(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !c.removedImages["ghost:5"] {
		t.Fatalf("removed = %v, want the floating reference our pull created", c.removedImages)
	}
	// The marker exists only to make a *failed* drop retryable, so a successful
	// one takes it away again: a marker left behind names a reference that is
	// already gone, and GC would go on retrying it forever.
	if marker := pullMarker(t, "app1", "ghost:5"); !c.removedImages[marker] {
		t.Fatalf("removed = %v, want the spent marker cleaned up", c.removedImages)
	}
}

// A reference that already existed belongs to the operator — running an app
// from it must never untag it.
func TestPullKeepsAPreexistingReference(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.localImages = map[string]bool{"ghost:5": true} // the operator pulled it
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if c.removedImages["ghost:5"] {
		t.Fatal("untagged a reference CypherPanel did not create")
	}
	// And nothing claims it as ours: a marker is a licence for GC to remove the
	// reference later, so recording one here would only defer the same mistake.
	if marker := pullMarker(t, "app1", "ghost:5"); c.localImages[marker] {
		t.Fatal("claimed ownership of a reference the operator already had")
	}
}

// GC reclaims only managed aliases. An unrelated tag on the same image — an
// operator's, or another tool's — must survive the app's deletion.
func TestGCLeavesUnownedReferences(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.images = []Image{{
		ID:         "sha256:img",
		AppIDs:     []string{"app1"},
		References: []string{"cypher/app1:rev1"}, // engine filters unmanaged tags out
	}}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !c.removedImages["cypher/app1:rev1"] {
		t.Fatal("the managed alias was not reclaimed")
	}
	if c.removedImages["ghost:5"] {
		t.Fatal("an unowned reference was removed")
	}
}

// The digest is resolved from the alias this rollout pinned, not the source
// tag — which another app can repoint, making it name an image we never ran.
func TestDigestResolvedFromManagedAlias(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.digests = map[string]string{
		"cypher/app1:rev1": "ghost@sha256:whatweran",
		"ghost:5":          "ghost@sha256:somethingelse",
	}
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if st := statusOf(got, "app1"); st.GetResolvedImage() != "ghost@sha256:whatweran" {
		t.Fatalf("resolved_image = %q, want the digest behind the managed alias", st.GetResolvedImage())
	}
}

// pullMarker is the marker reference the driver records ownership with. Built
// through the shared helper, so the test cannot drift from what the engine
// decodes on the way back.
func pullMarker(t *testing.T, appID, source string) string {
	t.Helper()
	ref, ok := driver.PullMarkerRef(appID, source)
	if !ok {
		t.Fatalf("PullMarkerRef(%q, %q) refused", appID, source)
	}
	return ref
}

// Ownership of a pull-created reference is only knowable at pull time, so it is
// recorded before anything that could lose it — and the record goes on the
// image, not on the container, because every step from here to a serving
// revision can fail and take the container with it.
//
// This is the case that made a container label the wrong home: the rollout dies
// at create, so there is no container to carry anything, and on the next
// reconcile the leaked reference is indistinguishable from one the operator
// made. The marker is on the image, which outlived the failure.
func TestPullOwnershipSurvivesARolloutThatNeverCreatesAContainer(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.removeImageErr = map[string]error{"ghost:5": errors.New("daemon busy")}
	c.createErrForImage["cypher/app1:rev1"] = errors.New("no such network")
	d := newDriver(c, r, p)

	got, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")})
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	if st := statusOf(got, "app1"); st.GetState() != stateError {
		t.Fatalf("state = %q, want the failed create reported", st.GetState())
	}
	if len(c.containers) != 0 {
		t.Fatalf("containers = %v, want none — the create failed", c.containers)
	}
	marker := pullMarker(t, "app1", "ghost:5")
	if !slices.Contains(c.tagged, "ghost:5 -> "+marker) {
		t.Fatalf("tagged = %v, want the reference recorded as ours before the create", c.tagged)
	}
	if !c.localImages[marker] {
		t.Fatal("the marker did not survive the failed rollout")
	}

	// Next reconcile: the daemon recovers and GC finishes the job from the
	// marker alone — no container to read, no memory of the rollout.
	c.removeImageErr = nil
	c.images = []Image{{
		ID:      "sha256:img",
		AppIDs:  []string{"app1"},
		Pending: []PendingRef{{Source: "ghost:5", Marker: marker}},
	}}
	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}); err != nil {
		t.Fatalf("second converge: %v", err)
	}
	if !c.removedImages["ghost:5"] {
		t.Fatalf("removed = %v, want the leaked reference reclaimed", c.removedImages)
	}
	if !c.removedImages[marker] {
		t.Fatalf("removed = %v, want the spent marker cleaned up too", c.removedImages)
	}
}

// The retry belongs to the image, not to the application's lifecycle: it must
// run for an app that is still desired (the usual case — the rollout succeeded
// and only the tidy-up failed) and for one already deleted.
func TestPendingReferenceIsRetriedForADesiredApp(t *testing.T) {
	marker, _ := driver.PullMarkerRef("app1", "ghost:5")
	for _, tc := range []struct {
		name    string
		desired []*agentv1.AppSpec
	}{
		{"still desired", []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}},
		{"deleted", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
			c.images = []Image{{
				ID:         "sha256:img",
				AppIDs:     []string{"app1"},
				References: []string{"cypher/app1:rev1"},
				Pending:    []PendingRef{{Source: "ghost:5", Marker: marker}},
			}}
			d := newDriver(c, r, p)

			if _, err := d.Reconcile(context.Background(), tc.desired); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !c.removedImages["ghost:5"] || !c.removedImages[marker] {
				t.Fatalf("removed = %v, want the pending reference and its marker gone", c.removedImages)
			}
			if _, wanted := c.removedImages["cypher/app1:rev1"]; wanted == (len(tc.desired) > 0) {
				t.Fatalf("managed alias removal = %v for a %s app", c.removedImages, tc.name)
			}
		})
	}
}

// A marker must outlive the reference it names. Removing it first would leave
// a reference nothing can prove is ours — which is to say one GC may never
// touch again, so the leak becomes permanent rather than deferred.
func TestMarkerOutlivesAReferenceItCannotYetRemove(t *testing.T) {
	marker, _ := driver.PullMarkerRef("app1", "ghost:5")
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	c.removeImageErr = map[string]error{"ghost:5": errors.New("still busy")}
	c.images = []Image{{
		ID:      "sha256:img",
		AppIDs:  []string{"app1"},
		Pending: []PendingRef{{Source: "ghost:5", Marker: marker}},
	}}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if c.removedImages[marker] {
		t.Fatal("the marker was removed while the reference it records is still there")
	}
}

// A converged app with nothing pending still makes zero mutating calls: no
// marker means no work, so the retry cannot break converge-twice.
func TestConvergeTwiceUnaffectedByReferenceRetry(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	d := newDriver(c, r, p)
	specs := []*agentv1.AppSpec{pullSpec("app1", "rev1", "ghost:5")}

	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("first: %v", err)
	}
	baseline := c.mutations + r.mutations
	if _, err := d.Reconcile(context.Background(), specs); err != nil {
		t.Fatalf("second: %v", err)
	}
	if after := c.mutations + r.mutations; after != baseline {
		t.Fatalf("second converge mutated state: %d calls (want 0)", after-baseline)
	}
}

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
	listErr           error

	mutations int // count of state-changing calls
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		containers:        map[string]*Container{},
		unmanaged:         map[string]*Container{},
		ipByID:            map[string]string{},
		createErrForImage: map[string]error{},
	}
}

func (f *fakeClient) EnsureNetwork(_ context.Context, _ string, _ map[string]string) error {
	f.mutations++
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

func (f *fakeClient) CreateContainer(_ context.Context, spec ContainerSpec) (string, error) {
	if err := f.createErrForImage[spec.Image]; err != nil {
		return "", err
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
	f.mutations++
	if c := f.containers[id]; c != nil {
		c.Running = false
	}
	return nil
}

func (f *fakeClient) RemoveContainer(_ context.Context, id string) error {
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

type fakeRouter struct {
	routes    map[string]string // appID → upstream
	setErr    error
	mutations int
}

func newFakeRouter() *fakeRouter { return &fakeRouter{routes: map[string]string{}} }

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

func TestAbsenceRemovesApp(t *testing.T) {
	c, r, p := newFakeClient(), newFakeRouter(), &fakeProber{}
	// A non-managed look-alike the driver must never touch.
	c.unmanaged["x1"] = &Container{ID: "x1", AppID: "someone-else", Running: true}
	d := newDriver(c, r, p)

	if _, err := d.Reconcile(context.Background(), []*agentv1.AppSpec{spec("app1", "rev1", "img:rev1")}); err != nil {
		t.Fatalf("rollout: %v", err)
	}
	// Now app1 is no longer desired.
	if _, err := d.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("teardown: %v", err)
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

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
)

// mockDaemon is a stand-in Docker Engine API: tests register per-path handlers
// and the client speaks to it exactly as it would the real socket. The
// NewWithHTTP seam (engine.go) exists for precisely this.
type mockDaemon struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	body   []byte
}

func newMockDaemon(t *testing.T, routes map[string]http.HandlerFunc) *mockDaemon {
	t.Helper()
	m := &mockDaemon{}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		handler := h
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.requests = append(m.requests, recordedRequest{r.Method, r.URL.Path, r.URL.Query(), body})
			m.mu.Unlock()
			handler(w, r)
		})
	}
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	return m
}

func (m *mockDaemon) client() *Client {
	return NewWithHTTP(m.URL, m.Client())
}

func (m *mockDaemon) lastTo(t *testing.T, path string) recordedRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.requests) - 1; i >= 0; i-- {
		if m.requests[i].path == path {
			return m.requests[i]
		}
	}
	t.Fatalf("no request recorded to %s (saw %+v)", path, m.requests)
	return recordedRequest{}
}

func writeJSON(t *testing.T, w http.ResponseWriter, code int, v any) {
	t.Helper()
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// ── network ─────────────────────────────────────────────────────────────────

func TestEnsureNetworkCreatesAndConflictIsIdempotent(t *testing.T) {
	status := http.StatusCreated
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/networks/create": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) },
	})
	c := m.client()

	if err := c.EnsureNetwork(context.Background(), "cypher-env1", map[string]string{driver.LabelManaged: "docker"}); err != nil {
		t.Fatalf("create network: %v", err)
	}
	req := m.lastTo(t, "/networks/create")
	var body map[string]any
	if err := json.Unmarshal(req.body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["Name"] != "cypher-env1" || body["Driver"] != "bridge" {
		t.Fatalf("network body = %+v", body)
	}

	// A name conflict means the network already exists → treated as success.
	status = http.StatusConflict
	if err := c.EnsureNetwork(context.Background(), "cypher-env1", nil); err != nil {
		t.Fatalf("conflict should be idempotent success, got %v", err)
	}
}

// ── containers ──────────────────────────────────────────────────────────────

func TestListManagedMapsContainers(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{
					"Id":    "c1",
					"Names": []string{"/cypher-app1-rev1"},
					"State": "running",
					"Labels": map[string]string{
						driver.LabelManaged:    "docker",
						driver.LabelAppID:      "app1",
						driver.LabelRevisionID: "rev1",
					},
				},
				{"Id": "c2", "Names": []string{"/other"}, "State": "exited", "Labels": map[string]string{driver.LabelAppID: "app2"}},
			})
		},
	})

	got, err := m.client().ListManaged(context.Background())
	if err != nil {
		t.Fatalf("ListManaged: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 containers, got %d", len(got))
	}
	// The managed-label filter must be applied and "all" requested.
	req := m.lastTo(t, "/containers/json")
	if req.query.Get("all") != "true" || !strings.Contains(req.query.Get("filters"), driver.LabelManaged) {
		t.Fatalf("list query missing filter/all: %v", req.query)
	}
	var c1 docker.Container
	for _, c := range got {
		if c.ID == "c1" {
			c1 = c
		}
	}
	if c1.Name != "cypher-app1-rev1" || !c1.Running || c1.AppID != "app1" || c1.RevisionID != "rev1" {
		t.Fatalf("c1 mapped wrong: %+v", c1)
	}
}

func TestCreateContainerSendsSpec(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusCreated, map[string]string{"Id": "newid"})
		},
	})

	id, err := m.client().CreateContainer(context.Background(), docker.ContainerSpec{
		Name:    "cypher-app1-rev1",
		Image:   "cypher/app1:rev1",
		Env:     map[string]string{"K": "V"},
		Network: "cypher-env1",
		Port:    8080,
		Labels:  map[string]string{driver.LabelAppID: "app1"},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if id != "newid" {
		t.Fatalf("id = %q, want newid", id)
	}
	req := m.lastTo(t, "/containers/create")
	if req.query.Get("name") != "cypher-app1-rev1" {
		t.Fatalf("name query = %q", req.query.Get("name"))
	}
	var body map[string]any
	if err := json.Unmarshal(req.body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["Image"] != "cypher/app1:rev1" {
		t.Fatalf("image = %v", body["Image"])
	}
	env, _ := body["Env"].([]any)
	if len(env) != 1 || env[0] != "K=V" {
		t.Fatalf("env = %v, want [K=V]", env)
	}
	hc, _ := body["HostConfig"].(map[string]any)
	if hc["NetworkMode"] != "cypher-env1" {
		t.Fatalf("network mode = %v", hc["NetworkMode"])
	}
	rp, _ := hc["RestartPolicy"].(map[string]any)
	if rp["Name"] != "unless-stopped" {
		t.Fatalf("restart policy = %v, want unless-stopped", rp)
	}
}

func TestStartStopRemoveIdempotency(t *testing.T) {
	startCode, stopCode, removeCode := http.StatusNoContent, http.StatusNoContent, http.StatusNoContent
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/c1/start": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(startCode) },
		"/containers/c1/stop":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(stopCode) },
		"/containers/c1":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(removeCode) },
	})
	c := m.client()
	ctx := context.Background()

	if err := c.StartContainer(ctx, "c1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Already started → 304 is success.
	startCode = http.StatusNotModified
	if err := c.StartContainer(ctx, "c1"); err != nil {
		t.Fatalf("start (already running) should be nil, got %v", err)
	}

	if err := c.StopContainer(ctx, "c1", 7*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := m.lastTo(t, "/containers/c1/stop").query.Get("t"); got != "7" {
		t.Fatalf("stop timeout query = %q, want 7", got)
	}
	stopCode = http.StatusNotModified
	if err := c.StopContainer(ctx, "c1", time.Second); err != nil {
		t.Fatalf("stop (already stopped) should be nil, got %v", err)
	}

	if err := c.RemoveContainer(ctx, "c1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := m.lastTo(t, "/containers/c1").query.Get("force"); got != "true" {
		t.Fatalf("remove force = %q, want true", got)
	}
	// Missing container → 404 removes idempotently.
	removeCode = http.StatusNotFound
	if err := c.RemoveContainer(ctx, "c1"); err != nil {
		t.Fatalf("remove (absent) should be nil, got %v", err)
	}
}

func TestContainerIP(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/c1/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"NetworkSettings": map[string]any{
					"Networks": map[string]any{
						"cypher-env1": map[string]any{"IPAddress": "10.1.2.3"},
					},
				},
			})
		},
	})
	c := m.client()

	ip, err := c.ContainerIP(context.Background(), "c1", "cypher-env1")
	if err != nil || ip != "10.1.2.3" {
		t.Fatalf("ContainerIP = %q, %v", ip, err)
	}
	// Not attached to the requested network → error, not empty success.
	if _, err := c.ContainerIP(context.Background(), "c1", "other-net"); err == nil {
		t.Fatal("expected an error for a container absent from the network")
	}
}

// ── images ──────────────────────────────────────────────────────────────────

func TestImagesListRemoveAndHas(t *testing.T) {
	removeCode := http.StatusOK
	hasCode := http.StatusOK
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"Id": "i1", "Labels": map[string]string{driver.LabelAppID: "app1", driver.LabelRevisionID: "rev1"}},
			})
		},
		"/images/i1":                    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(removeCode) },
		"/images/cypher/app1:rev1/json": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(hasCode) },
	})
	c := m.client()
	ctx := context.Background()

	imgs, err := c.ListManagedImages(ctx)
	if err != nil || len(imgs) != 1 || imgs[0].AppID != "app1" || imgs[0].RevisionID != "rev1" {
		t.Fatalf("ListManagedImages = %+v, %v", imgs, err)
	}

	if err := c.RemoveImage(ctx, "i1"); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	removeCode = http.StatusNotFound
	if err := c.RemoveImage(ctx, "i1"); err != nil {
		t.Fatalf("RemoveImage (absent) should be nil, got %v", err)
	}

	has, err := c.HasImage(ctx, "cypher/app1:rev1")
	if err != nil || !has {
		t.Fatalf("HasImage present = %v, %v; want true", has, err)
	}
	hasCode = http.StatusNotFound
	has, err = c.HasImage(ctx, "cypher/app1:rev1")
	if err != nil || has {
		t.Fatalf("HasImage absent = %v, %v; want false", has, err)
	}
}

// ── build ───────────────────────────────────────────────────────────────────

func TestBuildImageStreamsLogs(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/build": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"stream":"Step 1/2\n"}`+"\n"+`{"stream":"Successfully built abc\n"}`+"\n")
		},
	})

	var lines []string
	err := m.client().BuildImage(context.Background(), strings.NewReader("tar"), "cypher/app1:rev1", "Dockerfile",
		map[string]string{driver.LabelAppID: "app1"}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	req := m.lastTo(t, "/build")
	if req.query.Get("t") != "cypher/app1:rev1" || req.query.Get("dockerfile") != "Dockerfile" {
		t.Fatalf("build query = %v", req.query)
	}
	if !strings.Contains(req.query.Get("labels"), "app1") {
		t.Fatalf("build labels query = %q", req.query.Get("labels"))
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "Step 1/2") {
		t.Fatalf("streamed lines = %v", lines)
	}
}

func TestBuildImageReportsStreamError(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/build": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"stream":"Step 1/2\n"}`+"\n"+`{"error":"no such file: Dockerfile"}`+"\n")
		},
	})
	err := m.client().BuildImage(context.Background(), strings.NewReader("tar"), "cypher/app1:rev1", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v, want the build stream error surfaced", err)
	}
}

// ── errors ──────────────────────────────────────────────────────────────────

func TestDaemonErrorBecomesStatusError(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/c1/start": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "daemon is unwell"})
		},
	})
	err := m.client().StartContainer(context.Background(), "c1")
	if err == nil {
		t.Fatal("expected an error from a 500")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusInternalServerError || !strings.Contains(se.Message, "daemon is unwell") {
		t.Fatalf("err = %v, want StatusError{500, ...}", err)
	}
}

// ── EnsureContainer / ConnectNetwork ────────────────────────────────────────

func TestEnsureContainerCreatesWhenAbsent(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/cypher-proxy/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
		"/containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusCreated, map[string]string{"Id": "pid"})
		},
		"/containers/pid/start":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"/images/traefik:v3/json": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, // present ⇒ no pull
	})
	cfg := RunConfig{
		Name:   "cypher-proxy",
		Image:  "traefik:v3",
		Cmd:    []string{"--providers.file.directory=/etc/traefik/apps"},
		Labels: map[string]string{driver.LabelManaged: "proxy"},
		Ports:  []PortMap{{Host: 80, Container: 80}, {Host: 443, Container: 443}},
		Mounts: []Mount{{Source: "/host/apps", Target: "/etc/traefik/apps", ReadOnly: true}},
	}
	if err := m.client().EnsureContainer(context.Background(), cfg); err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(m.lastTo(t, "/containers/create").body, &body); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if m.lastTo(t, "/containers/create").query.Get("name") != "cypher-proxy" {
		t.Fatal("create name query missing")
	}
	labels, _ := body["Labels"].(map[string]any)
	if labels[configHashLabel] != cfg.hash() {
		t.Fatalf("config-hash label = %v, want %s", labels[configHashLabel], cfg.hash())
	}
	if labels[driver.LabelManaged] != "proxy" {
		t.Fatalf("managed label = %v", labels[driver.LabelManaged])
	}
	hc, _ := body["HostConfig"].(map[string]any)
	pb, _ := hc["PortBindings"].(map[string]any)
	if _, ok := pb["80/tcp"]; !ok {
		t.Fatalf("no 80/tcp port binding: %v", pb)
	}
	binds, _ := hc["Binds"].([]any)
	if len(binds) != 1 || binds[0] != "/host/apps:/etc/traefik/apps:ro" {
		t.Fatalf("binds = %v", binds)
	}
	if _, ok := body["Cmd"]; !ok {
		t.Fatal("cmd not sent")
	}
}

func TestEnsureContainerNoOpWhenMatching(t *testing.T) {
	cfg := RunConfig{Name: "cypher-proxy", Image: "traefik:v3", Labels: map[string]string{driver.LabelManaged: "proxy"}}
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/cypher-proxy/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"State":  map[string]any{"Running": true},
				"Config": map[string]any{"Labels": map[string]string{configHashLabel: cfg.hash()}},
			})
		},
		"/containers/create": func(w http.ResponseWriter, _ *http.Request) { t.Fatal("must not create a matching, running container") },
	})
	if err := m.client().EnsureContainer(context.Background(), cfg); err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
}

func TestEnsureContainerRecreatesOnDrift(t *testing.T) {
	cfg := RunConfig{Name: "cypher-proxy", Image: "traefik:v3.1"}
	removed := false
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/cypher-proxy/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"State":  map[string]any{"Running": true},
				"Config": map[string]any{"Labels": map[string]string{configHashLabel: "stale-hash"}},
			})
		},
		"/containers/cypher-proxy": func(w http.ResponseWriter, _ *http.Request) { removed = true; w.WriteHeader(http.StatusNoContent) },
		"/containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusCreated, map[string]string{"Id": "pid"})
		},
		"/containers/pid/start":     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"/images/traefik:v3.1/json": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	})
	if err := m.client().EnsureContainer(context.Background(), cfg); err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
	if !removed {
		t.Fatal("drifted container was not removed before recreate")
	}
	m.lastTo(t, "/containers/create") // fails the test if create never happened
}

func TestConnectNetworkIdempotent(t *testing.T) {
	code := http.StatusOK
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/networks/cypher-env1/connect": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) },
	})
	c := m.client()
	if err := c.ConnectNetwork(context.Background(), "cypher-proxy", "cypher-env1"); err != nil {
		t.Fatalf("ConnectNetwork: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(m.lastTo(t, "/networks/cypher-env1/connect").body, &body)
	if body["Container"] != "cypher-proxy" {
		t.Fatalf("connect body = %v", body)
	}
	// Already connected → 403 is success.
	code = http.StatusForbidden
	if err := c.ConnectNetwork(context.Background(), "cypher-proxy", "cypher-env1"); err != nil {
		t.Fatalf("already-connected should be nil, got %v", err)
	}
}

func TestEnsureContainerPullsMissingImage(t *testing.T) {
	pulled := false
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/containers/cypher-proxy/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "absent"})
		},
		"/images/traefik:v3/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "no such image"}) // absent ⇒ pull
		},
		"/images/create": func(w http.ResponseWriter, r *http.Request) {
			pulled = true
			if r.URL.Query().Get("fromImage") != "traefik" || r.URL.Query().Get("tag") != "v3" {
				t.Errorf("pull query = %v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Downloaded"}`+"\n")
		},
		"/containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusCreated, map[string]string{"Id": "pid"})
		},
		"/containers/pid/start": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	if err := m.client().EnsureContainer(context.Background(), RunConfig{Name: "cypher-proxy", Image: "traefik:v3"}); err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
	if !pulled {
		t.Fatal("missing image was not pulled before create")
	}
}

func TestPullImageSurfacesStreamError(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/create": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
		},
	})
	err := m.client().PullImage(context.Background(), "traefik:nope")
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("err = %v, want the pull stream error surfaced", err)
	}
}

func TestEnsureContainerRefusesUnmanagedNameCollision(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		// A container with our name but WITHOUT the config-hash label — not ours.
		"/containers/cypher-proxy/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"State":  map[string]any{"Running": true},
				"Config": map[string]any{"Labels": map[string]string{"some.other": "owner"}},
			})
		},
		"/containers/cypher-proxy": func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("must not remove a container it does not own")
		},
	})
	err := m.client().EnsureContainer(context.Background(), RunConfig{Name: "cypher-proxy", Image: "traefik:v3"})
	if err == nil || !strings.Contains(err.Error(), "not agent-managed") {
		t.Fatalf("err = %v, want a refusal to replace an unmanaged container", err)
	}
}

// Package engine is the real Docker Engine API client behind the docker
// driver's Client interface (and the builder's image builds). It speaks the
// Engine's HTTP API directly over the local socket — no docker CLI, no SDK
// dependency — keeping the agent a single static binary within its footprint
// budget (vision.md non-negotiable 1).
//
// Only this package (and the proxy writer) may touch orchestrator specifics
// (ENGINEERING rule 11); everything is invoked through the interfaces the
// reconciler and builder define.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
)

// DefaultSocket is the standard Docker daemon socket.
const DefaultSocket = "/var/run/docker.sock"

// Client talks to one Docker daemon. Construct with New (socket) or
// NewWithHTTP (tests).
type Client struct {
	http *http.Client
	base string
}

// New connects over the unix socket at path (DefaultSocket when empty).
func New(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocket
	}
	return &Client{
		// The host in the URL is a placeholder; the transport dials the socket.
		base: "http://docker",
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// NewWithHTTP wires the client against an arbitrary base URL and HTTP client —
// the seam unit tests use (httptest servers speak the same API shape).
func NewWithHTTP(base string, hc *http.Client) *Client {
	return &Client{base: base, http: hc}
}

// apiError is the daemon's error body.
type apiError struct {
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("engine: building request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("engine: %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		var e apiError
		msg := resp.Status
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e) == nil && e.Message != "" {
			msg = e.Message
		}
		return nil, &StatusError{Code: resp.StatusCode, Message: fmt.Sprintf("engine: %s %s: %s", method, path, msg)}
	}
	return resp, nil
}

// StatusError carries the daemon's HTTP status for callers that branch on it.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("engine: marshaling body: %w", err)
		}
		body = strings.NewReader(string(data))
		contentType = "application/json"
	}
	resp, err := c.do(ctx, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("engine: decoding %s response: %w", path, err)
		}
		return nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func labelFilter(extra ...string) url.Values {
	labels := append([]string{driver.LabelManaged + "=docker"}, extra...)
	f, _ := json.Marshal(map[string][]string{"label": labels})
	v := url.Values{}
	v.Set("filters", string(f))
	return v
}

// ─── docker.Client implementation ───────────────────────────────────────────

// EnsureNetwork creates the named network if absent (idempotent: the daemon's
// duplicate-name conflict counts as success).
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	err := c.doJSON(ctx, http.MethodPost, "/networks/create", nil, map[string]any{
		"Name":   name,
		"Driver": "bridge",
		"Labels": labels,
	}, nil)
	var se *StatusError
	if err != nil && asStatus(err, &se) && se.Code == http.StatusConflict {
		return nil //nolint:nilerr // a name conflict means the network already exists (idempotent)
	}
	return err
}

func asStatus(err error, target **StatusError) bool {
	se, ok := err.(*StatusError)
	if ok {
		*target = se
	}
	return ok
}

type containerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

// ListManaged returns every container carrying this driver's managed label.
func (c *Client) ListManaged(ctx context.Context) ([]docker.Container, error) {
	q := labelFilter()
	q.Set("all", "true")
	var list []containerSummary
	if err := c.doJSON(ctx, http.MethodGet, "/containers/json", q, nil, &list); err != nil {
		return nil, err
	}
	out := make([]docker.Container, 0, len(list))
	for _, s := range list {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		out = append(out, docker.Container{
			ID:         s.ID,
			Name:       name,
			AppID:      s.Labels[driver.LabelAppID],
			RevisionID: s.Labels[driver.LabelRevisionID],
			Running:    s.State == "running",
		})
	}
	return out, nil
}

// CreateContainer creates (does not start) a container per the driver's spec:
// attached to the app's deterministic network, restart unless-stopped so a
// host reboot restores workloads without the plane.
func (c *Client) CreateContainer(ctx context.Context, spec docker.ContainerSpec) (string, error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	hostConfig := map[string]any{
		"NetworkMode":   spec.Network,
		"RestartPolicy": map[string]any{"Name": "unless-stopped"},
	}
	// Resource limits (noisy-neighbor control); 0 = no limit. Same mapping as
	// managed databases.
	if spec.CPULimit > 0 {
		hostConfig["NanoCpus"] = int64(spec.CPULimit * 1e9)
	}
	if spec.MemoryLimitMB > 0 {
		hostConfig["Memory"] = int64(spec.MemoryLimitMB) * 1024 * 1024
	}
	body := map[string]any{
		"Image":      spec.Image,
		"Env":        env,
		"Labels":     spec.Labels,
		"HostConfig": hostConfig,
	}
	q := url.Values{}
	q.Set("name", spec.Name)
	var resp struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/create", q, body, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// StartContainer starts a created container ("already started" is success).
func (c *Client) StartContainer(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotModified {
		return nil
	}
	return err
}

// StopContainer stops with the drain timeout ("already stopped" is success).
func (c *Client) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{}
	q.Set("t", strconv.Itoa(int(timeout.Seconds())))
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+id+"/stop", q, nil, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotModified {
		return nil
	}
	return err
}

// RemoveContainer deletes a container; force covers a container that resisted
// its stop, and a missing container removes idempotently.
func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	q := url.Values{}
	q.Set("force", "true")
	err := c.doJSON(ctx, http.MethodDelete, "/containers/"+id, q, nil, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// StreamLogs attaches to the container's log stream and copies it to out.
func (c *Client) StreamLogs(ctx context.Context, id string, out io.Writer) error {
	q := url.Values{}
	q.Set("stdout", "1")
	q.Set("stderr", "1")
	q.Set("follow", "1")
	q.Set("tail", "100")

	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", q, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	_, err = io.Copy(out, resp.Body)
	return err
}

// ContainerIP returns the container's address on the given network.
func (c *Client) ContainerIP(ctx context.Context, id, network string) (string, error) {
	var info struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &info); err != nil {
		return "", err
	}
	n, ok := info.NetworkSettings.Networks[network]
	if !ok || n.IPAddress == "" {
		return "", fmt.Errorf("engine: container %s has no address on network %s", id, network)
	}
	return n.IPAddress, nil
}

type imageSummary struct {
	ID     string            `json:"Id"`
	Labels map[string]string `json:"Labels"`
}

// ListManagedImages returns every image carrying the managed label.
func (c *Client) ListManagedImages(ctx context.Context) ([]docker.Image, error) {
	var list []imageSummary
	if err := c.doJSON(ctx, http.MethodGet, "/images/json", labelFilter(), nil, &list); err != nil {
		return nil, err
	}
	out := make([]docker.Image, 0, len(list))
	for _, s := range list {
		out = append(out, docker.Image{
			ID:         s.ID,
			AppID:      s.Labels[driver.LabelAppID],
			RevisionID: s.Labels[driver.LabelRevisionID],
		})
	}
	return out, nil
}

// RemoveImage deletes an image (missing removes idempotently; in-use is the
// caller's error to log — desired-state GC retries next reconcile). The id may
// be an image ID or a name:tag reference; the Docker API expects it literal in
// the path (a namespaced name keeps its slashes), so it is not URL-escaped.
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/images/"+id, nil, nil, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// ─── builder support ────────────────────────────────────────────────────────

// HasImage reports whether the exact image reference exists locally — the
// builder's idempotency check under work redelivery. The reference is placed
// literally in the path: the Docker API treats the slashes of a namespaced
// name (cypher/<app>:<rev>) as part of the name, so URL-escaping them would
// query for an image that can never exist and always report false.
func (c *Client) HasImage(ctx context.Context, ref string) (bool, error) {
	err := c.doJSON(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, nil)
	if err == nil {
		return true, nil
	}
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// SaveImage exports one image (manifest + layers) as a docker-save tar
// stream — the relay's source on a builder (builder-role-and-relay.md §3).
// The image reference goes into the path literally: registry namespaces
// contain "/" the daemon expects unescaped. The caller must Close the stream.
func (c *Client) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/images/"+ref+"/get", nil, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// LoadImage imports a docker-save tar into the local daemon — the relay's
// sink on a target. A truncated or corrupt tar fails here and yields no tag,
// so HasImage stays false and a retry is honest (spec §6).
func (c *Client) LoadImage(ctx context.Context, tar io.Reader) error {
	q := url.Values{}
	q.Set("quiet", "1")
	resp, err := c.do(ctx, http.MethodPost, "/images/load", q, tar, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// The daemon streams JSON-lines progress; an error mid-stream (bad tar)
	// arrives as an error record after a 200 header, same as pulls.
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("engine: reading load stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("engine: loading image: %s", line.Error)
		}
	}
}

// buildLine is one JSON-lines record of the daemon's build progress stream.
type buildLine struct {
	Stream string `json:"stream"`
	Error  string `json:"error"`
}

// BuildImage runs a daemon-side image build from the tar context, tagging the
// result and stamping the managed labels. Every progress line is handed to
// onLog (verbose by default — no hidden progress, feature-matrix row); a
// build error in the stream is returned as the error.
func (c *Client) BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, onLog func(line string)) error {
	q := url.Values{}
	q.Set("t", tag)
	if dockerfile != "" {
		q.Set("dockerfile", dockerfile)
	}
	lbl, _ := json.Marshal(labels)
	q.Set("labels", string(lbl))

	resp, err := c.do(ctx, http.MethodPost, "/build", q, buildContext, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	dec := json.NewDecoder(resp.Body)
	for {
		var line buildLine
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("engine: reading build stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("engine: build failed: %s", strings.TrimSpace(line.Error))
		}
		if line.Stream != "" && onLog != nil {
			onLog(strings.TrimRight(line.Stream, "\n"))
		}
	}
}

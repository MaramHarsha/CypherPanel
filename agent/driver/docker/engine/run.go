package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// configHashLabel records the hash of the RunConfig a managed container was
// created from, so EnsureContainer can detect config drift and recreate
// without any in-memory state (reconciler-development: truth is on the host,
// not in memory).
const configHashLabel = "cypherpanel.config-hash"

// PortMap publishes a container port to a host port (TCP).
type PortMap struct {
	Host      int
	Container int
}

// Mount is a read-only-or-not bind of a host path into the container.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// RunConfig fully describes a long-lived managed container (the Proxy today).
// Unlike the app-rollout ContainerSpec it carries published ports, bind
// mounts, and an explicit command — the things the Proxy needs and an
// Application deliberately does not.
type RunConfig struct {
	Name    string
	Image   string
	Cmd     []string
	Env     map[string]string
	Labels  map[string]string
	Ports   []PortMap
	Mounts  []Mount
	Network string // initial network (empty ⇒ default bridge); more via ConnectNetwork
}

// hash is a stable digest of the fields that, when changed, require recreating
// the container. Two RunConfigs that would produce the same container hash
// equal — the basis of idempotent EnsureContainer.
func (c RunConfig) hash() string {
	// Marshal a canonical, order-independent view.
	env := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	labels := make([]string, 0, len(c.Labels))
	for k, v := range c.Labels {
		if k == configHashLabel {
			continue
		}
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	canon := struct {
		Image   string
		Cmd     []string
		Env     []string
		Labels  []string
		Ports   []PortMap
		Mounts  []Mount
		Network string
	}{c.Image, c.Cmd, env, labels, c.Ports, c.Mounts, c.Network}
	data, _ := json.Marshal(canon)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// EnsureContainer converges one named container toward cfg: a running
// container whose recorded config hash matches is left untouched; anything
// else (absent, stopped, or drifted) is removed and recreated from cfg, then
// started, then connected to cfg.Network. Idempotent by construction — the
// config-hash label makes converging twice a no-op.
func (c *Client) EnsureContainer(ctx context.Context, cfg RunConfig) error {
	want := cfg.hash()
	existing, err := c.inspectContainer(ctx, cfg.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.running && existing.labels[configHashLabel] == want {
			return nil // already converged
		}
		if err := c.RemoveContainer(ctx, cfg.Name); err != nil {
			return fmt.Errorf("engine: replacing container %s: %w", cfg.Name, err)
		}
	}

	// The image must be present before create. A locally-built app image
	// already is (HasImage true ⇒ no-op); a registry image like the Proxy's
	// Traefik is pulled here on first use.
	if present, herr := c.HasImage(ctx, cfg.Image); herr == nil && !present {
		if err := c.PullImage(ctx, cfg.Image); err != nil {
			return fmt.Errorf("engine: pulling %s: %w", cfg.Image, err)
		}
	}

	labels := make(map[string]string, len(cfg.Labels)+1)
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	labels[configHashLabel] = want

	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range cfg.Ports {
		key := fmt.Sprintf("%d/tcp", p.Container)
		exposed[key] = struct{}{}
		bindings[key] = []map[string]string{{"HostPort": fmt.Sprintf("%d", p.Host)}}
	}
	binds := make([]string, 0, len(cfg.Mounts))
	for _, m := range cfg.Mounts {
		s := m.Source + ":" + m.Target
		if m.ReadOnly {
			s += ":ro"
		}
		binds = append(binds, s)
	}
	host := map[string]any{
		"RestartPolicy": map[string]any{"Name": "unless-stopped"},
	}
	if len(bindings) > 0 {
		host["PortBindings"] = bindings
	}
	if len(binds) > 0 {
		host["Binds"] = binds
	}
	if cfg.Network != "" {
		host["NetworkMode"] = cfg.Network
	}
	body := map[string]any{
		"Image":      cfg.Image,
		"Env":        env,
		"Labels":     labels,
		"HostConfig": host,
	}
	if len(cfg.Cmd) > 0 {
		body["Cmd"] = cfg.Cmd
	}
	if len(exposed) > 0 {
		body["ExposedPorts"] = exposed
	}

	var resp struct {
		ID string `json:"Id"`
	}
	q := url.Values{}
	q.Set("name", cfg.Name)
	if err := c.doJSON(ctx, http.MethodPost, "/containers/create", q, body, &resp); err != nil {
		return fmt.Errorf("engine: creating container %s: %w", cfg.Name, err)
	}
	if err := c.StartContainer(ctx, resp.ID); err != nil {
		return fmt.Errorf("engine: starting container %s: %w", cfg.Name, err)
	}
	return nil
}

// PullImage pulls an image reference from its registry. The daemon streams
// JSON-lines pull progress; this reads it to completion and surfaces a stream
// error (auth failure, missing tag) as the returned error.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	q := url.Values{}
	q.Set("fromImage", name)
	q.Set("tag", tag)
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("engine: reading pull stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("engine: pull failed: %s", strings.TrimSpace(line.Error))
		}
	}
}

// ConnectNetwork attaches container to network. Idempotent: a container
// already on the network (403 from the daemon) is success.
func (c *Client) ConnectNetwork(ctx context.Context, container, network string) error {
	err := c.doJSON(ctx, http.MethodPost, "/networks/"+network+"/connect", nil, map[string]any{
		"Container": container,
	}, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusForbidden {
		return nil // already connected
	}
	if err != nil {
		return fmt.Errorf("engine: connecting %s to %s: %w", container, network, err)
	}
	return nil
}

type inspectedContainer struct {
	running bool
	labels  map[string]string
}

// inspectContainer returns the container by name, or nil when it does not
// exist (a 404 is "absent", not an error).
func (c *Client) inspectContainer(ctx context.Context, name string) (*inspectedContainer, error) {
	var info struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil, &info)
	if err != nil {
		var se *StatusError
		if asStatus(err, &se) && se.Code == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &inspectedContainer{running: info.State.Running, labels: info.Config.Labels}, nil
}

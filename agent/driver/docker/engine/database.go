package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
)

// EnsureVolume creates a named volume if absent (idempotent).
func (c *Client) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	body := map[string]any{
		"Name":   name,
		"Labels": labels,
	}
	err := c.doJSON(ctx, http.MethodPost, "/volumes/create", nil, body, nil)
	var se *StatusError
	if err != nil && asStatus(err, &se) && se.Code == http.StatusConflict {
		return nil
	}
	return err
}

// RemoveVolume removes a named volume.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/volumes/"+name, nil, nil, nil)
	var se *StatusError
	if asStatus(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

func dbFilter() url.Values {
	f, _ := json.Marshal(map[string][]string{
		"label": {docker.LabelDbManaged + "=docker"},
	})
	v := url.Values{}
	v.Set("filters", string(f))
	return v
}

// ListManagedDb returns database containers.
func (c *Client) ListManagedDb(ctx context.Context) ([]docker.DbContainer, error) {
	q := dbFilter()
	q.Set("all", "true")
	var list []containerSummary
	if err := c.doJSON(ctx, http.MethodGet, "/containers/json", q, nil, &list); err != nil {
		return nil, err
	}

	out := make([]docker.DbContainer, 0, len(list))
	for _, s := range list {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}

		// Inspect container to get its health check status.
		var inspect struct {
			State struct {
				Running bool `json:"Running"`
				Health  *struct {
					Status string `json:"Status"` // "starting", "healthy", "unhealthy"
				} `json:"Health"`
			} `json:"State"`
		}
		healthy := false
		if s.State == "running" {
			if err := c.doJSON(ctx, http.MethodGet, "/containers/"+s.ID+"/json", nil, nil, &inspect); err == nil {
				if inspect.State.Health == nil || inspect.State.Health.Status == "healthy" {
					healthy = true
				}
			}
		}

		out = append(out, docker.DbContainer{
			ID:         s.ID,
			Name:       name,
			DbID:       s.Labels[docker.LabelDbID],
			RevisionID: s.Labels[docker.LabelDbRevisionID],
			Running:    s.State == "running",
			Healthy:    healthy,
		})
	}
	return out, nil
}

// CreateDbContainer creates a database container with binds, health checks,
// and limits.
func (c *Client) CreateDbContainer(ctx context.Context, spec docker.DbContainerSpec) (string, error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	hostConfig := map[string]any{
		"NetworkMode":   spec.Network,
		"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		"Binds":         []string{spec.VolumeName + ":" + spec.DataPath},
	}

	if spec.CPULimit > 0 {
		hostConfig["NanoCpus"] = int64(spec.CPULimit * 1e9)
	}
	if spec.MemoryLimitMB > 0 {
		hostConfig["Memory"] = int64(spec.MemoryLimitMB) * 1024 * 1024
	}

	// Exposed ports mapping.
	var containerPort string
	img := strings.ToLower(spec.Image)
	if strings.Contains(img, "postgres") {
		containerPort = "5432/tcp"
	} else if strings.Contains(img, "mysql") || strings.Contains(img, "mariadb") {
		containerPort = "3306/tcp"
	} else if strings.Contains(img, "mongo") {
		containerPort = "27017/tcp"
	} else if strings.Contains(img, "redis") || strings.Contains(img, "valkey") {
		containerPort = "6379/tcp"
	}

	config := map[string]any{
		"Image":  spec.Image,
		"Env":    env,
		"Labels": spec.Labels,
	}

	if spec.ExposePort > 0 && containerPort != "" {
		config["ExposedPorts"] = map[string]any{
			containerPort: map[string]any{},
		}
		hostConfig["PortBindings"] = map[string]any{
			containerPort: []map[string]any{
				{"HostPort": strconv.Itoa(int(spec.ExposePort))},
			},
		}
	}

	if spec.HealthCmd != "" {
		config["Healthcheck"] = map[string]any{
			"Test":     []string{"CMD-SHELL", spec.HealthCmd},
			"Interval": 10 * 1e9, // 10s in ns
			"Timeout":  5 * 1e9,  // 5s in ns
			"Retries":  3,
		}
	}

	config["HostConfig"] = hostConfig

	q := url.Values{}
	q.Set("name", spec.Name)
	var resp struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/create", q, config, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// WaitHealthy blocks until the container's HEALTHCHECK reports healthy,
// or the context expires.
func (c *Client) WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var inspect struct {
				State struct {
					Running bool `json:"Running"`
					Health  *struct {
						Status string `json:"Status"` // "starting", "healthy", "unhealthy"
					} `json:"Health"`
				} `json:"State"`
			}
			if err := c.doJSON(ctx, http.MethodGet, "/containers/"+containerID+"/json", nil, nil, &inspect); err != nil {
				return err
			}
			if !inspect.State.Running {
				return fmt.Errorf("engine: container stopped running")
			}
			if inspect.State.Health == nil || inspect.State.Health.Status == "healthy" {
				return nil // healthy or no health check configured
			}
			if inspect.State.Health.Status == "unhealthy" {
				return fmt.Errorf("engine: container health check failed")
			}
		}
	}
}

// Exec runs a command inside a running container and returns its stdout.
func (c *Client) Exec(ctx context.Context, containerID string, cmd []string) (io.Reader, error) {
	// Step 1: Create exec config.
	execConfig := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
	}
	var resp struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/"+containerID+"/exec", nil, execConfig, &resp); err != nil {
		return nil, fmt.Errorf("exec: create failed: %w", err)
	}

	// Step 2: Start exec.
	startConfig := map[string]any{
		"Detach": false,
		"Tty":    false,
	}
	bodyData, err := json.Marshal(startConfig)
	if err != nil {
		return nil, fmt.Errorf("exec: marshal start config: %w", err)
	}
	httpResp, err := c.do(ctx, http.MethodPost, "/exec/"+resp.ID+"/start", nil, strings.NewReader(string(bodyData)), "application/json")
	if err != nil {
		return nil, fmt.Errorf("exec: start failed: %w", err)
	}

	// Read everything into a buffer to ensure exec completes and returns stdout safely.
	buf := new(strings.Builder)
	_, err = io.Copy(buf, httpResp.Body)
	_ = httpResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("exec: copying response: %w", err)
	}

	return strings.NewReader(buf.String()), nil
}

package prober

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Prober implements docker.HealthProber. It gates a rollout by an internal
// probe of the container (agent → container), never through any public route,
// so the health gate is orthogonal to whether the app is HTTP-exposed.
type Prober struct {
	client *http.Client
}

// New constructs a Prober that uses a default HTTP client.
func New() *Prober {
	return &Prober{
		client: &http.Client{},
	}
}

// Probe blocks until the upstream is serving, per the health-check kind:
//
//   - "http" (default): a GET at hc.Path returns 200-299.
//   - "tcp": a TCP connection to the container port succeeds.
//   - "none": returns immediately — liveness only, for raw UDP services with no
//     readiness signal (the reconciler has already confirmed the container ran).
//
// Returns an error if retries are exhausted or the context is canceled.
func (p *Prober) Probe(ctx context.Context, upstream string, hc *agentv1.HealthCheck) error {
	path := "/"
	interval := 1 * time.Second
	timeout := 1 * time.Second
	retries := uint32(3)
	kind := "http"

	if hc != nil {
		if hc.Path != "" {
			path = hc.Path
			if path[0] != '/' {
				path = "/" + path
			}
		}
		if hc.IntervalSeconds > 0 {
			interval = time.Duration(hc.IntervalSeconds) * time.Second
		}
		if hc.TimeoutSeconds > 0 {
			timeout = time.Duration(hc.TimeoutSeconds) * time.Second
		}
		if hc.Retries > 0 {
			retries = hc.Retries
		}
		if hc.Kind != "" {
			kind = hc.Kind
		}
	}

	if kind == "none" {
		return nil
	}

	url := "http://" + upstream + path
	single := func(ctx context.Context) error { return p.singleProbe(ctx, url, timeout) }
	if kind == "tcp" {
		single = func(ctx context.Context) error { return tcpProbe(ctx, upstream, timeout) }
	}

	var lastErr error
	for i := uint32(0); i <= retries; i++ {
		if err := single(ctx); err != nil {
			lastErr = err
		} else {
			return nil
		}

		if i < retries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}

	return fmt.Errorf("health check failed after %d retries: %w", retries, lastErr)
}

// tcpProbe succeeds if a TCP connection to upstream can be established within
// timeout — the readiness signal for a raw TCP service.
func tcpProbe(ctx context.Context, upstream string, timeout time.Duration) error {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", upstream)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (p *Prober) singleProbe(ctx context.Context, url string, timeout time.Duration) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return nil
}

package prober

import (
	"context"
	"fmt"
	"net/http"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Prober implements docker.HealthProber using actual HTTP GET requests.
type Prober struct {
	client *http.Client
}

// New constructs a Prober that uses a default HTTP client.
func New() *Prober {
	return &Prober{
		client: &http.Client{},
	}
}

// Probe blocks until the upstream responds successfully (HTTP 200-299) to a GET
// at hc.Path, or returns an error if all retries are exhausted or context is
// canceled.
func (p *Prober) Probe(ctx context.Context, upstream string, hc *agentv1.HealthCheck) error {
	path := "/"
	interval := 1 * time.Second
	timeout := 1 * time.Second
	retries := uint32(3)

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
	}

	url := "http://" + upstream + path
	var lastErr error

	for i := uint32(0); i <= retries; i++ {
		if err := p.singleProbe(ctx, url, timeout); err != nil {
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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return nil
}

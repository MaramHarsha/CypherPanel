package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
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

	return fmt.Errorf("health check failed after %d retries: %s", retries, diagnose(upstream, path, kind, lastErr))
}

// diagnose turns the transport's own words into the sentence that names the
// fix.
//
// This exists because of a real hour lost on a real panel. A Next.js
// application listened on 3000, the panel was configured for 8080, and the
// rollout failed with:
//
//	health check failed after 3 retries: Get "http://172.19.0.2:8080/":
//	dial tcp 172.19.0.2:8080: connect: connection refused
//
// Every fact needed to explain that is in the message, and none of the meaning
// is: an operator has to already know that the agent probes the container
// directly, that 8080 came from the application's own configuration, and that a
// refused connection means nothing is bound rather than something rejecting us.
// A message that requires you to know the answer is not a message
// (ui-principles §11).
//
// The three shapes are distinguished because they have three different fixes,
// and a message that offers all three offers none.
func diagnose(upstream, path, kind string, err error) string {
	if err == nil {
		return "unknown"
	}
	detail := err.Error()
	port := upstream
	if _, p, splitErr := net.SplitHostPort(upstream); splitErr == nil {
		port = p
	}
	switch {
	case refused(err):
		// Nothing is bound. The container is running — the reconciler started
		// it and would have said so otherwise — so this is almost always a port
		// the application does not listen on.
		return fmt.Sprintf(
			"nothing is listening on port %s inside the container. That is the port this application is"+
				" configured to serve on — if it listens on a different one, set that port on the"+
				" application's settings. (%s)", port, detail)
	case timedOut(err):
		// Something IS bound and did not answer in time. Raising the timeout is
		// the fix when the app is slow to become ready; it is not the fix when
		// the app is wedged, so the message says which question to ask.
		return fmt.Sprintf(
			"port %s accepted the connection but did not answer within the health check's timeout."+
				" Either the application is still starting — raise the timeout or the retries on its settings —"+
				" or it is not serving %s. (%s)", port, path, detail)
	case kind == "http":
		// It answered, with the wrong thing. That is a path question, never a
		// port one.
		return fmt.Sprintf(
			"the application answered on port %s but not with a success at %s."+
				" Set the health check path to something it serves, or leave it at / and make / answer. (%s)",
			port, path, detail)
	}
	return detail
}

// refused covers both "nothing is bound" shapes: a closed port answers RST, and
// a container whose address moved answers no route to host. Both unwrap to a
// syscall errno through *url.Error → *net.OpError → *os.SyscallError, so one
// errors.Is walks the whole chain.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH)
}

// timedOut is the deadline this probe set, not the caller's cancellation: the
// caller's cancellation never reaches here, because Probe returns ctx.Err()
// directly for that case.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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

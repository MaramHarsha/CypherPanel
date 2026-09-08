package prober_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/prober"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

func TestProber(t *testing.T) {
	failures := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if failures < 2 {
			failures++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Parse host:port from srv.URL
	upstream := srv.URL[len("http://"):]

	p := prober.New()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hc := &agentv1.HealthCheck{
		Path:            "/healthz",
		IntervalSeconds: 1, // 1 second
		Retries:         3,
		TimeoutSeconds:  1,
	}

	if err := p.Probe(ctx, upstream, hc); err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	// Test failure exhaust retries
	failures = 0
	hc.Retries = 1
	hc.Path = "/notfound"
	if err := p.Probe(ctx, upstream, hc); err == nil {
		t.Fatal("Probe expected to fail, but succeeded")
	}
}

func TestProberTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	p := prober.New()
	hc := &agentv1.HealthCheck{Kind: "tcp", TimeoutSeconds: 1, Retries: 1}
	if err := p.Probe(context.Background(), ln.Addr().String(), hc); err != nil {
		t.Fatalf("tcp probe of a live listener failed: %v", err)
	}

	// A closed port fails the tcp gate.
	ln.Close()
	if err := p.Probe(context.Background(), ln.Addr().String(), hc); err == nil {
		t.Fatal("tcp probe of a closed port should fail")
	}
}

func TestProberNoneIsLivenessOnly(t *testing.T) {
	p := prober.New()
	// "none" returns immediately even for an address nothing is listening on.
	hc := &agentv1.HealthCheck{Kind: "none", Retries: 1}
	if err := p.Probe(context.Background(), "127.0.0.1:1", hc); err != nil {
		t.Fatalf("none probe should always pass: %v", err)
	}
}

// ─── The message an operator actually reads ─────────────────────────────────
//
// A real panel lost an hour to "connect: connection refused" on a Next.js app
// that listened on 3000 while the panel was configured for 8080. Every fact
// needed to explain it was in that message and none of the meaning was, so
// these assert the meaning rather than the words.

// Nothing bound: the port is the answer, and the message says so.
func TestProbeNamesThePortWhenNothingIsListening(t *testing.T) {
	// A port nobody is on: bind one, learn its number, release it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	_ = l.Close()

	err = prober.New().Probe(context.Background(), addr, &agentv1.HealthCheck{Retries: 0, TimeoutSeconds: 1})
	if err == nil {
		t.Fatal("probe of a closed port succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nothing is listening on port "+port) {
		t.Errorf("message does not say nothing is listening on %s:\n  %s", port, msg)
	}
	if !strings.Contains(msg, "set that port on the application's settings") {
		t.Errorf("message does not name the fix:\n  %s", msg)
	}
	// The transport's own words survive: they are what a bug report quotes.
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("message dropped the underlying error:\n  %s", msg)
	}
}

// Bound, and answering the wrong thing: that is a PATH question, and offering
// the port fix here would send the operator to change the one setting that is
// already right.
func TestProbeNamesThePathWhenTheStatusIsWrong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := prober.New().Probe(context.Background(), strings.TrimPrefix(srv.URL, "http://"),
		&agentv1.HealthCheck{Retries: 0, TimeoutSeconds: 2, Path: "/healthz"})
	if err == nil {
		t.Fatal("probe of a 404 succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "/healthz") {
		t.Errorf("message does not name the path it asked for:\n  %s", msg)
	}
	if strings.Contains(msg, "nothing is listening") {
		t.Errorf("a 404 is not a port problem, but the message says it is:\n  %s", msg)
	}
}

// Bound, and too slow: raising the timeout is a candidate fix and the port is
// not, so the message must not name the port as the thing to change.
//
// The delay is the health check's own timeout, exceeded — NOT the caller's
// context, which Probe returns verbatim by design so a shutdown does not read
// as an application fault.
func TestProbeSaysSlowRatherThanClosedOnATimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := prober.New().Probe(context.Background(), strings.TrimPrefix(srv.URL, "http://"),
		&agentv1.HealthCheck{Retries: 1, TimeoutSeconds: 1})
	if err == nil {
		t.Fatal("probe with a 1s budget against a 1.5s handler succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "did not answer within") {
		t.Errorf("a timeout does not read as a timeout:\n  %s", msg)
	}
	if strings.Contains(msg, "nothing is listening") {
		t.Errorf("a slow answer is not a closed port:\n  %s", msg)
	}
}

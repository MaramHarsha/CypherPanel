package prober_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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

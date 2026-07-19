// Command cypher-agent is the CypherPanel data-plane agent: a single static
// binary per server. It enrolls once with a join token to obtain its mTLS
// identity (ADR-002), then maintains an outbound-only connection to the control
// plane and publishes heartbeats. Phase 1 does the handshake and heartbeat;
// workload reconciliation arrives with the driver interface in Phase 2.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/cypherpanel/agent/builder"
	"github.com/MaramHarsha/cypherpanel/agent/conn"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/prober"
	"github.com/MaramHarsha/cypherpanel/agent/heartbeat"
	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/agent/proxy"
	"github.com/MaramHarsha/cypherpanel/agent/stream"
	"github.com/MaramHarsha/cypherpanel/agent/worker"
)

// version is stamped at build time via -ldflags; "dev" in local builds.
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "enroll":
		err = runEnroll(os.Args[2:], log)
	case "run":
		err = runAgent(os.Args[2:], log)
	case "version":
		fmt.Println("cypher-agent", version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("cypher-agent failed", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `cypher-agent — CypherPanel data-plane agent

Usage:
  cypher-agent enroll --plane HOST:PORT --token TOKEN --ca-file PATH [--state-dir DIR] [--hostname NAME]
  cypher-agent run    [--state-dir DIR] [--driver docker] [--heartbeat 30s]
  cypher-agent version`)
}

func runEnroll(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	plane := fs.String("plane", "", "control-plane enrollment address (host:port)")
	token := fs.String("token", "", "single-use join token")
	caFile := fs.String("ca-file", "", "path to the control-plane CA certificate (PEM)")
	stateDir := fs.String("state-dir", defaultStateDir(), "directory for the agent identity")
	hostname := fs.String("hostname", defaultHostname(), "hostname to report to the plane")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *plane == "" || *token == "" || *caFile == "" {
		return fmt.Errorf("--plane, --token and --ca-file are required")
	}
	caPEM, err := os.ReadFile(*caFile)
	if err != nil {
		return fmt.Errorf("reading ca file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := conn.Enroll(ctx, *plane, *token, caPEM, *hostname, version)
	if err != nil {
		return err
	}
	if err := identity.Save(*stateDir, id); err != nil {
		return err
	}
	log.Info("enrolled successfully", "server_id", id.ServerID, "state_dir", *stateDir)
	return nil
}

func runAgent(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory for the agent identity")
	driver := fs.String("driver", "docker", "orchestrator driver to report")
	interval := fs.Duration("heartbeat", 30*time.Second, "heartbeat interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.Load(*stateDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	nc, err := conn.ConnectBus(id, log)
	if err != nil {
		return err
	}
	defer nc.Close()
	// The client reconnects forever through outages, so CLOSED is terminal: it
	// only happens when the plane actively refuses this identity (revocation).
	// Exit rather than linger publishing into a dead connection; the operator's
	// init system decides whether to restart.
	nc.SetClosedHandler(func(*nats.Conn) { cancel() })
	log.Info("agent running", "server_id", id.ServerID, "driver", *driver, "version", version)

	go heartbeat.NewPublisher(nc, id.ServerID, version, *driver, *interval, log).Run(ctx)

	if *driver == "docker" {
		eng := engine.New("")

		// The Proxy owns this host directory (routing-and-tls.md §5): fragments
		// in <Dir>/apps, static config + acme.json alongside. It is bind-mounted
		// into the managed Traefik container, so it must be an absolute path.
		proxyDir := "/etc/cypherpanel/traefik"
		if d := os.Getenv("CYPHER_TRAEFIK_DIR"); d != "" {
			proxyDir = d
		}
		prx := proxy.New(proxy.Config{
			Dir:          proxyDir,
			Image:        envOr("CYPHER_PROXY_IMAGE", "traefik:v3.3"),
			ACMEEmail:    os.Getenv("CYPHER_ACME_EMAIL"),    // empty ⇒ HTTP-only, no cert resolver
			ACMECAServer: os.Getenv("CYPHER_ACME_CASERVER"), // empty ⇒ Let's Encrypt production
			Engine:       eng,
			Log:          log,
		})
		prb := prober.New()

		bldDir := filepath.Join(*stateDir, "builds")
		bld := builder.NewBuilder(eng, bldDir)

		strm := stream.NewStreamer(nc, eng, id.ServerID)
		go strm.Start(ctx, 10*time.Second)

		drv := docker.New(eng, prx, prb, log)
		wbus, err := worker.NewNATSBus(nc, id.ServerID)
		if err != nil {
			return err
		}
		w := worker.New(wbus, id.ServerID, drv, bld, eng, log)
		errCh := make(chan error, 1)
		go func() {
			if err := w.Run(ctx); err != nil {
				log.Error("worker failed", "error", err)
				errCh <- err
			}
		}()

		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
		}
	} else {
		<-ctx.Done()
	}

	if nc.IsClosed() {
		if lastErr := nc.LastError(); lastErr != nil {
			return fmt.Errorf("bus connection closed permanently (identity revoked?): %w", lastErr)
		}
		return fmt.Errorf("bus connection closed permanently (identity revoked?)")
	}
	if err := nc.Drain(); err != nil {
		log.Warn("draining bus connection", "error", err)
	}
	log.Info("agent stopped")
	return nil
}

func defaultStateDir() string {
	if d := os.Getenv("CYPHER_STATE_DIR"); d != "" {
		return d
	}
	return "/var/lib/cypher-agent"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

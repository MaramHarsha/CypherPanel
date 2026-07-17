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
	"syscall"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/conn"
	"github.com/MaramHarsha/cypherpanel/agent/heartbeat"
	"github.com/MaramHarsha/cypherpanel/agent/identity"
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

	nc, err := conn.ConnectBus(id, log)
	if err != nil {
		return err
	}
	defer nc.Close()
	log.Info("agent running", "server_id", id.ServerID, "driver", *driver, "version", version)

	heartbeat.NewPublisher(nc, id.ServerID, version, *driver, *interval, log).Run(ctx)

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

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

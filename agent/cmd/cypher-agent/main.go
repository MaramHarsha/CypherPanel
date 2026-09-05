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
	"github.com/MaramHarsha/cypherpanel/agent/cron"
	"github.com/MaramHarsha/cypherpanel/agent/driver"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/engine"
	"github.com/MaramHarsha/cypherpanel/agent/driver/docker/prober"
	"github.com/MaramHarsha/cypherpanel/agent/heartbeat"
	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/agent/proxy"
	"github.com/MaramHarsha/cypherpanel/agent/relay"
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
  cypher-agent run    [--state-dir DIR] [--driver docker] [--role all|builder|worker] [--heartbeat 30s]
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
	drvName := fs.String("driver", "docker", "orchestrator driver to report")
	role := fs.String("role", "all", "agent role: all (build + run), builder (build only), worker (run only)")
	interval := fs.Duration("heartbeat", 30*time.Second, "heartbeat interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *role {
	case "all", "builder", "worker":
	default:
		return fmt.Errorf("--role must be all, builder or worker (got %q)", *role)
	}
	if *interval <= 0 {
		// The publisher hands this straight to time.NewTicker, which panics
		// on non-positive intervals.
		return fmt.Errorf("--heartbeat must be a positive duration (got %s)", *interval)
	}
	id, err := identity.Load(*stateDir)
	if err != nil {
		return err
	}
	// The Keeper owns the identity from here on: every TLS handshake reads its
	// certificate from it, so a renewal takes effect without reconnecting
	// anything (agent-identity-and-tls.md §3).
	keeper, err := identity.NewKeeper(*stateDir, id)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	nc, err := conn.ConnectBus(keeper, log)
	if err != nil {
		return err
	}
	defer nc.Close()
	// The client reconnects forever through outages, so CLOSED is terminal: it
	// only happens when the plane actively refuses this identity (revocation).
	// Exit rather than linger publishing into a dead connection; the operator's
	// init system decides whether to restart.
	nc.SetClosedHandler(func(*nats.Conn) { cancel() })
	log.Info("agent running",
		"server_id", id.ServerID,
		"driver", *drvName,
		"role", *role,
		"version", version,
		"cert_not_after", keeper.NotAfter(),
	)

	// Certificate renewal: an owned goroutine that re-signs this agent's
	// identity two thirds of the way through its life, over the mTLS channel
	// it already holds (ADR-002; threat-model §5.2). Without it every agent
	// goes dark at CYPHERD_AGENT_CERT_TTL and has to be re-enrolled by hand.
	//
	// It needs the plane's gRPC address, which identities enrolled before
	// plane_addr existed do not carry; CYPHER_PLANE_ADDR supplies it. Without
	// either, the agent says so loudly rather than failing silently in two
	// months.
	if planeAddr := envOr("CYPHER_PLANE_ADDR", id.PlaneAddr); planeAddr != "" {
		renewClient, err := conn.NewRenewer(planeAddr, keeper, version)
		if err != nil {
			return err
		}
		defer func() { _ = renewClient.Close() }()
		renewer := identity.NewRenewer(keeper, renewClient, identity.SystemClock{}, log)
		go renewer.Run(ctx)
	} else {
		log.Warn("no plane address on this identity: certificate renewal is disabled and this agent will stop connecting when its certificate expires; set CYPHER_PLANE_ADDR",
			"server_id", id.ServerID, "cert_not_after", keeper.NotAfter())
	}

	// Shared with the docker driver below so a Proxy that cannot bind :80
	// turns the server amber instead of leaving it green while every routed
	// deploy fails.
	health := &heartbeat.Health{}
	go heartbeat.NewPublisher(nc, id.ServerID, version, *drvName, *role, *interval, health, log).Run(ctx)

	if *drvName == "docker" {
		eng := engine.New("")

		// The image relay dials the plane address the agent enrolled against
		// (builder-role-and-relay.md §3). Identities saved before plane_addr
		// existed have none — CYPHER_PLANE_ADDR overrides; without either,
		// relay work items fail with the remedy in the detail.
		var imgRelay worker.ImageRelay
		if planeAddr := envOr("CYPHER_PLANE_ADDR", id.PlaneAddr); planeAddr != "" {
			rc, err := relay.New(planeAddr, keeper, eng, log)
			if err != nil {
				return err
			}
			imgRelay = rc
		}

		// Runtime stack (docker reconciler + Proxy + runtime-log streamer):
		// everything except builder-role agents, which run nothing and must
		// not bind :80/:443 (builder-role-and-relay.md §1).
		var drv driver.Reconciler
		var dockerDrv *docker.Driver // concrete handle for the cron executor
		var proxyTLS worker.ProxyTLS // the Proxy's ACME sink, on app-role nodes
		if *role != "builder" {
			// The Proxy owns this host directory (routing-and-tls.md §5):
			// fragments in <Dir>/apps, static config + acme.json alongside.
			// It is bind-mounted into the managed Traefik container, so it
			// must be an absolute path.
			proxyDir := "/etc/cypherpanel/traefik"
			if d := os.Getenv("CYPHER_TRAEFIK_DIR"); d != "" {
				proxyDir = d
			}
			prx := proxy.New(proxy.Config{
				Dir:   proxyDir,
				Image: envOr("CYPHER_PROXY_IMAGE", "traefik:v3.3"),
				// Host-local overrides. Empty is the normal case: the panel's
				// ACME account arrives in desired state and is applied through
				// SetACME below (agent-identity-and-tls.md §4).
				ACMEEmail:    os.Getenv("CYPHER_ACME_EMAIL"),
				ACMECAServer: os.Getenv("CYPHER_ACME_CASERVER"),
				Engine:       eng,
				Log:          log,
			})
			proxyTLS = prx
			prb := prober.New()
			strm := stream.NewStreamer(nc, eng, id.ServerID)
			go strm.Start(ctx, 10*time.Second)
			dockerDrv = docker.New(eng, prx, prb, log)
			dockerDrv.OnProxyHealth(health.Set)
			drv = dockerDrv
		}

		// Managed databases run on the same nodes as apps (worker/all roles),
		// converged by their own reconciler over the same engine client
		// (managed-databases.md §6). Builder-role agents run nothing.
		var dbRec driver.DbReconciler
		var backupRunner worker.BackupRunner
		if *role != "builder" {
			dbRec = docker.NewDatabaseReconciler(eng, log)
			// Backup/restore executor over the same engine client + a tiny
			// SigV4 S3 client (managed-databases.md §7).
			backupRunner = docker.NewBackupExecutor(eng, docker.NewS3Client(), log)
		}

		// Builds: everything except worker-role agents, which never receive
		// build work (routing) and keep no build toolchain warm.
		var bld *builder.Builder
		if *role != "worker" {
			bld = builder.NewBuilder(eng, filepath.Join(*stateDir, "builds"))
		}

		wbus, err := worker.NewNATSBus(nc, id.ServerID)
		if err != nil {
			return err
		}
		w := worker.New(wbus, id.ServerID, drv, dbRec, backupRunner, bld, imgRelay, log)
		if proxyTLS != nil {
			w.SetProxyTLS(proxyTLS)
		}
		if dockerDrv != nil {
			// Scheduled tasks run only on app-role nodes (they need a container
			// to exec into) — scheduled-tasks.md §5, ADR-011.
			w.SetCron(cron.New(dockerDrv, wbus, id.ServerID, log))
		}
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

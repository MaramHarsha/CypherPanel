// Command cypherd is the CypherPanel control plane: a single static binary that
// serves the REST API + web console, runs the embedded NATS JetStream bus, and
// hosts the gRPC enrollment endpoint. Its only external dependency is
// PostgreSQL (ADR-001). It stores no server credentials (ADR-002).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"

	grpcapi "github.com/MaramHarsha/cypherpanel/core/api/grpc"
	"github.com/MaramHarsha/cypherpanel/core/api/rest"
	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/bus"
	"github.com/MaramHarsha/cypherpanel/core/config"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	"github.com/MaramHarsha/cypherpanel/core/guard"
	"github.com/MaramHarsha/cypherpanel/core/identity"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/status"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// version is stamped at build time via -ldflags; "dev" in local builds.
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("cypherd exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("starting cypherd", "version", version, "public_host", cfg.PublicHost)

	// Self-protection: refuse to boot into a nearly-full disk rather than run
	// until Postgres can no longer write (threat-model §8 req 10).
	if err := guard.CheckDiskHeadroom(".", cfg.MinDiskFree, guard.FreeBytes); err != nil {
		if errors.Is(err, guard.ErrUnsupported) {
			log.Warn("disk headroom check unsupported on this platform", "error", err)
		} else {
			return err
		}
	}

	// Migrate before opening the pool so the schema is present.
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	box, err := secret.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}

	ca, err := identity.LoadOrCreateCA(ctx, st, box, time.Now())
	if err != nil {
		return err
	}

	if err := bootstrapAdmin(ctx, st, cfg, log); err != nil {
		return err
	}

	// Issue the plane's own server certificate for its TLS listeners.
	dnsNames, ips := planeSANs(cfg.PublicHost)
	planeCert, planeKey, err := ca.IssueServerCert(dnsNames, ips, 365*24*time.Hour, time.Now())
	if err != nil {
		return fmt.Errorf("issuing plane server cert: %w", err)
	}

	// The file-backed WORK stream needs a writable data directory. Create it
	// up front so a missing/unwritable path fails with a clear message here
	// rather than surfacing later as an opaque "NATS server not ready".
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir %q (set CYPHERD_DATA_DIR to a writable path): %w", cfg.DataDir, err)
	}

	// Embedded NATS JetStream bus (mTLS, per-agent authz).
	busTLS, err := pki.ServerTLSConfig(planeCert, planeKey, ca.CertPEM())
	if err != nil {
		return fmt.Errorf("building bus TLS: %w", err)
	}
	b, err := bus.Start(ctx, bus.Options{
		ListenAddr:          cfg.NATSAddr,
		TLSConfig:           busTLS,
		Authorizer:          st,
		Log:                 log.With("component", "bus"),
		StoreDir:            cfg.DataDir,
		RuntimeLogsMaxAge:   cfg.RuntimeLogsMaxAge,
		RuntimeLogsMaxBytes: int64(cfg.RuntimeLogsMaxBytes),
	})
	if err != nil {
		return err
	}
	defer b.Close()
	log.Info("event bus ready", "addr", cfg.NATSAddr, "advertised", cfg.AdvertisedNATSURL())

	// Heartbeats → observed state.
	recorder := status.NewRecorder(st, log)
	consume, err := b.ConsumeHeartbeats(ctx, func(data []byte) {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		recorder.Record(c, data)
	})
	if err != nil {
		return err
	}
	defer consume.Stop()

	// Stale sweeper.
	var wg sync.WaitGroup
	sweeper := status.NewSweeper(st, cfg.HeartbeatStale, cfg.SweepInterval, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweeper.Run(ctx)
	}()

	// Services.
	enrollSvc := enroll.NewService(st, ca, cfg.AgentCertTTL, cfg.AdvertisedNATSURL())
	serverSvc := servers.NewService(st, b, cfg.JoinTokenTTL, log)
	projectSvc := projects.NewService(st)
	appSvc := applications.NewService(st, box)
	deployKeySvc := deploykeys.NewService(st, box)
	authr := auth.NewAuthenticator(st, auth.NewLimiter(5, 15*time.Minute), cfg.SessionTTL)

	// Deploy pipeline: the scheduler publishes work items and advances
	// deployments from the agents' observed reports (ADR-005).
	sched := scheduler.New(st, b, box, log)
	if err := sched.Recover(ctx); err != nil {
		return err
	}
	if err := b.RespondDesiredState(func(serverID string) ([]byte, error) {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return sched.DesiredStateFor(c, serverID)
	}); err != nil {
		return err
	}
	deployConsume, err := b.ConsumeDeployEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DeployEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling deploy event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleDeployEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer deployConsume.Stop()
	statusConsume, err := b.ConsumeAppStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.AppStatus
		if err := proto.Unmarshal(data, &st); err != nil {
			log.Error("unmarshaling app status", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleAppStatus(c, serverID, &st)
	})
	if err != nil {
		return err
	}
	defer statusConsume.Stop()

	// gRPC enrollment endpoint (server-auth TLS; join-token gated).
	grpcSrv, err := startEnrollmentServer(cfg, planeCert, planeKey, enrollSvc, log)
	if err != nil {
		return err
	}

	// REST API + console.
	api := rest.New(rest.Deps{
		Auth:         authr,
		Servers:      serverSvc,
		Projects:     projectSvc,
		Applications: appSvc,
		DeployKeys:   deployKeySvc,
		Scheduler:    sched,
		Deployments:  st,
		Opener:       box,
		Pinger:       st,
		CACertPEM:    ca.CertPEM(),
		EnrollAddr:   cfg.AdvertisedEnrollAddr(),
		NATSURL:      cfg.AdvertisedNATSURL(),
		Logs:         b,
		ConsoleURL:   cfg.AdvertisedConsoleURL(),
		Log:          log,
	})
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 2)
	go func() {
		log.Info("http api + console listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server: %w", err)
		}
	}()

	// Wait for a signal or a fatal serve error.
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serveErr:
		log.Error("server failed", "error", err)
		stop() // trigger the same graceful path
	}

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http graceful shutdown", "error", err)
	}
	grpcSrv.GracefulStop()
	wg.Wait()
	log.Info("cypherd stopped")
	return nil
}

func startEnrollmentServer(cfg config.Config, certPEM, keyPEM []byte, svc *enroll.Service, log *slog.Logger) (*grpc.Server, error) {
	tlsCfg, err := pki.ServerBootstrapTLSConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("building enrollment TLS: %w", err)
	}
	lis, err := net.Listen("tcp", cfg.EnrollAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on enroll addr: %w", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	agentv1.RegisterEnrollmentServiceServer(srv, grpcapi.NewEnrollmentServer(svc, log))
	go func() {
		log.Info("enrollment endpoint listening", "addr", cfg.EnrollAddr, "advertised", cfg.AdvertisedEnrollAddr())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error("enrollment server", "error", err)
		}
	}()
	return srv, nil
}

func bootstrapAdmin(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) error {
	if cfg.AdminEmail == "" {
		n, err := st.CountUsers(ctx)
		if err != nil {
			return fmt.Errorf("counting users: %w", err)
		}
		if n == 0 {
			log.Warn("no admin account and none configured — set CYPHERD_ADMIN_EMAIL/PASSWORD; login is unavailable until an account exists")
		}
		return nil
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if n > 0 {
		return nil // already bootstrapped; do not overwrite
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, ids.New(ids.PrefixUser), cfg.AdminEmail, hash, "owner"); err != nil {
		return fmt.Errorf("creating admin user: %w", err)
	}
	log.Info("bootstrapped admin account", "email", cfg.AdminEmail)
	return nil
}

// planeSANs derives the certificate SANs for the plane's server cert from the
// configured public host, always including loopback so local tooling works.
func planeSANs(publicHost string) (dnsNames []string, ips []net.IP) {
	dnsNames = []string{"localhost"}
	ips = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if publicHost == "" || publicHost == "localhost" {
		return dnsNames, ips
	}
	if ip := net.ParseIP(publicHost); ip != nil {
		ips = append(ips, ip)
	} else {
		dnsNames = append(dnsNames, publicHost)
	}
	return dnsNames, ips
}

// Command cypherd is the CypherPanel control plane: a single static binary that
// serves the REST API + web console, runs the embedded NATS JetStream bus, and
// hosts the gRPC enrollment endpoint. Its only external dependency is
// PostgreSQL (ADR-001). It stores no server credentials (ADR-002).
package main

import (
	"context"
	"crypto/tls"
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
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	"github.com/MaramHarsha/cypherpanel/core/guard"
	"github.com/MaramHarsha/cypherpanel/core/identity"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/onboarding"
	"github.com/MaramHarsha/cypherpanel/core/previews"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/relay"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/status"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/teams"
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

	onboardSvc := onboarding.New(st)
	if err := bootstrapAdmin(ctx, onboardSvc, cfg, log); err != nil {
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
	teamSvc := teams.NewService(st)
	authr := auth.NewAuthenticator(st, box, auth.NewLimiter(5, 15*time.Minute), cfg.SessionTTL)

	// Deploy pipeline: the scheduler publishes work items and advances
	// deployments from the agents' observed reports (ADR-005).
	sched := scheduler.New(st, b, box, log)

	// Notifications: terminal deploy/backup outcomes fan out to a project's
	// configured channels (notifications.md). Delivery is best-effort and
	// detached, so it never blocks the pipeline.
	notifySvc := notify.NewService(st, box)
	notifyMgr := notify.New(st, box, log)
	sched.SetNotifier(notifyMgr)

	// Scheduled tasks: cron declared on an app, run by the agent in the app's
	// own container (scheduled-tasks.md, ADR-011). CRUD converges via the
	// scheduler; run observations flow back on state.<server>.task.
	scheduledTaskSvc := scheduledtasks.NewService(st, sched, log)

	dbSvc := databases.NewService(st, box, sched)
	backupTargetSvc := databases.NewBackupTargetService(st, box)
	backupScheduleSvc := databases.NewBackupScheduleService(st)

	// Preview environments: PR events (via the app webhook) spawn/destroy
	// templated child environments; a sweeper reclaims any past their TTL
	// (preview-environments.md).
	previewMgr := previews.New(st, appSvc, sched, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		previewMgr.RunSweeper(ctx, cfg.SweepInterval)
	}()

	// Scheduled database backups: the plane-side cron evaluator fires each
	// enabled schedule when its next run is due (managed-databases.md §7).
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.RunBackupSweeper(ctx, cfg.SweepInterval)
	}()

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

	dbStatusConsume, err := b.ConsumeDbStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.DbStatus
		if err := proto.Unmarshal(data, &st); err != nil {
			log.Error("unmarshaling db status", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleDbStatus(c, serverID, &st)
	})
	if err != nil {
		return err
	}
	defer dbStatusConsume.Stop()

	dbBackupConsume, err := b.ConsumeDbBackupEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbBackupEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db backup event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbBackupEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbBackupConsume.Stop()

	dbRestoreConsume, err := b.ConsumeDbRestoreEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbRestoreEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db restore event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbRestoreEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbRestoreConsume.Stop()

	dbBackupPruneConsume, err := b.ConsumeDbBackupPruneEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbBackupPruneEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db backup prune event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbBackupPruneEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbBackupPruneConsume.Stop()

	taskRunConsume, err := b.ConsumeTaskRuns(ctx, func(serverID string, data []byte) {
		var ev agentv1.ScheduledTaskRun
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling scheduled task run", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleScheduledTaskRun(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer taskRunConsume.Stop()

	// gRPC enrollment + image-relay endpoint. Enroll needs no client cert
	// (first contact, join-token gated); the relay RPCs require a verified
	// agent certificate on the same listener (builder-role-and-relay.md §3).
	relaySrv := grpcapi.NewRelayServer(relay.New(0), st, log)
	grpcSrv, err := startEnrollmentServer(cfg, planeCert, planeKey, ca.CertPEM(), enrollSvc, relaySrv, log)
	if err != nil {
		return err
	}

	// REST API + console.
	api := rest.New(rest.Deps{
		Auth:            authr,
		Onboarding:      onboardSvc,
		Servers:         serverSvc,
		Projects:        projectSvc,
		Applications:    appSvc,
		DeployKeys:      deployKeySvc,
		Databases:       dbSvc,
		BackupTargets:   backupTargetSvc,
		BackupSchedules: backupScheduleSvc,
		Backups:         sched,
		Previews:        previewMgr,
		Notifiers:       notifySvc,
		NotifyDelivery:  notifyMgr,
		ScheduledTasks:  scheduledTaskSvc,
		Teams:           teamSvc,
		Scheduler:       sched,
		Deployments:     st,
		Opener:          box,
		Pinger:          st,
		CACertPEM:       ca.CertPEM(),
		EnrollAddr:      cfg.AdvertisedEnrollAddr(),
		NATSURL:         cfg.AdvertisedNATSURL(),
		Logs:            b,
		ConsoleURL:      cfg.AdvertisedConsoleURL(),
		Log:             log,
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

func startEnrollmentServer(cfg config.Config, certPEM, keyPEM, caPEM []byte, svc *enroll.Service, relaySrv *grpcapi.RelayServer, log *slog.Logger) (*grpc.Server, error) {
	tlsCfg, err := pki.ServerBootstrapTLSConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("building enrollment TLS: %w", err)
	}
	// Client certificates are optional at the TLS layer (Enroll is the first
	// contact that issues them) but verified against the agent CA when
	// presented; the relay handlers then require a verified identity.
	pool, err := pki.CertPool(caPEM)
	if err != nil {
		return nil, fmt.Errorf("building agent CA pool: %w", err)
	}
	tlsCfg.ClientCAs = pool
	tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	lis, err := net.Listen("tcp", cfg.EnrollAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on enroll addr: %w", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	agentv1.RegisterEnrollmentServiceServer(srv, grpcapi.NewEnrollmentServer(svc, log))
	agentv1.RegisterImageRelayServiceServer(srv, relaySrv)
	go func() {
		log.Info("enrollment endpoint listening", "addr", cfg.EnrollAddr, "advertised", cfg.AdvertisedEnrollAddr())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error("enrollment server", "error", err)
		}
	}()
	return srv, nil
}

// bootstrapAdmin creates the first owner from CYPHERD_ADMIN_EMAIL/PASSWORD when
// the panel has none. It shares CreateFirstOwner with the in-browser setup path
// (onboarding), so an env-var boot and a browser setup produce the same shape.
// When no admin is configured, the operator completes setup in the browser on
// first visit (first-run-setup.md) — no longer a dead-end login screen.
func bootstrapAdmin(ctx context.Context, onb *onboarding.Service, cfg config.Config, log *slog.Logger) error {
	needs, err := onb.NeedsSetup(ctx)
	if err != nil {
		return err
	}
	if !needs {
		return nil // already bootstrapped; do not overwrite
	}
	if cfg.AdminEmail == "" {
		log.Info("no admin account yet — complete first-run setup in the browser, or set CYPHERD_ADMIN_EMAIL/PASSWORD")
		return nil
	}
	if _, err := onb.CreateFirstOwner(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return fmt.Errorf("bootstrapping admin: %w", err)
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

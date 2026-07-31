// cypher-core is the CypherPanel control-plane API server.
//
//	cypher-core                                  run the API + agent gRPC servers
//	cypher-core create-admin --username U --password P --email E
//	cypher-core pki init [--dir DIR]
//	cypher-core pki issue-server --name core [--dns H1,H2] [--ip I1,I2] [--dir DIR]
//	cypher-core pki issue-agent  --name agent-host1 [--dir DIR]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/agentrpc"
	"github.com/MaramHarsha/CypherPanel/internal/api"
	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/backupsched"
	"github.com/MaramHarsha/CypherPanel/internal/config"
	"github.com/MaramHarsha/CypherPanel/internal/dns"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/pki"
	"github.com/MaramHarsha/CypherPanel/internal/secretcrypt"
	"github.com/MaramHarsha/CypherPanel/internal/sslrenew"
	"github.com/MaramHarsha/CypherPanel/internal/store"
	"github.com/MaramHarsha/CypherPanel/internal/version"
	"github.com/MaramHarsha/CypherPanel/internal/webhooks"
)

// @title           CypherPanel API
// @version         0.1.0
// @description     Control-plane REST API for CypherPanel, the open-source cPanel/WHM alternative.
// @license.name    Apache-2.0
// @license.url     https://www.apache.org/licenses/LICENSE-2.0
// @BasePath        /api/v1
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     Type "Bearer" followed by a space and the access token.
func main() {
	if err := run(); err != nil {
		slog.Error("cypher-core failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// PKI subcommands need no datastores; handle them before connecting.
	if len(os.Args) > 1 && os.Args[1] == "pki" {
		return pkiCommand(os.Args[2:])
	}
	// `version` prints the build's version identity without touching the DB.
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("cypher-core %s (minimum supported agent %s)\n", version.Core, version.MinAgent)
		return nil
	}

	cfg, err := config.LoadCore()
	if err != nil {
		return err
	}
	if cfg.Env != config.EnvProduction {
		slog.Warn("running in development mode; do not expose this instance publicly")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres (is the dev stack up? `docker compose up -d`): %w", err)
	}
	// Record the running Core version so upgrades/rollbacks have a definitive
	// "from" version (best-effort; a fresh DB before migrations won't have the
	// table yet, which is fine).
	if _, err := pool.Exec(ctx, `
		INSERT INTO system_version (id, version) VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET version = $1, updated_at = now()`, version.Core); err != nil {
		slog.Debug("recording system version (table may not exist yet)", "error", err)
	}

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parsing redis url: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("pinging redis: %w", err)
	}

	users := store.NewUsers(pool)
	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, rdb)
	auditLog := audit.NewLogger(pool)

	// Subcommands
	if len(os.Args) > 1 && os.Args[1] == "create-admin" {
		return createAdmin(ctx, users, auditLog, os.Args[2:])
	}

	natsOpts := []nats.Option{nats.Name("cypher-core")}
	if cfg.NATSCreds != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(cfg.NATSCreds))
	}
	nc, err := nats.Connect(cfg.NATSURL, natsOpts...)
	if err != nil {
		return fmt.Errorf("connecting to NATS: %w", err)
	}
	defer nc.Drain()
	publisher, err := jobs.NewPublisher(ctx, nc)
	if err != nil {
		return err
	}

	eventBus, err := events.NewBus(ctx, nc)
	if err != nil {
		return err
	}
	// Example in-process subscriber: log every domain event. Real reactions
	// (webhooks, plugins) subscribe here too once those land.
	eventBus.Subscribe(events.SubjectWildcard, func(_ context.Context, e events.Event) {
		slog.Info("domain event", "subject", e.Subject, "aggregate_id", e.AggregateID)
	})

	tasksStore := store.NewTasks(pool)
	serversStore := store.NewServers(pool)
	accountsStore := store.NewAccounts(pool)
	packagesStore := store.NewPackages(pool)
	resellersStore := store.NewResellers(pool)
	databasesStore := store.NewDatabases(pool)
	ftpStore := store.NewFTPAccounts(pool)
	mailStore := store.NewMailAccounts(pool)
	backupsStore := store.NewBackups(pool)
	webhooksStore := store.NewWebhooks(pool)

	crypt, err := secretcrypt.New(cfg.DBEncryptionKey)
	if err != nil {
		return fmt.Errorf("initializing secret cipher: %w", err)
	}

	// DNS is optional: only wired when a PowerDNS API is configured. When
	// secondaries are set, writes fan out to them (cluster sync).
	var dnsProvider dns.Provider
	if cfg.PDNSAPIURL != "" {
		primary := dns.NewPowerDNS(cfg.PDNSAPIURL, cfg.PDNSAPIKey)
		var secondaries []dns.Provider
		for _, entry := range cfg.DNSSecondaries {
			url, key, found := strings.Cut(entry, "|")
			if !found {
				return fmt.Errorf("CYPHER_DNS_SECONDARIES entries must be 'url|apikey': %q", entry)
			}
			secondaries = append(secondaries, dns.NewPowerDNS(url, key))
		}
		if len(secondaries) > 0 {
			dnsProvider = dns.NewClustered(primary, secondaries)
			slog.Info("DNS management enabled (PowerDNS cluster)", "secondaries", len(secondaries))
		} else {
			dnsProvider = primary
			slog.Info("DNS management enabled (PowerDNS)")
		}
	}
	// Shared by the HTTP surface and the scheduler so an on-demand backup and a
	// scheduled one take exactly the same dispatch path and record identical
	// history.
	backupsHandler := &api.BackupsHandler{
		Accounts: accountsStore, Backups: backupsStore, Databases: databasesStore,
		Tasks: tasksStore, Publisher: publisher, Crypt: crypt, Audit: auditLog,
	}

	router := api.NewRouter(api.Deps{
		Config:   cfg,
		Redis:    rdb,
		Tokens:   tokens,
		Auth:     &api.AuthHandler{Users: users, Tokens: tokens, Audit: auditLog},
		Tasks:    &api.TasksHandler{Tasks: tasksStore, Publisher: publisher, Audit: auditLog},
		Servers: &api.ServersHandler{
			Servers: serversStore, Accounts: accountsStore,
			Tasks: tasksStore, Publisher: publisher, Audit: auditLog,
			PHPVersions: cfg.PHPVersions,
		},
		Packages: &api.PackagesHandler{Packages: packagesStore, Events: eventBus, Audit: auditLog},
		Accounts: &api.AccountsHandler{
			Accounts: accountsStore, Packages: packagesStore, Resellers: resellersStore,
			Tasks: tasksStore, Publisher: publisher, Events: eventBus, Audit: auditLog,
			PHPVersion: cfg.DefaultPHPVersion, PHPVersions: cfg.PHPVersions,
		},
		Plugins:   &api.PluginsHandler{Plugins: store.NewPlugins(pool), Audit: auditLog},
		Resellers: &api.ResellersHandler{Resellers: resellersStore, Events: eventBus, Audit: auditLog},
		Databases: &api.DatabasesHandler{
			Accounts: accountsStore, Databases: databasesStore, Packages: packagesStore,
			Tasks: tasksStore, Publisher: publisher, Audit: auditLog, Crypt: crypt,
			AdminerURL: cfg.AdminerURL,
		},
		FTP: &api.FTPHandler{
			Accounts: accountsStore, FTP: ftpStore,
			Tasks: tasksStore, Publisher: publisher, Audit: auditLog, Crypt: crypt,
		},
		FileManager: &api.FileManagerHandler{Accounts: accountsStore, Packages: packagesStore, NC: nc, Audit: auditLog},
		DNS: &api.DNSHandler{
			Accounts: accountsStore, Servers: serversStore, Provider: dnsProvider,
			Nameservers: cfg.DNSNameservers, Audit: auditLog,
		},
		Metrics: &api.MetricsHandler{
			Servers: serversStore, Accounts: accountsStore,
			Databases: databasesStore, FTP: ftpStore,
		},
		AuditLog: &api.AuditHandler{Audit: auditLog},
		Cron:     &api.CronHandler{Accounts: accountsStore, NC: nc, Audit: auditLog},
		Mail: &api.MailHandler{
			Accounts: accountsStore, Mail: mailStore, Packages: packagesStore, Servers: serversStore,
			Tasks: tasksStore, Publisher: publisher, Audit: auditLog,
			DNS: dnsProvider, Nameservers: cfg.DNSNameservers,
		},
		Terminal: &api.TerminalHandler{Accounts: accountsStore, Tokens: tokens, NC: nc},
		Backups:  backupsHandler,
		Webhooks: &api.WebhooksHandler{Webhooks: webhooksStore, Crypt: crypt, Audit: auditLog},
	})

	// SSL auto-renewal: re-dispatches the idempotent ssl.issue task for certs
	// nearing expiry. Runs for the lifetime of the process; stops on ctx cancel.
	renewer := &sslrenew.Scheduler{
		Accounts:  accountsStore,
		Threshold: cfg.SSLRenewThreshold,
		Interval:  cfg.SSLRenewInterval,
		Dispatch: func(ctx context.Context, a store.Account) error {
			payload, err := json.Marshal(jobs.SSLIssuePayload{
				Username: a.SystemUsername, Domain: a.PrimaryDomain, Email: a.Email,
			})
			if err != nil {
				return err
			}
			// createdBy "" → NULL: this is a system-initiated task, no actor.
			task, err := tasksStore.Create(ctx, a.ServerID, jobs.TypeSSLIssue, payload, "", a.ID)
			if err != nil {
				return err
			}
			return publisher.Publish(ctx, jobs.Task{
				ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
			})
		},
	}
	go renewer.Run(ctx)

	// Scheduled backups: sweep hourly and dispatch for destinations whose
	// daily/weekly cadence has come due.
	backupScheduler := &backupsched.Scheduler{
		Destinations: backupsStore,
		Accounts:     accountsStore,
		Interval:     time.Hour,
		Dispatch: func(ctx context.Context, a store.Account, dest store.BackupDestination) error {
			// actorID "" → NULL: system-initiated, no human actor.
			_, err := backupsHandler.Dispatch(ctx, &a, &dest, "scheduled", "")
			return err
		},
	}
	go backupScheduler.Run(ctx)

	// Webhooks: one durable JetStream consumer turns domain events into
	// delivery rows, and a worker owns retry/dead-lettering from those rows.
	webhookDispatcher := &webhooks.Dispatcher{
		Store: webhooksStore,
		Crypt: crypt,
	}
	go func() {
		if err := webhookDispatcher.Consume(ctx, nc); err != nil {
			slog.Error("webhook event consumer stopped", "error", err)
		}
	}()
	go webhookDispatcher.Run(ctx)

	// Audit retention: prune rows older than the policy once a day (append-only
	// log; retention is age-based pruning, never in-place edits).
	if cfg.AuditRetentionDays > 0 {
		go func() {
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			prune := func() {
				cutoff := time.Now().AddDate(0, 0, -cfg.AuditRetentionDays)
				if n, err := auditLog.Prune(ctx, cutoff); err != nil {
					slog.Error("audit retention prune", "error", err)
				} else if n > 0 {
					slog.Info("audit retention pruned old entries", "removed", n, "older_than_days", cfg.AuditRetentionDays)
				}
			}
			prune() // once on boot
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					prune()
				}
			}
		}()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	grpcSrv, err := newAgentGRPCServer(cfg)
	if err != nil {
		return err
	}
	agentv1.RegisterAgentServiceServer(grpcSrv, &agentrpc.Server{
		Servers:   serversStore,
		Tasks:     tasksStore,
		Accounts:  accountsStore,
		Databases: databasesStore,
		FTP:       ftpStore,
		Mail:      mailStore,
		Backups:   backupsStore,
		DNS:       dnsProvider,
		Events:    eventBus,
		Audit:     auditLog,
		Crypt:     crypt,
	})

	errCh := make(chan error, 2)
	go func() {
		slog.Info("cypher-core HTTP listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		lis, err := net.Listen("tcp", cfg.GRPCAddr)
		if err != nil {
			errCh <- fmt.Errorf("gRPC listen on %s: %w", cfg.GRPCAddr, err)
			return
		}
		slog.Info("cypher-core agent gRPC listening", "addr", cfg.GRPCAddr, "mtls", cfg.GRPCTLSCert != "")
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		grpcSrv.GracefulStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// newAgentGRPCServer builds the agent-facing gRPC server: mTLS whenever cert
// material is configured (always, in production — config enforces it);
// plaintext only in bare development setups.
func newAgentGRPCServer(cfg config.Core) (*grpc.Server, error) {
	if cfg.GRPCTLSCert == "" {
		slog.Warn("agent gRPC running WITHOUT mTLS — development only")
		return grpc.NewServer(), nil
	}
	tlsCfg, err := pki.ServerTLS(cfg.GRPCTLSCert, cfg.GRPCTLSKey, cfg.GRPCTLSClientCA)
	if err != nil {
		return nil, err
	}
	return grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg))), nil
}

func pkiCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("pki: subcommand required (init | issue-server | issue-agent)")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("pki "+sub, flag.ExitOnError)
	dir := fs.String("dir", "pki", "PKI directory")
	name := fs.String("name", "", "certificate name / CommonName")
	dns := fs.String("dns", "localhost", "comma-separated DNS SANs (issue-server)")
	ips := fs.String("ip", "127.0.0.1", "comma-separated IP SANs (issue-server)")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch sub {
	case "init":
		if err := pki.InitCA(*dir); err != nil {
			return err
		}
		fmt.Printf("CA created in %s (ca.crt / ca.key). Keep ca.key on the control plane only.\n", *dir)
		return nil
	case "issue-server", "issue-agent":
		if *name == "" {
			return fmt.Errorf("pki %s: --name is required", sub)
		}
		opts := pki.IssueOptions{Name: *name, IsServer: sub == "issue-server"}
		if opts.IsServer {
			opts.DNSNames = strings.Split(*dns, ",")
			opts.IPs = strings.Split(*ips, ",")
		}
		if err := pki.Issue(*dir, opts); err != nil {
			return err
		}
		fmt.Printf("Issued %s.crt / %s.key in %s\n", *name, *name, *dir)
		return nil
	default:
		return fmt.Errorf("pki: unknown subcommand %q", sub)
	}
}

func createAdmin(ctx context.Context, users *store.Users, auditLog *audit.Logger, args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	username := fs.String("username", "", "admin username (required)")
	password := fs.String("password", "", "admin password (required)")
	email := fs.String("email", "", "admin email (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" || *email == "" {
		return errors.New("create-admin: --username, --password and --email are all required")
	}
	if len(*password) < 12 {
		return errors.New("create-admin: password must be at least 12 characters")
	}

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	user, err := users.Create(ctx, *username, *email, hash, string(auth.RoleRootAdmin))
	if err != nil {
		return fmt.Errorf("creating admin user: %w", err)
	}

	_ = auditLog.Record(ctx, audit.Entry{
		ActorRole: "system", Action: "user.create_admin", TargetType: "user", TargetID: user.ID,
	})
	fmt.Printf("Root admin %q created (id %s)\n", user.Username, user.ID)
	return nil
}

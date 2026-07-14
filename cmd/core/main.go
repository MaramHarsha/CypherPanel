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
	"github.com/MaramHarsha/CypherPanel/internal/config"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/pki"
	"github.com/MaramHarsha/CypherPanel/internal/store"
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

	tasksStore := store.NewTasks(pool)
	router := api.NewRouter(api.Deps{
		Config: cfg,
		Tokens: tokens,
		Auth:   &api.AuthHandler{Users: users, Tokens: tokens, Audit: auditLog},
		Tasks:  &api.TasksHandler{Tasks: tasksStore, Publisher: publisher, Audit: auditLog},
	})

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
		Servers: store.NewServers(pool),
		Tasks:   tasksStore,
		Audit:   auditLog,
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

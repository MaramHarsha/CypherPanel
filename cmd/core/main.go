// cypher-core is the CypherPanel control-plane API server.
//
//	cypher-core                                  run the API server
//	cypher-core create-admin --username U --password P --email E
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/MaramHarsha/CypherPanel/internal/api"
	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/config"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("cypher-core failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
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

	router := api.NewRouter(api.Deps{
		Config: cfg,
		Tokens: tokens,
		Auth:   &api.AuthHandler{Users: users, Tokens: tokens, Audit: auditLog},
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("cypher-core listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
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

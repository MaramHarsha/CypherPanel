// cypher-agent is the per-server CypherPanel daemon. It detects the distro
// layout, connects to CypherCore over mTLS gRPC, and executes provisioning
// tasks. Target: <50MB idle RSS, no per-hosted-account processes (plan.md
// Section 8).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MaramHarsha/CypherPanel/internal/config"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
)

func main() {
	if err := run(); err != nil {
		slog.Error("cypher-agent failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return err
	}

	family, layout := paths.Detect()
	slog.Info("cypher-agent starting",
		"env", cfg.Env,
		"core_addr", cfg.CoreAddr,
		"distro_family", string(family),
		"nginx_conf_dir", layout.NginxConfDir,
	)
	if family == paths.FamilyUnknown && cfg.Env == config.EnvProduction {
		slog.Warn("unrecognized distro family; using generic path layout — set CYPHER_PATH_* overrides if needed")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// TODO(phase-1): dial CypherCore with mTLS credentials from cfg and run
	// Register/Heartbeat against the AgentService contract in proto/agent/v1.
	// Generated client lands via `make proto` once the gRPC server side exists.

	slog.Info("cypher-agent ready; waiting for shutdown signal")
	<-ctx.Done()
	slog.Info("cypher-agent shutting down")
	return nil
}

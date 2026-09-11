package main

// `cypherd join-local` — the root one-shot that installs an agent on the
// panel's own host (local-server.md §3).
//
// It is started by cypherd-localjoin.path when the plane places a request, and
// it exists for the same reason the upgrade helper does: the plane runs with
// DynamicUser and ProtectSystem=strict, so it cannot write /usr/local/bin, a
// systemd unit, or call systemctl — deliberately. Its entire power is to ask.

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/MaramHarsha/cypherpanel/core/api/rest"
	"github.com/MaramHarsha/cypherpanel/core/config"
	"github.com/MaramHarsha/cypherpanel/core/localjoin"
)

func runLocalJoinHelper(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.UpgradeDir == "" {
		return fmt.Errorf("cypherd join-local: no handoff directory is configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return localjoin.Run(ctx, localjoin.Options{
		Dir: localjoin.Dir(cfg.UpgradeDir),
		// The panel's own embedded installer, not a fetch: this installs on the
		// machine it is already running on, so reaching over the network for
		// bytes it already holds would add a failure mode for nothing.
		Script: rest.AgentInstallScript(),
		Log:    log.With("component", "localjoin"),
	})
}

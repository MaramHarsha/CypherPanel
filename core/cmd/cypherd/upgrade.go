package main

// The two non-serving entry points: the root upgrade helper, and a standalone
// migrate (panel-updates.md §§3, 6).

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/config"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/updates"
	"github.com/MaramHarsha/cypherpanel/core/upgrade"
)

// runMigrate applies migrations and exits. It takes the same DATABASE_URL the
// service does, from the same root-owned env file, so the helper never has to
// carry a credential in a request file.
func runMigrate(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.Info("migrations applied")
	return nil
}

// runUpgradeHelper consumes one request and performs it.
//
// It runs as ROOT, and everything it is allowed to touch is enumerated here:
// the handoff directory, the installed binary, the snapshot directory, and
// systemctl. It opens the env file READ-ONLY through config.Load and never
// writes anywhere under /etc — that file holds the master key, and destroying
// it is exactly what the reference platform's update button did.
func runUpgradeHelper(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir := upgrade.Dir(cfg.UpgradeDir)
	if dir == "" {
		return fmt.Errorf("cypherd upgrade: no handoff directory is configured")
	}

	// A generous ceiling rather than none: an upgrade that hangs forever holds
	// the panel read-only until the lock expires, and the lock's own TTL is
	// what this is sized against.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()

	checker, err := updates.New(updates.Options{
		Current: updates.RuntimeInfo(version, commit, buildDate),
		FeedURL: cfg.UpdateFeedURL,
		// The helper never polls a feed; it only needs the hardened fetch.
		Enabled: false,
		Log:     log.With("component", "upgrade-fetch"),
	})
	if err != nil {
		return err
	}

	helper := upgrade.NewHelper(upgrade.HelperOptions{
		Dir:         dir,
		BinaryPath:  cfg.UpgradeBinaryPath,
		Unit:        cfg.UpgradeUnit,
		BaseURL:     cfg.ReleaseBaseURL,
		ReadyURL:    cfg.UpgradeReadyURL,
		FromVersion: version,
		SnapshotDir: cfg.SnapshotDir(),
		DatabaseURL: cfg.DatabaseURL,
		Probation:   cfg.UpgradeProbation,
		Fetcher:     checker,
		Log:         log.With("component", "upgrade-helper"),
	})
	if err := helper.Run(ctx); err != nil {
		return err
	}

	// Prune what retention no longer keeps, from the same process that created
	// it — the plane sweeps too, and both are idempotent.
	if removed, err := upgrade.PruneSnapshots(cfg.SnapshotDir(), cfg.SnapshotRetention, time.Now()); err == nil && len(removed) > 0 {
		log.Info("upgrade: pruned expired snapshots", "count", len(removed))
	}
	_ = os.Stdout.Sync()
	return nil
}

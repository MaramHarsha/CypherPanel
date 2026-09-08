package main

// `cypherd restore` — recovering a control plane on a host that has never seen
// this panel before (plane-disaster-recovery.md §6).
//
// It runs with the plane STOPPED, and there is no restore button in the panel:
// a panel that can rewind itself from its own UI is a panel one compromised
// session can rewind.
//
// The identity is read from a FILE, never from an argv value — argv is
// world-readable through ps, which is the same rule the pack builder applies to
// its environment.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MaramHarsha/cypherpanel/core/config"
	"github.com/MaramHarsha/cypherpanel/core/planebackup"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/s3"
)

func runRestore(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := fs.String("from", "", "the snapshot: an s3:// URL or a local path")
	identityPath := fs.String("identity", "", "file holding the Recovery Key (never pass the key on the command line)")
	endpoint := fs.String("endpoint", "", "S3 endpoint, for an s3:// source")
	region := fs.String("region", "us-east-1", "S3 region")
	force := fs.Bool("force", false, "allow a non-empty target database")
	databaseURL := fs.String("database-url", "", "target database; defaults to CYPHERD_DATABASE_URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *identityPath == "" {
		fs.Usage()
		return fmt.Errorf("restore: --from and --identity are both required")
	}

	identity, err := os.ReadFile(*identityPath)
	if err != nil {
		return fmt.Errorf("restore: reading the recovery key: %w", err)
	}

	target := *databaseURL
	if target == "" {
		cfg, cerr := config.Load()
		if cerr != nil {
			return fmt.Errorf("restore: no --database-url and the environment is incomplete: %w", cerr)
		}
		target = cfg.DatabaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	body, err := fetchSnapshot(ctx, *from, *endpoint, *region)
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, target)
	if err != nil {
		return fmt.Errorf("restore: connecting to the target database: %w", err)
	}
	defer pool.Close()

	migrator, err := store.NewRestoreMigrator(target)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	store.SetTableSorter(planebackup.SortTables)
	res, err := planebackup.Restore(ctx, strings.NewReader(string(body)), planebackup.RestoreOptions{
		DB: backupSurface{store.NewBackupConn(pool)}, Enc: planebackup.AgeCrypto{}, Migrate: migrator,
		Identity: string(identity), Force: *force, Source: *from,
		Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err != nil {
		return err
	}

	fmt.Printf("\nRestored %d tables from a snapshot taken %s.\n",
		res.Loaded, res.Manifest.CreatedAt.Format(time.RFC3339))
	fmt.Println("Agents re-adopt on their next heartbeat — desired state reconverges, and nothing redeploys.")
	fmt.Println("Your applications never stopped running.")
	if res.MasterKey != "" {
		// Printed rather than written: this process must not decide where an
		// operator's master key lives, and a file it wrote would be one more
		// copy nobody remembers making.
		fmt.Println("\nAdd this line to /etc/cypherpanel/cypherd.env before starting the panel:")
		fmt.Printf("\nCYPHERD_MASTER_KEY=%s\n\n", strings.TrimSpace(res.MasterKey))
	}
	log.Info("restore complete", "tables", res.Loaded)
	return nil
}

// fetchSnapshot takes an s3:// URL or a local path.
//
// The local path is NOT a convenience: the S3 credentials for the target are
// inside the archive, so an operator who no longer has them separately has to
// fetch the object with their provider's own tooling first — and the command
// must accept the result.
func fetchSnapshot(ctx context.Context, from, endpoint, region string) ([]byte, error) {
	if !strings.HasPrefix(from, "s3://") {
		body, err := os.ReadFile(from)
		if err != nil {
			return nil, fmt.Errorf("restore: reading the snapshot: %w", err)
		}
		return body, nil
	}
	rest := strings.TrimPrefix(from, "s3://")
	bucket, key, found := strings.Cut(rest, "/")
	if !found || bucket == "" || key == "" {
		return nil, fmt.Errorf("restore: an s3 source looks like s3://bucket/path/to/object.tar.age")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("restore: --endpoint is required for an s3:// source")
	}
	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return nil, fmt.Errorf("restore: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or fetch the object yourself and pass a local path")
	}
	rc, err := s3.New().Download(ctx, endpoint, bucket, region, key, access, secret)
	if err != nil {
		return nil, fmt.Errorf("restore: downloading the snapshot: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("restore: reading the snapshot: %w", err)
	}
	return body, nil
}

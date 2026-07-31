package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/backups"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
)

// fetchRepo pulls a destination's credentials from Core over the mTLS gRPC
// channel. They are never in the task payload (JetStream retains messages), so
// this call is the only way an agent learns a repository password.
func (e *taskExecutor) fetchRepo(ctx context.Context, taskID, destinationID string) (backups.Repo, error) {
	if e.core == nil {
		return backups.Repo{}, jobs.Permanent(errors.New("no control-plane client for backup credentials"))
	}
	rpcCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := e.core.FetchBackupCredentials(rpcCtx, &agentv1.FetchBackupCredentialsRequest{
		ServerId:      e.serverID,
		TaskId:        taskID,
		DestinationId: destinationID,
	})
	if err != nil {
		// A rejected pairing (task/destination mismatch) will never succeed on
		// redelivery, but a transport blip will — so only the former is
		// permanent. Treat it conservatively: retry, and let maxDeliveries cap it.
		return backups.Repo{}, fmt.Errorf("fetching backup credentials: %w", err)
	}
	return backups.Repo{
		Repository: resp.GetRepository(),
		Password:   resp.GetPassword(),
		Env:        resp.GetEnv(),
	}, nil
}

// runBackup dumps the account's databases, snapshots home + dumps into the
// destination repository, then applies the retention policy.
//
// Idempotent under redelivery: restic deduplicates, so a repeated run adds a
// near-empty snapshot rather than a second full copy.
func (e *taskExecutor) runBackup(ctx context.Context, taskID string, raw []byte) (map[string]string, error) {
	var p jobs.BackupRunPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.DestinationID == "" {
		return nil, jobs.Permanent(errors.New("username and destination_id are required"))
	}
	if e.backups == nil {
		return nil, jobs.Permanent(backups.ErrUnsupported)
	}

	repo, err := e.fetchRepo(ctx, taskID, p.DestinationID)
	if err != nil {
		return nil, err
	}

	paths := []string{e.layout.AccountHome(p.Username)}

	// Dump databases first so files and data are captured together. The dump
	// directory is agent-owned (0700, outside every account home) so one
	// account can neither read another's dump nor tamper with its own before
	// the snapshot is taken.
	dumpDir := e.layout.BackupDumpDir(p.Username)
	if len(p.Databases) > 0 {
		if e.dumper == nil {
			return nil, jobs.Permanent(errors.New("database dumps requested but no database backend is configured"))
		}
		if err := os.MkdirAll(dumpDir, 0o700); err != nil {
			return nil, fmt.Errorf("preparing dump dir: %w", err)
		}
		// The staging dir is reused every run; clear it so a database deleted
		// since the last backup does not linger in new snapshots forever.
		defer os.RemoveAll(dumpDir)
		for _, db := range p.Databases {
			dest := filepath.Join(dumpDir, db+".sql")
			if err := e.dumper.Dump(ctx, db, dest); err != nil {
				return nil, fmt.Errorf("dumping %s: %w", db, err)
			}
		}
		paths = append(paths, dumpDir)
	}

	spec := backups.Spec{
		Repo:     repo,
		Paths:    paths,
		Excludes: p.Excludes,
		Tags:     []string{"cypherpanel", "account:" + p.AccountID},
	}
	snap, err := e.backups.Backup(ctx, spec)
	if err != nil {
		return nil, err
	}

	// Retention runs after a successful snapshot so a failed backup can never
	// prune the good snapshots that preceded it.
	keep := backups.Retention{Daily: p.Retention.Daily, Weekly: p.Retention.Weekly, Monthly: p.Retention.Monthly}
	if !keep.IsZero() {
		if err := e.backups.Forget(ctx, spec, keep); err != nil {
			// The snapshot succeeded; a prune failure must not fail the backup
			// or the task would retry and snapshot all over again.
			slog.Warn("applying backup retention", "account_id", p.AccountID, "error", err)
		}
	}

	return map[string]string{
		jobs.MetaBackupSnapshotID: snap.ID,
		jobs.MetaBackupSizeBytes:  strconv.FormatInt(snap.SizeBytes, 10),
	}, nil
}

// runRestore restores a snapshot. By default it lands in an agent-owned
// staging directory so an operator can inspect before promoting; target
// "home" restores in place over live account data.
func (e *taskExecutor) runRestore(ctx context.Context, taskID string, raw []byte) (map[string]string, error) {
	var p jobs.BackupRestorePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.SnapshotID == "" || p.DestinationID == "" {
		return nil, jobs.Permanent(errors.New("username, snapshot_id and destination_id are required"))
	}
	if e.backups == nil {
		return nil, jobs.Permanent(backups.ErrUnsupported)
	}

	repo, err := e.fetchRepo(ctx, taskID, p.DestinationID)
	if err != nil {
		return nil, err
	}

	// Only two targets are accepted, both derived from the distro layout —
	// the payload never names a filesystem path, so a crafted task cannot
	// direct a restore at /etc or another account's home.
	target := e.layout.BackupRestoreDir(p.Username)
	if p.Target == "home" {
		target = "/"
	} else if p.Target != "" {
		return nil, jobs.Permanent(fmt.Errorf("unsupported restore target %q", p.Target))
	}

	spec := backups.Spec{Repo: repo, Tags: []string{"account:" + p.AccountID}}
	if err := e.backups.Restore(ctx, spec, p.SnapshotID, target); err != nil {
		return nil, err
	}
	return map[string]string{jobs.MetaBackupRestorePath: target}, nil
}

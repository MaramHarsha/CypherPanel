// Package docker — database backup and restore execution (managed-databases.md
// §7). The plane sends the engine (never a shell command — threat-model §8 req
// 4); this file derives the dump/restore command from its own matrix and moves
// the data through the Docker archive API (docker cp), so no exec-stream
// framing corrupts the dump and no exec stdin is needed.
package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// In-container paths the dump/restore file occupies. Deterministic and
// per-operation-idempotent (overwritten each run).
const (
	backupInContainerPath  = "/tmp/cypher-backup"
	restoreInContainerPath = "/tmp/cypher-restore"
)

// ExecClient is the container-execution surface the executor needs
// (consumer-defined; *engine.Client satisfies it).
type ExecClient interface {
	ExecAndWait(ctx context.Context, containerID string, cmd []string) (exitCode int, output []byte, err error)
	CopyFromContainer(ctx context.Context, containerID, path string) (io.ReadCloser, error)
	CopyToContainer(ctx context.Context, containerID, destDir string, tarStream io.Reader) error
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error
}

// S3Client is the object-storage surface (consumer-defined; *RealS3Client
// satisfies it).
type S3Client interface {
	Upload(ctx context.Context, endpoint, bucket, region, key, accessKey, secretKey string, body io.Reader, size int64) error
	Download(ctx context.Context, endpoint, bucket, region, key, accessKey, secretKey string) (io.ReadCloser, error)
	Delete(ctx context.Context, endpoint, bucket, region, key, accessKey, secretKey string) error
}

// BackupExecutor runs one backup or restore against a database container.
type BackupExecutor struct {
	engine ExecClient
	s3     S3Client
	log    *slog.Logger
}

// NewBackupExecutor wires the executor.
func NewBackupExecutor(engine ExecClient, s3 S3Client, log *slog.Logger) *BackupExecutor {
	return &BackupExecutor{engine: engine, s3: s3, log: log}
}

// enginePlan is how one engine is dumped and restored. dumpCmd writes the
// backup to dumpPath inside the container; restoreCmd reads restorePath;
// rdbRestart marks the redis/valkey path where restore = place the RDB file and
// restart the container (it reloads dump.rdb on boot) rather than exec a
// restore command.
type enginePlan struct {
	dumpCmd     []string
	dumpPath    string
	restoreCmd  []string
	restorePath string
	rdbRestart  bool
}

// planFor derives the dump/restore plan from the engine, using the container's
// own environment variables for credentials (so no db password crosses the
// wire). dataPath is the engine's data directory (for the RDB engines).
func planFor(engine, dataPath string) (enginePlan, error) {
	switch engine {
	case "postgresql":
		return enginePlan{
			dumpCmd:     []string{"sh", "-c", "pg_dumpall -U postgres > " + backupInContainerPath},
			dumpPath:    backupInContainerPath,
			restoreCmd:  []string{"sh", "-c", "psql -U postgres -f " + restoreInContainerPath},
			restorePath: restoreInContainerPath,
		}, nil
	case "mysql":
		return enginePlan{
			dumpCmd:     []string{"sh", "-c", `mysqldump -u root -p"$MYSQL_ROOT_PASSWORD" --all-databases > ` + backupInContainerPath},
			dumpPath:    backupInContainerPath,
			restoreCmd:  []string{"sh", "-c", `mysql -u root -p"$MYSQL_ROOT_PASSWORD" < ` + restoreInContainerPath},
			restorePath: restoreInContainerPath,
		}, nil
	case "mariadb":
		return enginePlan{
			dumpCmd:     []string{"sh", "-c", `mariadb-dump -u root -p"$MARIADB_ROOT_PASSWORD" --all-databases > ` + backupInContainerPath},
			dumpPath:    backupInContainerPath,
			restoreCmd:  []string{"sh", "-c", `mariadb -u root -p"$MARIADB_ROOT_PASSWORD" < ` + restoreInContainerPath},
			restorePath: restoreInContainerPath,
		}, nil
	case "mongodb":
		return enginePlan{
			dumpCmd:     []string{"sh", "-c", `mongodump --archive=` + backupInContainerPath + ` --gzip -u root -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin`},
			dumpPath:    backupInContainerPath,
			restoreCmd:  []string{"sh", "-c", `mongorestore --archive=` + restoreInContainerPath + ` --gzip --drop -u root -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin`},
			restorePath: restoreInContainerPath,
		}, nil
	case "redis", "valkey":
		cli := "redis-cli"
		if engine == "valkey" {
			cli = "valkey-cli"
		}
		return enginePlan{
			dumpCmd:     []string{cli, "save"},
			dumpPath:    dataPath + "/dump.rdb",
			restorePath: dataPath + "/dump.rdb",
			rdbRestart:  true,
		}, nil
	default:
		return enginePlan{}, fmt.Errorf("backup: unsupported engine %q", engine)
	}
}

// ExecuteBackup dumps the database inside its container, copies the dump out via
// the archive API, gzips it, and uploads it to S3. It always returns an event
// (never a bare error) — success or failure is the observation the plane acts on.
func (b *BackupExecutor) ExecuteBackup(ctx context.Context, work *agentv1.DbBackupWork) *agentv1.DbBackupEvent {
	fail := func(detail string) *agentv1.DbBackupEvent {
		return &agentv1.DbBackupEvent{
			BackupRecordId: work.BackupRecordId, DbId: work.DbId,
			Outcome: agentv1.DbBackupEvent_OUTCOME_FAILED, Detail: detail, OccurredAt: timestamppb.Now(),
		}
	}

	plan, err := planFor(work.Engine, work.DataPath)
	if err != nil {
		return fail(err.Error())
	}

	// 1. Dump to a file inside the container; a non-zero exit is a failed dump.
	if code, out, err := b.engine.ExecAndWait(ctx, work.ContainerName, plan.dumpCmd); err != nil {
		return fail(fmt.Sprintf("dump exec: %v", err))
	} else if code != 0 {
		return fail(fmt.Sprintf("dump command exited %d: %s", code, truncate(out)))
	}

	// 2. Copy the dump file out via the archive API and gzip it to a temp file.
	gzPath, size, err := b.archiveOutGzip(ctx, work.ContainerName, plan.dumpPath)
	if err != nil {
		return fail(err.Error())
	}
	defer func() { _ = os.Remove(gzPath) }()

	// 3. Upload to S3.
	f, err := os.Open(gzPath)
	if err != nil {
		return fail(fmt.Sprintf("opening compressed dump: %v", err))
	}
	defer func() { _ = f.Close() }()
	if err := b.s3.Upload(ctx, work.S3Endpoint, work.S3Bucket, work.S3Region, work.S3Key, work.S3AccessKey, work.S3SecretKey, f, size); err != nil {
		return fail(fmt.Sprintf("s3 upload: %v", err))
	}

	// 4. Best-effort cleanup of the in-container dump file.
	_, _, _ = b.engine.ExecAndWait(ctx, work.ContainerName, []string{"rm", "-f", plan.dumpPath})

	b.log.Info("database backup uploaded", "db_id", work.DbId, "key", work.S3Key, "size_bytes", size)
	return &agentv1.DbBackupEvent{
		BackupRecordId: work.BackupRecordId, DbId: work.DbId,
		Outcome:   agentv1.DbBackupEvent_OUTCOME_SUCCEEDED,
		ObjectKey: work.S3Key, SizeBytes: size, OccurredAt: timestamppb.Now(),
	}
}

// ExecutePrune deletes the given S3 objects (a retention sweep the plane
// computed) and reports which keys were removed vs. left behind. Deleting an
// absent object is treated as success — S3 DELETE is idempotent — so redelivery
// and a partially-applied prior sweep both converge. The plane deletes the
// matching BackupRecord rows only for the keys reported deleted.
func (b *BackupExecutor) ExecutePrune(ctx context.Context, work *agentv1.DbBackupPruneWork) *agentv1.DbBackupPruneEvent {
	ev := &agentv1.DbBackupPruneEvent{DbId: work.DbId, OccurredAt: timestamppb.Now()}
	for _, key := range work.GetS3Keys() {
		if err := b.s3.Delete(ctx, work.S3Endpoint, work.S3Bucket, work.S3Region, key, work.S3AccessKey, work.S3SecretKey); err != nil {
			// Never log the key's credentials; the key itself is not a secret.
			b.log.Error("backup prune: deleting object", "db_id", work.DbId, "key", key, "error", err)
			ev.FailedKeys = append(ev.FailedKeys, key)
			continue
		}
		ev.DeletedKeys = append(ev.DeletedKeys, key)
	}
	b.log.Info("backup prune complete", "db_id", work.DbId, "deleted", len(ev.DeletedKeys), "failed", len(ev.FailedKeys))
	return ev
}

// archiveOutGzip copies path out of the container (a tar stream), extracts the
// single regular file, gzips it to a temp file on the agent, and returns the
// temp path and its compressed size.
func (b *BackupExecutor) archiveOutGzip(ctx context.Context, containerID, path string) (string, int64, error) {
	tarStream, err := b.engine.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return "", 0, fmt.Errorf("archive out: %w", err)
	}
	defer func() { _ = tarStream.Close() }()

	tr := tar.NewReader(tarStream)
	tmp, err := os.CreateTemp("", "cypher-backup-*.gz")
	if err != nil {
		return "", 0, fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	gzw := gzip.NewWriter(tmp)

	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gzw.Close()
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return "", 0, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if _, err := io.Copy(gzw, tr); err != nil {
			_ = gzw.Close()
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return "", 0, fmt.Errorf("compressing dump: %w", err)
		}
		found = true
		break // one file per backup
	}
	if err := gzw.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("finalizing gzip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	if !found {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("archive contained no regular file for %s", path)
	}
	fi, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	return tmpPath, fi.Size(), nil
}

// ExecuteRestore downloads a backup, copies it into the container via the
// archive API, and runs the engine's restore (or, for RDB engines, restarts the
// container so it reloads the placed dump). Returns a terminal event.
func (b *BackupExecutor) ExecuteRestore(ctx context.Context, work *agentv1.DbRestoreWork, progress func(*agentv1.DbRestoreEvent)) *agentv1.DbRestoreEvent {
	fail := func(detail string) *agentv1.DbRestoreEvent {
		return &agentv1.DbRestoreEvent{
			RestoreId: work.RestoreId, DbId: work.DbId,
			Outcome: agentv1.DbRestoreEvent_OUTCOME_FAILED, Detail: detail, OccurredAt: timestamppb.Now(),
		}
	}
	// step reports where the restore has got to. Bytes are sent only where they
	// are real: an engine restart is not measured in bytes, and a bar that
	// moves for it would be drawing something nobody counted.
	step := func(s agentv1.DbRestoreEvent_Step, done, total int64) {
		if progress == nil {
			return
		}
		progress(&agentv1.DbRestoreEvent{
			RestoreId: work.RestoreId, DbId: work.DbId,
			Outcome: agentv1.DbRestoreEvent_OUTCOME_RUNNING, Step: s,
			BytesDone: done, BytesTotal: total, OccurredAt: timestamppb.Now(),
		})
	}

	plan, err := planFor(work.Engine, work.DataPath)
	if err != nil {
		return fail(err.Error())
	}

	// 1. Download + decompress into memory-backed bytes (backups are bounded
	// v1-scale; a temp file would be a small refinement for very large dumps).
	step(agentv1.DbRestoreEvent_STEP_FETCHING, 0, 0)
	dump, err := b.downloadGunzip(ctx, work)
	if err != nil {
		return fail(err.Error())
	}
	total := int64(len(dump))

	if plan.rdbRestart {
		// Redis/Valkey: stop, place dump.rdb in the data dir, start (it reloads
		// on boot). The container name doubles as its id for engine calls.
		step(agentv1.DbRestoreEvent_STEP_STOPPING, 0, total)
		if err := b.engine.StopContainer(ctx, work.ContainerName, 30*time.Second); err != nil {
			return fail(fmt.Sprintf("stopping container: %v", err))
		}
		step(agentv1.DbRestoreEvent_STEP_APPLYING, 0, total)
		if err := b.copyFileIn(ctx, work.ContainerName, plan.restorePath, dump); err != nil {
			return fail(err.Error())
		}
		step(agentv1.DbRestoreEvent_STEP_RESTARTING, total, total)
		if err := b.engine.StartContainer(ctx, work.ContainerName); err != nil {
			return fail(fmt.Sprintf("starting container: %v", err))
		}
		if err := b.engine.WaitHealthy(ctx, work.ContainerName, 60*time.Second); err != nil {
			return fail(fmt.Sprintf("post-restore health: %v", err))
		}
		return b.restoreOK(work)
	}

	// SQL / Mongo: copy the dump in, run the restore command reading it.
	step(agentv1.DbRestoreEvent_STEP_APPLYING, 0, total)
	if err := b.copyFileIn(ctx, work.ContainerName, plan.restorePath, dump); err != nil {
		return fail(err.Error())
	}
	if code, out, err := b.engine.ExecAndWait(ctx, work.ContainerName, plan.restoreCmd); err != nil {
		return fail(fmt.Sprintf("restore exec: %v", err))
	} else if code != 0 {
		return fail(fmt.Sprintf("restore command exited %d: %s", code, truncate(out)))
	}
	_, _, _ = b.engine.ExecAndWait(ctx, work.ContainerName, []string{"rm", "-f", plan.restorePath})
	return b.restoreOK(work)
}

func (b *BackupExecutor) restoreOK(work *agentv1.DbRestoreWork) *agentv1.DbRestoreEvent {
	b.log.Info("database restore completed", "db_id", work.DbId, "key", work.S3Key)
	return &agentv1.DbRestoreEvent{
		RestoreId: work.RestoreId, DbId: work.DbId,
		Outcome: agentv1.DbRestoreEvent_OUTCOME_SUCCEEDED, OccurredAt: timestamppb.Now(),
	}
}

func (b *BackupExecutor) downloadGunzip(ctx context.Context, work *agentv1.DbRestoreWork) ([]byte, error) {
	body, err := b.s3.Download(ctx, work.S3Endpoint, work.S3Bucket, work.S3Region, work.S3Key, work.S3AccessKey, work.S3SecretKey)
	if err != nil {
		return nil, fmt.Errorf("s3 download: %w", err)
	}
	defer func() { _ = body.Close() }()
	gzr, err := gzip.NewReader(body)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	defer func() { _ = gzr.Close() }()
	data, err := io.ReadAll(gzr)
	if err != nil {
		return nil, fmt.Errorf("reading dump: %w", err)
	}
	return data, nil
}

// copyFileIn writes content as a single-entry tar and uploads it under the
// destination directory, so it lands at destPath inside the container.
func (b *BackupExecutor) copyFileIn(ctx context.Context, containerID, destPath string, content []byte) error {
	dir, name := splitPath(destPath)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("tar write: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("tar close: %w", err)
	}
	if err := b.engine.CopyToContainer(ctx, containerID, dir, &buf); err != nil {
		return fmt.Errorf("archive in: %w", err)
	}
	return nil
}

func splitPath(p string) (dir, name string) {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i+1], p[i+1:]
		}
	}
	return "./", p
}

func truncate(b []byte) string {
	const max = 500
	s := string(bytes.TrimSpace(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

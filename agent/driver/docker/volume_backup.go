package docker

// Volume backups (volume-backups.md). The whole delta from a database backup is
// what produces the bytes: a database is dumped by its engine into a consistent
// artefact first, and a volume has nothing to ask for a consistent view — so
// the agent tars the mount path as it stands.
//
// That difference is the feature's one honest difficulty and it is not solved
// here, it is STATED: the artefact is crash-consistent, the same thing you would
// have if the power had been cut at that instant. Applications that survive
// power loss survive this; SQLite, LevelDB and search indexes maintaining their
// own on-disk invariants may not. Stopping the container would fix it and turn
// a nightly backup into a nightly outage, which vision non-negotiable 4 exists
// to prevent.

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// ExecuteVolumeBackup archives one mount path out of a running container and
// uploads it. Idempotent by record id: redelivery re-uploads to the same key,
// and S3 PUT is last-writer-wins, so a retry converges rather than duplicating.
func (b *BackupExecutor) ExecuteVolumeBackup(ctx context.Context, work *agentv1.VolumeBackupWork) *agentv1.VolumeBackupEvent {
	fail := func(detail string) *agentv1.VolumeBackupEvent {
		return &agentv1.VolumeBackupEvent{
			BackupRecordId: work.BackupRecordId,
			ApplicationId:  work.ApplicationId,
			VolumeName:     work.VolumeName,
			Outcome:        agentv1.VolumeBackupEvent_OUTCOME_FAILED,
			Detail:         detail,
			OccurredAt:     timestamppb.Now(),
		}
	}

	gzPath, size, err := b.archiveDirGzip(ctx, work.ContainerName, work.MountPath)
	if err != nil {
		return fail(err.Error())
	}
	defer func() { _ = os.Remove(gzPath) }()

	f, err := os.Open(gzPath)
	if err != nil {
		return fail(fmt.Sprintf("opening archive: %v", err))
	}
	defer func() { _ = f.Close() }()
	if err := b.s3.Upload(ctx, work.S3Endpoint, work.S3Bucket, work.S3Region, work.S3Key,
		work.S3AccessKey, work.S3SecretKey, f, size); err != nil {
		return fail(fmt.Sprintf("s3 upload: %v", err))
	}

	b.log.Info("volume backup uploaded",
		"application_id", work.ApplicationId, "volume", work.VolumeName, "key", work.S3Key, "size_bytes", size)
	return &agentv1.VolumeBackupEvent{
		BackupRecordId: work.BackupRecordId,
		ApplicationId:  work.ApplicationId,
		VolumeName:     work.VolumeName,
		Outcome:        agentv1.VolumeBackupEvent_OUTCOME_SUCCEEDED,
		ObjectKey:      work.S3Key,
		SizeBytes:      size,
		OccurredAt:     timestamppb.Now(),
	}
}

// archiveDirGzip gzips the container's own tar stream for a directory, verbatim.
//
// It is SIMPLER than archiveOutGzip rather than a variant of it: that one
// extracts a single regular file from the stream because a dump is one file,
// while a volume is a tree and the tar IS the artefact. Nothing is unpacked, so
// permissions, symlinks, nesting and empty directories all survive — and a
// restore is an ordinary `tar -x`, on any machine, with no tool of ours.
func (b *BackupExecutor) archiveDirGzip(ctx context.Context, containerID, path string) (string, int64, error) {
	tarStream, err := b.engine.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return "", 0, fmt.Errorf("archive out: %w", err)
	}
	defer func() { _ = tarStream.Close() }()

	tmp, err := os.CreateTemp("", "cypher-volume-*.tar.gz")
	if err != nil {
		return "", 0, fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func(e error) (string, int64, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", 0, e
	}

	gzw := gzip.NewWriter(tmp)
	if _, err := io.Copy(gzw, tarStream); err != nil {
		_ = gzw.Close()
		return cleanup(fmt.Errorf("compressing volume: %w", err))
	}
	if err := gzw.Close(); err != nil {
		return cleanup(fmt.Errorf("finalizing gzip: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("flushing archive: %w", err))
	}
	fi, err := tmp.Stat()
	if err != nil {
		return cleanup(fmt.Errorf("sizing archive: %w", err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("closing archive: %w", err)
	}
	// An empty archive means the path held nothing. That is a real state — a
	// volume mounted but never written — and reporting it as a zero-byte
	// success is more honest than inventing a failure.
	return tmpPath, fi.Size(), nil
}

package scheduler

// Database backup/restore orchestration (managed-databases.md §7). The
// scheduler creates the history record, unseals the S3 credentials, and
// publishes an engine-only work item (never a shell command); it records the
// terminal outcome from the agent's event and prunes history beyond retention.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ErrRestoreNotConfirmed is returned when a restore is requested without the
// explicit confirmation the destructive operation requires (spec §7).
var ErrRestoreNotConfirmed = errors.New("scheduler: restore requires confirm=true")

// s3Coords resolves and unseals a backup target's connection details. The
// unsealed keys live only in the returned struct, travel only on the mTLS work
// item (rule 23), and are never logged (rule 20).
type s3Coords struct {
	endpoint, bucket, region, accessKey, secretKey, prefix string
}

func (s *Scheduler) resolveTarget(ctx context.Context, targetID string) (s3Coords, error) {
	t, err := s.store.GetBackupTarget(ctx, targetID)
	if err != nil {
		return s3Coords{}, fmt.Errorf("scheduler: loading backup target: %w", err)
	}
	ak, err := s.opener.Open(t.AccessKeyCT, t.AccessKeyNonce)
	if err != nil {
		return s3Coords{}, fmt.Errorf("scheduler: unsealing access key: %w", err)
	}
	sk, err := s.opener.Open(t.SecretKeyCT, t.SecretKeyNonce)
	if err != nil {
		return s3Coords{}, fmt.Errorf("scheduler: unsealing secret key: %w", err)
	}
	return s3Coords{
		endpoint: t.Endpoint, bucket: t.Bucket, region: t.Region,
		accessKey: string(ak), secretKey: string(sk), prefix: t.PathPrefix,
	}, nil
}

// objectKey is the deterministic S3 key for one backup run.
func objectKey(prefix, dbID string, ts time.Time) string {
	key := dbID + "/" + ts.UTC().Format("20060102T150405Z") + ".gz"
	if prefix != "" {
		key = prefix + "/" + key
	}
	return key
}

// RunBackup starts one backup for a schedule: it records a running BackupRecord
// and publishes DbBackupWork to the database's host. The record is completed
// later from the agent's DbBackupEvent (ADR-005: outcome from observation).
func (s *Scheduler) RunBackup(ctx context.Context, scheduleID string) (domain.BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sched, err := s.store.GetDatabaseBackup(ctx, scheduleID)
	if err != nil {
		return domain.BackupRecord{}, fmt.Errorf("scheduler: loading backup schedule: %w", err)
	}
	db, err := s.store.GetDatabase(ctx, sched.DatabaseID)
	if err != nil {
		return domain.BackupRecord{}, fmt.Errorf("scheduler: loading database: %w", err)
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		return domain.BackupRecord{}, err
	}

	now := s.now()
	key := objectKey(coords.prefix, db.ID, now)
	rec, err := s.store.CreateBackupRecord(ctx, domain.BackupRecord{
		ID:               ids.New(ids.PrefixBackupRecord),
		DatabaseBackupID: sched.ID,
		ObjectKey:        key,
		Status:           domain.BackupRunning,
		StartedAt:        now,
	})
	if err != nil {
		return domain.BackupRecord{}, fmt.Errorf("scheduler: creating backup record: %w", err)
	}
	if err := s.store.SetDatabaseBackupLastRun(ctx, sched.ID, &now, domain.BackupRunning); err != nil {
		s.log.Error("backup: setting last run", "schedule_id", sched.ID, "error", err)
	}

	work := &agentv1.DbBackupWork{
		BackupRecordId: rec.ID,
		DbId:           db.ID,
		ContainerName:  "cypher-db-" + db.ID,
		Engine:         string(db.Engine),
		DataPath:       db.DataPath,
		S3Endpoint:     coords.endpoint,
		S3Bucket:       coords.bucket,
		S3Region:       coords.region,
		S3Key:          key,
		S3AccessKey:    coords.accessKey,
		S3SecretKey:    coords.secretKey,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return rec, fmt.Errorf("scheduler: marshaling backup work: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.DbBackup(db.ServerID), fmt.Sprintf("%s.backup.%d", rec.ID, now.UnixNano()), data); err != nil {
		return rec, fmt.Errorf("scheduler: publishing backup work: %w", err)
	}
	return rec, nil
}

// RunRestore publishes a restore of one BackupRecord back into its database.
// confirm must be true — restore is destructive (spec §7).
func (s *Scheduler) RunRestore(ctx context.Context, dbID, backupRecordID string, confirm bool) error {
	if !confirm {
		return ErrRestoreNotConfirmed
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.store.GetBackupRecord(ctx, backupRecordID)
	if err != nil {
		return fmt.Errorf("scheduler: loading backup record: %w", err)
	}
	if rec.Status != domain.BackupSucceeded {
		return fmt.Errorf("scheduler: backup record %s is not a succeeded backup", rec.ID)
	}
	sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID)
	if err != nil {
		return fmt.Errorf("scheduler: loading backup schedule: %w", err)
	}
	db, err := s.store.GetDatabase(ctx, dbID)
	if err != nil {
		return fmt.Errorf("scheduler: loading database: %w", err)
	}
	if sched.DatabaseID != db.ID {
		return fmt.Errorf("scheduler: backup record does not belong to database %s", dbID)
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		return err
	}

	work := &agentv1.DbRestoreWork{
		RestoreId:     ids.New(ids.PrefixBackupRecord),
		DbId:          db.ID,
		ContainerName: "cypher-db-" + db.ID,
		Engine:        string(db.Engine),
		DataPath:      db.DataPath,
		S3Endpoint:    coords.endpoint,
		S3Bucket:      coords.bucket,
		S3Region:      coords.region,
		S3Key:         rec.ObjectKey,
		S3AccessKey:   coords.accessKey,
		S3SecretKey:   coords.secretKey,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling restore work: %w", err)
	}
	return s.bus.PublishWork(ctx, subjects.DbRestore(db.ServerID), fmt.Sprintf("%s.restore.%d", rec.ID, s.now().UnixNano()), data)
}

// HandleDbBackupEvent records a backup's terminal outcome (ADR-005) and, on
// success, prunes history beyond the schedule's retention_count.
func (s *Scheduler) HandleDbBackupEvent(ctx context.Context, serverID string, ev *agentv1.DbBackupEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.store.GetBackupRecord(ctx, ev.GetBackupRecordId())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("backup event: loading record", "record_id", ev.GetBackupRecordId(), "error", err)
		}
		return
	}
	status := domain.BackupSucceeded
	if ev.GetOutcome() != agentv1.DbBackupEvent_OUTCOME_SUCCEEDED {
		status = domain.BackupFailed
	}
	finished := s.now()
	if err := s.store.UpdateBackupRecord(ctx, rec.ID, ev.GetObjectKey(), ev.GetSizeBytes(), status, ev.GetDetail(), &finished); err != nil {
		s.log.Error("backup event: updating record", "record_id", rec.ID, "error", err)
		return
	}
	if err := s.store.SetDatabaseBackupLastRun(ctx, rec.DatabaseBackupID, &finished, status); err != nil {
		s.log.Error("backup event: updating schedule status", "schedule_id", rec.DatabaseBackupID, "error", err)
	}
	s.log.Info("database backup finished", "record_id", rec.ID, "status", status, "size_bytes", ev.GetSizeBytes())

	if s.notify != nil {
		// Resolve the database for the message; a lookup miss just drops the
		// notice (the outcome is already recorded and logged).
		rec.Status, rec.Detail, rec.ObjectKey, rec.FinishedAt = status, ev.GetDetail(), ev.GetObjectKey(), &finished
		if sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID); err == nil {
			if db, err := s.store.GetDatabase(ctx, sched.DatabaseID); err == nil {
				s.notify.NotifyBackup(ctx, db, rec)
			}
		}
	}

	if status == domain.BackupSucceeded {
		sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID)
		if err == nil && sched.RetentionCount > 0 {
			// Prune history rows beyond the retention window. (Deleting the
			// pruned S3 objects is a follow-on within this feature.)
			if err := s.store.DeleteOldBackupRecords(ctx, sched.ID, int32(sched.RetentionCount)); err != nil {
				s.log.Error("backup event: pruning history", "schedule_id", sched.ID, "error", err)
			}
		}
	}
}

// HandleDbRestoreEvent records a restore's terminal outcome. The database's
// running state is separately observed through DbStatus after the container
// restart, so here we only log the restore's success/failure.
func (s *Scheduler) HandleDbRestoreEvent(_ context.Context, serverID string, ev *agentv1.DbRestoreEvent) {
	if ev.GetOutcome() == agentv1.DbRestoreEvent_OUTCOME_SUCCEEDED {
		s.log.Info("database restore succeeded", "db_id", ev.GetDbId(), "restore_id", ev.GetRestoreId(), "server_id", serverID)
		return
	}
	s.log.Warn("database restore failed", "db_id", ev.GetDbId(), "restore_id", ev.GetRestoreId(), "detail", ev.GetDetail())
}

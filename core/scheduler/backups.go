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

	robfig "github.com/robfig/cron/v3"
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

// RunRestore publishes a restore of one BackupRecord back into its database and
// returns the record that tracks it. confirm must be true — restore is
// destructive (spec §7).
//
// The record is written, and the database marked restoring, BEFORE the work is
// published: a plane that dies in between restarts knowing a restore is in
// flight rather than having forgotten it (ENGINEERING rule 15).
func (s *Scheduler) RunRestore(ctx context.Context, dbID, backupRecordID string, confirm bool) (domain.DatabaseRestore, error) {
	if !confirm {
		return domain.DatabaseRestore{}, ErrRestoreNotConfirmed
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.store.GetBackupRecord(ctx, backupRecordID)
	if err != nil {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: loading backup record: %w", err)
	}
	if rec.Status != domain.BackupSucceeded {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: backup record %s is not a succeeded backup", rec.ID)
	}
	sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID)
	if err != nil {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: loading backup schedule: %w", err)
	}
	db, err := s.store.GetDatabase(ctx, dbID)
	if err != nil {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: loading database: %w", err)
	}
	if sched.DatabaseID != db.ID {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: backup record does not belong to database %s", dbID)
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		return domain.DatabaseRestore{}, err
	}

	restore, err := s.store.CreateDatabaseRestore(ctx, ids.New(ids.PrefixDatabaseRestore), db.ID, rec.ID, domain.RestoreStepFetching)
	if err != nil {
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: recording restore: %w", err)
	}
	// The database is offline for the duration, and saying so is the point of
	// the record. A failure here is logged rather than fatal: the restore is
	// already recorded and the agent will report what happens to it.
	if err := s.store.SetDatabaseStatus(ctx, db.ID, domain.DbRestoring, "restoring from "+rec.ObjectKey); err != nil {
		s.log.Error("marking database restoring", "db_id", db.ID, "restore_id", restore.ID, "error", err)
	}

	work := &agentv1.DbRestoreWork{
		RestoreId:     restore.ID,
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
		return domain.DatabaseRestore{}, fmt.Errorf("scheduler: marshaling restore work: %w", err)
	}
	// Keyed by the restore, not by the record and a clock reading: the restore
	// id IS the idempotency key the agent already echoes back, so a redelivery
	// is the same work item rather than a second one.
	if err := s.bus.PublishWork(ctx, subjects.DbRestore(db.ServerID), restore.ID, data); err != nil {
		return domain.DatabaseRestore{}, err
	}
	return restore, nil
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

	if len(s.sinks) > 0 {
		// Resolve the database for the announcement; a lookup miss just drops
		// the notice (the outcome is already recorded and logged).
		rec.Status, rec.Detail, rec.ObjectKey, rec.FinishedAt = status, ev.GetDetail(), ev.GetObjectKey(), &finished
		if sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID); err == nil {
			if db, err := s.store.GetDatabase(ctx, sched.DatabaseID); err == nil {
				s.emitBackup(ctx, db, rec)
			}
		}
	}

	if status == domain.BackupSucceeded {
		if sched, err := s.store.GetDatabaseBackup(ctx, rec.DatabaseBackupID); err == nil && sched.RetentionCount > 0 {
			s.dispatchRetentionPrune(ctx, sched)
		}
	}
}

// dispatchRetentionPrune computes the retention sweep — succeeded backups beyond
// the schedule's retention_count — and commands the database's host agent to
// delete those S3 objects. The BackupRecord rows survive until the agent
// confirms deletion (HandleDbBackupPruneEvent), so a failed delete is retried on
// the next backup's prune rather than orphaning the object silently (ADR-005).
func (s *Scheduler) dispatchRetentionPrune(ctx context.Context, sched domain.DatabaseBackup) {
	prunable, err := s.store.ListBackupRecordsBeyondRetention(ctx, sched.ID, int32(sched.RetentionCount))
	if err != nil {
		s.log.Error("backup prune: listing retention set", "schedule_id", sched.ID, "error", err)
		return
	}
	if len(prunable) == 0 {
		return
	}
	db, err := s.store.GetDatabase(ctx, sched.DatabaseID)
	if err != nil {
		s.log.Error("backup prune: loading database", "schedule_id", sched.ID, "error", err)
		return
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		s.log.Error("backup prune: resolving target", "schedule_id", sched.ID, "error", err)
		return
	}
	keys := make([]string, 0, len(prunable))
	for _, p := range prunable {
		keys = append(keys, p.ObjectKey)
	}

	work := &agentv1.DbBackupPruneWork{
		DbId:        db.ID,
		S3Endpoint:  coords.endpoint,
		S3Bucket:    coords.bucket,
		S3Region:    coords.region,
		S3Keys:      keys,
		S3AccessKey: coords.accessKey,
		S3SecretKey: coords.secretKey,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		s.log.Error("backup prune: marshaling work", "schedule_id", sched.ID, "error", err)
		return
	}
	msgID := fmt.Sprintf("%s.prune.%d", sched.ID, s.now().UnixNano())
	if err := s.bus.PublishWork(ctx, subjects.DbBackupPrune(db.ServerID), msgID, data); err != nil {
		s.log.Error("backup prune: publishing work", "schedule_id", sched.ID, "error", err)
	}
}

// HandleDbBackupPruneEvent deletes the BackupRecord rows for objects the agent
// confirmed removed from S3. Rows for keys the agent could not delete are left
// in place and swept again by the next backup's prune (self-healing).
func (s *Scheduler) HandleDbBackupPruneEvent(ctx context.Context, serverID string, ev *agentv1.DbBackupPruneEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(ev.GetDeletedKeys()) == 0 {
		if n := len(ev.GetFailedKeys()); n > 0 {
			s.log.Warn("backup prune: agent deleted no objects", "db_id", ev.GetDbId(), "failed", n)
		}
		return
	}
	if err := s.store.DeleteBackupRecordsByObjectKeys(ctx, ev.GetDeletedKeys()); err != nil {
		s.log.Error("backup prune event: deleting records", "db_id", ev.GetDbId(), "error", err)
		return
	}
	s.log.Info("database backups pruned", "db_id", ev.GetDbId(), "deleted", len(ev.GetDeletedKeys()), "failed", len(ev.GetFailedKeys()))
}

// restoreSteps maps the agent's step enum onto the stored vocabulary. An
// unrecognised value becomes the empty string rather than a guess: a newer agent
// naming a step this plane does not know should leave the step blank, not
// invent one.
var restoreSteps = map[agentv1.DbRestoreEvent_Step]string{
	agentv1.DbRestoreEvent_STEP_FETCHING:   domain.RestoreStepFetching,
	agentv1.DbRestoreEvent_STEP_STOPPING:   domain.RestoreStepStopping,
	agentv1.DbRestoreEvent_STEP_APPLYING:   domain.RestoreStepApplying,
	agentv1.DbRestoreEvent_STEP_RESTARTING: domain.RestoreStepRestarting,
}

// HandleDbRestoreEvent advances or closes the restore record.
//
// A restore takes the database offline, so its progress is the answer someone
// staring at the screen needs, not a detail. Terminal events also put the
// database back into an observed state: it was marked `restoring` when the work
// was published, and nothing else would clear that if the restore failed before
// the container came back for DbStatus to observe.
func (s *Scheduler) HandleDbRestoreEvent(ctx context.Context, serverID string, ev *agentv1.DbRestoreEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	restoreID := ev.GetRestoreId()

	if ev.GetOutcome() == agentv1.DbRestoreEvent_OUTCOME_RUNNING {
		step := restoreSteps[ev.GetStep()]
		if _, err := s.store.AdvanceDatabaseRestore(ctx, restoreID, step, ev.GetBytesDone(), ev.GetBytesTotal()); err != nil {
			// ErrNotFound here is a late event for a restore that already
			// finished — a redelivery, or an agent that reconnected. Ignoring it
			// is the point of pinning the update to status='running'.
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("advancing restore", "restore_id", restoreID, "error", err)
			}
			return
		}
		s.log.Info("database restore progress", "db_id", ev.GetDbId(), "restore_id", restoreID, "step", step)
		return
	}

	status, dbStatus := domain.RestoreSucceeded, domain.DbRunning
	if ev.GetOutcome() != agentv1.DbRestoreEvent_OUTCOME_SUCCEEDED {
		status, dbStatus = domain.RestoreFailed, domain.DbError
	}
	if _, err := s.store.FinishDatabaseRestore(ctx, restoreID, status, ev.GetDetail()); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("finishing restore", "restore_id", restoreID, "error", err)
		}
		// Already closed: a redelivered terminal event must not re-decide the
		// outcome, and must not move the database's status a second time.
		return
	}
	// A guess, deliberately: the agent knows the restore finished, not whether
	// the container is healthy. DbStatus overwrites this within a heartbeat
	// with what is actually observed — this only stops the database sitting on
	// `restoring` forever if the restore failed before the container returned.
	if err := s.store.SetDatabaseStatus(ctx, ev.GetDbId(), dbStatus, ev.GetDetail()); err != nil {
		s.log.Error("clearing restoring status", "db_id", ev.GetDbId(), "error", err)
	}

	if status == domain.RestoreSucceeded {
		s.log.Info("database restore succeeded", "db_id", ev.GetDbId(), "restore_id", restoreID, "server_id", serverID)
		return
	}
	s.log.Warn("database restore failed", "db_id", ev.GetDbId(), "restore_id", restoreID, "detail", ev.GetDetail())
}

// RunBackupSweeper fires due scheduled backups on a ticker until ctx is done
// (managed-databases.md §7). It is the plane-side cron evaluator that turns a
// stored `schedule` into actual runs — the same sweeper pattern as previews and
// the heartbeat-stale sweep.
func (s *Scheduler) RunBackupSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepDueBackups(ctx)
		}
	}
}

// SweepDueBackups runs one backup for every enabled schedule whose next cron
// fire (measured from its last run, or its creation if it has never run) has
// passed. RunBackup advances last_run_at when it starts, so a schedule fires at
// most once per due window — no catch-up storms and no double-fire before the
// run completes.
func (s *Scheduler) SweepDueBackups(ctx context.Context) {
	schedules, err := s.store.ListEnabledBackupSchedules(ctx)
	if err != nil {
		s.log.Error("backup sweep: listing schedules", "error", err)
		return
	}
	now := s.now()
	for _, sch := range schedules {
		cronSched, err := robfig.ParseStandard(sch.Schedule)
		if err != nil {
			// A schedule is validated at create time; a bad one here is
			// defensive — skip it rather than stall the whole sweep.
			s.log.Error("backup sweep: unparseable schedule; skipped", "schedule_id", sch.ID, "schedule", sch.Schedule, "error", err)
			continue
		}
		anchor := sch.CreatedAt
		if sch.LastRunAt != nil {
			anchor = *sch.LastRunAt
		}
		if cronSched.Next(anchor).After(now) {
			continue // not due yet
		}
		if _, err := s.RunBackup(ctx, sch.ID); err != nil {
			s.log.Error("backup sweep: running due backup", "schedule_id", sch.ID, "error", err)
		}
	}
}

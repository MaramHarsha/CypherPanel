package scheduler

// Volume backups (volume-backups.md). The whole pipeline is the database one:
// the same targets, the same S3 credentials, the same in-flight record written
// BEFORE the work is published (rule 15), the same retention sweep. What differs
// is that one run fans out across every volume the application has flagged,
// because a failure belongs to the volume that failed rather than to the run.

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// volumeObjectKey mirrors objectKey's shape with the volume in the path, so an
// operator browsing the bucket can tell which directory an archive came from
// without opening it.
func volumeObjectKey(prefix, appID, volume string, ts time.Time) string {
	key := appID + "/" + volume + "/" + ts.UTC().Format("20060102T150405Z") + ".tar.gz"
	if prefix != "" {
		key = prefix + "/" + key
	}
	return key
}

// RunVolumeBackup archives every flagged volume of one application. It returns
// the records it created — one per volume — so a caller can render the run
// without a second request.
//
// An application with no flagged volume is not an error: it is an operator who
// has a schedule and has not marked anything yet, and answering with an empty
// list says exactly that.
func (s *Scheduler) RunVolumeBackup(ctx context.Context, appID string) ([]domain.VolumeBackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sched, err := s.store.GetVolumeBackupByApplication(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: loading volume backup schedule: %w", err)
	}
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: loading application: %w", err)
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	var out []domain.VolumeBackupRecord
	for _, v := range app.Volumes {
		if !v.BackedUp {
			continue
		}
		key := volumeObjectKey(coords.prefix, app.ID, v.Name, now)
		rec, err := s.store.CreateVolumeBackupRecord(ctx, ids.New(ids.PrefixVolumeRecord), sched.ID, v.Name)
		if err != nil {
			return out, fmt.Errorf("scheduler: creating volume backup record: %w", err)
		}
		work := &agentv1.VolumeBackupWork{
			BackupRecordId: rec.ID,
			ApplicationId:  app.ID,
			ContainerName:  "cypher-" + app.ID,
			VolumeName:     v.Name,
			MountPath:      v.Path,
			S3Endpoint:     coords.endpoint,
			S3Bucket:       coords.bucket,
			S3Region:       coords.region,
			S3Key:          key,
			S3AccessKey:    coords.accessKey,
			S3SecretKey:    coords.secretKey,
		}
		data, err := proto.Marshal(work)
		if err != nil {
			return out, fmt.Errorf("scheduler: marshaling volume backup work: %w", err)
		}
		if err := s.bus.PublishWork(ctx, subjects.VolumeBackup(app.Runtime.ServerID),
			fmt.Sprintf("%s.volbackup.%d", rec.ID, now.UnixNano()), data); err != nil {
			return out, fmt.Errorf("scheduler: publishing volume backup work: %w", err)
		}
		out = append(out, rec)
	}
	if len(out) > 0 {
		if err := s.store.SetVolumeBackupLastRun(ctx, sched.ID, &now, domain.BackupRunning); err != nil {
			s.log.Error("volume backup: setting last run", "schedule_id", sched.ID, "error", err)
		}
	}
	return out, nil
}

// HandleVolumeBackupEvent records a terminal outcome and prunes what retention
// no longer keeps. Pruning is per volume — keeping "the last 7" across a
// two-volume application would keep three of one and four of the other.
func (s *Scheduler) HandleVolumeBackupEvent(ctx context.Context, ev *agentv1.VolumeBackupEvent) {
	status := domain.BackupSucceeded
	if ev.GetOutcome() != agentv1.VolumeBackupEvent_OUTCOME_SUCCEEDED {
		status = domain.BackupFailed
	}
	if err := s.store.UpdateVolumeBackupRecord(ctx, ev.GetBackupRecordId(),
		ev.GetObjectKey(), ev.GetSizeBytes(), status, ev.GetDetail()); err != nil {
		s.log.Error("volume backup: recording outcome", "record_id", ev.GetBackupRecordId(), "error", err)
		return
	}
	rec, err := s.store.GetVolumeBackupRecord(ctx, ev.GetBackupRecordId())
	if err != nil {
		return
	}
	sched, err := s.store.GetVolumeBackup(ctx, rec.VolumeBackupID)
	if err != nil {
		return
	}
	now := s.now()
	if err := s.store.SetVolumeBackupLastRun(ctx, sched.ID, &now, status); err != nil {
		s.log.Error("volume backup: setting last run", "schedule_id", sched.ID, "error", err)
	}
	if status != domain.BackupSucceeded {
		return
	}
	s.pruneVolumeBackups(ctx, sched, rec.VolumeName)
}

// pruneVolumeBackups deletes the S3 objects retention no longer keeps, reusing
// the database prune work message: the payload is a list of keys and a set of
// credentials, and a volume's keys are not a different kind of thing.
func (s *Scheduler) pruneVolumeBackups(ctx context.Context, sched domain.VolumeBackup, volumeName string) {
	keep := sched.RetentionCount
	if keep < 1 {
		keep = 1
	}
	stale, err := s.store.ListVolumeRecordsBeyondRetention(ctx, sched.ID, volumeName, keep)
	if err != nil || len(stale) == 0 {
		return
	}
	app, err := s.store.GetApplication(ctx, sched.ApplicationID)
	if err != nil {
		return
	}
	coords, err := s.resolveTarget(ctx, sched.TargetID)
	if err != nil {
		return
	}
	keys := make([]string, 0, len(stale))
	ids_ := make([]string, 0, len(stale))
	for _, r := range stale {
		if r.ObjectKey == "" {
			continue
		}
		keys = append(keys, r.ObjectKey)
		ids_ = append(ids_, r.ID)
	}
	if len(keys) == 0 {
		return
	}
	work := &agentv1.DbBackupPruneWork{
		DbId: sched.ApplicationID, S3Keys: keys,
		S3Endpoint: coords.endpoint, S3Bucket: coords.bucket, S3Region: coords.region,
		S3AccessKey: coords.accessKey, S3SecretKey: coords.secretKey,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return
	}
	now := s.now()
	if err := s.bus.PublishWork(ctx, subjects.DbBackupPrune(app.Runtime.ServerID),
		fmt.Sprintf("%s.volprune.%d", sched.ID, now.UnixNano()), data); err != nil {
		s.log.Error("volume backup: publishing prune", "schedule_id", sched.ID, "error", err)
		return
	}
	// The rows go when the objects do. A prune event names the keys actually
	// deleted, but those keys belong to the database prune handler; deleting
	// the rows here would be optimistic. So they are removed only after the
	// object is gone — which the shared prune event reports.
	s.pendingVolumePrune(ids_, keys)
}

// pendingVolumePrune remembers which volume records a prune is clearing, so the
// shared prune event can delete the right rows when the objects are confirmed
// gone. Keyed by object key because that is what the event reports.
func (s *Scheduler) pendingVolumePrune(recordIDs, keys []string) {
	if s.volumePrunes == nil {
		s.volumePrunes = map[string]string{}
	}
	for i, k := range keys {
		s.volumePrunes[k] = recordIDs[i]
	}
}

// ClaimVolumePruned turns the deleted keys a prune event reports back into the
// volume record rows they belonged to, and deletes them.
func (s *Scheduler) ClaimVolumePruned(ctx context.Context, deletedKeys []string) {
	if len(s.volumePrunes) == 0 {
		return
	}
	var rows []string
	for _, k := range deletedKeys {
		if id, ok := s.volumePrunes[k]; ok {
			rows = append(rows, id)
			delete(s.volumePrunes, k)
		}
	}
	if len(rows) == 0 {
		return
	}
	if err := s.store.DeleteVolumeBackupRecords(ctx, rows); err != nil {
		s.log.Error("volume backup: deleting pruned records", "error", err)
	}
}

// RunDueVolumeBackups is the volume half of the backup sweep, on the same tick.
func (s *Scheduler) RunDueVolumeBackups(ctx context.Context, due func(schedule string, anchor, now time.Time) bool) {
	schedules, err := s.store.ListEnabledVolumeBackupSchedules(ctx)
	if err != nil {
		s.log.Error("volume backup sweep: listing schedules", "error", err)
		return
	}
	now := s.now()
	for _, sch := range schedules {
		anchor := sch.CreatedAt
		if sch.LastRunAt != nil {
			anchor = *sch.LastRunAt
		}
		if !due(sch.Schedule, anchor, now) {
			continue
		}
		if _, err := s.RunVolumeBackup(ctx, sch.ApplicationID); err != nil {
			s.log.Error("volume backup sweep: running", "schedule_id", sch.ID, "error", err)
		}
	}
}

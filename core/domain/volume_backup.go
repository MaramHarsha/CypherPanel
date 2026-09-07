package domain

import "time"

// VolumeBackup is one application's schedule for archiving the volumes it has
// marked as backed up (volume-backups.md §3). One per application: an operator
// wanting two cadences for two directories of the same app is describing two
// applications.
type VolumeBackup struct {
	ID             string
	ApplicationID  string
	TargetID       string
	Schedule       string // 5-field cron; empty means manual only
	RetentionCount int
	Enabled        bool
	LastRunAt      *time.Time
	LastStatus     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// VolumeBackupRecord is one archived volume. One run of a schedule produces one
// record per flagged volume, because a failure belongs to the volume that
// failed rather than to the run.
type VolumeBackupRecord struct {
	ID             string
	VolumeBackupID string
	VolumeName     string
	ObjectKey      string
	SizeBytes      int64
	// running | succeeded | failed
	Status     string
	Detail     string
	StartedAt  time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
}

// Package databases — backup target and backup schedule management.
// See managed-databases.md §7 for the full backup/restore design.
package databases

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// BackupTargetStore is the persistence the backup target service needs.
type BackupTargetStore interface {
	CreateBackupTarget(ctx context.Context, t domain.BackupTarget) (domain.BackupTarget, error)
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
	ListBackupTargets(ctx context.Context) ([]domain.BackupTarget, error)
	UpdateBackupTarget(ctx context.Context, t domain.BackupTarget) (domain.BackupTarget, error)
	DeleteBackupTarget(ctx context.Context, id string) error
}

// BackupTargetService manages S3-compatible backup destinations.
type BackupTargetService struct {
	store  BackupTargetStore
	sealer Sealer
}

// NewBackupTargetService wires the service.
func NewBackupTargetService(s BackupTargetStore, sealer Sealer) *BackupTargetService {
	return &BackupTargetService{store: s, sealer: sealer}
}

// BackupTargetInput is the caller-supplied config for a backup target.
type BackupTargetInput struct {
	Name       string
	Endpoint   string
	Bucket     string
	Region     string
	AccessKey  string // plaintext — sealed before storage
	SecretKey  string // plaintext — sealed before storage
	PathPrefix string
}

// CreateTarget validates and creates a backup target with sealed credentials.
func (s *BackupTargetService) CreateTarget(ctx context.Context, in BackupTargetInput) (domain.BackupTarget, error) {
	if err := validateTarget(in); err != nil {
		return domain.BackupTarget{}, err
	}

	akCT, akNonce, err := s.sealer.Seal([]byte(in.AccessKey))
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: sealing access key: %w", err)
	}
	skCT, skNonce, err := s.sealer.Seal([]byte(in.SecretKey))
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: sealing secret key: %w", err)
	}

	t := domain.BackupTarget{
		ID:             ids.New(ids.PrefixBackupTarget),
		Name:           in.Name,
		Endpoint:       in.Endpoint,
		Bucket:         in.Bucket,
		Region:         in.Region,
		AccessKeyCT:    akCT,
		AccessKeyNonce: akNonce,
		SecretKeyCT:    skCT,
		SecretKeyNonce: skNonce,
		PathPrefix:     in.PathPrefix,
	}
	created, err := s.store.CreateBackupTarget(ctx, t)
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: creating backup target: %w", err)
	}
	return created, nil
}

// GetTarget returns one backup target.
func (s *BackupTargetService) GetTarget(ctx context.Context, id string) (domain.BackupTarget, error) {
	t, err := s.store.GetBackupTarget(ctx, id)
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: getting backup target: %w", err)
	}
	return t, nil
}

// ListTargets returns all backup targets.
func (s *BackupTargetService) ListTargets(ctx context.Context) ([]domain.BackupTarget, error) {
	targets, err := s.store.ListBackupTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("databases: listing backup targets: %w", err)
	}
	return targets, nil
}

// UpdateTargetInput is a partial edit of a backup target. A nil field is
// "leave it alone", which is what makes rotating one credential possible
// without re-sending the other — the whole reason this is a PATCH and not a
// PUT. The two keys are write-only in both directions: they go in and are
// never read back, so a caller that omits them keeps what is stored.
type UpdateTargetInput struct {
	Name       *string
	Endpoint   *string
	Bucket     *string
	Region     *string
	AccessKey  *string
	SecretKey  *string
	PathPrefix *string
}

// UpdateTarget applies a partial edit. The merged result is validated as a
// whole, so a PATCH cannot leave a target in a state CreateTarget would have
// refused: emptying a required field is rejected the same way omitting it is.
func (s *BackupTargetService) UpdateTarget(ctx context.Context, id string, in UpdateTargetInput) (domain.BackupTarget, error) {
	t, err := s.store.GetBackupTarget(ctx, id)
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: getting backup target: %w", err)
	}

	// Validate against the merged view. The stored credentials are sealed and
	// not readable here, so their placeholders stand in for "already set" —
	// only a field the caller actually sent can fail the emptiness check.
	merged := BackupTargetInput{
		Name: t.Name, Endpoint: t.Endpoint, Bucket: t.Bucket, Region: t.Region,
		PathPrefix: t.PathPrefix,
		AccessKey:  "(stored)", SecretKey: "(stored)",
	}
	applyString(&merged.Name, in.Name)
	applyString(&merged.Endpoint, in.Endpoint)
	applyString(&merged.Bucket, in.Bucket)
	applyString(&merged.Region, in.Region)
	applyString(&merged.PathPrefix, in.PathPrefix)
	applyString(&merged.AccessKey, in.AccessKey)
	applyString(&merged.SecretKey, in.SecretKey)
	if err := validateTarget(merged); err != nil {
		return domain.BackupTarget{}, err
	}

	t.Name, t.Endpoint, t.Bucket = merged.Name, merged.Endpoint, merged.Bucket
	t.Region, t.PathPrefix = merged.Region, merged.PathPrefix

	// Credentials are resealed only when sent, so an unchanged key keeps its
	// existing ciphertext rather than being re-encrypted for no reason.
	if in.AccessKey != nil {
		ct, nonce, err := s.sealer.Seal([]byte(*in.AccessKey))
		if err != nil {
			return domain.BackupTarget{}, fmt.Errorf("databases: sealing access key: %w", err)
		}
		t.AccessKeyCT, t.AccessKeyNonce = ct, nonce
	}
	if in.SecretKey != nil {
		ct, nonce, err := s.sealer.Seal([]byte(*in.SecretKey))
		if err != nil {
			return domain.BackupTarget{}, fmt.Errorf("databases: sealing secret key: %w", err)
		}
		t.SecretKeyCT, t.SecretKeyNonce = ct, nonce
	}

	updated, err := s.store.UpdateBackupTarget(ctx, t)
	if err != nil {
		return domain.BackupTarget{}, fmt.Errorf("databases: updating backup target: %w", err)
	}
	return updated, nil
}

// applyString overwrites dst when the caller sent a value.
func applyString(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

// DeleteTarget removes a backup target. Fails if any backup schedule
// references it (RESTRICT FK).
func (s *BackupTargetService) DeleteTarget(ctx context.Context, id string) error {
	if err := s.store.DeleteBackupTarget(ctx, id); err != nil {
		return fmt.Errorf("databases: deleting backup target: %w", err)
	}
	return nil
}

func validateTarget(in BackupTargetInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return invalid("name is required")
	}
	if strings.TrimSpace(in.Endpoint) == "" {
		return invalid("endpoint is required")
	}
	if strings.TrimSpace(in.Bucket) == "" {
		return invalid("bucket is required")
	}
	if strings.TrimSpace(in.AccessKey) == "" {
		return invalid("access_key is required")
	}
	if strings.TrimSpace(in.SecretKey) == "" {
		return invalid("secret_key is required")
	}
	return nil
}

// --- Backup schedule management ---

// BackupScheduleStore is the persistence for backup schedules and records.
type BackupScheduleStore interface {
	CreateDatabaseBackup(ctx context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error)
	GetDatabaseBackup(ctx context.Context, id string) (domain.DatabaseBackup, error)
	ListDatabaseBackups(ctx context.Context, dbID string) ([]domain.DatabaseBackup, error)
	UpdateDatabaseBackup(ctx context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error)
	DeleteDatabaseBackup(ctx context.Context, id string) error
	GetDatabase(ctx context.Context, id string) (domain.Database, error)
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
	ListBackupRecords(ctx context.Context, backupID string) ([]domain.BackupRecord, error)
}

// BackupScheduleService manages backup configurations for databases.
type BackupScheduleService struct {
	store BackupScheduleStore
}

// NewBackupScheduleService wires the service.
func NewBackupScheduleService(s BackupScheduleStore) *BackupScheduleService {
	return &BackupScheduleService{store: s}
}

// BackupScheduleInput is the caller-supplied config for a backup schedule.
type BackupScheduleInput struct {
	TargetID       string
	Schedule       string // cron expression; empty = manual only
	RetentionCount int    // keep last N; defaults to 7
	Enabled        bool
}

// CreateSchedule creates a backup schedule for a database.
func (s *BackupScheduleService) CreateSchedule(ctx context.Context, dbID string, in BackupScheduleInput) (domain.DatabaseBackup, error) {
	if _, err := s.store.GetDatabase(ctx, dbID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.DatabaseBackup{}, invalid("database not found")
		}
		return domain.DatabaseBackup{}, fmt.Errorf("databases: getting database: %w", err)
	}
	if _, err := s.store.GetBackupTarget(ctx, in.TargetID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.DatabaseBackup{}, invalid("backup target not found")
		}
		return domain.DatabaseBackup{}, fmt.Errorf("databases: getting backup target: %w", err)
	}

	retention := in.RetentionCount
	if retention <= 0 {
		retention = 7
	}

	b := domain.DatabaseBackup{
		ID:             ids.New(ids.PrefixDatabaseBackup),
		DatabaseID:     dbID,
		TargetID:       in.TargetID,
		Schedule:       in.Schedule,
		RetentionCount: retention,
		Enabled:        in.Enabled,
	}
	created, err := s.store.CreateDatabaseBackup(ctx, b)
	if err != nil {
		return domain.DatabaseBackup{}, fmt.Errorf("databases: creating backup schedule: %w", err)
	}
	return created, nil
}

// UpdateScheduleInput is a partial edit of a backup schedule. Pausing a
// schedule without forgetting where it wrote, or changing the retention window
// without restating the cron expression, is the whole point of a PATCH here.
type UpdateScheduleInput struct {
	TargetID       *string
	Schedule       *string
	RetentionCount *int
	Enabled        *bool
}

// UpdateSchedule applies a partial edit. A target that does not exist is a
// validation failure rather than a foreign-key error surfacing from the
// database, and a retention of zero or less falls back to the same default
// creation uses, so the two paths cannot disagree about what "unset" means.
func (s *BackupScheduleService) UpdateSchedule(ctx context.Context, id string, in UpdateScheduleInput) (domain.DatabaseBackup, error) {
	b, err := s.store.GetDatabaseBackup(ctx, id)
	if err != nil {
		return domain.DatabaseBackup{}, fmt.Errorf("databases: getting backup schedule: %w", err)
	}
	if in.TargetID != nil {
		if _, err := s.store.GetBackupTarget(ctx, *in.TargetID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return domain.DatabaseBackup{}, invalid("backup target not found")
			}
			return domain.DatabaseBackup{}, fmt.Errorf("databases: getting backup target: %w", err)
		}
		b.TargetID = *in.TargetID
	}
	if in.Schedule != nil {
		b.Schedule = *in.Schedule
	}
	if in.RetentionCount != nil {
		b.RetentionCount = *in.RetentionCount
		if b.RetentionCount <= 0 {
			b.RetentionCount = 7
		}
	}
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	}

	updated, err := s.store.UpdateDatabaseBackup(ctx, b)
	if err != nil {
		return domain.DatabaseBackup{}, fmt.Errorf("databases: updating backup schedule: %w", err)
	}
	return updated, nil
}

// GetSchedule returns one backup schedule.
func (s *BackupScheduleService) GetSchedule(ctx context.Context, id string) (domain.DatabaseBackup, error) {
	b, err := s.store.GetDatabaseBackup(ctx, id)
	if err != nil {
		return domain.DatabaseBackup{}, fmt.Errorf("databases: getting backup schedule: %w", err)
	}
	return b, nil
}

// ListSchedules returns all backup schedules for a database.
func (s *BackupScheduleService) ListSchedules(ctx context.Context, dbID string) ([]domain.DatabaseBackup, error) {
	backups, err := s.store.ListDatabaseBackups(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("databases: listing backup schedules: %w", err)
	}
	return backups, nil
}

// DeleteSchedule removes a backup schedule.
func (s *BackupScheduleService) DeleteSchedule(ctx context.Context, id string) error {
	if err := s.store.DeleteDatabaseBackup(ctx, id); err != nil {
		return fmt.Errorf("databases: deleting backup schedule: %w", err)
	}
	return nil
}

// ListRecords returns the backup history for a schedule.
func (s *BackupScheduleService) ListRecords(ctx context.Context, backupID string) ([]domain.BackupRecord, error) {
	records, err := s.store.ListBackupRecords(ctx, backupID)
	if err != nil {
		return nil, fmt.Errorf("databases: listing backup records: %w", err)
	}
	return records, nil
}

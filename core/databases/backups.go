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

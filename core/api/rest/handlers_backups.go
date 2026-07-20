package rest

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// --- Backup Target DTOs ---

type backupTargetDTO struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Endpoint   string    `json:"endpoint"`
	Bucket     string    `json:"bucket"`
	Region     string    `json:"region"`
	PathPrefix string    `json:"path_prefix"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// AccessKey and SecretKey are never returned (rule 20).
}

func toBackupTargetDTO(t domain.BackupTarget) backupTargetDTO {
	return backupTargetDTO{
		ID:         t.ID,
		Name:       t.Name,
		Endpoint:   t.Endpoint,
		Bucket:     t.Bucket,
		Region:     t.Region,
		PathPrefix: t.PathPrefix,
		CreatedAt:  t.CreatedAt,
		UpdatedAt:  t.UpdatedAt,
	}
}

type createBackupTargetRequest struct {
	Name       string `json:"name"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	PathPrefix string `json:"path_prefix"`
}

// --- Backup Target Handlers ---

func (a *API) handleCreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req createBackupTargetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	t, err := a.deps.BackupTargets.CreateTarget(r.Context(), databases.BackupTargetInput{
		Name:       req.Name,
		Endpoint:   req.Endpoint,
		Bucket:     req.Bucket,
		Region:     req.Region,
		AccessKey:  req.AccessKey,
		SecretKey:  req.SecretKey,
		PathPrefix: req.PathPrefix,
	})
	if err != nil {
		var ve *databases.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Msg)
			return
		}
		a.deps.Log.Error("creating backup target", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create backup target")
		return
	}
	writeJSON(w, http.StatusCreated, toBackupTargetDTO(t))
}

func (a *API) handleListBackupTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := a.deps.BackupTargets.ListTargets(r.Context())
	if err != nil {
		a.deps.Log.Error("listing backup targets", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list backup targets")
		return
	}
	out := make([]backupTargetDTO, 0, len(targets))
	for _, t := range targets {
		out = append(out, toBackupTargetDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetBackupTarget(w http.ResponseWriter, r *http.Request) {
	t, err := a.deps.BackupTargets.GetTarget(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup target not found")
			return
		}
		a.deps.Log.Error("getting backup target", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get backup target")
		return
	}
	writeJSON(w, http.StatusOK, toBackupTargetDTO(t))
}

func (a *API) handleDeleteBackupTarget(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if err := a.deps.BackupTargets.DeleteTarget(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "backup target not found")
		case errors.Is(err, store.ErrInUse):
			writeError(w, http.StatusConflict, "backup target is in use by one or more backup schedules")
		default:
			a.deps.Log.Error("deleting backup target", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete backup target")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Database Backup Schedule DTOs ---

type databaseBackupDTO struct {
	ID             string     `json:"id"`
	DatabaseID     string     `json:"database_id"`
	TargetID       string     `json:"target_id"`
	Schedule       string     `json:"schedule"`
	RetentionCount int        `json:"retention_count"`
	Enabled        bool       `json:"enabled"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastStatus     string     `json:"last_status,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toDatabaseBackupDTO(b domain.DatabaseBackup) databaseBackupDTO {
	return databaseBackupDTO{
		ID:             b.ID,
		DatabaseID:     b.DatabaseID,
		TargetID:       b.TargetID,
		Schedule:       b.Schedule,
		RetentionCount: b.RetentionCount,
		Enabled:        b.Enabled,
		LastRunAt:      b.LastRunAt,
		LastStatus:     b.LastStatus,
		CreatedAt:      b.CreatedAt,
		UpdatedAt:      b.UpdatedAt,
	}
}

type backupRecordDTO struct {
	ID               string     `json:"id"`
	DatabaseBackupID string     `json:"database_backup_id"`
	ObjectKey        string     `json:"object_key"`
	SizeBytes        int64      `json:"size_bytes"`
	Status           string     `json:"status"`
	Detail           string     `json:"detail,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

func toBackupRecordDTO(r domain.BackupRecord) backupRecordDTO {
	return backupRecordDTO{
		ID:               r.ID,
		DatabaseBackupID: r.DatabaseBackupID,
		ObjectKey:        r.ObjectKey,
		SizeBytes:        r.SizeBytes,
		Status:           r.Status,
		Detail:           r.Detail,
		StartedAt:        r.StartedAt,
		FinishedAt:       r.FinishedAt,
		CreatedAt:        r.CreatedAt,
	}
}

type createDatabaseBackupRequest struct {
	TargetID       string `json:"target_id"`
	Schedule       string `json:"schedule"`
	RetentionCount int    `json:"retention_count"`
	Enabled        bool   `json:"enabled"`
}

// --- Database Backup Schedule Handlers ---

func (a *API) handleCreateDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	dbID := r.PathValue("id")
	var req createDatabaseBackupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	b, err := a.deps.BackupSchedules.CreateSchedule(r.Context(), dbID, databases.BackupScheduleInput{
		TargetID:       req.TargetID,
		Schedule:       req.Schedule,
		RetentionCount: req.RetentionCount,
		Enabled:        req.Enabled,
	})
	if err != nil {
		var ve *databases.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Msg)
			return
		}
		a.deps.Log.Error("creating database backup", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create backup schedule")
		return
	}
	writeJSON(w, http.StatusCreated, toDatabaseBackupDTO(b))
}

func (a *API) handleListDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	dbID := r.PathValue("id")
	backups, err := a.deps.BackupSchedules.ListSchedules(r.Context(), dbID)
	if err != nil {
		a.deps.Log.Error("listing database backups", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list backup schedules")
		return
	}
	out := make([]databaseBackupDTO, 0, len(backups))
	for _, b := range backups {
		out = append(out, toDatabaseBackupDTO(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleDeleteDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	bakID := r.PathValue("bak_id")
	if err := a.deps.BackupSchedules.DeleteSchedule(r.Context(), bakID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup schedule not found")
			return
		}
		a.deps.Log.Error("deleting database backup", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete backup schedule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListBackupRecords(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	bakID := r.PathValue("bak_id")
	records, err := a.deps.BackupSchedules.ListRecords(r.Context(), bakID)
	if err != nil {
		a.deps.Log.Error("listing backup records", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list backup records")
		return
	}
	out := make([]backupRecordDTO, 0, len(records))
	for _, rec := range records {
		out = append(out, toBackupRecordDTO(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

type runBackupResponse struct {
	Record backupRecordDTO `json:"record"`
}

// handleRunBackup triggers a backup for a schedule now: 202 with the running
// record; the outcome is reported later via the agent's event.
func (a *API) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	rec, err := a.deps.Backups.RunBackup(r.Context(), r.PathValue("bak_id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "backup schedule not found")
			return
		}
		a.deps.Log.Error("running backup", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start backup")
		return
	}
	writeJSON(w, http.StatusAccepted, runBackupResponse{Record: toBackupRecordDTO(rec)})
}

type restoreRequest struct {
	BackupRecordID string `json:"backup_record_id"`
	Confirm        bool   `json:"confirm"`
}

// handleRestoreDatabase triggers a destructive restore (requires confirm=true).
func (a *API) handleRestoreDatabase(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	var req restoreRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.BackupRecordID == "" {
		writeError(w, http.StatusBadRequest, "backup_record_id is required")
		return
	}
	err := a.deps.Backups.RunRestore(r.Context(), r.PathValue("id"), req.BackupRecordID, req.Confirm)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, scheduler.ErrRestoreNotConfirmed):
		writeError(w, http.StatusBadRequest, "restore is destructive: set confirm=true to proceed")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "database or backup record not found")
	default:
		a.deps.Log.Error("restoring database", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start restore")
	}
}

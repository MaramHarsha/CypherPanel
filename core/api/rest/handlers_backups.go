package rest

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
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

// patchBackupTargetRequest is a partial edit: an omitted field is left alone,
// which is what lets one credential rotate without re-sending the other.
type patchBackupTargetRequest struct {
	Name       *string `json:"name"`
	Endpoint   *string `json:"endpoint"`
	Bucket     *string `json:"bucket"`
	Region     *string `json:"region"`
	AccessKey  *string `json:"access_key"`
	SecretKey  *string `json:"secret_key"`
	PathPrefix *string `json:"path_prefix"`
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
	// The bucket and endpoint, never the access key beside them (§6).
	a.audit(r, audit.Entry{
		Action:   audit.ActionBackupTargetCreated,
		Resource: audit.Resource(audit.ResourceBackupTarget, t.ID, t.Name),
		Detail:   map[string]any{"endpoint": t.Endpoint, "bucket": t.Bucket, "region": t.Region},
	})
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

func (a *API) handlePatchBackupTarget(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req patchBackupTargetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t, err := a.deps.BackupTargets.UpdateTarget(r.Context(), r.PathValue("id"), databases.UpdateTargetInput{
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
		switch {
		case errors.As(err, &ve):
			writeError(w, http.StatusBadRequest, ve.Msg)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "backup target not found")
		default:
			a.deps.Log.Error("updating backup target", "target_id", r.PathValue("id"), "error", err)
			writeError(w, http.StatusInternalServerError, "could not update backup target")
		}
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionBackupTargetUpdated,
		Resource: audit.Resource(audit.ResourceBackupTarget, t.ID, t.Name),
	})
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
	a.audit(r, audit.Entry{
		Action:   audit.ActionBackupTargetDeleted,
		Resource: audit.Resource(audit.ResourceBackupTarget, r.PathValue("id"), ""),
	})
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
	// Enabled defaults to true when omitted (a schedule created disabled would
	// silently never run) — same *bool pattern as notifiers and scheduled tasks.
	Enabled *bool `json:"enabled"`
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

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	b, err := a.deps.BackupSchedules.CreateSchedule(r.Context(), dbID, databases.BackupScheduleInput{
		TargetID:       req.TargetID,
		Schedule:       req.Schedule,
		RetentionCount: req.RetentionCount,
		Enabled:        enabled,
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
	a.audit(r, audit.Entry{
		Action:        audit.ActionBackupScheduleCreated,
		Resource:      audit.Resource(audit.ResourceBackupSchedule, b.ID, b.Schedule),
		EnvironmentID: a.auditScopeForDatabase(r.Context(), b.DatabaseID),
		Detail:        map[string]any{"database_id": b.DatabaseID, "target_id": b.TargetID, "retention_count": b.RetentionCount},
	})
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

// patchDatabaseBackupRequest is a partial edit of a schedule — pause it, move
// it to another target, or change the retention window, one at a time.
type patchDatabaseBackupRequest struct {
	TargetID       *string `json:"target_id"`
	Schedule       *string `json:"schedule"`
	RetentionCount *int    `json:"retention_count"`
	Enabled        *bool   `json:"enabled"`
}

func (a *API) handlePatchDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, r.PathValue("id"))
	}) {
		return
	}
	var req patchDatabaseBackupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	b, err := a.deps.BackupSchedules.UpdateSchedule(r.Context(), r.PathValue("bak_id"), databases.UpdateScheduleInput{
		TargetID:       req.TargetID,
		Schedule:       req.Schedule,
		RetentionCount: req.RetentionCount,
		Enabled:        req.Enabled,
	})
	if err != nil {
		var ve *databases.ValidationError
		switch {
		case errors.As(err, &ve):
			writeError(w, http.StatusBadRequest, ve.Msg)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "backup schedule not found")
		default:
			a.deps.Log.Error("updating database backup", "backup_id", r.PathValue("bak_id"), "error", err)
			writeError(w, http.StatusInternalServerError, "could not update backup schedule")
		}
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionBackupScheduleUpdated,
		Resource:      audit.Resource(audit.ResourceBackupSchedule, b.ID, b.Schedule),
		EnvironmentID: a.auditScopeForDatabase(r.Context(), b.DatabaseID),
		Detail:        map[string]any{"database_id": b.DatabaseID, "enabled": b.Enabled},
	})
	writeJSON(w, http.StatusOK, toDatabaseBackupDTO(b))
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
	a.audit(r, audit.Entry{
		Action:        audit.ActionBackupScheduleDeleted,
		Resource:      audit.Resource(audit.ResourceBackupSchedule, bakID, ""),
		EnvironmentID: a.auditScopeForDatabase(r.Context(), r.PathValue("id")),
		Detail:        map[string]any{"database_id": r.PathValue("id")},
	})
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
	a.audit(r, audit.Entry{
		Action:        audit.ActionBackupRunRequested,
		Resource:      audit.Resource(audit.ResourceBackupSchedule, r.PathValue("bak_id"), ""),
		EnvironmentID: a.auditScopeForDatabase(r.Context(), r.PathValue("id")),
		Detail:        map[string]any{"database_id": r.PathValue("id"), "record_id": rec.ID},
	})
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
	restore, err := a.deps.Backups.RunRestore(r.Context(), r.PathValue("id"), req.BackupRecordID, req.Confirm)
	switch {
	case err == nil:
		// The most destructive verb the API has: it overwrites live data from a
		// snapshot. Who asked, and from which record.
		a.auditDatabaseRestore(r, req.BackupRecordID)
		// The record, not an empty 202: the caller has to be able to follow an
		// operation that takes the database offline while it runs.
		writeJSON(w, http.StatusAccepted, toRestoreDTO(restore))
	case errors.Is(err, scheduler.ErrRestoreNotConfirmed):
		writeError(w, http.StatusBadRequest, "restore is destructive: set confirm=true to proceed")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "database or backup record not found")
	default:
		a.deps.Log.Error("restoring database", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start restore")
	}
}

// auditDatabaseRestore records a restore against the database it overwrites,
// resolving the name and environment for the snapshot.
func (a *API) auditDatabaseRestore(r *http.Request, recordID string) {
	if a.deps.Audit == nil || a.deps.Databases == nil {
		return
	}
	db, _ := a.deps.Databases.Get(r.Context(), r.PathValue("id"))
	a.audit(r, audit.Entry{
		Action:        audit.ActionDatabaseRestored,
		Resource:      audit.Resource(audit.ResourceDatabase, r.PathValue("id"), db.Name),
		EnvironmentID: db.EnvironmentID,
		Detail:        map[string]any{"backup_record_id": recordID},
	})
}

// ─── restore progress (managed-databases.md §"Restoring", canvas 10d) ────────

type restoreDTO struct {
	ID             string     `json:"id"`
	DatabaseID     string     `json:"database_id"`
	BackupRecordID string     `json:"backup_record_id,omitempty"`
	Status         string     `json:"status"`
	Step           string     `json:"step,omitempty"`
	BytesDone      int64      `json:"bytes_done"`
	BytesTotal     int64      `json:"bytes_total"`
	Detail         string     `json:"detail,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func toRestoreDTO(r domain.DatabaseRestore) restoreDTO {
	return restoreDTO{
		ID: r.ID, DatabaseID: r.DatabaseID, BackupRecordID: r.BackupRecordID,
		Status: r.Status, Step: r.Step,
		BytesDone: r.BytesDone, BytesTotal: r.BytesTotal, Detail: r.Detail,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// handleListDatabaseRestores returns a database's restore history, newest
// first. A restore is not a backup: the history of what was put back is a
// different question from the history of what was taken.
func (a *API) handleListDatabaseRestores(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	dbID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, dbID)
	}) {
		return
	}
	if a.deps.Restores == nil {
		writeError(w, http.StatusNotImplemented, "restores are not enabled")
		return
	}
	list, err := a.deps.Restores.ListDatabaseRestores(r.Context(), dbID, 50)
	if err != nil {
		a.deps.Log.Error("listing database restores", "db_id", dbID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list restores")
		return
	}
	out := make([]restoreDTO, 0, len(list))
	for _, rec := range list {
		out = append(out, toRestoreDTO(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetDatabaseRestore returns one restore. The blocking popup polls this
// while it is running, and reopens onto it when someone closes the tab and
// comes back to a database that is still offline.
func (a *API) handleGetDatabaseRestore(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	dbID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForDatabase(ctx, dbID)
	}) {
		return
	}
	if a.deps.Restores == nil {
		writeError(w, http.StatusNotImplemented, "restores are not enabled")
		return
	}
	rec, err := a.deps.Restores.GetDatabaseRestore(r.Context(), r.PathValue("rid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "restore not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting database restore", "restore_id", r.PathValue("rid"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the restore")
		return
	}
	// The restore is addressed under its database, so one that belongs to a
	// different database is not found here even though the id exists.
	if rec.DatabaseID != dbID {
		writeError(w, http.StatusNotFound, "restore not found")
		return
	}
	writeJSON(w, http.StatusOK, toRestoreDTO(rec))
}

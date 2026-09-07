package rest

// Volume backups (volume-backups.md §3). One schedule per application, covering
// every volume the application marks as backed up.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

type volumeBackupDTO struct {
	ID             string  `json:"id"`
	ApplicationID  string  `json:"application_id"`
	TargetID       string  `json:"target_id"`
	Schedule       string  `json:"schedule"`
	RetentionCount int     `json:"retention_count"`
	Enabled        bool    `json:"enabled"`
	LastRunAt      *string `json:"last_run_at"`
	LastStatus     string  `json:"last_status"`
}

type volumeRecordDTO struct {
	ID         string  `json:"id"`
	VolumeName string  `json:"volume_name"`
	ObjectKey  string  `json:"object_key"`
	SizeBytes  int64   `json:"size_bytes"`
	Status     string  `json:"status"`
	Detail     string  `json:"detail"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

type setVolumeBackupRequest struct {
	TargetID       string `json:"target_id"`
	Schedule       string `json:"schedule"`
	RetentionCount int    `json:"retention_count"`
	Enabled        bool   `json:"enabled"`
}

func toVolumeBackupDTO(v domain.VolumeBackup) volumeBackupDTO {
	return volumeBackupDTO{
		ID: v.ID, ApplicationID: v.ApplicationID, TargetID: v.TargetID,
		Schedule: v.Schedule, RetentionCount: v.RetentionCount, Enabled: v.Enabled,
		LastRunAt: formatTime(v.LastRunAt), LastStatus: v.LastStatus,
	}
}

func toVolumeRecordDTO(r domain.VolumeBackupRecord) volumeRecordDTO {
	return volumeRecordDTO{
		ID: r.ID, VolumeName: r.VolumeName, ObjectKey: r.ObjectKey, SizeBytes: r.SizeBytes,
		Status: r.Status, Detail: r.Detail,
		StartedAt:  r.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		FinishedAt: formatTime(r.FinishedAt),
	}
}

func (a *API) volumeBackupAuthorized(w http.ResponseWriter, r *http.Request, role string) bool {
	if a.deps.VolumeBackups == nil {
		writeError(w, http.StatusNotImplemented, "volume backups are not enabled on this panel")
		return false
	}
	user, _ := userFromContext(r.Context())
	return a.authorizeResolved(w, r, user, role, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	})
}

func (a *API) handleGetVolumeBackup(w http.ResponseWriter, r *http.Request) {
	if !a.volumeBackupAuthorized(w, r, domain.RoleMember) {
		return
	}
	v, err := a.deps.VolumeBackups.GetVolumeBackupByApplication(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		// No schedule is a normal state, not a 404 on the application: the UI
		// renders "not backed up" from it rather than an error.
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		a.deps.Log.Error("reading volume backup schedule", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the schedule")
		return
	}
	writeJSON(w, http.StatusOK, toVolumeBackupDTO(v))
}

func (a *API) handleSetVolumeBackup(w http.ResponseWriter, r *http.Request) {
	if !a.volumeBackupAuthorized(w, r, domain.RoleMember) {
		return
	}
	var req setVolumeBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TargetID == "" {
		writeError(w, http.StatusBadRequest, "a backup target is required")
		return
	}
	if req.RetentionCount < 1 {
		req.RetentionCount = 7
	}
	id := ids.New(ids.PrefixVolumeBackup)
	if existing, err := a.deps.VolumeBackups.GetVolumeBackupByApplication(r.Context(), r.PathValue("id")); err == nil {
		id = existing.ID
	}
	v, err := a.deps.VolumeBackups.UpsertVolumeBackup(r.Context(), domain.VolumeBackup{
		ID: id, ApplicationID: r.PathValue("id"), TargetID: req.TargetID,
		Schedule: req.Schedule, RetentionCount: req.RetentionCount, Enabled: req.Enabled,
	})
	if err != nil {
		a.deps.Log.Error("saving volume backup schedule", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the schedule")
		return
	}
	a.audit(r, a.appAuditEntry(r.Context(), audit.ActionVolumeBackupChanged, r.PathValue("id"), map[string]any{
		"schedule": req.Schedule, "retention_count": req.RetentionCount, "enabled": req.Enabled,
	}))
	writeJSON(w, http.StatusOK, toVolumeBackupDTO(v))
}

func (a *API) handleDeleteVolumeBackup(w http.ResponseWriter, r *http.Request) {
	if !a.volumeBackupAuthorized(w, r, domain.RoleMember) {
		return
	}
	if err := a.deps.VolumeBackups.DeleteVolumeBackup(r.Context(), r.PathValue("id")); err != nil {
		a.deps.Log.Error("deleting volume backup schedule", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove the schedule")
		return
	}
	a.audit(r, a.appAuditEntry(r.Context(), audit.ActionVolumeBackupChanged, r.PathValue("id"), map[string]any{"schedule": "removed"}))
	w.WriteHeader(http.StatusNoContent)
}

// handleRunVolumeBackup archives every flagged volume now. An application with
// nothing flagged answers 200 with an empty list rather than an error: that is
// an operator who has a schedule and has not marked a directory yet, and saying
// so is more useful than refusing.
func (a *API) handleRunVolumeBackup(w http.ResponseWriter, r *http.Request) {
	if !a.volumeBackupAuthorized(w, r, domain.RoleMember) {
		return
	}
	if a.deps.Backups == nil {
		writeError(w, http.StatusNotImplemented, "backups are not enabled on this panel")
		return
	}
	recs, err := a.deps.Backups.RunVolumeBackup(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "this application has no volume backup schedule — add one first")
		return
	}
	if err != nil {
		a.deps.Log.Error("running volume backup", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the backup")
		return
	}
	out := make([]volumeRecordDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toVolumeRecordDTO(rec))
	}
	a.audit(r, a.appAuditEntry(r.Context(), audit.ActionVolumeBackupRan, r.PathValue("id"), map[string]any{"volumes": len(out)}))
	writeJSON(w, http.StatusAccepted, out)
}

func (a *API) handleVolumeBackupHistory(w http.ResponseWriter, r *http.Request) {
	if !a.volumeBackupAuthorized(w, r, domain.RoleMember) {
		return
	}
	v, err := a.deps.VolumeBackups.GetVolumeBackupByApplication(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, []volumeRecordDTO{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the history")
		return
	}
	recs, err := a.deps.VolumeBackups.ListVolumeBackupRecords(r.Context(), v.ID, 50)
	if err != nil {
		a.deps.Log.Error("listing volume backup records", "app_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the history")
		return
	}
	out := make([]volumeRecordDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toVolumeRecordDTO(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

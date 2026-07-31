package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/secretcrypt"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// BackupsHandler manages backup destinations (root-admin fleet infrastructure)
// and per-account backup runs (root admin + resellers, account-scoped).
//
// Destination credentials are encrypted at rest and never leave this process
// toward a browser — they are decrypted only for agents, over mTLS gRPC.
type BackupsHandler struct {
	Accounts  *store.Accounts
	Backups   *store.Backups
	Databases *store.Databases
	Tasks     *store.Tasks
	Publisher *jobs.Publisher
	Crypt     *secretcrypt.Cipher
	Audit     *audit.Logger
}

// DestinationCredentials is the secret half of a destination: the restic
// repository password plus any backend credentials restic needs in its
// environment. Stored only as an AES-GCM blob.
type DestinationCredentials struct {
	Password string            `json:"password"`
	Env      map[string]string `json:"env,omitempty"`
}

type destinationResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind"`
	Repository       string     `json:"repository"`
	Schedule         string     `json:"schedule"`
	RetentionDaily   int        `json:"retention_daily"`
	RetentionWeekly  int        `json:"retention_weekly"`
	RetentionMonthly int        `json:"retention_monthly"`
	LastRunAt        *time.Time `json:"last_run_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// toDestinationResponse deliberately drops credentials_encrypted — there is no
// API path, for any role, that returns a destination's secrets.
func toDestinationResponse(d store.BackupDestination) destinationResponse {
	return destinationResponse{
		ID: d.ID, Name: d.Name, Kind: d.Kind, Repository: d.Repository,
		Schedule: d.Schedule, RetentionDaily: d.RetentionDaily,
		RetentionWeekly: d.RetentionWeekly, RetentionMonthly: d.RetentionMonthly,
		LastRunAt: d.LastRunAt, CreatedAt: d.CreatedAt,
	}
}

type backupResponse struct {
	ID            string     `json:"id"`
	AccountID     string     `json:"account_id"`
	DestinationID string     `json:"destination_id"`
	TaskID        string     `json:"task_id,omitempty"`
	SnapshotID    string     `json:"snapshot_id,omitempty"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	SizeBytes     int64      `json:"size_bytes"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

func toBackupResponse(b store.AccountBackup) backupResponse {
	return backupResponse{
		ID: b.ID, AccountID: b.AccountID, DestinationID: b.DestinationID,
		TaskID: b.TaskID, SnapshotID: b.SnapshotID, Kind: b.Kind, Status: b.Status,
		SizeBytes: b.SizeBytes, Error: b.Error, StartedAt: b.StartedAt, CompletedAt: b.CompletedAt,
	}
}

var validDestinationKinds = map[string]bool{"local": true, "s3": true, "sftp": true, "rest": true}
var validSchedules = map[string]bool{"off": true, "daily": true, "weekly": true}

// ListDestinations returns every backup destination (without credentials).
//
//	@Summary  List backup destinations
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} destinationResponse
//	@Security BearerAuth
//	@Router   /admin/backup/destinations [get]
func (h *BackupsHandler) ListDestinations(c *gin.Context) {
	items, err := h.Backups.ListDestinations(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]destinationResponse, 0, len(items))
	for _, d := range items {
		out = append(out, toDestinationResponse(d))
	}
	c.JSON(http.StatusOK, out)
}

type createDestinationRequest struct {
	Name             string            `json:"name" binding:"required"`
	Kind             string            `json:"kind" binding:"required"`
	Repository       string            `json:"repository" binding:"required"`
	Password         string            `json:"password" binding:"required,min=8"`
	Env              map[string]string `json:"env"`
	Schedule         string            `json:"schedule"`
	RetentionDaily   int               `json:"retention_daily"`
	RetentionWeekly  int               `json:"retention_weekly"`
	RetentionMonthly int               `json:"retention_monthly"`
}

// CreateDestination registers a restic repository as a backup target.
//
//	@Summary  Create a backup destination
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body createDestinationRequest true "Destination"
//	@Success  201 {object} destinationResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/backup/destinations [post]
func (h *BackupsHandler) CreateDestination(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req createDestinationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, kind, repository and a password (8+ chars) are required"})
		return
	}
	if !validDestinationKinds[req.Kind] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be one of local, s3, sftp, rest"})
		return
	}
	if req.Schedule == "" {
		req.Schedule = "off"
	}
	if !validSchedules[req.Schedule] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "schedule must be one of off, daily, weekly"})
		return
	}
	// Defaults chosen so a destination is never created with an all-zero
	// retention policy, which restic would read as "keep nothing".
	if req.RetentionDaily == 0 && req.RetentionWeekly == 0 && req.RetentionMonthly == 0 {
		req.RetentionDaily, req.RetentionWeekly, req.RetentionMonthly = 7, 4, 6
	}
	if h.Crypt == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "secret encryption is not configured"})
		return
	}

	blob, err := json.Marshal(DestinationCredentials{Password: req.Password, Env: req.Env})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	enc, err := h.Crypt.Encrypt(blob)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	dest, err := h.Backups.CreateDestination(c.Request.Context(), store.BackupDestination{
		Name: req.Name, Kind: req.Kind, Repository: req.Repository,
		CredentialsEncrypted: enc, Schedule: req.Schedule,
		RetentionDaily: req.RetentionDaily, RetentionWeekly: req.RetentionWeekly,
		RetentionMonthly: req.RetentionMonthly,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create destination (duplicate name?)"})
		return
	}

	// The audit detail records the repository but never the password.
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "backup.destination.create", TargetType: "backup_destination", TargetID: dest.ID,
		Detail: map[string]any{"name": dest.Name, "kind": dest.Kind, "repository": dest.Repository},
		IP:     c.ClientIP(),
	})
	c.JSON(http.StatusCreated, toDestinationResponse(*dest))
}

type updateDestinationRequest struct {
	Schedule         string `json:"schedule" binding:"required"`
	RetentionDaily   int    `json:"retention_daily"`
	RetentionWeekly  int    `json:"retention_weekly"`
	RetentionMonthly int    `json:"retention_monthly"`
}

// UpdateDestination changes a destination's schedule and retention policy.
//
//	@Summary  Update a backup destination's schedule and retention
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    destid  path string                   true "Destination ID"
//	@Param    request body updateDestinationRequest true "Schedule"
//	@Success  200 {object} destinationResponse
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/backup/destinations/{destid} [patch]
func (h *BackupsHandler) UpdateDestination(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req updateDestinationRequest
	if err := c.ShouldBindJSON(&req); err != nil || !validSchedules[req.Schedule] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "schedule must be one of off, daily, weekly"})
		return
	}
	// Refuse a policy that keeps nothing: restic would prune every snapshot,
	// and a silently emptied repository is not a recoverable mistake.
	if req.RetentionDaily <= 0 && req.RetentionWeekly <= 0 && req.RetentionMonthly <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "retention must keep at least one daily, weekly or monthly snapshot"})
		return
	}
	id := c.Param("destid")
	err := h.Backups.UpdateDestinationSchedule(c.Request.Context(), id, req.Schedule,
		req.RetentionDaily, req.RetentionWeekly, req.RetentionMonthly)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "destination not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	dest, err := h.Backups.GetDestination(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "backup.destination.update", TargetType: "backup_destination", TargetID: id,
		Detail: map[string]any{"schedule": req.Schedule}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, toDestinationResponse(*dest))
}

// DeleteDestination removes a destination. The remote repository itself is
// left untouched — deleting a panel record must never destroy the only copy of
// somebody's data.
//
//	@Summary  Delete a backup destination
//	@Tags     admin
//	@Produce  json
//	@Param    destid path string true "Destination ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/backup/destinations/{destid} [delete]
func (h *BackupsHandler) DeleteDestination(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	id := c.Param("destid")
	err := h.Backups.DeleteDestination(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "destination not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "backup.destination.delete", TargetType: "backup_destination", TargetID: id,
		IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "note": "the remote repository was not modified"})
}

func (h *BackupsHandler) scopedAccount(c *gin.Context) *store.Account {
	claims := auth.ClaimsFrom(c)
	account, err := h.Accounts.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return nil
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return nil
	}
	return account
}

// ListBackups returns an account's backup history, most recent first.
//
//	@Summary  List an account's backups
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {array} backupResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/backups [get]
func (h *BackupsHandler) ListBackups(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	items, err := h.Backups.ListByAccount(c.Request.Context(), account.ID, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]backupResponse, 0, len(items))
	for _, b := range items {
		out = append(out, toBackupResponse(b))
	}
	c.JSON(http.StatusOK, out)
}

type runBackupRequest struct {
	DestinationID string `json:"destination_id" binding:"required"`
}

// RunBackup dispatches an on-demand backup of the account.
//
//	@Summary  Back up an account now
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string           true "Account ID"
//	@Param    request body runBackupRequest true "Destination"
//	@Success  202 {object} backupResponse
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/backups [post]
func (h *BackupsHandler) RunBackup(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	var req runBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "destination_id is required"})
		return
	}
	dest, err := h.Backups.GetDestination(c.Request.Context(), req.DestinationID)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "destination not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	run, err := h.Dispatch(c.Request.Context(), account, dest, "manual", claims.Subject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not dispatch backup"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "backup.run", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"destination": dest.Name}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, toBackupResponse(*run))
}

// Dispatch creates the task + tracking row for one backup run. It is shared by
// the on-demand endpoint and the scheduler so both record history identically —
// a scheduled backup that failed must be as visible as a manual one.
func (h *BackupsHandler) Dispatch(ctx context.Context, account *store.Account, dest *store.BackupDestination, kind, actorID string) (*store.AccountBackup, error) {
	// Coordinate DB dumps with the file snapshot: a file backup taken while a
	// database is mid-write is not restorable.
	var dbNames []string
	if dbs, err := h.Databases.ListByAccount(ctx, account.ID); err == nil {
		for _, d := range dbs {
			if d.Status == "active" {
				dbNames = append(dbNames, d.Name)
			}
		}
	}

	payload, err := json.Marshal(jobs.BackupRunPayload{
		DestinationID: dest.ID,
		AccountID:     account.ID,
		Username:      account.SystemUsername,
		Databases:     dbNames,
		Excludes:      defaultExcludes,
		Retention: jobs.BackupRetention{
			Daily: dest.RetentionDaily, Weekly: dest.RetentionWeekly, Monthly: dest.RetentionMonthly,
		},
	})
	if err != nil {
		return nil, err
	}

	task, err := h.Tasks.Create(ctx, account.ServerID, jobs.TypeBackupRun, payload, actorID, account.ID)
	if err != nil {
		return nil, err
	}
	run, err := h.Backups.CreateRun(ctx, account.ID, dest.ID, task.ID, kind)
	if err != nil {
		return nil, err
	}
	if err := h.Publisher.Publish(ctx, jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	}); err != nil {
		slog.Error("dispatching backup", "account_id", account.ID, "error", err)
		_ = h.Backups.CompleteRun(ctx, run.ID, "", 0, "dispatch failed: "+err.Error())
		return nil, err
	}
	return run, nil
}

// defaultExcludes keep regenerable junk out of every snapshot. Caches and
// version-control checkouts dominate a typical account's inode count and
// restore to nothing useful.
var defaultExcludes = []string{
	"**/.cache", "**/node_modules", "**/vendor/composer/tmp*",
	"**/*.log", "**/tmp", "**/.git",
}

type restoreRequest struct {
	SnapshotID string `json:"snapshot_id" binding:"required"`
	// Target is optional. Empty restores into the agent's staging area for
	// inspection; "home" restores in place over the account's live data.
	Target string `json:"target"`
}

// RestoreBackup dispatches a restore of one snapshot.
//
//	@Summary  Restore an account from a snapshot
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id       path string          true "Account ID"
//	@Param    backupid path string          true "Backup ID"
//	@Param    request  body restoreRequest  true "Snapshot"
//	@Success  202 {object} backupResponse
//	@Failure  400 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/backups/{backupid}/restore [post]
func (h *BackupsHandler) RestoreBackup(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "snapshot_id is required"})
		return
	}
	src, err := h.Backups.GetRun(c.Request.Context(), c.Param("backupid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && src.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "backup not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if req.Target != "" && req.Target != "home" {
		c.JSON(http.StatusBadRequest, gin.H{"error": `target must be empty (staging) or "home" (in place)`})
		return
	}

	payload, err := json.Marshal(jobs.BackupRestorePayload{
		DestinationID: src.DestinationID,
		AccountID:     account.ID,
		Username:      account.SystemUsername,
		SnapshotID:    strings.TrimSpace(req.SnapshotID),
		Target:        req.Target,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	task, err := h.Tasks.Create(c.Request.Context(), account.ServerID, jobs.TypeBackupRestore, payload, claims.Subject, account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	run, err := h.Backups.CreateRun(c.Request.Context(), account.ID, src.DestinationID, task.ID, "restore")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.Publisher.Publish(c.Request.Context(), jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	}); err != nil {
		_ = h.Backups.CompleteRun(c.Request.Context(), run.ID, "", 0, "dispatch failed: "+err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "backup.restore", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"snapshot_id": req.SnapshotID, "target": req.Target}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, toBackupResponse(*run))
}

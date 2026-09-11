package rest

// The control plane's own disaster recovery (plane-disaster-recovery.md §9).
//
// PANEL OWNER, session only, on everything. Arming this decides where a
// complete copy of the panel — including the master key — is written, and
// reading the Recovery Key back is impossible by construction rather than by
// permission: the panel never had it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// PlaneDRService is the disaster recovery surface (consumer-defined).
type PlaneDRService interface {
	Config(ctx context.Context) (domain.PlaneDRConfig, bool)
	Arm(ctx context.Context, c domain.PlaneDRConfig, generate bool) (domain.PlaneDRConfig, string, error)
	Disarm(ctx context.Context) error
	RunNow(ctx context.Context) (domain.PlaneSnapshot, error)
	Snapshots(ctx context.Context, limit int) ([]domain.PlaneSnapshot, error)
	Verify(ctx context.Context, identity string, fetch func(ctx context.Context, target domain.BackupTarget, key string) ([]byte, error)) error
}

type planeDRDTO struct {
	Armed          bool   `json:"armed"`
	TargetID       string `json:"target_id"`
	PathPrefix     string `json:"path_prefix"`
	Schedule       string `json:"schedule"`
	RetentionCount int    `json:"retention_count"`
	// Recipient is the PUBLIC half, safe to return. The private half is not
	// here because the panel does not have it — there is no endpoint that
	// could return it and no column that holds it.
	Recipient     string `json:"recipient"`
	RecipientMode string `json:"recipient_mode"`
	// RecipientVerifiedAt null means armed but NOT PROVEN. "Armed" and
	// "recoverable" are different claims, and the panel must not make the
	// second on the strength of the first.
	RecipientVerifiedAt *string `json:"recipient_verified_at"`
	LastRunAt           *string `json:"last_run_at"`
	LastStatus          string  `json:"last_status"`
	LastDetail          string  `json:"last_detail"`
}

type planeSnapshotDTO struct {
	ID            string  `json:"id"`
	ObjectKey     string  `json:"object_key"`
	PanelVersion  string  `json:"panel_version"`
	SchemaVersion int64   `json:"schema_version"`
	SizeBytes     int64   `json:"size_bytes"`
	SHA256        string  `json:"sha256"`
	RowCount      int64   `json:"row_count"`
	Status        string  `json:"status"`
	Detail        string  `json:"detail"`
	StartedAt     string  `json:"started_at"`
	FinishedAt    *string `json:"finished_at"`
}

type armPlaneDRRequest struct {
	TargetID       string `json:"target_id"`
	PathPrefix     string `json:"path_prefix"`
	Schedule       string `json:"schedule"`
	RetentionCount int    `json:"retention_count"`
	// Generate asks the panel to mint the pair. The private half comes back in
	// THIS response and never again.
	Generate bool `json:"generate"`
	// Recipient is an age public key the operator already holds the private
	// half of — their own key from a password manager, or a team's shared one.
	Recipient string `json:"recipient"`
}

type armPlaneDRResponse struct {
	Config planeDRDTO `json:"config"`
	// RecoveryKey is present EXACTLY ONCE, in the response to the request that
	// generated it. It is the only thing that can open a snapshot and it is
	// exactly as powerful as the master key, because the master key is inside.
	RecoveryKey string `json:"recovery_key,omitempty"`
}

func planeDRDTOOf(c domain.PlaneDRConfig, armed bool) planeDRDTO {
	if !armed {
		return planeDRDTO{Armed: false}
	}
	out := planeDRDTO{
		Armed: true, TargetID: c.TargetID, PathPrefix: c.PathPrefix,
		Schedule: c.Schedule, RetentionCount: c.RetentionCount,
		Recipient: c.Recipient, RecipientMode: c.RecipientMode,
		LastStatus: c.LastStatus, LastDetail: c.LastDetail,
	}
	if c.RecipientVerifiedAt != nil {
		s := c.RecipientVerifiedAt.UTC().Format(time.RFC3339)
		out.RecipientVerifiedAt = &s
	}
	if c.LastRunAt != nil {
		s := c.LastRunAt.UTC().Format(time.RFC3339)
		out.LastRunAt = &s
	}
	return out
}

func (a *API) planeDRReady(w http.ResponseWriter) bool {
	if a.deps.PlaneDR == nil {
		writeError(w, http.StatusNotImplemented, "plane backups are not enabled on this panel")
		return false
	}
	return true
}

func (a *API) handleGetPlaneDR(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	cfg, armed := a.deps.PlaneDR.Config(r.Context())
	writeJSON(w, http.StatusOK, planeDRDTOOf(cfg, armed))
}

func (a *API) handleArmPlaneDR(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req armPlaneDRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TargetID == "" {
		writeError(w, http.StatusBadRequest, "pick the backup target the snapshots are written to")
		return
	}
	if !req.Generate && req.Recipient == "" {
		writeError(w, http.StatusBadRequest, "either let the panel generate a recovery key, or give it a public key you already hold")
		return
	}
	cfg, identity, err := a.deps.PlaneDR.Arm(r.Context(), domain.PlaneDRConfig{
		TargetID: req.TargetID, PathPrefix: req.PathPrefix, Schedule: req.Schedule,
		RetentionCount: req.RetentionCount, Recipient: req.Recipient,
	}, req.Generate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The audit record names the destination and the mode, NEVER the key —
	// neither half. The public half is harmless and the private half is not
	// ours to have.
	a.audit(r, audit.Entry{
		Action:   audit.ActionPlaneDRArmed,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "disaster recovery"),
		Detail: map[string]any{
			"target_id": cfg.TargetID, "schedule": cfg.Schedule,
			"retention_count": cfg.RetentionCount, "recipient_mode": cfg.RecipientMode,
		},
	})
	writeJSON(w, http.StatusOK, armPlaneDRResponse{
		Config: planeDRDTOOf(cfg, true), RecoveryKey: identity,
	})
}

func (a *API) handleDisarmPlaneDR(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if err := a.deps.PlaneDR.Disarm(r.Context()); err != nil {
		a.deps.Log.Error("disarming plane backups", "error", err)
		writeError(w, http.StatusInternalServerError, "could not disarm")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPlaneDRDisarmed,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "disaster recovery"),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRunPlaneDR(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	sn, err := a.deps.PlaneDR.RunNow(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "disaster recovery is not armed")
		return
	}
	if err != nil {
		a.deps.Log.Error("taking a plane snapshot", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPlaneSnapshotTaken,
		Resource: audit.Resource(audit.ResourcePanel, sn.ID, sn.ObjectKey),
		Detail:   map[string]any{"rows": sn.RowCount, "bytes": sn.SizeBytes},
	})
	writeJSON(w, http.StatusAccepted, toPlaneSnapshotDTO(sn))
}

func toPlaneSnapshotDTO(sn domain.PlaneSnapshot) planeSnapshotDTO {
	var finished *string
	if sn.FinishedAt != nil {
		s := sn.FinishedAt.UTC().Format(time.RFC3339)
		finished = &s
	}
	return planeSnapshotDTO{
		ID: sn.ID, ObjectKey: sn.ObjectKey, PanelVersion: sn.PanelVersion,
		SchemaVersion: sn.SchemaVersion, SizeBytes: sn.SizeBytes, SHA256: sn.SHA256,
		RowCount: sn.RowCount, Status: sn.Status, Detail: sn.Detail,
		StartedAt: sn.StartedAt.UTC().Format(time.RFC3339), FinishedAt: finished,
	}
}

func (a *API) handleListPlaneSnapshots(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	snaps, err := a.deps.PlaneDR.Snapshots(r.Context(), 30)
	if err != nil {
		a.deps.Log.Error("listing plane snapshots", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the snapshots")
		return
	}
	out := make([]planeSnapshotDTO, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, toPlaneSnapshotDTO(sn))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleVerifyPlaneDR proves the operator still holds the Recovery Key.
//
// It exists because "armed" and "recoverable" are different claims, and a
// backup nobody can open is worse than no backup — it is a year of green
// checkmarks ending in a discovery. The key is used to decrypt one manifest and
// discarded: it is never stored, never logged, and never returned.
func (a *API) handleVerifyPlaneDR(w http.ResponseWriter, r *http.Request) {
	if !a.planeDRReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req struct {
		RecoveryKey string `json:"recovery_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.RecoveryKey == "" {
		writeError(w, http.StatusBadRequest, "paste the recovery key to check it")
		return
	}
	if a.deps.PlaneDRFetch == nil {
		writeError(w, http.StatusNotImplemented, "this panel cannot fetch a snapshot to check against")
		return
	}
	if err := a.deps.PlaneDR.Verify(r.Context(), req.RecoveryKey, a.deps.PlaneDRFetch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPlaneDRVerified,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "disaster recovery"),
	})
	w.WriteHeader(http.StatusNoContent)
}

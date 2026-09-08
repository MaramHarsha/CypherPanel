package rest

// Guided panel upgrades (panel-updates.md §10).
//
// OWNER, AND SESSION-ONLY, on everything that acts. This is the control that
// decides what code the control plane runs; an API token that could move it —
// and an API token may live in a CI runner — would be the most valuable
// credential in the install.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/upgrade"
)

// UpgradeService is the plane's half of a guided upgrade (consumer-defined).
type UpgradeService interface {
	Mode() string
	Locked(ctx context.Context) bool
	Active(ctx context.Context) (domain.PanelUpgrade, bool)
	Preflight(ctx context.Context, version string) (upgrade.Preflight, error)
	Start(ctx context.Context, version, actor string, retentionDays int, rollback bool) (domain.PanelUpgrade, error)
	Cancel(ctx context.Context, id string) error
	Restore(ctx context.Context, snapshotID, actor string) error
	Sync(ctx context.Context)
	History(ctx context.Context, limit int) ([]domain.PanelUpgrade, error)
	Snapshots(ctx context.Context) ([]domain.PanelSnapshot, error)
	SetSnapshotRetention(ctx context.Context, id string, expiresAt *time.Time, pinned bool) (domain.PanelSnapshot, error)
	DeleteSnapshot(ctx context.Context, id string) error
}

type upgradeDTO struct {
	ID          string  `json:"id"`
	FromVersion string  `json:"from_version"`
	ToVersion   string  `json:"to_version"`
	Phase       string  `json:"phase"`
	Detail      string  `json:"detail"`
	Actor       string  `json:"actor"`
	Rollback    bool    `json:"rollback"`
	SnapshotID  string  `json:"snapshot_id,omitempty"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
}

type snapshotDTO struct {
	ID        string  `json:"id"`
	Version   string  `json:"version"`
	SizeBytes int64   `json:"size_bytes"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	Pinned    bool    `json:"pinned"`
}

type updatesDTO struct {
	Current  string `json:"current"`
	Latest   string `json:"latest,omitempty"`
	Kind     string `json:"kind,omitempty"`
	NotesURL string `json:"notes_url,omitempty"`
	// Mode is `assisted` on a systemd install and `manual` in a container,
	// where the panel cannot upgrade itself and SAYS SO rather than drawing a
	// button that would not work.
	Mode   string      `json:"mode"`
	Active *upgradeDTO `json:"active"`
}

func toUpgradeDTO(u domain.PanelUpgrade) upgradeDTO {
	var finished *string
	if u.FinishedAt != nil {
		s := u.FinishedAt.UTC().Format(time.RFC3339)
		finished = &s
	}
	return upgradeDTO{
		ID: u.ID, FromVersion: u.FromVersion, ToVersion: u.ToVersion,
		Phase: u.Phase, Detail: u.Detail, Actor: u.Actor, Rollback: u.Rollback,
		SnapshotID: u.SnapshotID,
		StartedAt:  u.StartedAt.UTC().Format(time.RFC3339), FinishedAt: finished,
	}
}

func toSnapshotDTO(s domain.PanelSnapshot) snapshotDTO {
	var expires *string
	if s.ExpiresAt != nil {
		v := s.ExpiresAt.UTC().Format(time.RFC3339)
		expires = &v
	}
	return snapshotDTO{
		ID: s.ID, Version: s.Version, SizeBytes: s.SizeBytes,
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: expires, Pinned: s.Pinned,
	}
}

func (a *API) upgradesReady(w http.ResponseWriter) bool {
	if a.deps.Upgrades == nil {
		writeError(w, http.StatusNotImplemented, "guided upgrades are not enabled on this panel")
		return false
	}
	return true
}

// handleGetUpdates is the only route here a member may read: what is running,
// what is available, and whether an upgrade is in flight.
func (a *API) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	out := updatesDTO{Mode: a.deps.Upgrades.Mode()}
	if a.deps.Updates != nil {
		out.Current = a.deps.Updates.Current().Version
		if latest := a.deps.Updates.Latest(); latest != nil {
			out.Latest, out.Kind, out.NotesURL = latest.Version, latest.Kind, latest.NotesURL
		}
	}
	// Mirror the helper's file before answering: the plane that started the
	// upgrade is not the plane that records its outcome, because the restart
	// happens in the middle.
	a.deps.Upgrades.Sync(r.Context())
	if u, ok := a.deps.Upgrades.Active(r.Context()); ok {
		dto := toUpgradeDTO(u)
		out.Active = &dto
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if version == "" {
		writeError(w, http.StatusBadRequest, "name the version to check")
		return
	}
	// Refused here as well as inside VerifyRelease, so the operator gets "that
	// is not a version" rather than a refusal that reads like a signature
	// failure. The check inside is the one that is load-bearing.
	if !upgrade.ValidTag(version) {
		writeError(w, http.StatusBadRequest, "that is not a release version — they look like v1.2.3")
		return
	}
	pf, err := a.deps.Upgrades.Preflight(r.Context(), version)
	if err != nil {
		a.deps.Log.Error("upgrade preflight", "version", version, "error", err)
		writeError(w, http.StatusInternalServerError, "could not run the pre-flight")
		return
	}
	writeJSON(w, http.StatusOK, pf)
}

type startUpgradeRequest struct {
	Version string `json:"version"`
	// SnapshotRetentionDays 0 means keep forever. The one decision the operator
	// makes, and it must not be made for them.
	SnapshotRetentionDays int `json:"snapshot_retention_days"`
	// AcknowledgeIncompatibleAgents is the typed confirm: it must equal the
	// target version. Orphaning the fleet's management plane is close enough to
	// irreversible to earn one, and it stays ONE dialog because confirmations
	// never stack.
	AcknowledgeIncompatibleAgents string `json:"acknowledge_incompatible_agents"`
	// Rollback puts back a version this host has already run, keeping every row
	// written since. The helper refuses any downgrade without it, and refuses
	// one with it unless it can prove the host ran that version — so the flag
	// opens a door bounded by history, not by trust.
	Rollback bool `json:"rollback"`
}

func (a *API) handleStartUpgrade(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req startUpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Version = strings.TrimSpace(req.Version)
	if req.Version == "" {
		writeError(w, http.StatusBadRequest, "name the version to install")
		return
	}
	if !upgrade.ValidTag(req.Version) {
		writeError(w, http.StatusBadRequest, "that is not a release version — they look like v1.2.3")
		return
	}

	// The pre-flight runs again here rather than trusting the one the screen
	// saw: it is cheap, and a check an operator passed ten minutes ago is not a
	// check that holds now.
	pf, err := a.deps.Upgrades.Preflight(r.Context(), req.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not run the pre-flight")
		return
	}
	if !pf.CanProceed {
		if !pf.NeedsTypedConfirm || req.AcknowledgeIncompatibleAgents != req.Version {
			writeError(w, http.StatusBadRequest, firstRefusal(pf))
			return
		}
	}

	// req.Rollback, not a literal false. The helper is the real gate — it
	// refuses any downgrade unless this flag is set AND it can prove the host
	// ran that version — and passing false unconditionally made that whole
	// branch unreachable from every client.
	u, err := a.deps.Upgrades.Start(r.Context(), req.Version, user.Email, req.SnapshotRetentionDays, req.Rollback)
	if errors.Is(err, upgrade.ErrActive) {
		writeError(w, http.StatusConflict, "an upgrade is already running")
		return
	}
	if errors.Is(err, upgrade.ErrUnavailable) {
		writeError(w, http.StatusNotImplemented, "this panel runs as a container, so it cannot upgrade itself — pull the new image and recreate it")
		return
	}
	if err != nil {
		a.deps.Log.Error("starting the upgrade", "version", req.Version, "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the upgrade")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPanelUpgradeStarted,
		Resource: audit.Resource(audit.ResourcePanel, u.ID, req.Version),
		Detail: map[string]any{
			"from": u.FromVersion, "to": u.ToVersion,
			"snapshot_retention_days": req.SnapshotRetentionDays,
			"agents_overridden":       pf.NeedsTypedConfirm,
		},
	})
	writeJSON(w, http.StatusAccepted, toUpgradeDTO(u))
}

func firstRefusal(pf upgrade.Preflight) string {
	for _, c := range pf.Checks {
		if c.Status == upgrade.CheckRefused {
			if c.Remedy != "" {
				return c.Text + " " + c.Remedy
			}
			return c.Text
		}
	}
	return "the pre-flight refused this upgrade"
}

func (a *API) handleCancelUpgrade(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	u, ok := a.deps.Upgrades.Active(r.Context())
	if !ok {
		writeError(w, http.StatusNotFound, "no upgrade is running")
		return
	}
	// After the swap the helper owns the host and the panel has nothing to
	// cancel with. Saying so is better than a button that appears to work.
	switch u.Phase {
	case upgrade.PhaseMigrating, upgrade.PhaseRestarting, upgrade.PhaseHealthGate:
		writeError(w, http.StatusConflict, "the swap has already started — it will roll itself back if the new version does not come up")
		return
	}
	if err := a.deps.Upgrades.Cancel(r.Context(), u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not cancel the upgrade")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPanelUpgradeCancelled,
		Resource: audit.Resource(audit.ResourcePanel, u.ID, u.ToVersion),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleUpgradeHistory(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	history, err := a.deps.Upgrades.History(r.Context(), 50)
	if err != nil {
		a.deps.Log.Error("reading upgrade history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the history")
		return
	}
	snapshots, err := a.deps.Upgrades.Snapshots(r.Context())
	if err != nil {
		a.deps.Log.Error("reading snapshots", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the snapshots")
		return
	}
	out := struct {
		Upgrades  []upgradeDTO  `json:"upgrades"`
		Snapshots []snapshotDTO `json:"snapshots"`
	}{Upgrades: make([]upgradeDTO, 0, len(history)), Snapshots: make([]snapshotDTO, 0, len(snapshots))}
	for _, u := range history {
		out.Upgrades = append(out.Upgrades, toUpgradeDTO(u))
	}
	for _, s := range snapshots {
		out.Snapshots = append(out.Snapshots, toSnapshotDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSetSnapshotRetention(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req struct {
		RetentionDays int  `json:"retention_days"`
		Pinned        bool `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var expires *time.Time
	if req.RetentionDays > 0 {
		t := time.Now().AddDate(0, 0, req.RetentionDays)
		expires = &t
	}
	sn, err := a.deps.Upgrades.SetSnapshotRetention(r.Context(), r.PathValue("id"), expires, req.Pinned)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, toSnapshotDTO(sn))
}

func (a *API) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if err := a.deps.Upgrades.DeleteSnapshot(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete the snapshot")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPanelSnapshotDeleted,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), "snapshot"),
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleRestoreSnapshot is the last resort, and it is loud about it: restoring
// rewinds the panel's database to the moment the snapshot was taken, and
// everything written since is gone.
func (a *API) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if !a.upgradesReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Confirm != "restore" {
		writeError(w, http.StatusBadRequest, `type "restore" to confirm: this rewinds the panel's database and everything written since the snapshot is lost`)
		return
	}
	if err := a.deps.Upgrades.Restore(r.Context(), r.PathValue("id"), user.Email); err != nil {
		if errors.Is(err, upgrade.ErrActive) {
			writeError(w, http.StatusConflict, "an upgrade is already running")
			return
		}
		a.deps.Log.Error("restoring a snapshot", "snapshot_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the restore")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionPanelSnapshotRestored,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), "snapshot"),
	})
	w.WriteHeader(http.StatusAccepted)
}

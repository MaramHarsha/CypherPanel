package store

// Deploy protection persistence (deploy-protection.md §2). Domain types in,
// domain types out; pgx/pgtype stays inside this package.
//
// Nothing here evaluates a freeze window: minute-of-week arithmetic belongs to
// core/protection, against an injected clock, because a window is wall clock in
// its OWN zone and the database's now() knows nothing about that zone (§4).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── The protection document ────────────────────────────────────────────────

// GetEnvironmentProtection returns one environment's policy with its windows.
// ErrNotFound means the environment has never been protected — a normal state,
// which the service turns into domain.DefaultProtection rather than a 404 (§6).
func (s *Store) GetEnvironmentProtection(ctx context.Context, envID string) (domain.EnvironmentProtection, error) {
	row, err := s.q.GetEnvironmentProtection(ctx, envID)
	if err != nil {
		return domain.EnvironmentProtection{}, wrap("getting environment protection", err)
	}
	windows, err := s.ListFreezeWindows(ctx, envID)
	if err != nil {
		return domain.EnvironmentProtection{}, err
	}
	p := protectionFromRow(row)
	p.Windows = windows
	return p, nil
}

// ListFreezeWindows returns an environment's declared windows, in a stable
// order so two reads of an unchanged calendar render identically.
func (s *Store) ListFreezeWindows(ctx context.Context, envID string) ([]domain.FreezeWindow, error) {
	rows, err := s.q.ListFreezeWindows(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("store: listing freeze windows: %w", err)
	}
	out := make([]domain.FreezeWindow, 0, len(rows))
	for _, r := range rows {
		out = append(out, freezeWindowFromRow(r))
	}
	return out, nil
}

// SetEnvironmentProtection replaces the whole document — flags plus the
// complete window list — in ONE transaction. Wholesale replacement is what the
// PUT means (§6, desired state), and doing it transactionally is what keeps the
// gate from ever reading the old flags beside the new windows.
func (s *Store) SetEnvironmentProtection(ctx context.Context, p domain.EnvironmentProtection) (domain.EnvironmentProtection, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.EnvironmentProtection{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	qtx := s.q.WithTx(tx)
	row, err := qtx.UpsertEnvironmentProtection(ctx, db.UpsertEnvironmentProtectionParams{
		EnvironmentID:   p.EnvironmentID,
		RequireApproval: p.RequireApproval,
		MinApproverRole: p.MinApproverRole,
		FreezeEnabled:   p.FreezeEnabled,
	})
	if err != nil {
		return domain.EnvironmentProtection{}, wrapCreate("saving environment protection", err)
	}
	if err := qtx.DeleteFreezeWindows(ctx, p.EnvironmentID); err != nil {
		return domain.EnvironmentProtection{}, fmt.Errorf("store: clearing freeze windows: %w", err)
	}
	if len(p.Windows) > 0 {
		arg := db.InsertFreezeWindowsParams{EnvironmentID: p.EnvironmentID}
		for _, w := range p.Windows {
			arg.Ids = append(arg.Ids, w.ID)
			arg.StartDows = append(arg.StartDows, int16(w.StartDOW)) //nolint:gosec // 0–6, checked by the column
			arg.StartMinutes = append(arg.StartMinutes, int32(w.StartMinute))
			arg.EndDows = append(arg.EndDows, int16(w.EndDOW)) //nolint:gosec // 0–6, checked by the column
			arg.EndMinutes = append(arg.EndMinutes, int32(w.EndMinute))
			arg.Timezones = append(arg.Timezones, w.Timezone)
		}
		if err := qtx.InsertFreezeWindows(ctx, arg); err != nil {
			return domain.EnvironmentProtection{}, wrapCreate("inserting freeze windows", err)
		}
	}
	windows, err := qtx.ListFreezeWindows(ctx, p.EnvironmentID)
	if err != nil {
		return domain.EnvironmentProtection{}, fmt.Errorf("store: listing freeze windows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EnvironmentProtection{}, fmt.Errorf("store: committing environment protection: %w", err)
	}
	out := protectionFromRow(row)
	out.Windows = make([]domain.FreezeWindow, 0, len(windows))
	for _, w := range windows {
		out.Windows = append(out.Windows, freezeWindowFromRow(w))
	}
	return out, nil
}

// ─── Approvals ──────────────────────────────────────────────────────────────

// CreateDeployApproval opens the gate decision for a parked deployment.
// requestedBy is empty for a webhook deploy — a push has no panel user behind
// it — and is stored NULL.
func (s *Store) CreateDeployApproval(ctx context.Context, deploymentID, envID, requestedBy, requiredRole string) (domain.DeployApproval, error) {
	var by pgtype.Text
	if requestedBy != "" {
		by = pgText(requestedBy)
	}
	row, err := s.q.CreateDeployApproval(ctx, db.CreateDeployApprovalParams{
		DeploymentID:  deploymentID,
		EnvironmentID: envID,
		RequestedBy:   by,
		RequiredRole:  requiredRole,
	})
	if err != nil {
		return domain.DeployApproval{}, wrapCreate("creating deploy approval", err)
	}
	return domain.DeployApproval{
		DeploymentID:  row.DeploymentID,
		EnvironmentID: row.EnvironmentID,
		RequestedBy:   row.RequestedBy.String,
		RequiredRole:  row.RequiredRole,
		State:         row.State,
		Reason:        row.Reason,
		CreatedAt:     row.CreatedAt.Time,
	}, nil
}

// GetDeployApproval returns one deployment's gate decision, with the requester
// and decider emails the pending card renders. ErrNotFound means the deployment
// was never gated, which is the ordinary case.
func (s *Store) GetDeployApproval(ctx context.Context, deploymentID string) (domain.DeployApproval, error) {
	row, err := s.q.GetDeployApproval(ctx, deploymentID)
	if err != nil {
		return domain.DeployApproval{}, wrap("getting deploy approval", err)
	}
	return approvalFromGetRow(row), nil
}

// ListDeployApprovalsByEnvironment is the environment's approval queue. An
// empty state means every state, and limit bounds the answer: a long-lived
// environment's whole decision history is not a screen and is not a response.
func (s *Store) ListDeployApprovalsByEnvironment(ctx context.Context, envID, state string, limit int32) ([]domain.DeployApproval, error) {
	rows, err := s.q.ListDeployApprovalsByEnvironment(ctx, db.ListDeployApprovalsByEnvironmentParams{
		EnvironmentID: envID,
		State:         state,
		RowLimit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing deploy approvals: %w", err)
	}
	out := make([]domain.DeployApproval, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.DeployApproval{
			DeploymentID:     r.DeploymentID,
			EnvironmentID:    r.EnvironmentID,
			RequestedBy:      r.RequestedBy.String,
			RequestedByEmail: r.RequestedByEmail,
			RequiredRole:     r.RequiredRole,
			State:            r.State,
			DecidedBy:        r.DecidedBy.String,
			DecidedByEmail:   r.DecidedByEmail,
			DecidedAt:        timePtr(r.DecidedAt),
			Reason:           r.Reason,
			CreatedAt:        r.CreatedAt.Time,
		})
	}
	return out, nil
}

// ListDeployApprovalsByApplication answers "which of THESE deployments has a
// gate decision" for one page of a Deployments tab in one round trip, keyed by
// deployment id. The ids are the page being decorated, not the application's
// whole history: an application deploys forever, and a read model that grows
// with it eventually stops answering.
func (s *Store) ListDeployApprovalsByApplication(ctx context.Context, appID string, deploymentIDs []string) (map[string]domain.DeployApproval, error) {
	if len(deploymentIDs) == 0 {
		return map[string]domain.DeployApproval{}, nil
	}
	rows, err := s.q.ListDeployApprovalsByApplication(ctx, db.ListDeployApprovalsByApplicationParams{
		ApplicationID: appID,
		DeploymentIds: deploymentIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing deploy approvals by application: %w", err)
	}
	out := make(map[string]domain.DeployApproval, len(rows))
	for _, r := range rows {
		out[r.DeploymentID] = domain.DeployApproval{
			DeploymentID:     r.DeploymentID,
			EnvironmentID:    r.EnvironmentID,
			RequestedBy:      r.RequestedBy.String,
			RequestedByEmail: r.RequestedByEmail,
			RequiredRole:     r.RequiredRole,
			State:            r.State,
			DecidedBy:        r.DecidedBy.String,
			DecidedByEmail:   r.DecidedByEmail,
			DecidedAt:        timePtr(r.DecidedAt),
			Reason:           r.Reason,
			CreatedAt:        r.CreatedAt.Time,
		}
	}
	return out, nil
}

// DecideDeployApproval records an approve or a reject. It matches only a
// PENDING row, so a second decision — or an approve racing a reject — writes
// nothing and comes back ErrConflict, which handlers map to 409 (§5). There is
// no read-then-write, so there is no window between the check and the update.
func (s *Store) DecideDeployApproval(ctx context.Context, deploymentID, state, decidedBy, reason string) (domain.DeployApproval, error) {
	var by pgtype.Text
	if decidedBy != "" {
		by = pgText(decidedBy)
	}
	if _, err := s.q.DecideDeployApproval(ctx, db.DecideDeployApprovalParams{
		DeploymentID: deploymentID,
		State:        state,
		DecidedBy:    by,
		Reason:       reason,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the approval does not exist or it is already decided.
			// Which one it is decides the status code, so ask.
			if _, gerr := s.q.GetDeployApproval(ctx, deploymentID); gerr != nil {
				return domain.DeployApproval{}, wrap("deciding deploy approval", gerr)
			}
			return domain.DeployApproval{}, fmt.Errorf("store: deciding deploy approval: %w", ErrConflict)
		}
		return domain.DeployApproval{}, fmt.Errorf("store: deciding deploy approval: %w", err)
	}
	// Read back through the joined query so the caller gets the decider's
	// email without a second shape.
	return s.GetDeployApproval(ctx, deploymentID)
}

// CountQualifiedApprovers counts the project's team members at or above
// minRole, excluding one user. Zero is what lifts the no-self-approval rule for
// a solo operator (§5).
func (s *Store) CountQualifiedApprovers(ctx context.Context, projectID, minRole, excludeUserID string) (int64, error) {
	n, err := s.q.CountQualifiedApprovers(ctx, db.CountQualifiedApproversParams{
		ProjectID:     projectID,
		MinRole:       minRole,
		ExcludeUserID: excludeUserID,
	})
	if err != nil {
		return 0, fmt.Errorf("store: counting qualified approvers: %w", err)
	}
	return n, nil
}

// ─── Break glass ────────────────────────────────────────────────────────────

// CreateBreakGlassGrant opens a bounded, recorded freeze override. opened_by is
// stored NULL when it is empty and set NULL when the opener's account is later
// deleted: the grant is append-only and outlives the person named in it.
func (s *Store) CreateBreakGlassGrant(ctx context.Context, g domain.BreakGlassGrant) (domain.BreakGlassGrant, error) {
	var by pgtype.Text
	if g.OpenedBy != "" {
		by = pgText(g.OpenedBy)
	}
	row, err := s.q.CreateBreakGlassGrant(ctx, db.CreateBreakGlassGrantParams{
		ID:            g.ID,
		EnvironmentID: g.EnvironmentID,
		OpenedBy:      by,
		Reason:        g.Reason,
		ExpiresAt:     pgtype.Timestamptz{Time: g.ExpiresAt, Valid: true},
	})
	if err != nil {
		return domain.BreakGlassGrant{}, wrapCreate("creating break-glass grant", err)
	}
	return domain.BreakGlassGrant{
		ID:            row.ID,
		EnvironmentID: row.EnvironmentID,
		OpenedBy:      row.OpenedBy.String,
		Reason:        row.Reason,
		CreatedAt:     row.CreatedAt.Time,
		ExpiresAt:     row.ExpiresAt.Time,
	}, nil
}

// BreakGlassOpen reports whether an unexpired grant suspends this
// environment's freeze at now. The clock is the caller's — the plane's injected
// one — so the gate and its tests read a single time source (ENGINEERING rule 9).
func (s *Store) BreakGlassOpen(ctx context.Context, envID string, now time.Time) (bool, error) {
	n, err := s.q.CountActiveBreakGlassGrants(ctx, db.CountActiveBreakGlassGrantsParams{
		EnvironmentID: envID,
		Now:           pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("store: counting break-glass grants: %w", err)
	}
	return n > 0, nil
}

// ListBreakGlassGrants returns an environment's grants newest first, bounded.
func (s *Store) ListBreakGlassGrants(ctx context.Context, envID string, limit int32) ([]domain.BreakGlassGrant, error) {
	rows, err := s.q.ListBreakGlassGrants(ctx, db.ListBreakGlassGrantsParams{
		EnvironmentID: envID,
		Limit:         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing break-glass grants: %w", err)
	}
	out := make([]domain.BreakGlassGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.BreakGlassGrant{
			ID:            r.ID,
			EnvironmentID: r.EnvironmentID,
			OpenedBy:      r.OpenedBy.String,
			OpenedByEmail: r.OpenedByEmail,
			Reason:        r.Reason,
			CreatedAt:     r.CreatedAt.Time,
			ExpiresAt:     r.ExpiresAt.Time,
		})
	}
	return out, nil
}

// ─── mapping ────────────────────────────────────────────────────────────────

func protectionFromRow(r db.EnvironmentProtection) domain.EnvironmentProtection {
	return domain.EnvironmentProtection{
		EnvironmentID:   r.EnvironmentID,
		RequireApproval: r.RequireApproval,
		MinApproverRole: r.MinApproverRole,
		FreezeEnabled:   r.FreezeEnabled,
		Windows:         []domain.FreezeWindow{},
		CreatedAt:       r.CreatedAt.Time,
		UpdatedAt:       r.UpdatedAt.Time,
	}
}

func freezeWindowFromRow(r db.FreezeWindow) domain.FreezeWindow {
	return domain.FreezeWindow{
		ID:            r.ID,
		EnvironmentID: r.EnvironmentID,
		StartDOW:      time.Weekday(r.StartDow),
		StartMinute:   int(r.StartMinute),
		EndDOW:        time.Weekday(r.EndDow),
		EndMinute:     int(r.EndMinute),
		Timezone:      r.Timezone,
		CreatedAt:     r.CreatedAt.Time,
	}
}

func approvalFromGetRow(r db.GetDeployApprovalRow) domain.DeployApproval {
	return domain.DeployApproval{
		DeploymentID:     r.DeploymentID,
		EnvironmentID:    r.EnvironmentID,
		RequestedBy:      r.RequestedBy.String,
		RequestedByEmail: r.RequestedByEmail,
		RequiredRole:     r.RequiredRole,
		State:            r.State,
		DecidedBy:        r.DecidedBy.String,
		DecidedByEmail:   r.DecidedByEmail,
		DecidedAt:        timePtr(r.DecidedAt),
		Reason:           r.Reason,
		CreatedAt:        r.CreatedAt.Time,
	}
}

// timePtr is nil for a NULL timestamp — the shape domain types use for
// "has not happened yet".
func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

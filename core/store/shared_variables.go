package store

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// Project shared variables (shared-variables.md §2, §5, §7). Nothing in this
// file unseals a value: the used-by count and the drift marker are answered
// from `shared_refs` and two timestamps, in SQL.

// CreateSharedVariable inserts a shared variable. A duplicate of
// (project, environment, key) — including two project-scoped rows, which the
// NULLS NOT DISTINCT constraint catches — comes back as ErrConflict.
func (s *Store) CreateSharedVariable(ctx context.Context, v domain.SharedVariable) (domain.SharedVariable, error) {
	row, err := s.q.CreateSharedVariable(ctx, db.CreateSharedVariableParams{
		ID:            v.ID,
		ProjectID:     v.ProjectID,
		EnvironmentID: textFromPtr(v.EnvironmentID),
		Key:           v.Key,
		ValueCt:       v.ValueCT,
		ValueNonce:    v.ValueNonce,
	})
	if err != nil {
		return domain.SharedVariable{}, wrapCreate("creating shared variable", err)
	}
	return sharedVariableFromRow(row), nil
}

func (s *Store) GetSharedVariable(ctx context.Context, id string) (domain.SharedVariable, error) {
	row, err := s.q.GetSharedVariable(ctx, id)
	if err != nil {
		return domain.SharedVariable{}, wrap("getting shared variable", err)
	}
	return sharedVariableFromRow(row), nil
}

func (s *Store) ListSharedVariablesByProject(ctx context.Context, projectID string) ([]domain.SharedVariable, error) {
	rows, err := s.q.ListSharedVariablesByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: listing shared variables: %w", err)
	}
	return sharedVariablesFromRows(rows), nil
}

// ListSharedVariablesInScope returns the variables in force for one
// environment: the project-scoped rows with any environment-scoped row of the
// same key shadowing them (shared-variables.md §3). This is what the scheduler
// resolves against at spec-build time.
func (s *Store) ListSharedVariablesInScope(ctx context.Context, projectID, environmentID string) ([]domain.SharedVariable, error) {
	rows, err := s.q.ListSharedVariablesInScope(ctx, db.ListSharedVariablesInScopeParams{
		ProjectID:     projectID,
		EnvironmentID: pgText(environmentID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing shared variables in scope: %w", err)
	}
	return sharedVariablesFromRows(rows), nil
}

// ListSharedVariableKeysInScope is the same resolution reduced to key names —
// what the write-time reference check reads (shared-variables.md §3).
func (s *Store) ListSharedVariableKeysInScope(ctx context.Context, projectID, environmentID string) ([]string, error) {
	keys, err := s.q.ListSharedVariableKeysInScope(ctx, db.ListSharedVariableKeysInScopeParams{
		ProjectID:     projectID,
		EnvironmentID: pgText(environmentID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing shared variable keys in scope: %w", err)
	}
	return keys, nil
}

// UpdateSharedVariableValue reseals a variable's value. Key and scope are
// immutable (shared-variables.md §7), so there is no other update path.
func (s *Store) UpdateSharedVariableValue(ctx context.Context, id string, ct, nonce []byte) (domain.SharedVariable, error) {
	row, err := s.q.UpdateSharedVariableValue(ctx, db.UpdateSharedVariableValueParams{
		ID:         id,
		ValueCt:    ct,
		ValueNonce: nonce,
	})
	if err != nil {
		return domain.SharedVariable{}, wrapUpdate("updating shared variable", err)
	}
	return sharedVariableFromRow(row), nil
}

func (s *Store) DeleteSharedVariable(ctx context.Context, id string) error {
	if err := s.q.DeleteSharedVariable(ctx, id); err != nil {
		return wrapDelete("deleting shared variable", err)
	}
	return nil
}

// CountSharedVariableUsage counts the applications that reference one variable,
// scope-accurately (shared-variables.md §7).
func (s *Store) CountSharedVariableUsage(ctx context.Context, id string) (int64, error) {
	n, err := s.q.CountSharedVariableUsage(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("store: counting shared variable usage: %w", err)
	}
	return n, nil
}

// CountSharedVariableUsageByProject answers the same question for every
// variable in a project in one round trip, keyed by variable id.
func (s *Store) CountSharedVariableUsageByProject(ctx context.Context, projectID string) (map[string]int64, error) {
	rows, err := s.q.CountSharedVariableUsageByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: counting shared variable usage: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.UsedByCount
	}
	return out, nil
}

// ListSharedVariableUsage names the applications that reference one variable,
// each with its own "redeploy to apply" marker relative to that variable.
func (s *Store) ListSharedVariableUsage(ctx context.Context, id string) ([]domain.SharedVariableUsage, error) {
	rows, err := s.q.ListSharedVariableUsage(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("store: listing shared variable usage: %w", err)
	}
	out := make([]domain.SharedVariableUsage, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.SharedVariableUsage{
			ApplicationID:   r.ApplicationID,
			ApplicationName: r.ApplicationName,
			EnvironmentName: r.EnvironmentName,
			RedeployPending: r.RedeployPending.Bool,
		})
	}
	return out, nil
}

// ApplicationRedeployPending reports whether some shared variable this
// application references changed after the environment it is actually running
// was frozen onto the wire (shared-variables.md §5).
func (s *Store) ApplicationRedeployPending(ctx context.Context, appID string) (bool, error) {
	pending, err := s.q.ApplicationRedeployPending(ctx, appID)
	if err != nil {
		return false, fmt.Errorf("store: deriving redeploy-pending: %w", err)
	}
	return pending, nil
}

// ListRedeployPendingApplications answers the same for a whole environment in
// one query, so listing applications never becomes one round trip per row.
func (s *Store) ListRedeployPendingApplications(ctx context.Context, envID string) ([]string, error) {
	ids, err := s.q.ListRedeployPendingApplications(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("store: listing redeploy-pending applications: %w", err)
	}
	return ids, nil
}

// SetDeploymentEnvResolved stamps the instant this rollout's environment was
// frozen onto the wire (shared-variables.md §5). The stamp is the database's
// clock, the same one that writes shared_variables.updated_at — the two are
// only ever compared with each other.
func (s *Store) SetDeploymentEnvResolved(ctx context.Context, id string) error {
	if err := s.q.SetDeploymentEnvResolved(ctx, id); err != nil {
		return fmt.Errorf("store: stamping resolved environment: %w", err)
	}
	return nil
}

// ApplyDeploymentEnvStamp copies a deployment's env_resolved_at onto its
// application. Called only when the deployment is observed running, which is
// what stops a failed deploy from marking an application clean.
func (s *Store) ApplyDeploymentEnvStamp(ctx context.Context, deploymentID string) error {
	if err := s.q.ApplyDeploymentEnvStamp(ctx, deploymentID); err != nil {
		return fmt.Errorf("store: applying resolved environment stamp: %w", err)
	}
	return nil
}

func sharedVariablesFromRows(rows []db.SharedVariable) []domain.SharedVariable {
	out := make([]domain.SharedVariable, 0, len(rows))
	for _, r := range rows {
		out = append(out, sharedVariableFromRow(r))
	}
	return out
}

func sharedVariableFromRow(r db.SharedVariable) domain.SharedVariable {
	return domain.SharedVariable{
		ID:            r.ID,
		ProjectID:     r.ProjectID,
		EnvironmentID: ptrFromText(r.EnvironmentID),
		Key:           r.Key,
		ValueCT:       r.ValueCt,
		ValueNonce:    r.ValueNonce,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

// ListSharedVariableKeysByProject returns keys and scope only, for the reason
// ListEnvVarKeys exists (project-export.md §4).
func (s *Store) ListSharedVariableKeysByProject(ctx context.Context, projectID string) ([]domain.SharedVariableKey, error) {
	rows, err := s.q.ListSharedVariableKeysByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: listing shared variable keys: %w", err)
	}
	out := make([]domain.SharedVariableKey, 0, len(rows))
	for _, r := range rows {
		v := domain.SharedVariableKey{Key: r.Key}
		if r.EnvironmentID.Valid {
			id := r.EnvironmentID.String
			v.EnvironmentID = &id
		}
		out = append(out, v)
	}
	return out, nil
}

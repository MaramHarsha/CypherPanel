package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// volumesJSON marshals an app's volume mounts for the JSONB column; a nil slice
// stores as "[]" (never SQL NULL — the column is NOT NULL).
func volumesJSON(v []domain.VolumeMount) []byte {
	if len(v) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// volumesFromJSON parses the JSONB column back to volume mounts.
func volumesFromJSON(b []byte) []domain.VolumeMount {
	if len(b) == 0 {
		return nil
	}
	var out []domain.VolumeMount
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// portsJSON marshals an app's port mappings for the JSONB column; a nil slice
// stores as "[]" (never SQL NULL — the column is NOT NULL).
func portsJSON(p []domain.PortMapping) []byte {
	if len(p) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(p)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// portsFromJSON parses the JSONB column back to port mappings.
func portsFromJSON(b []byte) []domain.PortMapping {
	if len(b) == 0 {
		return nil
	}
	var out []domain.PortMapping
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// Persistence for the Phase 2 resource model. Like store.go, all pgx/pgtype
// types stay in this package; callers speak domain types.

// ─── Projects ───────────────────────────────────────────────────────────────

func (s *Store) CreateProject(ctx context.Context, id, name, teamID, slug string) (domain.Project, error) {
	row, err := s.q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: name, TeamID: teamID, Slug: slug})
	if err != nil {
		return domain.Project{}, wrapCreate("creating project", err)
	}
	return projectFromRow(row), nil
}

func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error) {
	row, err := s.q.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, wrap("getting project", err)
	}
	return projectFromRow(row), nil
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing projects: %w", err)
	}
	out := make([]domain.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	if err := s.q.DeleteProject(ctx, id); err != nil {
		return fmt.Errorf("store: deleting project: %w", err)
	}
	return nil
}

// CreateProjectWithEnvironment creates a project and its default environment in
// one transaction, so a project never exists without somewhere to put
// resources (the spec's "default production env").
func (s *Store) CreateProjectWithEnvironment(ctx context.Context, projectID, name, teamID, slug, envID, envName string) (domain.Project, domain.Environment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	qtx := s.q.WithTx(tx)
	if _, err := qtx.CreateProject(ctx, db.CreateProjectParams{ID: projectID, Name: name, TeamID: teamID, Slug: slug}); err != nil {
		return domain.Project{}, domain.Environment{}, wrapCreate("creating project", err)
	}
	erow, err := qtx.CreateEnvironment(ctx, db.CreateEnvironmentParams{ID: envID, ProjectID: projectID, Name: envName})
	if err != nil {
		return domain.Project{}, domain.Environment{}, wrapCreate("creating environment", err)
	}
	// The project's first environment is its default. Done inside the same
	// transaction so a project never exists with environments but no default.
	prow, err := qtx.UpdateProject(ctx, db.UpdateProjectParams{
		ID:                   projectID,
		DefaultEnvironmentID: pgtype.Text{String: envID, Valid: true},
	})
	if err != nil {
		return domain.Project{}, domain.Environment{}, wrapUpdate("setting default environment", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("store: committing project: %w", err)
	}
	return projectFromRow(prow), environmentFromRow(erow), nil
}

// ─── Environments ───────────────────────────────────────────────────────────

func (s *Store) CreateEnvironment(ctx context.Context, id, projectID, name string) (domain.Environment, error) {
	row, err := s.q.CreateEnvironment(ctx, db.CreateEnvironmentParams{ID: id, ProjectID: projectID, Name: name})
	if err != nil {
		return domain.Environment{}, wrapCreate("creating environment", err)
	}
	return environmentFromRow(row), nil
}

func (s *Store) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	row, err := s.q.GetEnvironment(ctx, id)
	if err != nil {
		return domain.Environment{}, wrap("getting environment", err)
	}
	return environmentFromRow(row), nil
}

// DeleteEnvironment removes an environment; its applications and previews
// cascade (preview-environments.md §4 teardown).
// RenameEnvironment changes an environment's name. The unique (project_id,
// name) index makes a collision a conflict rather than a silent overwrite.
func (s *Store) RenameEnvironment(ctx context.Context, id, name string) (domain.Environment, error) {
	row, err := s.q.RenameEnvironment(ctx, db.RenameEnvironmentParams{ID: id, Name: name})
	if err != nil {
		return domain.Environment{}, wrapUpdate("renaming environment", err)
	}
	return environmentFromRow(row), nil
}

// CreateEnvironmentOfKind creates an environment with an explicit kind, which
// is how the preview lifecycle marks the ones it owns.
func (s *Store) CreateEnvironmentOfKind(ctx context.Context, id, projectID, name, kind string) (domain.Environment, error) {
	row, err := s.q.CreateEnvironmentOfKind(ctx, db.CreateEnvironmentOfKindParams{
		ID: id, ProjectID: projectID, Name: name, Kind: kind,
	})
	if err != nil {
		return domain.Environment{}, wrapCreate("creating environment", err)
	}
	return environmentFromRow(row), nil
}

func (s *Store) DeleteEnvironment(ctx context.Context, id string) error {
	if err := s.q.DeleteEnvironment(ctx, id); err != nil {
		return wrapDelete("deleting environment", err)
	}
	return nil
}

func (s *Store) ListEnvironmentsByProject(ctx context.Context, projectID string) ([]domain.Environment, error) {
	rows, err := s.q.ListEnvironmentsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: listing environments: %w", err)
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, r := range rows {
		out = append(out, environmentFromRow(r))
	}
	return out, nil
}

// ─── Applications ───────────────────────────────────────────────────────────

func (s *Store) CreateApplication(ctx context.Context, a domain.Application) (domain.Application, error) {
	row, err := s.q.CreateApplication(ctx, appParams(a))
	if err != nil {
		return domain.Application{}, wrapCreate("creating application", err)
	}
	return applicationFromRow(row), nil
}

// CreateApplicationWithEnv creates an application and its sealed env vars in one
// transaction, so an application never exists with a partial env set.
func (s *Store) CreateApplicationWithEnv(ctx context.Context, a domain.Application, envVars []domain.EnvVar) (domain.Application, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Application{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateApplication(ctx, appParams(a))
	if err != nil {
		return domain.Application{}, wrapCreate("creating application", err)
	}
	for _, v := range envVars {
		if err := qtx.UpsertEnvVar(ctx, db.UpsertEnvVarParams{
			ApplicationID: a.ID,
			Key:           v.Key,
			ValueCt:       v.ValueCT,
			ValueNonce:    v.ValueNonce,
			SharedRefs:    refsColumn(v.SharedRefs),
		}); err != nil {
			return domain.Application{}, wrapCreate("creating env var", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Application{}, fmt.Errorf("store: committing application: %w", err)
	}
	return applicationFromRow(row), nil
}

func appParams(a domain.Application) db.CreateApplicationParams {
	return db.CreateApplicationParams{
		ID:                    a.ID,
		EnvironmentID:         a.EnvironmentID,
		Name:                  a.Name,
		SourceKind:            a.Source.Kind,
		SourceRepo:            a.Source.Repo,
		SourceBranch:          a.Source.Branch,
		SourceDeployKeyID:     textFromPtr(a.Source.DeployKeyID),
		SourceImage:           a.Source.Image,
		BuildKind:             a.Build.Kind,
		BuildDockerfilePath:   a.Build.DockerfilePath,
		BuildContext:          a.Build.Context,
		RuntimeServerID:       a.Runtime.ServerID,
		RuntimePort:           int32(a.Runtime.Port),
		RuntimeReplicas:       int32(a.Runtime.Replicas),
		RouteDomain:           a.Route.Domain,
		RouteHttps:            a.Route.HTTPS,
		RoutePathPrefix:       a.Route.PathPrefix,
		HealthKind:            a.Health.Kind,
		HealthPath:            a.Health.Path,
		HealthIntervalSeconds: int32(a.Health.IntervalSeconds),
		HealthTimeoutSeconds:  int32(a.Health.TimeoutSeconds),
		HealthRetries:         int32(a.Health.Retries),
		WebhookID:             a.WebhookID,
		WebhookSecretCt:       a.WebhookSecretCT,
		WebhookSecretNonce:    a.WebhookSecretNonce,
		PreviewEnabled:        a.PreviewEnabled,
		PreviewBaseDomain:     a.PreviewBaseDomain,
		PreviewTtlHours:       int32(a.PreviewTTLHours),
		CpuLimit:              float4FromPtr(a.Runtime.CPULimit),
		MemoryLimitMb:         int4FromPtr(a.Runtime.MemoryLimitMB),
		Volumes:               volumesJSON(a.Volumes),
		Ports:                 portsJSON(a.Ports),
		SourceRegistryID:      textFromPtr(a.Source.RegistryID),
		BuildPushRegistryID:   textFromPtr(a.Build.PushRegistryID),
		BuildPushRepository:   a.Build.PushRepository,
	}
}

func (s *Store) GetApplication(ctx context.Context, id string) (domain.Application, error) {
	row, err := s.q.GetApplication(ctx, id)
	if err != nil {
		return domain.Application{}, wrap("getting application", err)
	}
	return applicationFromRow(row), nil
}

func (s *Store) GetApplicationByWebhookID(ctx context.Context, webhookID string) (domain.Application, error) {
	row, err := s.q.GetApplicationByWebhookID(ctx, webhookID)
	if err != nil {
		return domain.Application{}, wrap("getting application by webhook", err)
	}
	return applicationFromRow(row), nil
}

func (s *Store) ListApplicationsByEnvironment(ctx context.Context, envID string) ([]domain.Application, error) {
	rows, err := s.q.ListApplicationsByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("store: listing applications: %w", err)
	}
	out := make([]domain.Application, 0, len(rows))
	for _, r := range rows {
		out = append(out, applicationFromRow(r))
	}
	return out, nil
}

// ListApplicationConfigsByEnvironment returns applications with every sealed
// field stripped, for callers that must be unable to hold one (see
// domain.ApplicationConfig).
func (s *Store) ListApplicationConfigsByEnvironment(ctx context.Context, envID string) ([]domain.ApplicationConfig, error) {
	apps, err := s.ListApplicationsByEnvironment(ctx, envID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ApplicationConfig, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.ConfigView())
	}
	return out, nil
}

func (s *Store) ListApplicationsByServer(ctx context.Context, serverID string) ([]domain.Application, error) {
	rows, err := s.q.ListApplicationsByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: listing applications by server: %w", err)
	}
	out := make([]domain.Application, 0, len(rows))
	for _, r := range rows {
		out = append(out, applicationFromRow(r))
	}
	return out, nil
}

func (s *Store) SetApplicationDesiredRevision(ctx context.Context, appID, revisionID string) (domain.Application, error) {
	row, err := s.q.SetApplicationDesiredRevision(ctx, db.SetApplicationDesiredRevisionParams{
		ID:                appID,
		DesiredRevisionID: pgtype.Text{String: revisionID, Valid: true},
	})
	if err != nil {
		return domain.Application{}, wrap("setting desired revision", err)
	}
	return applicationFromRow(row), nil
}

// SetApplicationObservedStatus records what an agent actually reported for
// the application (ADR-005: status is observation, never intention).
func (s *Store) SetApplicationObservedStatus(ctx context.Context, appID, status, detail, observedRevisionID string, observedAt time.Time) error {
	err := s.q.SetApplicationObservedStatus(ctx, db.SetApplicationObservedStatusParams{
		ID:                 appID,
		Status:             status,
		StatusDetail:       detail,
		ObservedRevisionID: observedRevisionID,
		StatusObservedAt:   pgtype.Timestamptz{Time: observedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("store: setting observed status: %w", err)
	}
	return nil
}

// SetApplicationStatus sets the plane-driven status ('deploying' while a
// pipeline runs); observations overwrite it as reports arrive.
func (s *Store) SetApplicationStatus(ctx context.Context, appID, status, detail string) error {
	if err := s.q.SetApplicationStatus(ctx, db.SetApplicationStatusParams{ID: appID, Status: status, StatusDetail: detail}); err != nil {
		return fmt.Errorf("store: setting status: %w", err)
	}
	return nil
}

// UpdateApplicationConfig replaces the mutable configuration of an
// application (PATCH semantics are applied by the service; the store writes
// the merged row). The runtime server is deliberately not updatable — moving
// an app between servers needs the distribute step (ADR-008).
func (s *Store) UpdateApplicationConfig(ctx context.Context, a domain.Application) (domain.Application, error) {
	row, err := s.q.UpdateApplicationConfig(ctx, db.UpdateApplicationConfigParams{
		ID:                    a.ID,
		Name:                  a.Name,
		SourceKind:            a.Source.Kind,
		SourceRepo:            a.Source.Repo,
		SourceBranch:          a.Source.Branch,
		SourceDeployKeyID:     textFromPtr(a.Source.DeployKeyID),
		SourceImage:           a.Source.Image,
		BuildKind:             a.Build.Kind,
		BuildDockerfilePath:   a.Build.DockerfilePath,
		BuildContext:          a.Build.Context,
		RuntimePort:           int32(a.Runtime.Port),
		RuntimeReplicas:       int32(a.Runtime.Replicas),
		RouteDomain:           a.Route.Domain,
		RouteHttps:            a.Route.HTTPS,
		RoutePathPrefix:       a.Route.PathPrefix,
		HealthKind:            a.Health.Kind,
		HealthPath:            a.Health.Path,
		HealthIntervalSeconds: int32(a.Health.IntervalSeconds),
		HealthTimeoutSeconds:  int32(a.Health.TimeoutSeconds),
		HealthRetries:         int32(a.Health.Retries),
		PreviewEnabled:        a.PreviewEnabled,
		PreviewBaseDomain:     a.PreviewBaseDomain,
		PreviewTtlHours:       int32(a.PreviewTTLHours),
		CpuLimit:              float4FromPtr(a.Runtime.CPULimit),
		MemoryLimitMb:         int4FromPtr(a.Runtime.MemoryLimitMB),
		Volumes:               volumesJSON(a.Volumes),
		Ports:                 portsJSON(a.Ports),
		SourceRegistryID:      textFromPtr(a.Source.RegistryID),
		BuildPushRegistryID:   textFromPtr(a.Build.PushRegistryID),
		BuildPushRepository:   a.Build.PushRepository,
	})
	if err != nil {
		return domain.Application{}, wrapUpdate("updating application", err)
	}
	return applicationFromRow(row), nil
}

// BumpApplicationRestartToken records a restart (deployment-control.md §3).
// Separate from UpdateApplicationConfig on purpose: a restart must not carry an
// unrelated config edit along with it.
func (s *Store) BumpApplicationRestartToken(ctx context.Context, id, token string) (domain.Application, error) {
	row, err := s.q.BumpApplicationRestartToken(ctx, db.BumpApplicationRestartTokenParams{ID: id, RestartToken: token})
	if err != nil {
		return domain.Application{}, wrapUpdate("restarting application", err)
	}
	return applicationFromRow(row), nil
}

func (s *Store) DeleteApplication(ctx context.Context, id string) error {
	if err := s.q.DeleteApplication(ctx, id); err != nil {
		return fmt.Errorf("store: deleting application: %w", err)
	}
	return nil
}

// ─── Env vars ───────────────────────────────────────────────────────────────

func (s *Store) UpsertEnvVar(ctx context.Context, appID string, v domain.EnvVar) error {
	err := s.q.UpsertEnvVar(ctx, db.UpsertEnvVarParams{
		ApplicationID: appID,
		Key:           v.Key,
		ValueCt:       v.ValueCT,
		ValueNonce:    v.ValueNonce,
		SharedRefs:    refsColumn(v.SharedRefs),
	})
	if err != nil {
		return wrapCreate("upserting env var", err)
	}
	return nil
}

// refsColumn normalises a nil ref slice to the empty array the column defaults
// to, so a rewrite that drops every {{shared.…}} reference clears the row
// rather than writing NULL into a NOT NULL column.
func refsColumn(refs []string) []string {
	if refs == nil {
		return []string{}
	}
	return refs
}

func (s *Store) ListEnvVars(ctx context.Context, appID string) ([]domain.EnvVar, error) {
	rows, err := s.q.ListEnvVars(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing env vars: %w", err)
	}
	out := make([]domain.EnvVar, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.EnvVar{Key: r.Key, ValueCT: r.ValueCt, ValueNonce: r.ValueNonce, SharedRefs: r.SharedRefs})
	}
	return out, nil
}

// ListEnvVarKeys returns the keys and their shared-variable references and
// nothing else. It exists so a caller that must never hold a ciphertext — the
// project exporter (project-export.md §4) — can be given an interface with no
// method capable of returning one, rather than being trusted to ignore the
// fields on domain.EnvVar.
func (s *Store) ListEnvVarKeys(ctx context.Context, appID string) ([]domain.EnvVarKey, error) {
	rows, err := s.q.ListEnvVarKeys(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing env var keys: %w", err)
	}
	out := make([]domain.EnvVarKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.EnvVarKey{Key: r.Key, SharedRefs: r.SharedRefs})
	}
	return out, nil
}

func (s *Store) DeleteEnvVar(ctx context.Context, appID, key string) error {
	if err := s.q.DeleteEnvVar(ctx, db.DeleteEnvVarParams{ApplicationID: appID, Key: key}); err != nil {
		return fmt.Errorf("store: deleting env var: %w", err)
	}
	return nil
}

// ─── Revisions ──────────────────────────────────────────────────────────────

func (s *Store) CreateRevision(ctx context.Context, id, appID, sourceCommit string, configSnapshot []byte) (domain.Revision, error) {
	row, err := s.q.CreateRevision(ctx, db.CreateRevisionParams{
		ID:             id,
		ApplicationID:  appID,
		SourceCommit:   sourceCommit,
		ConfigSnapshot: configSnapshot,
	})
	if err != nil {
		return domain.Revision{}, wrapCreate("creating revision", err)
	}
	return revisionFromRow(row), nil
}

func (s *Store) GetRevision(ctx context.Context, id string) (domain.Revision, error) {
	row, err := s.q.GetRevision(ctx, id)
	if err != nil {
		return domain.Revision{}, wrap("getting revision", err)
	}
	return revisionFromRow(row), nil
}

func (s *Store) SetRevisionImage(ctx context.Context, id, image string) (domain.Revision, error) {
	row, err := s.q.SetRevisionImage(ctx, db.SetRevisionImageParams{ID: id, Image: image})
	if err != nil {
		return domain.Revision{}, wrap("setting revision image", err)
	}
	return revisionFromRow(row), nil
}

// SetRevisionSourceCommit records the exact commit the builder resolved and
// built (a BuildWork may name a branch head; the revision of record needs the
// SHA the build actually used).
func (s *Store) SetRevisionSourceCommit(ctx context.Context, id, commitSHA string) (domain.Revision, error) {
	row, err := s.q.SetRevisionSourceCommit(ctx, db.SetRevisionSourceCommitParams{ID: id, SourceCommit: commitSHA})
	if err != nil {
		return domain.Revision{}, wrap("setting revision commit", err)
	}
	return revisionFromRow(row), nil
}

func (s *Store) ListRevisionsByApplication(ctx context.Context, appID string) ([]domain.Revision, error) {
	rows, err := s.q.ListRevisionsByApplication(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing revisions: %w", err)
	}
	out := make([]domain.Revision, 0, len(rows))
	for _, r := range rows {
		out = append(out, revisionFromRow(r))
	}
	return out, nil
}

// ─── Deployments ────────────────────────────────────────────────────────────

func (s *Store) CreateDeployment(ctx context.Context, id, appID, revisionID, trigger string) (domain.Deployment, error) {
	row, err := s.q.CreateDeployment(ctx, db.CreateDeploymentParams{
		ID:            id,
		ApplicationID: appID,
		RevisionID:    revisionID,
		Status:        string(domain.DeployQueued),
		Trigger:       trigger,
	})
	if err != nil {
		return domain.Deployment{}, wrapCreate("creating deployment", err)
	}
	return deploymentFromRow(row), nil
}

// SetDeploymentBuilder pins which server a deployment's build was routed to
// (builder-role-and-relay.md §4); it stays NULL when builder = target.
func (s *Store) SetDeploymentBuilder(ctx context.Context, id, builderServerID string) (domain.Deployment, error) {
	row, err := s.q.SetDeploymentBuilder(ctx, db.SetDeploymentBuilderParams{
		ID:              id,
		BuilderServerID: pgtype.Text{String: builderServerID, Valid: true},
	})
	if err != nil {
		return domain.Deployment{}, wrap("setting deployment builder", err)
	}
	return deploymentFromRow(row), nil
}

func (s *Store) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	row, err := s.q.GetDeployment(ctx, id)
	if err != nil {
		return domain.Deployment{}, wrap("getting deployment", err)
	}
	return deploymentFromRow(row), nil
}

func (s *Store) UpdateDeploymentStatus(ctx context.Context, id string, status domain.DeploymentStatus, detail string) (domain.Deployment, error) {
	row, err := s.q.UpdateDeploymentStatus(ctx, db.UpdateDeploymentStatusParams{
		ID:     id,
		Status: string(status),
		Detail: detail,
	})
	if err != nil {
		return domain.Deployment{}, wrap("updating deployment status", err)
	}
	return deploymentFromRow(row), nil
}

func (s *Store) ListDeploymentsByApplication(ctx context.Context, appID string, limit int32) ([]domain.Deployment, error) {
	rows, err := s.q.ListDeploymentsByApplication(ctx, db.ListDeploymentsByApplicationParams{ApplicationID: appID, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("store: listing deployments: %w", err)
	}
	out := make([]domain.Deployment, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploymentFromRow(r))
	}
	return out, nil
}

// ListActiveDeploymentsByApplication returns an app's non-terminal
// deployments oldest-first — the scheduler's per-app serialization queue.
func (s *Store) ListActiveDeploymentsByApplication(ctx context.Context, appID string) ([]domain.Deployment, error) {
	rows, err := s.q.ListActiveDeploymentsByApplication(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing active deployments for app: %w", err)
	}
	out := make([]domain.Deployment, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploymentFromRow(r))
	}
	return out, nil
}

func (s *Store) ListActiveDeployments(ctx context.Context) ([]domain.Deployment, error) {
	rows, err := s.q.ListActiveDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing active deployments: %w", err)
	}
	out := make([]domain.Deployment, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploymentFromRow(r))
	}
	return out, nil
}

// ─── Deploy Keys ────────────────────────────────────────────────────────────

func (s *Store) CreateDeployKey(ctx context.Context, dk domain.DeployKey) (domain.DeployKey, error) {
	row, err := s.q.CreateDeployKey(ctx, db.CreateDeployKeyParams{
		ID:              dk.ID,
		Name:            dk.Name,
		PublicKey:       dk.PublicKey,
		Fingerprint:     dk.Fingerprint,
		PrivateKeyCt:    dk.PrivateKeyCT,
		PrivateKeyNonce: dk.PrivateKeyNonce,
	})
	if err != nil {
		return domain.DeployKey{}, wrapCreate("creating deploy key", err)
	}
	return deployKeyFromRow(row), nil
}

func (s *Store) GetDeployKey(ctx context.Context, id string) (domain.DeployKey, error) {
	row, err := s.q.GetDeployKey(ctx, id)
	if err != nil {
		return domain.DeployKey{}, wrap("getting deploy key", err)
	}
	return deployKeyFromRow(row), nil
}

func (s *Store) ListDeployKeys(ctx context.Context) ([]domain.DeployKey, error) {
	rows, err := s.q.ListDeployKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing deploy keys: %w", err)
	}
	out := make([]domain.DeployKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, deployKeyFromRow(r))
	}
	return out, nil
}

// ListApplicationsByDeployKey names the applications still referencing a
// deploy key — the blockers a refused delete reports.
func (s *Store) ListApplicationsByDeployKey(ctx context.Context, keyID string) ([]domain.ApplicationRef, error) {
	rows, err := s.q.ListApplicationsByDeployKey(ctx, pgtype.Text{String: keyID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("store: listing applications by deploy key: %w", err)
	}
	out := make([]domain.ApplicationRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ApplicationRef{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func (s *Store) DeleteDeployKey(ctx context.Context, id string) error {
	if err := s.q.DeleteDeployKey(ctx, id); err != nil {
		return wrapDelete("deleting deploy key", err)
	}
	return nil
}

// ─── Row Mappers ────────────────────────────────────────────────────────────

func deployKeyFromRow(r db.DeployKey) domain.DeployKey {
	return domain.DeployKey{
		ID:              r.ID,
		Name:            r.Name,
		PublicKey:       r.PublicKey,
		Fingerprint:     r.Fingerprint,
		PrivateKeyCT:    r.PrivateKeyCt,
		PrivateKeyNonce: r.PrivateKeyNonce,
		CreatedAt:       r.CreatedAt.Time,
	}
}

func projectFromRow(r db.Project) domain.Project {
	p := domain.Project{
		ID: r.ID, Name: r.Name, TeamID: r.TeamID, Slug: r.Slug,
		LastActivityAt: r.LastActivityAt.Time,
		CreatedAt:      r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.DefaultEnvironmentID.Valid {
		p.DefaultEnvironmentID = r.DefaultEnvironmentID.String
	}
	return p
}

func environmentFromRow(r db.Environment) domain.Environment {
	return domain.Environment{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Kind: r.Kind,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// UpdateProjectFields is a partial edit. A nil field is left alone;
// ClearDefaultEnvironment is the one way to set the default back to nothing,
// because a nil pointer already means "do not touch".
type UpdateProjectFields struct {
	Name                    *string
	TeamID                  *string
	Slug                    *string
	DefaultEnvironmentID    *string
	ClearDefaultEnvironment bool
}

func (s *Store) UpdateProject(ctx context.Context, id string, f UpdateProjectFields) (domain.Project, error) {
	p := db.UpdateProjectParams{ID: id, ClearDefault: pgtype.Bool{Bool: f.ClearDefaultEnvironment, Valid: true}}
	if f.Name != nil {
		p.Name = pgtype.Text{String: *f.Name, Valid: true}
	}
	if f.TeamID != nil {
		p.TeamID = pgtype.Text{String: *f.TeamID, Valid: true}
	}
	if f.Slug != nil {
		p.Slug = pgtype.Text{String: *f.Slug, Valid: true}
	}
	if f.DefaultEnvironmentID != nil {
		p.DefaultEnvironmentID = pgtype.Text{String: *f.DefaultEnvironmentID, Valid: true}
	}
	row, err := s.q.UpdateProject(ctx, p)
	if err != nil {
		return domain.Project{}, wrapUpdate("updating project", err)
	}
	return projectFromRow(row), nil
}

// SlugTakenInTeam reports whether a slug is already used in the team, which is
// the scope of the unique index.
func (s *Store) SlugTakenInTeam(ctx context.Context, teamID, slug string) (bool, error) {
	taken, err := s.q.SlugTakenInTeam(ctx, db.SlugTakenInTeamParams{TeamID: teamID, Slug: slug})
	if err != nil {
		return false, wrap("checking project slug", err)
	}
	return taken, nil
}

// TouchProject records that something happened in the project.
func (s *Store) TouchProject(ctx context.Context, id string) error {
	if err := s.q.TouchProject(ctx, id); err != nil {
		return wrap("touching project", err)
	}
	return nil
}

// TouchProjectForEnvironment is the same, reached through the environment id
// most callers actually hold.
func (s *Store) TouchProjectForEnvironment(ctx context.Context, envID string) error {
	if err := s.q.TouchProjectForEnvironment(ctx, envID); err != nil {
		return wrap("touching project for environment", err)
	}
	return nil
}

// worstStatusByRank maps the rank the rollup query computes back to the status
// word. Kept beside the query it mirrors so the two cannot drift apart.
func worstStatusByRank(rank int32) string {
	switch rank {
	case 5:
		return domain.AppError
	case 4:
		return domain.AppDegraded
	case 3:
		return domain.AppDeploying
	case 1:
		return domain.AppRunning
	default:
		return domain.AppUnknown
	}
}

// ProjectRollups returns per-project resource counts and worst status, keyed by
// project id. Projects holding nothing are absent rather than zero-valued, so a
// caller can tell "empty" from "not counted".
func (s *Store) ProjectRollups(ctx context.Context) (map[string]domain.ProjectRollup, error) {
	rows, err := s.q.ProjectRollups(ctx)
	if err != nil {
		return nil, wrap("rolling up projects", err)
	}
	out := make(map[string]domain.ProjectRollup, len(rows))
	for _, r := range rows {
		out[r.ProjectID] = domain.ProjectRollup{
			ProjectID:        r.ProjectID,
			ApplicationCount: r.ApplicationCount,
			DatabaseCount:    r.DatabaseCount,
			ErrorCount:       r.ErrorCount,
			WorstStatus:      worstStatusByRank(r.WorstRank),
		}
	}
	return out, nil
}

func applicationFromRow(r db.Application) domain.Application {
	return domain.Application{
		ID:            r.ID,
		EnvironmentID: r.EnvironmentID,
		Name:          r.Name,
		Source: domain.AppSource{
			Kind:        r.SourceKind,
			Repo:        r.SourceRepo,
			Branch:      r.SourceBranch,
			DeployKeyID: ptrFromText(r.SourceDeployKeyID),
			Image:       r.SourceImage,
			RegistryID:  ptrFromText(r.SourceRegistryID),
		},
		Build: domain.AppBuild{
			Kind:           r.BuildKind,
			DockerfilePath: r.BuildDockerfilePath,
			Context:        r.BuildContext,
			PushRegistryID: ptrFromText(r.BuildPushRegistryID),
			PushRepository: r.BuildPushRepository,
		},
		Runtime: domain.AppRuntime{
			ServerID:      r.RuntimeServerID,
			Port:          int(r.RuntimePort),
			Replicas:      int(r.RuntimeReplicas),
			CPULimit:      ptrFromFloat4(r.CpuLimit),
			MemoryLimitMB: ptrFromInt4(r.MemoryLimitMb),
		},
		Route: domain.AppRoute{
			Domain:     r.RouteDomain,
			HTTPS:      r.RouteHttps,
			PathPrefix: r.RoutePathPrefix,
		},
		Volumes: volumesFromJSON(r.Volumes),
		Ports:   portsFromJSON(r.Ports),
		Health: domain.AppHealth{
			Kind:            r.HealthKind,
			Path:            r.HealthPath,
			IntervalSeconds: int(r.HealthIntervalSeconds),
			TimeoutSeconds:  int(r.HealthTimeoutSeconds),
			Retries:         int(r.HealthRetries),
		},
		WebhookID:          r.WebhookID,
		WebhookSecretCT:    r.WebhookSecretCt,
		WebhookSecretNonce: r.WebhookSecretNonce,
		PreviewEnabled:     r.PreviewEnabled,
		PreviewBaseDomain:  r.PreviewBaseDomain,
		PreviewTTLHours:    int(r.PreviewTtlHours),
		RestartToken:       r.RestartToken,
		DesiredRevisionID:  ptrFromText(r.DesiredRevisionID),
		Status:             r.Status,
		StatusDetail:       r.StatusDetail,
		ObservedRevisionID: r.ObservedRevisionID,
		StatusObservedAt:   ptrTime(r.StatusObservedAt),
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

func revisionFromRow(r db.Revision) domain.Revision {
	return domain.Revision{
		ID:             r.ID,
		ApplicationID:  r.ApplicationID,
		Image:          r.Image,
		SourceCommit:   r.SourceCommit,
		ConfigSnapshot: r.ConfigSnapshot,
		CreatedAt:      r.CreatedAt.Time,
	}
}

func deploymentFromRow(r db.Deployment) domain.Deployment {
	d := domain.Deployment{
		ID:            r.ID,
		ApplicationID: r.ApplicationID,
		RevisionID:    r.RevisionID,
		Status:        domain.DeploymentStatus(r.Status),
		Trigger:       r.Trigger,
		Detail:        r.Detail,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
		FinishedAt:    ptrTime(r.FinishedAt),
	}
	if r.BuilderServerID.Valid {
		d.BuilderServerID = &r.BuilderServerID.String
	}
	return d
}

func textFromPtr(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func ptrFromText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}

// pgText is textFromPtr for a value that is always present — a comparison
// operand rather than a nullable column.
func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

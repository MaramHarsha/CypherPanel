package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// Persistence for the Phase 2 resource model. Like store.go, all pgx/pgtype
// types stay in this package; callers speak domain types.

// ─── Projects ───────────────────────────────────────────────────────────────

func (s *Store) CreateProject(ctx context.Context, id, name string) (domain.Project, error) {
	row, err := s.q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: name})
	if err != nil {
		return domain.Project{}, fmt.Errorf("store: creating project: %w", err)
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
func (s *Store) CreateProjectWithEnvironment(ctx context.Context, projectID, name, envID, envName string) (domain.Project, domain.Environment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	qtx := s.q.WithTx(tx)
	prow, err := qtx.CreateProject(ctx, db.CreateProjectParams{ID: projectID, Name: name})
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("store: creating project: %w", err)
	}
	erow, err := qtx.CreateEnvironment(ctx, db.CreateEnvironmentParams{ID: envID, ProjectID: projectID, Name: envName})
	if err != nil {
		return domain.Project{}, domain.Environment{}, fmt.Errorf("store: creating environment: %w", err)
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
		return domain.Environment{}, fmt.Errorf("store: creating environment: %w", err)
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
	row, err := s.q.CreateApplication(ctx, db.CreateApplicationParams{
		ID:                    a.ID,
		EnvironmentID:         a.EnvironmentID,
		Name:                  a.Name,
		SourceKind:            a.Source.Kind,
		SourceRepo:            a.Source.Repo,
		SourceBranch:          a.Source.Branch,
		SourceDeployKeyID:     textFromPtr(a.Source.DeployKeyID),
		BuildKind:             a.Build.Kind,
		BuildDockerfilePath:   a.Build.DockerfilePath,
		BuildContext:          a.Build.Context,
		RuntimeServerID:       a.Runtime.ServerID,
		RuntimePort:           int32(a.Runtime.Port),
		RuntimeReplicas:       int32(a.Runtime.Replicas),
		RouteDomain:           a.Route.Domain,
		RouteHttps:            a.Route.HTTPS,
		RoutePathPrefix:       a.Route.PathPrefix,
		HealthPath:            a.Health.Path,
		HealthIntervalSeconds: int32(a.Health.IntervalSeconds),
		HealthTimeoutSeconds:  int32(a.Health.TimeoutSeconds),
		HealthRetries:         int32(a.Health.Retries),
		WebhookID:             a.WebhookID,
		WebhookSecretCt:       a.WebhookSecretCT,
		WebhookSecretNonce:    a.WebhookSecretNonce,
	})
	if err != nil {
		return domain.Application{}, fmt.Errorf("store: creating application: %w", err)
	}
	return applicationFromRow(row), nil
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
	})
	if err != nil {
		return fmt.Errorf("store: upserting env var: %w", err)
	}
	return nil
}

func (s *Store) ListEnvVars(ctx context.Context, appID string) ([]domain.EnvVar, error) {
	rows, err := s.q.ListEnvVars(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("store: listing env vars: %w", err)
	}
	out := make([]domain.EnvVar, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.EnvVar{Key: r.Key, ValueCT: r.ValueCt, ValueNonce: r.ValueNonce})
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
		return domain.Revision{}, fmt.Errorf("store: creating revision: %w", err)
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
		return domain.Deployment{}, fmt.Errorf("store: creating deployment: %w", err)
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

// ─── mapping helpers ────────────────────────────────────────────────────────

func projectFromRow(r db.Project) domain.Project {
	return domain.Project{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
}

func environmentFromRow(r db.Environment) domain.Environment {
	return domain.Environment{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
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
		},
		Build: domain.AppBuild{
			Kind:           r.BuildKind,
			DockerfilePath: r.BuildDockerfilePath,
			Context:        r.BuildContext,
		},
		Runtime: domain.AppRuntime{
			ServerID: r.RuntimeServerID,
			Port:     int(r.RuntimePort),
			Replicas: int(r.RuntimeReplicas),
		},
		Route: domain.AppRoute{
			Domain:     r.RouteDomain,
			HTTPS:      r.RouteHttps,
			PathPrefix: r.RoutePathPrefix,
		},
		Health: domain.AppHealth{
			Path:            r.HealthPath,
			IntervalSeconds: int(r.HealthIntervalSeconds),
			TimeoutSeconds:  int(r.HealthTimeoutSeconds),
			Retries:         int(r.HealthRetries),
		},
		WebhookID:          r.WebhookID,
		WebhookSecretCT:    r.WebhookSecretCt,
		WebhookSecretNonce: r.WebhookSecretNonce,
		DesiredRevisionID:  ptrFromText(r.DesiredRevisionID),
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
	return domain.Deployment{
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

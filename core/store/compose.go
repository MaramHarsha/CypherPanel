package store

// Compose Stacks (compose-stacks.md §2).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func (s *Store) CreateComposeStack(ctx context.Context, st domain.ComposeStack) (domain.ComposeStack, error) {
	row, err := s.q.CreateComposeStack(ctx, db.CreateComposeStackParams{
		ID: st.ID, EnvironmentID: st.EnvironmentID, Name: st.Name,
		RuntimeServerID: st.ServerID,
		RouteDomain:     st.Route.Domain, RouteService: st.Route.Service,
		RoutePort:  int32(st.Route.Port), //nolint:gosec // validated 0–65535
		RouteHttps: st.Route.HTTPS,
	})
	if err != nil {
		return domain.ComposeStack{}, wrapCreate("creating compose stack", err)
	}
	return composeStackFromRow(row), nil
}

func (s *Store) GetComposeStack(ctx context.Context, id string) (domain.ComposeStack, error) {
	row, err := s.q.GetComposeStack(ctx, id)
	if err != nil {
		return domain.ComposeStack{}, wrap("getting compose stack", err)
	}
	return composeStackFromRow(row), nil
}

func (s *Store) ListComposeStacksByEnvironment(ctx context.Context, envID string) ([]domain.ComposeStack, error) {
	rows, err := s.q.ListComposeStacksByEnvironment(ctx, envID)
	if err != nil {
		return nil, wrap("listing compose stacks", err)
	}
	return composeStacksFromRows(rows), nil
}

// ListComposeStacksByServer is what desired state is assembled from: one
// server's whole set, so absence means remove.
func (s *Store) ListComposeStacksByServer(ctx context.Context, serverID string) ([]domain.ComposeStack, error) {
	rows, err := s.q.ListComposeStacksByServer(ctx, serverID)
	if err != nil {
		return nil, wrap("listing compose stacks by server", err)
	}
	return composeStacksFromRows(rows), nil
}

func (s *Store) UpdateComposeStackConfig(ctx context.Context, st domain.ComposeStack) (domain.ComposeStack, error) {
	row, err := s.q.UpdateComposeStackConfig(ctx, db.UpdateComposeStackConfigParams{
		ID: st.ID, Name: st.Name,
		RouteDomain: st.Route.Domain, RouteService: st.Route.Service,
		RoutePort:  int32(st.Route.Port), //nolint:gosec // validated 0–65535
		RouteHttps: st.Route.HTTPS,
	})
	if err != nil {
		return domain.ComposeStack{}, wrapUpdate("updating compose stack", err)
	}
	return composeStackFromRow(row), nil
}

func (s *Store) SetComposeStackDesiredRevision(ctx context.Context, id, revisionID string) (domain.ComposeStack, error) {
	row, err := s.q.SetComposeStackDesiredRevision(ctx, db.SetComposeStackDesiredRevisionParams{
		ID: id, DesiredRevisionID: pgtype.Text{String: revisionID, Valid: true},
	})
	if err != nil {
		return domain.ComposeStack{}, wrapUpdate("setting compose desired revision", err)
	}
	return composeStackFromRow(row), nil
}

// SetComposeStackStatus is the plane's own override while a converge is in
// flight; SetComposeStackObservedStatus is what the agent reports back.
func (s *Store) SetComposeStackStatus(ctx context.Context, id, status, detail string) error {
	if err := s.q.SetComposeStackStatus(ctx, db.SetComposeStackStatusParams{
		ID: id, Status: status, StatusDetail: detail,
	}); err != nil {
		return wrapUpdate("setting compose stack status", err)
	}
	return nil
}

func (s *Store) SetComposeStackObservedStatus(ctx context.Context, id, status, detail, revisionID string, at time.Time) error {
	if err := s.q.SetComposeStackObservedStatus(ctx, db.SetComposeStackObservedStatusParams{
		ID: id, Status: status, StatusDetail: detail, ObservedRevisionID: revisionID,
		StatusObservedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return wrapUpdate("recording compose stack observation", err)
	}
	return nil
}

func (s *Store) DeleteComposeStack(ctx context.Context, id string) error {
	if err := s.q.DeleteComposeStack(ctx, id); err != nil {
		return wrapDelete("deleting compose stack", err)
	}
	return nil
}

func (s *Store) CreateComposeRevision(ctx context.Context, r domain.ComposeRevision) (domain.ComposeRevision, error) {
	row, err := s.q.CreateComposeRevision(ctx, db.CreateComposeRevisionParams{
		ID: r.ID, StackID: r.StackID, ComposeYaml: r.ComposeYAML,
	})
	if err != nil {
		return domain.ComposeRevision{}, wrapCreate("creating compose revision", err)
	}
	return composeRevisionFromRow(row), nil
}

func (s *Store) GetComposeRevision(ctx context.Context, id string) (domain.ComposeRevision, error) {
	row, err := s.q.GetComposeRevision(ctx, id)
	if err != nil {
		return domain.ComposeRevision{}, wrap("getting compose revision", err)
	}
	return composeRevisionFromRow(row), nil
}

func (s *Store) ListComposeRevisions(ctx context.Context, stackID string, limit int32) ([]domain.ComposeRevision, error) {
	rows, err := s.q.ListComposeRevisions(ctx, db.ListComposeRevisionsParams{StackID: stackID, Limit: limit})
	if err != nil {
		return nil, wrap("listing compose revisions", err)
	}
	out := make([]domain.ComposeRevision, 0, len(rows))
	for _, r := range rows {
		out = append(out, composeRevisionFromRow(r))
	}
	return out, nil
}

// LatestComposeRevision reports what the stack last deployed, so an edit that
// changes nothing about the file does not mint a revision nobody asked for.
// ErrNotFound before the first deploy.
func (s *Store) LatestComposeRevision(ctx context.Context, stackID string) (domain.ComposeRevision, error) {
	row, err := s.q.LatestComposeRevision(ctx, stackID)
	if err != nil {
		return domain.ComposeRevision{}, wrap("getting latest compose revision", err)
	}
	return composeRevisionFromRow(row), nil
}

func (s *Store) UpsertComposeEnvVar(ctx context.Context, stackID string, v domain.ComposeEnvVar) error {
	if err := s.q.UpsertComposeEnvVar(ctx, db.UpsertComposeEnvVarParams{
		StackID: stackID, Key: v.Key, ValueCt: v.ValueCT, ValueNonce: v.ValueNonce,
	}); err != nil {
		return wrapUpdate("setting compose env var", err)
	}
	return nil
}

func (s *Store) ListComposeEnvVars(ctx context.Context, stackID string) ([]domain.ComposeEnvVar, error) {
	rows, err := s.q.ListComposeEnvVars(ctx, stackID)
	if err != nil {
		return nil, wrap("listing compose env vars", err)
	}
	out := make([]domain.ComposeEnvVar, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ComposeEnvVar{Key: r.Key, ValueCT: r.ValueCt, ValueNonce: r.ValueNonce})
	}
	return out, nil
}

func (s *Store) DeleteComposeEnvVar(ctx context.Context, stackID, key string) error {
	if err := s.q.DeleteComposeEnvVar(ctx, db.DeleteComposeEnvVarParams{StackID: stackID, Key: key}); err != nil {
		return wrapDelete("deleting compose env var", err)
	}
	return nil
}

func composeStacksFromRows(rows []db.ComposeStack) []domain.ComposeStack {
	out := make([]domain.ComposeStack, 0, len(rows))
	for _, r := range rows {
		out = append(out, composeStackFromRow(r))
	}
	return out
}

func composeStackFromRow(r db.ComposeStack) domain.ComposeStack {
	return domain.ComposeStack{
		ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, ServerID: r.RuntimeServerID,
		DesiredRevisionID: ptrFromText(r.DesiredRevisionID),
		Route: domain.ComposeRoute{
			Domain: r.RouteDomain, Service: r.RouteService,
			Port: int(r.RoutePort), HTTPS: r.RouteHttps,
		},
		Status: r.Status, StatusDetail: r.StatusDetail,
		ObservedRevisionID: r.ObservedRevisionID,
		StatusObservedAt:   ptrTime(r.StatusObservedAt),
		CreatedAt:          r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func composeRevisionFromRow(r db.ComposeRevision) domain.ComposeRevision {
	return domain.ComposeRevision{
		ID: r.ID, StackID: r.StackID, ComposeYAML: r.ComposeYaml, CreatedAt: r.CreatedAt.Time,
	}
}

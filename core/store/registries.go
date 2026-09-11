package store

// Container registry credentials (registries.md; ADR-008 path 3).

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func (s *Store) CreateRegistry(ctx context.Context, r domain.Registry) (domain.Registry, error) {
	row, err := s.q.CreateRegistry(ctx, db.CreateRegistryParams{
		ID: r.ID, TeamID: r.TeamID, Name: r.Name, Url: r.URL, Username: r.Username,
		TokenCt: r.TokenCT, TokenNonce: r.TokenNonce,
		CanPull: r.CanPull, CanPush: r.CanPush,
	})
	if err != nil {
		return domain.Registry{}, wrapCreate("creating registry", err)
	}
	return registryFromRow(row), nil
}

func (s *Store) GetRegistry(ctx context.Context, id string) (domain.Registry, error) {
	row, err := s.q.GetRegistry(ctx, id)
	if err != nil {
		return domain.Registry{}, wrap("getting registry", err)
	}
	return registryFromRow(row), nil
}

// ListRegistriesByTeams returns the registries visible to a caller, given the
// teams they belong to. Listing filters rather than refuses (teams-and-roles.md
// §3), so a caller in no teams gets an empty list, not an error.
func (s *Store) ListRegistriesByTeams(ctx context.Context, teamIDs []string) ([]domain.Registry, error) {
	if len(teamIDs) == 0 {
		return []domain.Registry{}, nil
	}
	rows, err := s.q.ListRegistriesByTeams(ctx, teamIDs)
	if err != nil {
		return nil, wrap("listing registries", err)
	}
	out := make([]domain.Registry, 0, len(rows))
	for _, r := range rows {
		out = append(out, registryFromRow(r))
	}
	return out, nil
}

// UpdateRegistryFields is a partial edit. A nil field is left alone, so
// rotating a token does not mean re-sending the URL.
type UpdateRegistryFields struct {
	Name       *string
	URL        *string
	Username   *string
	TokenCT    []byte
	TokenNonce []byte
	CanPull    *bool
	CanPush    *bool
}

func (s *Store) UpdateRegistry(ctx context.Context, id string, f UpdateRegistryFields) (domain.Registry, error) {
	p := db.UpdateRegistryParams{ID: id, TokenCt: f.TokenCT, TokenNonce: f.TokenNonce}
	if f.Name != nil {
		p.Name = pgtype.Text{String: *f.Name, Valid: true}
	}
	if f.URL != nil {
		p.Url = pgtype.Text{String: *f.URL, Valid: true}
	}
	if f.Username != nil {
		p.Username = pgtype.Text{String: *f.Username, Valid: true}
	}
	if f.CanPull != nil {
		p.CanPull = pgtype.Bool{Bool: *f.CanPull, Valid: true}
	}
	if f.CanPush != nil {
		p.CanPush = pgtype.Bool{Bool: *f.CanPush, Valid: true}
	}
	row, err := s.q.UpdateRegistry(ctx, p)
	if err != nil {
		return domain.Registry{}, wrapUpdate("updating registry", err)
	}
	return registryFromRow(row), nil
}

// RecordRegistryTest stores the outcome of the last authentication attempt, so
// the list can show whether a credential is known-good without re-testing on
// every page render.
func (s *Store) RecordRegistryTest(ctx context.Context, id string, ok bool, detail string) (domain.Registry, error) {
	row, err := s.q.RecordRegistryTest(ctx, db.RecordRegistryTestParams{ID: id, LastTestOk: ok, LastTestDetail: detail})
	if err != nil {
		return domain.Registry{}, wrapUpdate("recording registry test", err)
	}
	return registryFromRow(row), nil
}

// DeleteRegistry removes a credential. Applications referencing it hold it back
// through ON DELETE RESTRICT, which surfaces as ErrConflict.
func (s *Store) DeleteRegistry(ctx context.Context, id string) error {
	if err := s.q.DeleteRegistry(ctx, id); err != nil {
		return wrapDelete("deleting registry", err)
	}
	return nil
}

// ApplicationsUsingRegistry names what would break if the registry went away.
func (s *Store) ApplicationsUsingRegistry(ctx context.Context, id string) ([]domain.RegistryUse, error) {
	rows, err := s.q.ApplicationsUsingRegistry(ctx, pgtype.Text{String: id, Valid: true})
	if err != nil {
		return nil, wrap("listing registry users", err)
	}
	out := make([]domain.RegistryUse, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.RegistryUse{
			ApplicationID:   r.ID,
			ApplicationName: r.Name,
			EnvironmentName: r.EnvironmentName,
			ProjectName:     r.ProjectName,
			Pulls:           r.Pulls,
			Pushes:          r.Pushes,
		})
	}
	return out, nil
}

func registryFromRow(r db.Registry) domain.Registry {
	return domain.Registry{
		ID: r.ID, TeamID: r.TeamID, Name: r.Name, URL: r.Url, Username: r.Username,
		TokenCT: r.TokenCt, TokenNonce: r.TokenNonce,
		CanPull: r.CanPull, CanPush: r.CanPush,
		LastTestAt: ptrTime(r.LastTestAt), LastTestOK: r.LastTestOk, LastTestDetail: r.LastTestDetail,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

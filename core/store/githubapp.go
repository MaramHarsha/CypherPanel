package store

// The GitHub App's persistence (github-app.md §2).

import (
	"context"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ListApplicationsByRepo finds every application a push should deploy. EVERY
// one: a repository can legitimately be deployed by several environments, and
// picking one would silently skip the rest (github-app.md §6).
//
// fullName is GitHub's canonical `owner/repo`. The query canonicalises the
// stored clone URL down to the same shape rather than comparing the two
// literally — see the SQL, and github-app.md's implementation note, for why a
// literal comparison made every App push a silent no-op.
func (s *Store) ListApplicationsByRepo(ctx context.Context, fullName, branch string) ([]domain.Application, error) {
	rows, err := s.q.ListApplicationsByRepo(ctx, db.ListApplicationsByRepoParams{
		FullName: fullName, Branch: branch,
	})
	if err != nil {
		return nil, wrap("listing applications by repo", err)
	}
	out := make([]domain.Application, 0, len(rows))
	for _, r := range rows {
		out = append(out, applicationFromRow(r))
	}
	return out, nil
}

func (s *Store) GetGitHubApp(ctx context.Context) (domain.GitHubApp, error) {
	row, err := s.q.GetGitHubApp(ctx)
	if err != nil {
		return domain.GitHubApp{}, wrap("reading the github app", err)
	}
	return domain.GitHubApp{
		AppID: row.AppID, Slug: row.Slug,
		ConfigCT: row.ConfigCt, ConfigNonce: row.ConfigNonce,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (s *Store) SetGitHubApp(ctx context.Context, appID int64, slug string, ct, nonce []byte) error {
	if err := s.q.SetGitHubApp(ctx, db.SetGitHubAppParams{
		AppID: appID, Slug: slug, ConfigCt: ct, ConfigNonce: nonce,
	}); err != nil {
		return wrapUpdate("saving the github app", err)
	}
	return nil
}

func (s *Store) DeleteGitHubApp(ctx context.Context) error {
	if err := s.q.DeleteGitHubApp(ctx); err != nil {
		return wrapDelete("deleting the github app", err)
	}
	return nil
}

func (s *Store) ListGitHubInstallations(ctx context.Context) ([]domain.GitHubInstallation, error) {
	rows, err := s.q.ListGitHubInstallations(ctx)
	if err != nil {
		return nil, wrap("listing github installations", err)
	}
	out := make([]domain.GitHubInstallation, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.GitHubInstallation{
			ID: r.ID, InstallationID: r.InstallationID,
			AccountLogin: r.AccountLogin, AccountType: r.AccountType,
			RepoSelection: r.RepoSelection, RefreshedAt: r.RefreshedAt.Time,
		})
	}
	return out, nil
}

// ReplaceGitHubInstallations makes the cache match GitHub's answer exactly:
// upsert what it reported, delete what it did not. An empty list is a legal
// instruction — it is what disconnecting the App means — so it must clear the
// table rather than being read as "nothing to do".
func (s *Store) ReplaceGitHubInstallations(ctx context.Context, rows []domain.GitHubInstallation) error {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.InstallationID)
	}
	if err := s.q.DeleteGitHubInstallationsNotIn(ctx, ids); err != nil {
		return wrapDelete("pruning github installations", err)
	}
	for _, r := range rows {
		if err := s.q.UpsertGitHubInstallation(ctx, db.UpsertGitHubInstallationParams{
			ID: r.ID, InstallationID: r.InstallationID,
			AccountLogin: r.AccountLogin, AccountType: r.AccountType,
			RepoSelection: r.RepoSelection,
		}); err != nil {
			return wrapUpdate("caching a github installation", err)
		}
	}
	return nil
}

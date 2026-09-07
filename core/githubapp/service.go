package githubapp

// The panel-level credential and the installation cache (github-app.md §2, §3).
//
// Shaped on core/dns's provider singleton deliberately rather than invented: one
// sealed panel-level credential, owner-only to write, with an OBSERVED cache
// beside it that is refreshed from the API and never authored. An operator who
// has connected Cloudflare has already met this screen, and a reviewer who has
// read that package has already reviewed this shape.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError is the operator's to fix, and carries a sentence for them.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Sealer is the master-key box (consumer-defined; core/secrets satisfies it).
type Sealer interface {
	Seal(plaintext []byte) (ct, nonce []byte, err error)
	Open(ct, nonce []byte) ([]byte, error)
}

// Store is the persistence this needs.
type Store interface {
	GetGitHubApp(ctx context.Context) (domain.GitHubApp, error)
	SetGitHubApp(ctx context.Context, appID int64, slug string, ct, nonce []byte) error
	DeleteGitHubApp(ctx context.Context) error
	ReplaceGitHubInstallations(ctx context.Context, rows []domain.GitHubInstallation) error
	ListGitHubInstallations(ctx context.Context) ([]domain.GitHubInstallation, error)
}

// Settings is the view a screen gets. It NEVER carries the private key, not
// even redacted: a field that is sometimes a secret is a field that eventually
// leaks one.
type Settings struct {
	Configured    bool
	AppID         int64
	Slug          string
	InstallURL    string
	Installations []domain.GitHubInstallation
	UpdatedAt     time.Time
}

// Service owns the App.
type Service struct {
	store Store
	box   Sealer
	log   *slog.Logger
	// newClient is swapped in tests. Production builds the real one.
	newClient func(Config) (*Client, error)
}

// New wires the service.
func New(s Store, box Sealer, log *slog.Logger) *Service {
	return &Service{store: s, box: box, log: log, newClient: NewClient}
}

// Get reports whether an App is connected and where it is installed.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	row, err := s.store.GetGitHubApp(ctx)
	if errors.Is(err, store.ErrNotFound) {
		// The ordinary state of every install that has not set one up. It is
		// not an error and must not surface as one — the mistake core/dns made
		// and recorded.
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	installs, err := s.store.ListGitHubInstallations(ctx)
	if err != nil {
		return Settings{}, err
	}
	out := Settings{
		Configured: true, AppID: row.AppID, Slug: row.Slug,
		Installations: installs, UpdatedAt: row.UpdatedAt,
	}
	if row.Slug != "" {
		// Where the operator sends themselves to install it. Built here because
		// the panel knows the slug and the operator would otherwise be told to
		// "go to GitHub and find your App".
		out.InstallURL = "https://github.com/apps/" + row.Slug + "/installations/new"
	}
	return out, nil
}

// Set validates the credentials against GitHub BEFORE persisting anything, then
// caches the installations it can see.
//
// A key that cannot authenticate is refused here rather than at the first
// deploy: a credential that fails at first use is a dead end, and a dead end is
// a bug (ui-principles §11). This is core/dns's rule applied to a second
// provider, which is the point of borrowing the shape.
func (s *Service) Set(ctx context.Context, c Config) (Settings, error) {
	if c.AppID <= 0 {
		return Settings{}, invalid("the App ID is the number on the App's settings page")
	}
	if strings.TrimSpace(c.PrivateKeyPEM) == "" {
		return Settings{}, invalid("the private key GitHub generated for the App is required")
	}
	cli, err := s.newClient(c)
	if err != nil {
		return Settings{}, err // ErrBadKey, already a sentence
	}
	installs, err := cli.ListInstallations(ctx)
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return Settings{}, invalid("GitHub rejected these credentials — check the App ID matches the key, and that the key was not regenerated")
		}
		return Settings{}, err
	}

	body, err := json.Marshal(struct {
		PrivateKeyPEM string `json:"private_key_pem"`
		WebhookSecret string `json:"webhook_secret"`
	}{c.PrivateKeyPEM, c.WebhookSecret})
	if err != nil {
		return Settings{}, fmt.Errorf("githubapp: encoding config: %w", err)
	}
	ct, nonce, err := s.box.Seal(body)
	if err != nil {
		return Settings{}, fmt.Errorf("githubapp: sealing config: %w", err)
	}
	if err := s.store.SetGitHubApp(ctx, c.AppID, c.Slug, ct, nonce); err != nil {
		return Settings{}, err
	}
	if err := s.cache(ctx, installs); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx)
}

// Delete forgets the App. Applications that used it fail their next deploy with
// a reason rather than silently falling back to an anonymous clone, which would
// succeed for a public repository and fail confusingly for a private one.
func (s *Service) Delete(ctx context.Context) error {
	if err := s.store.ReplaceGitHubInstallations(ctx, nil); err != nil {
		return err
	}
	return s.store.DeleteGitHubApp(ctx)
}

// RefreshInstallations re-reads where the App is installed. The API's answer is
// the whole truth, so anything it no longer reports is no longer ours (§3).
func (s *Service) RefreshInstallations(ctx context.Context) ([]domain.GitHubInstallation, error) {
	cli, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	installs, err := cli.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.cache(ctx, installs); err != nil {
		return nil, err
	}
	return s.store.ListGitHubInstallations(ctx)
}

func (s *Service) cache(ctx context.Context, installs []Installation) error {
	rows := make([]domain.GitHubInstallation, 0, len(installs))
	for _, i := range installs {
		rows = append(rows, domain.GitHubInstallation{
			ID: ids.New("ghi"), InstallationID: i.InstallationID,
			AccountLogin: i.AccountLogin, AccountType: i.AccountType,
			RepoSelection: i.RepoSelection,
		})
	}
	return s.store.ReplaceGitHubInstallations(ctx, rows)
}

// Repositories lists what every installation can see, so creating an
// application is "pick one" instead of "type a URL and hope" (§5).
func (s *Service) Repositories(ctx context.Context) ([]Repository, error) {
	cli, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	installs, err := s.store.ListGitHubInstallations(ctx)
	if err != nil {
		return nil, err
	}
	var out []Repository
	for _, in := range installs {
		repos, err := cli.ListRepositories(ctx, in.InstallationID)
		if err != nil {
			// One installation the App lost access to must not hide every other
			// organisation's repositories.
			s.log.Warn("githubapp: listing repositories", "installation", in.InstallationID, "error", err)
			continue
		}
		out = append(out, repos...)
	}
	return out, nil
}

// CloneToken mints the credential for one build. This is the one moment a clone
// credential exists (§4) and the one place outside Set that the key is unsealed.
func (s *Service) CloneToken(ctx context.Context, installationID int64) (username, password string, err error) {
	cli, err := s.client(ctx)
	if err != nil {
		return "", "", err
	}
	token, err := cli.Token(ctx, installationID)
	if err != nil {
		return "", "", err
	}
	u, p := CloneCredential(token)
	return u, p, nil
}

// WebhookSecret is what verifies the App's deliveries.
func (s *Service) WebhookSecret(ctx context.Context) (string, error) {
	_, cfg, err := s.load(ctx)
	if err != nil {
		return "", err
	}
	return cfg.WebhookSecret, nil
}

// client builds a real client from the sealed config. Built per call rather
// than held: the token cache it carries is a nicety, and a long-lived client
// holding an unsealed private key in memory for the process's whole life is a
// worse trade than re-parsing a PEM.
func (s *Service) client(ctx context.Context) (*Client, error) {
	row, cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	cfg.AppID, cfg.Slug = row.AppID, row.Slug
	return s.newClient(cfg)
}

func (s *Service) load(ctx context.Context) (domain.GitHubApp, Config, error) {
	row, err := s.store.GetGitHubApp(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return domain.GitHubApp{}, Config{}, ErrNotConfigured
	}
	if err != nil {
		return domain.GitHubApp{}, Config{}, err
	}
	plain, err := s.box.Open(row.ConfigCT, row.ConfigNonce)
	if err != nil {
		return domain.GitHubApp{}, Config{}, fmt.Errorf("githubapp: opening config: %w", err)
	}
	var cfg struct {
		PrivateKeyPEM string `json:"private_key_pem"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return domain.GitHubApp{}, Config{}, fmt.Errorf("githubapp: decoding config: %w", err)
	}
	return row, Config{
		AppID: row.AppID, Slug: row.Slug,
		PrivateKeyPEM: cfg.PrivateKeyPEM, WebhookSecret: cfg.WebhookSecret,
	}, nil
}

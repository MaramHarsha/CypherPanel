// Package applications is the operator-facing lifecycle for the Application
// resource (docs/features/application-deploy.md §1): create (with validated
// config and sealed secrets), inspect, list, delete, and manage environment
// variables. Deploying an application is the scheduler's job, wired separately.
//
// Secrets never leave this package in the clear on their way to storage: env
// var values and the generated webhook HMAC secret are sealed with the
// master-key Box before they reach the store (threat-model §5.1). The API
// returns the webhook secret exactly once, at creation, and never again.
package applications

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrServerNotFound is returned when the target runtime server does not exist.
var ErrServerNotFound = errors.New("applications: target server not found")

// ErrEnvironmentNotFound is returned when the addressed environment does not
// exist — distinct from store.ErrNotFound (which, from this package, means the
// application itself is missing) so handlers name the right entity.
var ErrEnvironmentNotFound = errors.New("applications: environment not found")

// ValidationError is a client-caused input error; handlers map it to 400 with
// its message (which never contains secrets).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "applications: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	CreateApplicationWithEnv(ctx context.Context, a domain.Application, envVars []domain.EnvVar) (domain.Application, error)
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetApplicationByWebhookID(ctx context.Context, webhookID string) (domain.Application, error)
	ListApplicationsByEnvironment(ctx context.Context, envID string) ([]domain.Application, error)
	UpdateApplicationConfig(ctx context.Context, a domain.Application) (domain.Application, error)
	DeleteApplication(ctx context.Context, id string) error
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
	UpsertEnvVar(ctx context.Context, appID string, v domain.EnvVar) error
	ListEnvVars(ctx context.Context, appID string) ([]domain.EnvVar, error)
	DeleteEnvVar(ctx context.Context, appID, key string) error
}

// Sealer seals plaintext for storage at rest. *secret.Box satisfies it.
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// Service manages applications.
type Service struct {
	store  Store
	sealer Sealer
}

// NewService wires the service.
func NewService(s Store, sealer Sealer) *Service {
	return &Service{store: s, sealer: sealer}
}

// CreateInput is the caller-supplied config for a new application. Missing
// optional fields are defaulted; invalid fields are rejected.
type CreateInput struct {
	Name    string
	Source  domain.AppSource
	Build   domain.AppBuild
	Runtime domain.AppRuntime
	Route   domain.AppRoute
	Health  domain.AppHealth
	EnvVars map[string]string // plaintext; sealed before storage
}

// Create validates and creates an application under envID, returning it along
// with the raw webhook secret (shown exactly once). Secrets are sealed before
// they reach the store.
func (s *Service) Create(ctx context.Context, envID string, in CreateInput) (app domain.Application, webhookSecret string, err error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Application{}, "", ErrEnvironmentNotFound
		}
		return domain.Application{}, "", fmt.Errorf("applications: getting environment: %w", err)
	}
	in, err = validateAndDefault(in)
	if err != nil {
		return domain.Application{}, "", err
	}
	if _, err := s.store.GetServer(ctx, in.Runtime.ServerID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Application{}, "", ErrServerNotFound
		}
		return domain.Application{}, "", fmt.Errorf("applications: getting server: %w", err)
	}

	webhookSecret = ids.Secret()
	wct, wnonce, err := s.sealer.Seal([]byte(webhookSecret))
	if err != nil {
		return domain.Application{}, "", fmt.Errorf("applications: sealing webhook secret: %w", err)
	}

	sealedVars := make([]domain.EnvVar, 0, len(in.EnvVars))
	for k, v := range in.EnvVars {
		ct, nonce, serr := s.sealer.Seal([]byte(v))
		if serr != nil {
			return domain.Application{}, "", fmt.Errorf("applications: sealing env var: %w", serr)
		}
		sealedVars = append(sealedVars, domain.EnvVar{Key: k, ValueCT: ct, ValueNonce: nonce})
	}

	created, err := s.store.CreateApplicationWithEnv(ctx, domain.Application{
		ID:                 ids.New(ids.PrefixApplication),
		EnvironmentID:      envID,
		Name:               in.Name,
		Source:             in.Source,
		Build:              in.Build,
		Runtime:            in.Runtime,
		Route:              in.Route,
		Health:             in.Health,
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    wct,
		WebhookSecretNonce: wnonce,
	}, sealedVars)
	if err != nil {
		return domain.Application{}, "", fmt.Errorf("applications: creating application: %w", err)
	}
	return created, webhookSecret, nil
}

// Get returns one application; the error wraps store.ErrNotFound when absent.
func (s *Service) Get(ctx context.Context, id string) (domain.Application, error) {
	app, err := s.store.GetApplication(ctx, id)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: getting application: %w", err)
	}
	return app, nil
}

// GetByWebhookID resolves an application from its public webhook id.
func (s *Service) GetByWebhookID(ctx context.Context, webhookID string) (domain.Application, error) {
	app, err := s.store.GetApplicationByWebhookID(ctx, webhookID)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: getting by webhook id: %w", err)
	}
	return app, nil
}

// UpdateInput patches an application's configuration. Nil sections are left
// unchanged; a non-nil section replaces that section wholesale. The runtime
// server is deliberately not patchable (moving an app needs the distribute
// step, ADR-008), and neither are replicas (fixed at 1 in the slice).
type UpdateInput struct {
	Name   *string
	Source *domain.AppSource
	Build  *domain.AppBuild
	Port   *int
	Route  *domain.AppRoute
	Health *domain.AppHealth
}

// Update applies a config patch. The change shapes the next revision — a
// running revision keeps its snapshot until the next deploy (spec §4).
func (s *Service) Update(ctx context.Context, appID string, in UpdateInput) (domain.Application, error) {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: getting application: %w", err)
	}
	if in.Name != nil {
		app.Name = *in.Name
	}
	if in.Source != nil {
		app.Source = *in.Source
	}
	if in.Build != nil {
		app.Build = *in.Build
	}
	if in.Port != nil {
		app.Runtime.Port = *in.Port
	}
	if in.Route != nil {
		app.Route = *in.Route
	}
	if in.Health != nil {
		app.Health = *in.Health
	}
	// The merged result must satisfy exactly the create-time rules.
	merged, err := validateAndDefault(CreateInput{
		Name:    app.Name,
		Source:  app.Source,
		Build:   app.Build,
		Runtime: app.Runtime,
		Route:   app.Route,
		Health:  app.Health,
	})
	if err != nil {
		return domain.Application{}, err
	}
	app.Name, app.Source, app.Build, app.Runtime, app.Route, app.Health =
		merged.Name, merged.Source, merged.Build, merged.Runtime, merged.Route, merged.Health
	updated, err := s.store.UpdateApplicationConfig(ctx, app)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: updating application: %w", err)
	}
	return updated, nil
}

// List returns the applications in an environment (verifying it exists first).
func (s *Service) List(ctx context.Context, envID string) ([]domain.Application, error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("applications: getting environment: %w", err)
	}
	list, err := s.store.ListApplicationsByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("applications: listing: %w", err)
	}
	return list, nil
}

// Delete removes an application and its env vars (by cascade).
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.store.DeleteApplication(ctx, id); err != nil {
		return fmt.Errorf("applications: deleting: %w", err)
	}
	return nil
}

// SetEnvVar seals and upserts one environment variable.
func (s *Service) SetEnvVar(ctx context.Context, appID, key, value string) error {
	if _, err := s.store.GetApplication(ctx, appID); err != nil {
		return fmt.Errorf("applications: getting application: %w", err)
	}
	if !validEnvKey(key) {
		return invalid("env var key must match [A-Za-z_][A-Za-z0-9_]*")
	}
	ct, nonce, err := s.sealer.Seal([]byte(value))
	if err != nil {
		return fmt.Errorf("applications: sealing env var: %w", err)
	}
	if err := s.store.UpsertEnvVar(ctx, appID, domain.EnvVar{Key: key, ValueCT: ct, ValueNonce: nonce}); err != nil {
		return fmt.Errorf("applications: setting env var: %w", err)
	}
	return nil
}

// ListEnvVarKeys returns only the keys of an application's env vars — values are
// write-only and never returned (ui-principles §6).
func (s *Service) ListEnvVarKeys(ctx context.Context, appID string) ([]string, error) {
	if _, err := s.store.GetApplication(ctx, appID); err != nil {
		return nil, fmt.Errorf("applications: getting application: %w", err)
	}
	vars, err := s.store.ListEnvVars(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("applications: listing env vars: %w", err)
	}
	keys := make([]string, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
	}
	return keys, nil
}

// DeleteEnvVar removes one environment variable.
func (s *Service) DeleteEnvVar(ctx context.Context, appID, key string) error {
	if _, err := s.store.GetApplication(ctx, appID); err != nil {
		return fmt.Errorf("applications: getting application: %w", err)
	}
	if err := s.store.DeleteEnvVar(ctx, appID, key); err != nil {
		return fmt.Errorf("applications: deleting env var: %w", err)
	}
	return nil
}

func validateAndDefault(in CreateInput) (CreateInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return in, invalid("name must be 1–100 characters")
	}

	switch in.Source.Kind {
	case "github", "git_url":
	default:
		return in, invalid(`source.kind must be "github" or "git_url"`)
	}
	if strings.TrimSpace(in.Source.Repo) == "" {
		return in, invalid("source.repo is required")
	}
	if in.Source.Branch == "" {
		in.Source.Branch = "main"
	}

	if in.Build.Kind == "" {
		in.Build.Kind = "dockerfile"
	}
	if in.Build.Kind != "dockerfile" {
		return in, invalid(`build.kind must be "dockerfile" (the only supported build at v1)`)
	}
	if in.Build.DockerfilePath == "" {
		in.Build.DockerfilePath = "./Dockerfile"
	}
	if in.Build.Context == "" {
		in.Build.Context = "."
	}

	if strings.TrimSpace(in.Runtime.ServerID) == "" {
		return in, invalid("runtime.server_id is required")
	}
	if in.Runtime.Port < 1 || in.Runtime.Port > 65535 {
		return in, invalid("runtime.port must be between 1 and 65535")
	}
	if in.Runtime.Replicas == 0 {
		in.Runtime.Replicas = 1
	}
	if in.Runtime.Replicas != 1 {
		return in, invalid("runtime.replicas must be 1 (multiple replicas are post-v1)")
	}

	if strings.TrimSpace(in.Route.Domain) == "" {
		return in, invalid("route.domain is required")
	}

	// Negative values must be rejected, not defaulted: the wire contract
	// (work.proto HealthCheck) carries these as uint32, so a negative slipping
	// through would wrap into a huge positive on conversion.
	if in.Health.IntervalSeconds < 0 || in.Health.TimeoutSeconds < 0 || in.Health.Retries < 0 {
		return in, invalid("health values must not be negative")
	}
	if in.Health.Path == "" {
		in.Health.Path = "/"
	}
	if in.Health.IntervalSeconds == 0 {
		in.Health.IntervalSeconds = 10
	}
	if in.Health.TimeoutSeconds == 0 {
		in.Health.TimeoutSeconds = 5
	}
	if in.Health.Retries == 0 {
		in.Health.Retries = 3
	}
	for k := range in.EnvVars {
		if !validEnvKey(k) {
			return in, invalid("env var key " + strconv.Quote(k) + " must match [A-Za-z_][A-Za-z0-9_]*")
		}
	}
	return in, nil
}

// validEnvKey enforces the portable environment-variable key charset; anything
// looser (an "=", a newline) would corrupt the container environment later.
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

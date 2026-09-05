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
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
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

// volumeName bounds an operator volume label so the derived Docker volume name
// is safe and deterministic: lowercase alphanumeric plus dashes.
var volumeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// maxVolumes caps mounts per app (a sane bound, not a hard product limit).
const maxVolumes = 20

// Health gate kinds (AppHealth.Kind). Empty defaults to http.
const (
	healthHTTP = "http"
	healthTCP  = "tcp"
	healthNone = "none"
)

// maxPorts caps raw host-port publishes per app.
const maxPorts = 20

// validatePorts checks raw host-port publishes: valid port ranges, a known
// protocol (empty defaults to tcp), no two mappings on the same host port and
// protocol, and a sane count. Ports are independent of the HTTP route.
func validatePorts(ports []domain.PortMapping) error {
	if len(ports) > maxPorts {
		return invalid("at most 20 published ports per application")
	}
	seen := map[string]bool{}
	for i := range ports {
		p := &ports[i]
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return invalid("port protocol must be tcp or udp: " + p.Protocol)
		}
		if p.HostPort < 1 || p.HostPort > 65535 {
			return invalid("host_port must be between 1 and 65535")
		}
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return invalid("container_port must be between 1 and 65535")
		}
		key := strconv.Itoa(p.HostPort) + "/" + p.Protocol
		if seen[key] {
			return invalid("duplicate host port binding: " + key)
		}
		seen[key] = true
	}
	return nil
}

// validateVolumes checks each mount: a safe unique name and an absolute unique
// mount path. nil/empty is valid (no volumes).
func validateVolumes(vols []domain.VolumeMount) error {
	if len(vols) > maxVolumes {
		return invalid("at most 20 volumes per application")
	}
	seenName, seenPath := map[string]bool{}, map[string]bool{}
	for _, v := range vols {
		if !volumeName.MatchString(v.Name) {
			return invalid("volume name must be lowercase alphanumeric with dashes: " + v.Name)
		}
		if !strings.HasPrefix(v.Path, "/") || strings.Contains(v.Path, "..") {
			return invalid("volume path must be an absolute path: " + v.Path)
		}
		if seenName[v.Name] {
			return invalid("duplicate volume name: " + v.Name)
		}
		if seenPath[v.Path] {
			return invalid("duplicate volume path: " + v.Path)
		}
		seenName[v.Name], seenPath[v.Path] = true, true
	}
	return nil
}

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

	// Phase 4: shared variables (shared-variables.md §3). Keys only — the
	// write-time reference check never unseals a value.
	ListSharedVariableKeysInScope(ctx context.Context, projectID, environmentID string) ([]string, error)

	// V1.x: registry credentials, for validating what an application may
	// attach (registries.md §5). GetProject resolves the owning team, which is
	// the boundary a credential may not cross.
	GetRegistry(ctx context.Context, id string) (domain.Registry, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
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
	Name              string
	Source            domain.AppSource
	Build             domain.AppBuild
	Runtime           domain.AppRuntime
	Route             domain.AppRoute
	Health            domain.AppHealth
	Volumes           []domain.VolumeMount
	Ports             []domain.PortMapping
	EnvVars           map[string]string // plaintext; sealed before storage
	PreviewEnabled    bool
	PreviewBaseDomain string
	PreviewTTLHours   int
}

// Create validates and creates an application under envID, returning it along
// with the raw webhook secret (shown exactly once). Secrets are sealed before
// they reach the store.
func (s *Service) Create(ctx context.Context, envID string, in CreateInput) (app domain.Application, webhookSecret string, err error) {
	env, err := s.store.GetEnvironment(ctx, envID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Application{}, "", ErrEnvironmentNotFound
		}
		return domain.Application{}, "", fmt.Errorf("applications: getting environment: %w", err)
	}
	in, err = validateAndDefault(in)
	if err != nil {
		return domain.Application{}, "", err
	}
	if err := s.checkRegistries(ctx, env, in.Source, in.Build); err != nil {
		return domain.Application{}, "", err
	}
	srv, err := s.store.GetServer(ctx, in.Runtime.ServerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Application{}, "", ErrServerNotFound
		}
		return domain.Application{}, "", fmt.Errorf("applications: getting server: %w", err)
	}
	// A builder-only agent has no application driver and rejects rollout work,
	// so placing an app there would create the row and then leave every deploy
	// failing. Refuse at creation rather than at the first deployment.
	if !srv.Runs() {
		return domain.Application{}, "", invalid("that server has the builder role and does not run applications")
	}

	webhookSecret = ids.Secret()
	wct, wnonce, err := s.sealer.Seal([]byte(webhookSecret))
	if err != nil {
		return domain.Application{}, "", fmt.Errorf("applications: sealing webhook secret: %w", err)
	}

	sealedVars := make([]domain.EnvVar, 0, len(in.EnvVars))
	for k, v := range in.EnvVars {
		// Same strict write-time check as PUT .../env/{key}: an unresolvable
		// {{shared.KEY}} is refused here rather than shipped and discovered at
		// the first deploy (shared-variables.md §3).
		refs, rerr := s.sharedRefs(ctx, env, k, v)
		if rerr != nil {
			return domain.Application{}, "", rerr
		}
		ct, nonce, serr := s.sealer.Seal([]byte(v))
		if serr != nil {
			return domain.Application{}, "", fmt.Errorf("applications: sealing env var: %w", serr)
		}
		sealedVars = append(sealedVars, domain.EnvVar{Key: k, ValueCT: ct, ValueNonce: nonce, SharedRefs: refs})
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
		Volumes:            in.Volumes,
		Ports:              in.Ports,
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    wct,
		WebhookSecretNonce: wnonce,
		PreviewEnabled:     in.PreviewEnabled,
		PreviewBaseDomain:  in.PreviewBaseDomain,
		PreviewTTLHours:    in.PreviewTTLHours,
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
	Name              *string
	Source            *domain.AppSource
	Build             *domain.AppBuild
	Port              *int
	Route             *domain.AppRoute
	Health            *domain.AppHealth
	PreviewEnabled    *bool
	PreviewBaseDomain *string
	PreviewTTLHours   *int
	// Resource limits: nil = unchanged; a non-positive value clears the limit
	// (same "non-nil zero removes" convention as database updates).
	CPULimit      *float64
	MemoryLimitMB *int
	// Volumes: nil = unchanged; a non-nil slice (possibly empty) replaces the set.
	Volumes *[]domain.VolumeMount
	// Ports: nil = unchanged; a non-nil slice (possibly empty) replaces the set.
	Ports *[]domain.PortMapping
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
	if in.PreviewEnabled != nil {
		app.PreviewEnabled = *in.PreviewEnabled
	}
	if in.PreviewBaseDomain != nil {
		app.PreviewBaseDomain = *in.PreviewBaseDomain
	}
	if in.PreviewTTLHours != nil {
		app.PreviewTTLHours = *in.PreviewTTLHours
	}
	if in.CPULimit != nil {
		if *in.CPULimit <= 0 {
			app.Runtime.CPULimit = nil // clear
		} else {
			app.Runtime.CPULimit = in.CPULimit
		}
	}
	if in.MemoryLimitMB != nil {
		if *in.MemoryLimitMB <= 0 {
			app.Runtime.MemoryLimitMB = nil // clear
		} else {
			app.Runtime.MemoryLimitMB = in.MemoryLimitMB
		}
	}
	if in.Volumes != nil {
		app.Volumes = *in.Volumes
	}
	if in.Ports != nil {
		app.Ports = *in.Ports
	}
	// The merged result must satisfy exactly the create-time rules — including
	// the preview contract (enabling previews via PATCH still needs a base
	// domain).
	merged, err := validateAndDefault(CreateInput{
		Name:              app.Name,
		Source:            app.Source,
		Build:             app.Build,
		Runtime:           app.Runtime,
		Route:             app.Route,
		Health:            app.Health,
		Volumes:           app.Volumes,
		Ports:             app.Ports,
		PreviewEnabled:    app.PreviewEnabled,
		PreviewBaseDomain: app.PreviewBaseDomain,
		PreviewTTLHours:   app.PreviewTTLHours,
	})
	if err != nil {
		return domain.Application{}, err
	}
	// Checked on the MERGED result, not the patch: attaching a push registry
	// and switching to an image source in one PATCH must be refused as the
	// combination it is, whichever half of it was sent.
	env, err := s.store.GetEnvironment(ctx, app.EnvironmentID)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: getting environment: %w", err)
	}
	if err := s.checkRegistries(ctx, env, merged.Source, merged.Build); err != nil {
		return domain.Application{}, err
	}
	app.Name, app.Source, app.Build, app.Runtime, app.Route, app.Health, app.Volumes, app.Ports =
		merged.Name, merged.Source, merged.Build, merged.Runtime, merged.Route, merged.Health, merged.Volumes, merged.Ports
	app.PreviewEnabled, app.PreviewBaseDomain, app.PreviewTTLHours =
		merged.PreviewEnabled, merged.PreviewBaseDomain, merged.PreviewTTLHours
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

// SetEnvVar seals and upserts one environment variable, recording which shared
// variables its value references (shared-variables.md §3).
func (s *Service) SetEnvVar(ctx context.Context, appID, key, value string) error {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return fmt.Errorf("applications: getting application: %w", err)
	}
	if !validEnvKey(key) {
		return invalid("env var key must match [A-Za-z_][A-Za-z0-9_]*")
	}
	env, err := s.store.GetEnvironment(ctx, app.EnvironmentID)
	if err != nil {
		return fmt.Errorf("applications: getting environment: %w", err)
	}
	refs, err := s.sharedRefs(ctx, env, key, value)
	if err != nil {
		return err
	}
	ct, nonce, err := s.sealer.Seal([]byte(value))
	if err != nil {
		return fmt.Errorf("applications: sealing env var: %w", err)
	}
	if err := s.store.UpsertEnvVar(ctx, appID, domain.EnvVar{Key: key, ValueCT: ct, ValueNonce: nonce, SharedRefs: refs}); err != nil {
		return fmt.Errorf("applications: setting env var: %w", err)
	}
	return nil
}

// EnvView is an application's environment-variable wiring: the keys (values are
// write-only and never returned — ui-principles §6) and, per key, the shared
// variables that key's value references. The refs let the Env vars tab show the
// wiring without a reveal (shared-variables.md §7).
type EnvView struct {
	Keys       []string
	SharedRefs map[string][]string
}

// ListEnv returns an application's env-var keys and their shared-variable
// references. No value is unsealed on this path.
func (s *Service) ListEnv(ctx context.Context, appID string) (EnvView, error) {
	if _, err := s.store.GetApplication(ctx, appID); err != nil {
		return EnvView{}, fmt.Errorf("applications: getting application: %w", err)
	}
	vars, err := s.store.ListEnvVars(ctx, appID)
	if err != nil {
		return EnvView{}, fmt.Errorf("applications: listing env vars: %w", err)
	}
	out := EnvView{Keys: make([]string, 0, len(vars)), SharedRefs: map[string][]string{}}
	for _, v := range vars {
		out.Keys = append(out.Keys, v.Key)
		if len(v.SharedRefs) > 0 {
			out.SharedRefs[v.Key] = v.SharedRefs
		}
	}
	return out, nil
}

// sharedRefs validates a value's {{shared.KEY}} references against the shared
// variables in force for the application's environment, and returns them for
// storage (shared-variables.md §3).
//
// Strict on both counts: a "{{…}}" that is not a well-formed {{shared.KEY}} is
// refused, and so is a well-formed reference that does not currently resolve.
// Neither message ever echoes the value — only the env key and the shared key,
// both of which the API already returns (ENGINEERING rule 20).
func (s *Service) sharedRefs(ctx context.Context, env domain.Environment, key, value string) ([]string, error) {
	refs, err := sharedvars.Refs(value)
	if err != nil {
		return nil, invalid("environment variable " + key + ": " + err.Error())
	}
	if len(refs) == 0 {
		return nil, nil
	}
	known, err := s.store.ListSharedVariableKeysInScope(ctx, env.ProjectID, env.ID)
	if err != nil {
		return nil, fmt.Errorf("applications: listing shared variables in scope: %w", err)
	}
	have := make(map[string]bool, len(known))
	for _, k := range known {
		have[k] = true
	}
	for _, ref := range refs {
		if !have[ref] {
			return nil, invalid(fmt.Sprintf(
				"environment variable %s references {{shared.%s}}, which is not defined in this project or in environment %q",
				key, ref, env.Name))
		}
	}
	return refs, nil
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

// checkRegistries refuses a registry an application may not use.
//
// Three things are checked, and each one is a failure an operator would
// otherwise meet mid-deploy as an unexplained 401 from a registry:
//
//   - the registry exists;
//   - it belongs to the same team as the application, because a credential is
//     a team's, and one project borrowing another team's push token is exactly
//     the tenancy hole the team boundary exists to close;
//   - it is allowed to do the thing it is being attached for — a pull-only
//     credential named as a push target is refused here rather than discovered
//     as a 403 after a five-minute build.
//
// A registry in another team answers the same "no such registry" as one that
// does not exist: an application's config screen must not become a way to
// enumerate other teams' credentials.
func (s *Service) checkRegistries(ctx context.Context, env domain.Environment, src domain.AppSource, build domain.AppBuild) error {
	if src.RegistryID == nil && build.PushRegistryID == nil {
		return nil // the overwhelming majority: no lookup, no cost
	}
	proj, err := s.store.GetProject(ctx, env.ProjectID)
	if err != nil {
		return fmt.Errorf("applications: getting project: %w", err)
	}
	if src.RegistryID != nil {
		if err := s.checkRegistry(ctx, proj.TeamID, *src.RegistryID, "source.registry_id", func(r domain.Registry) bool { return r.CanPull },
			"that registry is not allowed to pull"); err != nil {
			return err
		}
	}
	if build.PushRegistryID != nil {
		if err := s.checkRegistry(ctx, proj.TeamID, *build.PushRegistryID, "build.push_registry_id", func(r domain.Registry) bool { return r.CanPush },
			"that registry is not allowed to push"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkRegistry(ctx context.Context, teamID, id, field string, allows func(domain.Registry) bool, refusal string) error {
	reg, err := s.store.GetRegistry(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return invalid(field + ": no such registry")
		}
		return fmt.Errorf("applications: getting registry: %w", err)
	}
	if reg.TeamID != teamID {
		return invalid(field + ": no such registry")
	}
	if !allows(reg) {
		return invalid(field + ": " + refusal)
	}
	return nil
}

// validImageRef bounds an OCI image reference to its legal alphabet
// (registry/repository[:tag][@digest]). The engine's own reference parser is
// the real gate at pull time; this keeps junk (whitespace, shell metacharacters)
// out of the database and off the wire.
func validImageRef(ref string) bool {
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}

// validRepositoryPath bounds the repository half of an image reference: the
// lowercase path components an OCI name allows, and nothing else. Empty is
// valid — it means "use the application's name".
//
// Uppercase is rejected rather than folded. A registry treats `Acme/Web` as a
// name it has never heard of, and quietly lowercasing it here would push to a
// repository the operator did not type.
func validRepositoryPath(path string) bool {
	if path == "" {
		return true
	}
	if len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || !validRepositoryComponent(part) {
			return false
		}
	}
	return true
}

// validRepositoryComponent is one path segment: lowercase alphanumerics, with
// single separators between them and never at either end.
func validRepositoryComponent(part string) bool {
	prevSep := true // a leading separator is not allowed
	for i, r := range part {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			prevSep = false
		case r == '.' || r == '_' || r == '-':
			if prevSep || i == len(part)-1 {
				return false
			}
			prevSep = true
		default:
			return false
		}
	}
	return !prevSep
}

func validateAndDefault(in CreateInput) (CreateInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return in, invalid("name must be 1–100 characters")
	}

	switch in.Source.Kind {
	case "github", "git_url":
		if strings.TrimSpace(in.Source.Repo) == "" {
			return in, invalid("source.repo is required")
		}
		if in.Source.Branch == "" {
			in.Source.Branch = "main"
		}
		in.Source.Image = ""
	case "image":
		// Deploy from a prebuilt OCI image (feature-matrix V1): no repo, no
		// branch, no deploy key, and the pipeline skips build + distribute —
		// the target agent pulls the reference itself (work.proto AppSpec.pull).
		in.Source.Image = strings.TrimSpace(in.Source.Image)
		if in.Source.Image == "" {
			return in, invalid(`source.image is required when source.kind is "image"`)
		}
		if len(in.Source.Image) > 512 || !validImageRef(in.Source.Image) {
			return in, invalid("source.image must be a single OCI image reference")
		}
		if in.Source.DeployKeyID != nil {
			return in, invalid("source.deploy_key_id does not apply to image sources")
		}
		// Previews are driven by pull_request webhooks matched against the
		// app's branch, which an image source does not have. Accepting the
		// combination would report previews as enabled while no PR could ever
		// create one — refuse it instead of failing silently.
		if in.PreviewEnabled {
			return in, invalid("preview environments need a git source; they cannot be enabled for an image source")
		}
		in.Source.Repo, in.Source.Branch = "", ""
	default:
		return in, invalid(`source.kind must be "github", "git_url", or "image"`)
	}

	if in.Build.Kind == "" {
		// Existing applications and any client that omits the field keep the
		// behaviour they had; only new ones opt into detection.
		in.Build.Kind = domain.BuildDockerfile
	}
	switch in.Build.Kind {
	case domain.BuildDockerfile, domain.BuildStatic, domain.BuildAuto:
	default:
		return in, invalid(`build.kind must be one of "auto", "dockerfile", "static"`)
	}
	if in.Build.DockerfilePath == "" {
		in.Build.DockerfilePath = "./Dockerfile"
	}
	if in.Build.Context == "" {
		in.Build.Context = "."
	}
	// Push-after-build (registries.md; ADR-008 path 3). A repository with
	// nowhere to push it does nothing, and a push configured on a source that
	// is never built does nothing either — both are refused rather than stored
	// and silently ignored, which is the failure an operator cannot see.
	in.Build.PushRepository = strings.TrimSpace(in.Build.PushRepository)
	if in.Build.PushRepository != "" && in.Build.PushRegistryID == nil {
		return in, invalid("build.push_repository needs build.push_registry_id")
	}
	if in.Build.PushRegistryID != nil && in.Source.Kind == "image" {
		return in, invalid("an image source is not built, so it has nothing to push")
	}
	if !validRepositoryPath(in.Build.PushRepository) {
		return in, invalid("build.push_repository must be a lowercase image path such as acme/web")
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
	// Resource limits (feature-matrix V1). nil = no limit; a value must be sane,
	// and memory_limit_mb is persisted as int32 so it must not wrap on the cast
	// (same CWE-190 bound as the database limits).
	if in.Runtime.CPULimit != nil {
		if c := *in.Runtime.CPULimit; math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
			return in, invalid("runtime.cpu_limit must be a non-negative number")
		}
	}
	if in.Runtime.MemoryLimitMB != nil {
		if m := *in.Runtime.MemoryLimitMB; m < 0 || m > math.MaxInt32 {
			return in, invalid("runtime.memory_limit_mb must be between 0 and 2147483647")
		}
	}
	if err := validateVolumes(in.Volumes); err != nil {
		return in, err
	}
	if err := validatePorts(in.Ports); err != nil {
		return in, err
	}

	// The public HTTP route is optional: an app may expose only raw ports (a
	// non-HTTP service). When present the domain is trimmed; when absent the
	// agent writes no proxy fragment. A trailing-space-only domain is treated as
	// unset. (A health path/kind is still validated below either way — the probe
	// is internal to the agent, independent of any public route.)
	in.Route.Domain = strings.TrimSpace(in.Route.Domain)

	// Health gate kind: http (default) probes the path; tcp dials the container
	// port; none is liveness-only for raw UDP services. An empty kind defaults to
	// http, preserving every existing app's behavior.
	switch in.Health.Kind {
	case "", healthHTTP:
		in.Health.Kind = healthHTTP
	case healthTCP, healthNone:
	default:
		return in, invalid("health.kind must be one of http, tcp, none")
	}

	// Negative values must be rejected, not defaulted: the wire contract
	// (work.proto HealthCheck) carries these as uint32, so a negative slipping
	// through would wrap into a huge positive on conversion.
	if in.Health.IntervalSeconds < 0 || in.Health.TimeoutSeconds < 0 || in.Health.Retries < 0 {
		return in, invalid("health values must not be negative")
	}
	// Persisted as int32 columns; anything above would wrap on the cast (same
	// bound as preview_ttl_hours).
	if in.Health.IntervalSeconds > math.MaxInt32 || in.Health.TimeoutSeconds > math.MaxInt32 || in.Health.Retries > math.MaxInt32 {
		return in, invalid("health values must not exceed 2147483647")
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

	// Previews (preview-environments.md §2): a base domain is required to build
	// pr-<n>.<base>, and the TTL backstop defaults to 72h when unset.
	in.PreviewBaseDomain = strings.TrimSpace(in.PreviewBaseDomain)
	if in.PreviewEnabled && in.PreviewBaseDomain == "" {
		return in, invalid("preview_base_domain is required when preview_enabled is true")
	}
	// Stored as int32 (store: PreviewTtlHours); reject anything that would wrap
	// on the cast, not just negatives.
	if in.PreviewTTLHours < 0 || in.PreviewTTLHours > math.MaxInt32 {
		return in, invalid("preview_ttl_hours must be between 0 and 2147483647")
	}
	if in.PreviewTTLHours == 0 {
		in.PreviewTTLHours = 72
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

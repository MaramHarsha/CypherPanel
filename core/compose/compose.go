// Package compose owns Compose Stacks (compose-stacks.md): the third Resource
// in an Environment, defined by a compose file the operator supplies and the
// panel runs as-is.
//
// The file IS the desired state (ADR-005). Nothing here builds a command; the
// agent has one fixed invocation of its own, and what this package decides is
// only which file that invocation should be converging toward.
package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError marks input the caller can fix; REST maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrNeverDeployed is returned when an operation needs a revision and the stack
// has none — nothing has been deployed yet.
var ErrNeverDeployed = errors.New("compose: stack has never been deployed")

// maxComposeBytes bounds a stored file. A compose file is a description, not a
// payload; anything larger is a mistake that would otherwise ride into every
// DesiredState reply this server answers.
const maxComposeBytes = 512 << 10

// Store is the persistence this needs (consumer-defined, ENGINEERING rule 6).
type Store interface {
	CreateComposeStack(ctx context.Context, st domain.ComposeStack) (domain.ComposeStack, error)
	GetComposeStack(ctx context.Context, id string) (domain.ComposeStack, error)
	ListComposeStacksByEnvironment(ctx context.Context, envID string) ([]domain.ComposeStack, error)
	UpdateComposeStackConfig(ctx context.Context, st domain.ComposeStack) (domain.ComposeStack, error)
	SetComposeStackDesiredRevision(ctx context.Context, id, revisionID string) (domain.ComposeStack, error)
	DeleteComposeStack(ctx context.Context, id string) error

	CreateComposeRevision(ctx context.Context, r domain.ComposeRevision) (domain.ComposeRevision, error)
	GetComposeRevision(ctx context.Context, id string) (domain.ComposeRevision, error)
	ListComposeRevisions(ctx context.Context, stackID string, limit int32) ([]domain.ComposeRevision, error)
	LatestComposeRevision(ctx context.Context, stackID string) (domain.ComposeRevision, error)

	UpsertComposeEnvVar(ctx context.Context, stackID string, v domain.ComposeEnvVar) error
	ListComposeEnvVars(ctx context.Context, stackID string) ([]domain.ComposeEnvVar, error)
	DeleteComposeEnvVar(ctx context.Context, stackID, key string) error

	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
}

// Sealer seals plaintext for storage at rest. *secret.Box satisfies it.
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// Converger publishes a stack's desired state to its agent. Consumer-defined:
// this package needs one thing done, not the whole scheduler.
type Converger interface {
	ConvergeStack(ctx context.Context, stackID string) error
	RemoveStack(ctx context.Context, serverID, stackID string, deleteVolumes bool) error
}

type Service struct {
	store   Store
	sealer  Sealer
	agent   Converger
	maxRevs int32
}

// NewService wires the service. agent may be nil, which makes every mutation
// store-only — the shape the tests use, and what a plane with no bus would do.
func NewService(s Store, sealer Sealer, agent Converger) *Service {
	return &Service{store: s, sealer: sealer, agent: agent, maxRevs: 50}
}

// Input is a stack as the operator describes it.
type Input struct {
	Name        string
	ServerID    string
	ComposeYAML string
	Route       domain.ComposeRoute
	EnvVars     map[string]string // plaintext; sealed before storage
}

// Create validates the file and stores the stack. Nothing is deployed: a stack
// is born `stopped`, exactly as an application is, and the first converge
// happens when someone asks for it.
func (s *Service) Create(ctx context.Context, envID string, in Input) (domain.ComposeStack, error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		return domain.ComposeStack{}, fmt.Errorf("compose: getting environment: %w", err)
	}
	in, err := validateInput(in)
	if err != nil {
		return domain.ComposeStack{}, err
	}
	srv, err := s.store.GetServer(ctx, in.ServerID)
	if err != nil {
		return domain.ComposeStack{}, fmt.Errorf("compose: getting server: %w", err)
	}
	// A builder-only agent has no application driver and would reject the work,
	// so the stack would exist with every converge failing. Refuse at creation.
	if !srv.Runs() {
		return domain.ComposeStack{}, invalid("that server has the builder role and does not run workloads")
	}

	stack, err := s.store.CreateComposeStack(ctx, domain.ComposeStack{
		ID: ids.New(ids.PrefixComposeStack), EnvironmentID: envID,
		Name: in.Name, ServerID: in.ServerID, Route: in.Route,
	})
	if err != nil {
		return domain.ComposeStack{}, err
	}
	if err := s.sealInto(ctx, stack.ID, in.EnvVars); err != nil {
		return domain.ComposeStack{}, err
	}
	// The file is a revision from the start, so "what is stored" and "what a
	// deploy would ship" are never two different things.
	if _, err := s.newRevision(ctx, stack.ID, in.ComposeYAML); err != nil {
		return domain.ComposeStack{}, err
	}
	return stack, nil
}

// UpdateInput patches a stack; a nil field is left alone.
type UpdateInput struct {
	Name        *string
	ComposeYAML *string
	Route       *domain.ComposeRoute
}

// Update applies a patch. A changed file becomes a new revision but does NOT
// deploy: editing and shipping are separate acts, so an operator can review a
// file before it reaches a host.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (domain.ComposeStack, error) {
	stack, err := s.store.GetComposeStack(ctx, id)
	if err != nil {
		return domain.ComposeStack{}, fmt.Errorf("compose: getting stack: %w", err)
	}
	if in.Name != nil {
		stack.Name = strings.TrimSpace(*in.Name)
	}
	if in.Route != nil {
		stack.Route = *in.Route
	}
	if err := validateName(stack.Name); err != nil {
		return domain.ComposeStack{}, err
	}
	if err := validateRoute(stack.Route); err != nil {
		return domain.ComposeStack{}, err
	}
	if in.ComposeYAML != nil {
		if err := ValidateFile(*in.ComposeYAML); err != nil {
			return domain.ComposeStack{}, err
		}
		// Only when it actually changed: an edit that renames the stack must
		// not mint a revision nobody asked for, or the history stops meaning
		// "the versions that were deployed".
		latest, lerr := s.store.LatestComposeRevision(ctx, id)
		if lerr != nil && !errors.Is(lerr, store.ErrNotFound) {
			return domain.ComposeStack{}, fmt.Errorf("compose: reading latest revision: %w", lerr)
		}
		if errors.Is(lerr, store.ErrNotFound) || latest.ComposeYAML != *in.ComposeYAML {
			if _, err := s.newRevision(ctx, id, *in.ComposeYAML); err != nil {
				return domain.ComposeStack{}, err
			}
		}
	}
	return s.store.UpdateComposeStackConfig(ctx, stack)
}

func (s *Service) Get(ctx context.Context, id string) (domain.ComposeStack, error) {
	return s.store.GetComposeStack(ctx, id)
}

func (s *Service) List(ctx context.Context, envID string) ([]domain.ComposeStack, error) {
	return s.store.ListComposeStacksByEnvironment(ctx, envID)
}

func (s *Service) Revisions(ctx context.Context, stackID string) ([]domain.ComposeRevision, error) {
	return s.store.ListComposeRevisions(ctx, stackID, s.maxRevs)
}

// File returns the stack's current file: the newest revision, which is what a
// deploy would ship.
func (s *Service) File(ctx context.Context, stackID string) (domain.ComposeRevision, error) {
	rev, err := s.store.LatestComposeRevision(ctx, stackID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.ComposeRevision{}, ErrNeverDeployed
	}
	return rev, err
}

// Deploy points desired state at the stack's current file and asks the agent to
// converge. There is no Deployment row and no build — see spec §2.
func (s *Service) Deploy(ctx context.Context, stackID string) (domain.ComposeStack, error) {
	rev, err := s.File(ctx, stackID)
	if err != nil {
		return domain.ComposeStack{}, err
	}
	return s.rollTo(ctx, stackID, rev.ID)
}

// Rollback re-points desired state at an older revision of the same stack.
func (s *Service) Rollback(ctx context.Context, stackID, revisionID string) (domain.ComposeStack, error) {
	rev, err := s.store.GetComposeRevision(ctx, revisionID)
	if err != nil {
		return domain.ComposeStack{}, fmt.Errorf("compose: getting revision: %w", err)
	}
	// A revision id from another stack would otherwise deploy someone else's
	// file under this stack's identity.
	if rev.StackID != stackID {
		return domain.ComposeStack{}, invalid("that revision belongs to a different stack")
	}
	return s.rollTo(ctx, stackID, rev.ID)
}

// rollTo is the one path that moves desired state. Persisted before published
// (ENGINEERING rule 15): a publish that fails leaves the stack pointing at the
// revision the next sync will converge to anyway.
func (s *Service) rollTo(ctx context.Context, stackID, revisionID string) (domain.ComposeStack, error) {
	stack, err := s.store.SetComposeStackDesiredRevision(ctx, stackID, revisionID)
	if err != nil {
		return domain.ComposeStack{}, err
	}
	if s.agent == nil {
		return stack, nil
	}
	if err := s.agent.ConvergeStack(ctx, stackID); err != nil {
		return stack, fmt.Errorf("compose: publishing converge: %w", err)
	}
	return stack, nil
}

// Delete removes the stack. deleteVolumes is the operator's explicit say-so:
// convergence never removes a volume, so this is the only way a stack's data
// goes, and it is never the default.
func (s *Service) Delete(ctx context.Context, id string, deleteVolumes bool) error {
	stack, err := s.store.GetComposeStack(ctx, id)
	if err != nil {
		return fmt.Errorf("compose: getting stack: %w", err)
	}
	if err := s.store.DeleteComposeStack(ctx, id); err != nil {
		return err
	}
	if s.agent == nil {
		return nil
	}
	// After the row is gone: the agent's next sync would converge eventually
	// (the stack is absent from desired state), and this makes teardown prompt.
	if err := s.agent.RemoveStack(ctx, stack.ServerID, id, deleteVolumes); err != nil {
		return fmt.Errorf("compose: publishing removal: %w", err)
	}
	return nil
}

// ─── env vars ───────────────────────────────────────────────────────────────

// SetEnvVar seals one variable. Values are write-only: nothing reads one back
// (rule 20), and the keys are what a listing returns.
func (s *Service) SetEnvVar(ctx context.Context, stackID, key, value string) error {
	if err := validateEnvKey(key); err != nil {
		return err
	}
	ct, nonce, err := s.sealer.Seal([]byte(value))
	if err != nil {
		return fmt.Errorf("compose: sealing env var: %w", err)
	}
	return s.store.UpsertComposeEnvVar(ctx, stackID, domain.ComposeEnvVar{Key: key, ValueCT: ct, ValueNonce: nonce})
}

func (s *Service) EnvKeys(ctx context.Context, stackID string) ([]string, error) {
	vars, err := s.store.ListComposeEnvVars(ctx, stackID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
	}
	return keys, nil
}

func (s *Service) DeleteEnvVar(ctx context.Context, stackID, key string) error {
	return s.store.DeleteComposeEnvVar(ctx, stackID, key)
}

func (s *Service) sealInto(ctx context.Context, stackID string, vars map[string]string) error {
	for k, v := range vars {
		if err := s.SetEnvVar(ctx, stackID, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) newRevision(ctx context.Context, stackID, file string) (domain.ComposeRevision, error) {
	return s.store.CreateComposeRevision(ctx, domain.ComposeRevision{
		ID: ids.New(ids.PrefixComposeRevision), StackID: stackID, ComposeYAML: file,
	})
}

// ─── validation (spec §3) ───────────────────────────────────────────────────

// composeFile is only as much of the Compose Specification as the panel has an
// opinion about. Everything else is passed through untouched — the point of
// this resource is that the file runs as-is, and reimplementing the spec here
// would make that false.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Build         any    `yaml:"build"`
	ContainerName string `yaml:"container_name"`
}

// ValidateFile holds a compose file to the two rules the panel actually has,
// and nothing more.
//
// It deliberately does not check that compose would accept the file: compose's
// own error, surfaced at converge, is better than any paraphrase this could
// produce, and a validator that drifts from the real parser is worse than none.
func ValidateFile(file string) error {
	if strings.TrimSpace(file) == "" {
		return invalid("compose_yaml is required")
	}
	if len(file) > maxComposeBytes {
		return invalid("compose file is too large (512 KiB maximum)")
	}
	var parsed composeFile
	if err := yaml.Unmarshal([]byte(file), &parsed); err != nil {
		return invalid("compose_yaml is not valid YAML: " + err.Error())
	}
	if len(parsed.Services) == 0 {
		return invalid("compose_yaml declares no services")
	}
	for name, svc := range parsed.Services {
		// There is no builder on a target host: ADR-008's build story is a
		// builder-role agent producing an image that travels by local daemon or
		// relay, and a compose `build:` would run outside all of it.
		if svc.Build != nil {
			return invalid("service " + name + " uses build: — compose stacks run images; " +
				"build it as an Application, or reference an image that is already built")
		}
		// An absolute name collides across environments on a shared server, and
		// the second stack fails at create time with a message that names
		// Docker rather than the environment.
		if svc.ContainerName != "" {
			return invalid("service " + name + " sets container_name: — it collides across " +
				"environments on one server; compose already names containers per project")
		}
	}
	return nil
}

func validateInput(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.ServerID = strings.TrimSpace(in.ServerID)
	if err := validateName(in.Name); err != nil {
		return in, err
	}
	if in.ServerID == "" {
		return in, invalid("server_id is required")
	}
	if err := ValidateFile(in.ComposeYAML); err != nil {
		return in, err
	}
	if err := validateRoute(in.Route); err != nil {
		return in, err
	}
	for k := range in.EnvVars {
		if err := validateEnvKey(k); err != nil {
			return in, err
		}
	}
	return in, nil
}

func validateName(name string) error {
	if name == "" || len(name) > 100 {
		return invalid("name must be 1–100 characters")
	}
	return nil
}

// validateRoute holds the three route fields together: a domain with nothing to
// point at is a route that silently does nothing.
func validateRoute(r domain.ComposeRoute) error {
	if r.Domain == "" {
		if r.Service != "" || r.Port != 0 {
			return invalid("route.service and route.port need a route.domain")
		}
		return nil
	}
	if strings.ContainsAny(r.Domain, " \t\r\n/") {
		return invalid("route.domain must be a hostname")
	}
	if r.Service == "" {
		return invalid("route.service is required — name the compose service the domain reaches")
	}
	if r.Port < 1 || r.Port > 65535 {
		return invalid("route.port must be between 1 and 65535")
	}
	return nil
}

// validateEnvKey is the shell-safe alphabet compose interpolation uses. An env
// file is line-oriented, so a key carrying a newline or an '=' would write a
// second assignment nobody declared.
func validateEnvKey(key string) error {
	if key == "" || len(key) > 256 {
		return invalid("env var key must be 1–256 characters")
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return invalid("env var key " + key + " must be letters, digits and underscores, not starting with a digit")
		}
	}
	return nil
}

// Package sharedvars is the project shared variable
// (docs/features/shared-variables.md): one sealed value defined once for a
// whole project — or narrowed to a single environment of it — and referenced
// from any application's environment variables as {{shared.KEY}}.
//
// It holds two things that stay deliberately separate:
//
//   - the grammar (refs.go): the pure Refs/Expand pair, with no store and no
//     secrets, shared by the writer (core/applications) and the resolver
//     (core/scheduler) so the two cannot drift (§3);
//   - the CRUD service (this file): validation, scope checking, sealing, the
//     used-by read model, and the guarded delete (§7).
//
// Values are write-only and carry no masked hint (§6). A shared variable is
// already identified by its key, so a hint would be gratuitous partial
// disclosure — which is why nothing here ever unseals, and only
// core/scheduler.buildSpec ever does.
package sharedvars

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrProjectNotFound is returned when the addressed project does not exist —
// distinct from store.ErrNotFound, which from this service always means the
// shared variable itself is missing.
var ErrProjectNotFound = errors.New("sharedvars: project not found")

// ValidationError is a client-caused input error; handlers map it to 400. Its
// message never contains a value (ENGINEERING rule 20).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "sharedvars: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// InUseError refuses a delete that would leave a dangling reference, naming the
// applications that still reference the key (§7). There is deliberately no
// force override: the operator removes the references first, and `used-by` says
// exactly where they are.
type InUseError struct{ Applications []string }

func (e *InUseError) Error() string {
	return "sharedvars: still referenced by " + strings.Join(e.Applications, ", ")
}

// maxValueBytes caps a value at 32 KiB (§6) — an environment variable, not a
// file.
const maxValueBytes = 32 * 1024

// Sealer seals a value for storage at rest (consumer-defined; *secret.Box
// satisfies it). The same box that seals an application's own env vars: a
// shared variable is the same class of secret, moved up a level.
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// Store is the persistence the service needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	GetProject(ctx context.Context, id string) (domain.Project, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	ListEnvironmentsByProject(ctx context.Context, projectID string) ([]domain.Environment, error)

	CreateSharedVariable(ctx context.Context, v domain.SharedVariable) (domain.SharedVariable, error)
	GetSharedVariable(ctx context.Context, id string) (domain.SharedVariable, error)
	ListSharedVariablesByProject(ctx context.Context, projectID string) ([]domain.SharedVariable, error)
	UpdateSharedVariableValue(ctx context.Context, id string, ct, nonce []byte) (domain.SharedVariable, error)
	DeleteSharedVariable(ctx context.Context, id string) error

	CountSharedVariableUsage(ctx context.Context, id string) (int64, error)
	CountSharedVariableUsageByProject(ctx context.Context, projectID string) (map[string]int64, error)
	ListSharedVariableUsage(ctx context.Context, id string) ([]domain.SharedVariableUsage, error)

	ApplicationRedeployPending(ctx context.Context, appID string) (bool, error)
	ListRedeployPendingApplications(ctx context.Context, envID string) ([]string, error)
}

// Service is the shared-variable CRUD and read surface.
type Service struct {
	store  Store
	sealer Sealer
}

// NewService wires the service.
func NewService(st Store, sealer Sealer) *Service {
	return &Service{store: st, sealer: sealer}
}

// View is a shared variable plus the two fields the API surfaces that are
// DERIVED, never stored: the name of its environment (empty at project scope)
// and how many applications actually use it. The value is structurally absent —
// View has no field that could carry it.
type View struct {
	Variable        domain.SharedVariable
	EnvironmentName string
	UsedByCount     int
}

// CreateInput is a create request. Key and EnvironmentID are set exactly once:
// both are immutable afterwards, because changing either would silently
// re-point or orphan every referencing application (§7).
type CreateInput struct {
	Key string
	// EnvironmentID nil means project scope.
	EnvironmentID *string
	Value         string
}

// Create validates the key, the scope and the value, seals the value, and
// stores the variable under a project.
func (s *Service) Create(ctx context.Context, projectID string, in CreateInput) (View, error) {
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return View{}, ErrProjectNotFound
		}
		return View{}, fmt.Errorf("sharedvars: getting project: %w", err)
	}
	key := strings.TrimSpace(in.Key)
	if !ValidKey(key) {
		return View{}, invalid("key must match [A-Za-z_][A-Za-z0-9_]*")
	}
	if err := validateValue(in.Value); err != nil {
		return View{}, err
	}
	envName, err := s.scopeName(ctx, projectID, in.EnvironmentID)
	if err != nil {
		return View{}, err
	}
	ct, nonce, err := s.sealer.Seal([]byte(in.Value))
	if err != nil {
		return View{}, fmt.Errorf("sharedvars: sealing value: %w", err)
	}
	v, err := s.store.CreateSharedVariable(ctx, domain.SharedVariable{
		ID:            ids.New(ids.PrefixSharedVariable),
		ProjectID:     projectID,
		EnvironmentID: in.EnvironmentID,
		Key:           key,
		ValueCT:       ct,
		ValueNonce:    nonce,
	})
	if err != nil {
		return View{}, err
	}
	// A brand-new variable is referenced by nothing yet, so the count is known
	// without asking.
	return View{Variable: v, EnvironmentName: envName}, nil
}

// SetValue reseals a variable's value. It is the only mutation (§7), and it
// always moves updated_at — which is what marks every referencing application
// "redeploy to apply" (§5).
func (s *Service) SetValue(ctx context.Context, id, value string) (View, error) {
	if err := validateValue(value); err != nil {
		return View{}, err
	}
	ct, nonce, err := s.sealer.Seal([]byte(value))
	if err != nil {
		return View{}, fmt.Errorf("sharedvars: sealing value: %w", err)
	}
	v, err := s.store.UpdateSharedVariableValue(ctx, id, ct, nonce)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, v)
}

// Get returns one shared variable. It is also the authorization resolver's
// entry point, so it stays a plain lookup.
func (s *Service) Get(ctx context.Context, id string) (domain.SharedVariable, error) {
	return s.store.GetSharedVariable(ctx, id)
}

// View returns one shared variable with its derived fields.
func (s *Service) View(ctx context.Context, id string) (View, error) {
	v, err := s.store.GetSharedVariable(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, v)
}

// ListViews returns a project's shared variables — both scopes — each with its
// environment name and its scope-accurate used-by count, in a fixed number of
// round trips.
func (s *Service) ListViews(ctx context.Context, projectID string) ([]View, error) {
	list, err := s.store.ListSharedVariablesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.CountSharedVariableUsageByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	envs, err := s.store.ListEnvironmentsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("sharedvars: listing environments: %w", err)
	}
	names := make(map[string]string, len(envs))
	for _, e := range envs {
		names[e.ID] = e.Name
	}
	out := make([]View, 0, len(list))
	for _, v := range list {
		view := View{Variable: v, UsedByCount: int(counts[v.ID])}
		if v.EnvironmentID != nil {
			view.EnvironmentName = names[*v.EnvironmentID]
		}
		out = append(out, view)
	}
	return out, nil
}

// UsedBy names the applications that reference a variable, each with its own
// "redeploy to apply" marker (§7).
func (s *Service) UsedBy(ctx context.Context, id string) ([]domain.SharedVariableUsage, error) {
	if _, err := s.store.GetSharedVariable(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListSharedVariableUsage(ctx, id)
}

// Delete removes a variable, refusing while anything still references it (§7).
// With the write-time check in core/applications, no single operator action can
// produce an unresolvable reference.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.store.GetSharedVariable(ctx, id); err != nil {
		return err
	}
	usage, err := s.store.ListSharedVariableUsage(ctx, id)
	if err != nil {
		return err
	}
	if len(usage) > 0 {
		names := make([]string, 0, len(usage))
		for _, u := range usage {
			names = append(names, u.ApplicationName)
		}
		return &InUseError{Applications: names}
	}
	return s.store.DeleteSharedVariable(ctx, id)
}

// RedeployPending reports whether one application is running an environment
// older than the shared variables it references (§5).
func (s *Service) RedeployPending(ctx context.Context, appID string) (bool, error) {
	return s.store.ApplicationRedeployPending(ctx, appID)
}

// PendingInEnvironment answers the same for every application in an
// environment, in one query, so a list screen costs one extra round trip rather
// than one per row.
func (s *Service) PendingInEnvironment(ctx context.Context, envID string) (map[string]bool, error) {
	ids, err := s.store.ListRedeployPendingApplications(ctx, envID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// view attaches the derived read-model fields to a stored variable.
func (s *Service) view(ctx context.Context, v domain.SharedVariable) (View, error) {
	n, err := s.store.CountSharedVariableUsage(ctx, v.ID)
	if err != nil {
		return View{}, err
	}
	out := View{Variable: v, UsedByCount: int(n)}
	if v.EnvironmentID != nil {
		env, err := s.store.GetEnvironment(ctx, *v.EnvironmentID)
		if err != nil {
			return View{}, fmt.Errorf("sharedvars: getting environment: %w", err)
		}
		out.EnvironmentName = env.Name
	}
	return out, nil
}

// scopeName validates the requested scope and returns the environment's name
// (empty at project scope). The foreign-key pair cannot express "this
// environment belongs to that project", so the service checks it — otherwise a
// variable could be scoped to another project's environment and resolve for
// nobody (§2).
func (s *Service) scopeName(ctx context.Context, projectID string, envID *string) (string, error) {
	if envID == nil {
		return "", nil
	}
	env, err := s.store.GetEnvironment(ctx, *envID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", invalid("environment_id does not name an environment of this project")
		}
		return "", fmt.Errorf("sharedvars: getting environment: %w", err)
	}
	if env.ProjectID != projectID {
		return "", invalid("environment_id does not name an environment of this project")
	}
	return env.Name, nil
}

// validateValue bounds a value and refuses nesting. Values are stored verbatim
// (§3): a {{shared.…}} inside one would need a recursion, a cycle check and an
// expansion order this slice deliberately does not have.
func validateValue(value string) error {
	if len(value) > maxValueBytes {
		return invalid("value must be at most 32 KiB")
	}
	if ContainsReference(value) {
		return invalid("a shared variable's value cannot reference another shared variable")
	}
	return nil
}

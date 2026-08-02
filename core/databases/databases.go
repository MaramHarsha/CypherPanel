// Package databases is the operator-facing lifecycle for the Managed Database
// resource (docs/features/managed-databases.md): create (with generated and
// sealed root credentials), inspect, list, update config, reset password,
// start, stop, and delete. Backup scheduling is in backups.go.
//
// Root passwords never leave this package in the clear on their way to
// storage: they are sealed with the master-key Box before they reach the
// store (threat-model §5.1). The plaintext password is returned exactly once,
// at creation (or password reset), and never again.
package databases

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Sentinel errors handlers map to HTTP status codes.
var (
	ErrServerNotFound      = errors.New("databases: target server not found")
	ErrEnvironmentNotFound = errors.New("databases: environment not found")
)

// ValidationError is a client-caused input error; handlers map it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "databases: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Store is the persistence the service needs (consumer-defined, rule 6).
type Store interface {
	CreateDatabaseWithRevision(ctx context.Context, d domain.Database, rev domain.DatabaseRevision) (domain.Database, error)
	GetDatabase(ctx context.Context, id string) (domain.Database, error)
	ListDatabasesByEnvironment(ctx context.Context, envID string) ([]domain.Database, error)
	UpdateDatabaseConfig(ctx context.Context, d domain.Database) (domain.Database, error)
	UpdateDatabasePassword(ctx context.Context, id string, ct, nonce []byte) error
	SetDatabaseDesiredRevision(ctx context.Context, id string, revID string) (domain.Database, error)
	SetDatabaseDesiredState(ctx context.Context, id, desiredState string) (domain.Database, error)
	SetDatabaseStatus(ctx context.Context, id, status, detail string) error
	SetDatabasePendingDelete(ctx context.Context, id string, deleteVolume bool) error
	DeleteDatabase(ctx context.Context, id string) error
	CreateDatabaseRevision(ctx context.Context, rev domain.DatabaseRevision) (domain.DatabaseRevision, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
}

// Reconciler triggers agent database actions (consumer-defined).
type Reconciler interface {
	ReconcileDatabase(ctx context.Context, dbID string) error
}

// Sealer seals plaintext for storage at rest. *secret.Box satisfies it.
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// Service manages databases.
type Service struct {
	store      Store
	sealer     Sealer
	reconciler Reconciler
}

// NewService wires the service.
func NewService(s Store, sealer Sealer, r Reconciler) *Service {
	return &Service{store: s, sealer: sealer, reconciler: r}
}

// CreateInput is the caller-supplied config for a new managed database.
type CreateInput struct {
	Name            string
	Engine          string
	Version         string // optional — defaults to engine default
	ServerID        string
	CPULimit        *float64
	MemoryLimitMB   *int
	ExposePort      *int
	RequirePassword bool // Redis/Valkey: opt-in to password auth
}

// Create validates and creates a managed database under envID, returning it
// along with the plaintext root password (shown exactly once). The password is
// sealed before it reaches the store.
func (s *Service) Create(ctx context.Context, envID string, in CreateInput) (db domain.Database, rootPassword string, err error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Database{}, "", ErrEnvironmentNotFound
		}
		return domain.Database{}, "", fmt.Errorf("databases: getting environment: %w", err)
	}
	if _, err := s.store.GetServer(ctx, in.ServerID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Database{}, "", ErrServerNotFound
		}
		return domain.Database{}, "", fmt.Errorf("databases: getting server: %w", err)
	}

	in, err = validateAndDefault(in)
	if err != nil {
		return domain.Database{}, "", err
	}

	engine := domain.DbEngine(in.Engine)
	defaults := domain.EngineDefaults(engine, in.Version)

	dbID := ids.New(ids.PrefixDatabase)
	d := domain.Database{
		ID:              dbID,
		EnvironmentID:   envID,
		Name:            in.Name,
		Engine:          engine,
		Version:         in.Version,
		ServerID:        in.ServerID,
		CPULimit:        in.CPULimit,
		MemoryLimitMB:   in.MemoryLimitMB,
		VolumeName:      "cypher-db-" + dbID,
		DataPath:        defaults.DataPath,
		ExposePort:      in.ExposePort,
		Network:         "cypher-" + envID,
		RootUser:        defaults.RootUser,
		RequirePassword: in.RequirePassword,
		Status:          domain.DbStopped,
	}

	// Generate and seal root password for engines that require it, or when
	// the operator explicitly opts in for Redis/Valkey.
	if engine.NeedsPassword() || in.RequirePassword {
		pwd, sealErr := generateAndSealPassword(s.sealer)
		if sealErr != nil {
			return domain.Database{}, "", sealErr
		}
		rootPassword = pwd.plaintext
		d.RootPasswordCT = pwd.ct
		d.RootPasswordNonce = pwd.nonce
		d.RequirePassword = true
	}

	// Create the initial revision with a config snapshot.
	revID := ids.New(ids.PrefixDatabaseRevision)
	snapshot, err := configSnapshot(d)
	if err != nil {
		return domain.Database{}, "", fmt.Errorf("databases: marshaling config snapshot: %w", err)
	}
	rev := domain.DatabaseRevision{
		ID:             revID,
		DatabaseID:     dbID,
		ConfigSnapshot: snapshot,
	}

	created, err := s.store.CreateDatabaseWithRevision(ctx, d, rev)
	if err != nil {
		return domain.Database{}, "", fmt.Errorf("databases: creating database: %w", err)
	}
	if err := s.reconciler.ReconcileDatabase(ctx, created.ID); err != nil {
		return created, rootPassword, fmt.Errorf("databases: triggering reconciliation: %w", err)
	}
	return created, rootPassword, nil
}

// Get returns one database; the error wraps store.ErrNotFound when absent.
func (s *Service) Get(ctx context.Context, id string) (domain.Database, error) {
	d, err := s.store.GetDatabase(ctx, id)
	if err != nil {
		return domain.Database{}, fmt.Errorf("databases: getting database: %w", err)
	}
	return d, nil
}

// List returns all databases in an environment.
func (s *Service) List(ctx context.Context, envID string) ([]domain.Database, error) {
	dbs, err := s.store.ListDatabasesByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("databases: listing databases: %w", err)
	}
	return dbs, nil
}

// UpdateInput patches a database's configuration. Nil fields are left
// unchanged.
type UpdateInput struct {
	Name          *string
	Version       *string
	CPULimit      *float64 // set to a non-nil zero to remove the limit
	MemoryLimitMB *int
	ExposePort    *int
}

// Update applies a config patch. Creates a new revision if the config changed,
// triggering re-provisioning via the scheduler.
func (s *Service) Update(ctx context.Context, dbID string, in UpdateInput) (domain.Database, error) {
	d, err := s.store.GetDatabase(ctx, dbID)
	if err != nil {
		return domain.Database{}, fmt.Errorf("databases: getting database: %w", err)
	}

	if err := validateLimits(in.CPULimit, in.MemoryLimitMB, in.ExposePort); err != nil {
		return domain.Database{}, err
	}
	changed := false
	if in.Name != nil && *in.Name != d.Name {
		if strings.TrimSpace(*in.Name) == "" {
			return domain.Database{}, invalid("name cannot be empty")
		}
		d.Name = *in.Name
		changed = true
	}
	if in.Version != nil && *in.Version != d.Version {
		if strings.TrimSpace(*in.Version) == "" {
			return domain.Database{}, invalid("version cannot be empty")
		}
		d.Version = *in.Version
		changed = true
	}
	if in.CPULimit != nil {
		d.CPULimit = in.CPULimit
		changed = true
	}
	if in.MemoryLimitMB != nil {
		d.MemoryLimitMB = in.MemoryLimitMB
		changed = true
	}
	if in.ExposePort != nil {
		d.ExposePort = in.ExposePort
		changed = true
	}

	updated, err := s.store.UpdateDatabaseConfig(ctx, d)
	if err != nil {
		return domain.Database{}, fmt.Errorf("databases: updating database: %w", err)
	}

	// Create a new revision if config changed to trigger re-provisioning.
	if changed {
		revID := ids.New(ids.PrefixDatabaseRevision)
		snapshot, snapErr := configSnapshot(updated)
		if snapErr != nil {
			return domain.Database{}, fmt.Errorf("databases: marshaling config snapshot: %w", snapErr)
		}
		_, revErr := s.store.CreateDatabaseRevision(ctx, domain.DatabaseRevision{
			ID:             revID,
			DatabaseID:     dbID,
			ConfigSnapshot: snapshot,
		})
		if revErr != nil {
			return domain.Database{}, fmt.Errorf("databases: creating revision: %w", revErr)
		}
		updated, err = s.store.SetDatabaseDesiredRevision(ctx, dbID, revID)
		if err != nil {
			return domain.Database{}, fmt.Errorf("databases: setting desired revision: %w", err)
		}
	}
	if err := s.reconciler.ReconcileDatabase(ctx, dbID); err != nil {
		return updated, fmt.Errorf("databases: triggering reconciliation: %w", err)
	}
	return updated, nil
}

// Delete marks a database for deletion. The scheduler will emit DbRemoveWork;
// the row is deleted only after the agent confirms container removal.
func (s *Service) Delete(ctx context.Context, id string, deleteVolume bool) error {
	if _, err := s.store.GetDatabase(ctx, id); err != nil {
		return fmt.Errorf("databases: getting database: %w", err)
	}
	if err := s.store.SetDatabasePendingDelete(ctx, id, deleteVolume); err != nil {
		return fmt.Errorf("databases: setting pending delete: %w", err)
	}
	if err := s.reconciler.ReconcileDatabase(ctx, id); err != nil {
		return fmt.Errorf("databases: triggering deletion reconciliation: %w", err)
	}
	return nil
}

// ResetPassword generates a new root password, seals it, creates a new
// revision (triggering re-provisioning), and returns the plaintext password
// exactly once.
func (s *Service) ResetPassword(ctx context.Context, id string) (string, error) {
	d, err := s.store.GetDatabase(ctx, id)
	if err != nil {
		return "", fmt.Errorf("databases: getting database: %w", err)
	}
	if !d.Engine.NeedsPassword() && !d.RequirePassword {
		return "", invalid("this engine does not use password auth")
	}

	pwd, err := generateAndSealPassword(s.sealer)
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateDatabasePassword(ctx, id, pwd.ct, pwd.nonce); err != nil {
		return "", fmt.Errorf("databases: updating password: %w", err)
	}

	// Re-read the database to get the updated password fields, then create a
	// new revision so the scheduler picks up the password change.
	d, err = s.store.GetDatabase(ctx, id)
	if err != nil {
		return "", fmt.Errorf("databases: re-reading database: %w", err)
	}
	revID := ids.New(ids.PrefixDatabaseRevision)
	snapshot, err := configSnapshot(d)
	if err != nil {
		return "", fmt.Errorf("databases: marshaling config snapshot: %w", err)
	}
	if _, err := s.store.CreateDatabaseRevision(ctx, domain.DatabaseRevision{
		ID:             revID,
		DatabaseID:     id,
		ConfigSnapshot: snapshot,
	}); err != nil {
		return "", fmt.Errorf("databases: creating revision: %w", err)
	}
	if _, err := s.store.SetDatabaseDesiredRevision(ctx, id, revID); err != nil {
		return "", fmt.Errorf("databases: setting desired revision: %w", err)
	}
	if err := s.reconciler.ReconcileDatabase(ctx, id); err != nil {
		return pwd.plaintext, fmt.Errorf("databases: triggering reconciliation: %w", err)
	}
	return pwd.plaintext, nil
}

// Stop sets the database's desired intent to stopped. The scheduler emits a
// stop work item; the container's actual status follows from the agent's
// observation (ADR-005).
func (s *Service) Stop(ctx context.Context, id string) error {
	if _, err := s.store.GetDatabase(ctx, id); err != nil {
		return fmt.Errorf("databases: getting database: %w", err)
	}
	if _, err := s.store.SetDatabaseDesiredState(ctx, id, domain.DbDesiredStopped); err != nil {
		return err
	}
	if err := s.reconciler.ReconcileDatabase(ctx, id); err != nil {
		return fmt.Errorf("databases: triggering stop reconciliation: %w", err)
	}
	return nil
}

// Start sets the database's desired intent back to running, which triggers the
// scheduler to (re)provision.
func (s *Service) Start(ctx context.Context, id string) error {
	d, err := s.store.GetDatabase(ctx, id)
	if err != nil {
		return fmt.Errorf("databases: getting database: %w", err)
	}
	if d.DesiredState == domain.DbDesiredRunning {
		// Say which state is in the way. "not stopped" reads as a contradiction
		// to an operator looking at a database whose observed status is
		// anything but running — the guard is on intent, not observation, and
		// the message has to make that difference visible.
		return invalid("database is already set to run; it is " + d.Status +
			" because the agent has not finished reconciling it")
	}
	if _, err := s.store.SetDatabaseDesiredState(ctx, id, domain.DbDesiredRunning); err != nil {
		return err
	}
	if err := s.reconciler.ReconcileDatabase(ctx, id); err != nil {
		return fmt.Errorf("databases: triggering start reconciliation: %w", err)
	}
	return nil
}

// --- helpers ---

type sealedPassword struct {
	plaintext string
	ct        []byte
	nonce     []byte
}

func generateAndSealPassword(sealer Sealer) (sealedPassword, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return sealedPassword{}, fmt.Errorf("databases: generating password: %w", err)
	}
	plain := base64.RawURLEncoding.EncodeToString(buf)
	ct, nonce, err := sealer.Seal([]byte(plain))
	if err != nil {
		return sealedPassword{}, fmt.Errorf("databases: sealing password: %w", err)
	}
	return sealedPassword{plaintext: plain, ct: ct, nonce: nonce}, nil
}

// configSnapshot returns a JSON snapshot of the database's spec for the
// revision record.
func configSnapshot(d domain.Database) ([]byte, error) {
	snap := map[string]any{
		"engine":           string(d.Engine),
		"version":          d.Version,
		"server_id":        d.ServerID,
		"volume_name":      d.VolumeName,
		"data_path":        d.DataPath,
		"network":          d.Network,
		"root_user":        d.RootUser,
		"require_password": d.RequirePassword,
	}
	if d.CPULimit != nil {
		snap["cpu_limit"] = *d.CPULimit
	}
	if d.MemoryLimitMB != nil {
		snap["memory_limit_mb"] = *d.MemoryLimitMB
	}
	if d.ExposePort != nil {
		snap["expose_port"] = *d.ExposePort
	}
	return json.Marshal(snap)
}

// validateLimits bounds the operator-supplied resource numbers. They are
// persisted as int32 and travel as uint32 (DbSpec), so an unchecked negative or
// oversized value would wrap on the cast (CWE-190 — same class as the
// preview_ttl_hours bound in core/applications). 0 stays valid everywhere: it
// means "no limit" / "private only" / "remove on update".
func validateLimits(cpu *float64, memMB, port *int) error {
	if cpu != nil && (math.IsNaN(*cpu) || math.IsInf(*cpu, 0) || *cpu < 0) {
		return invalid("cpu_limit must be a non-negative number")
	}
	if memMB != nil && (*memMB < 0 || *memMB > math.MaxInt32) {
		return invalid("memory_limit_mb must be between 0 and 2147483647")
	}
	if port != nil && (*port < 0 || *port > 65535) {
		return invalid("expose_port must be between 0 and 65535")
	}
	return nil
}

func validateAndDefault(in CreateInput) (CreateInput, error) {
	if strings.TrimSpace(in.Name) == "" {
		return in, invalid("name is required")
	}
	engine := domain.DbEngine(in.Engine)
	if !engine.Valid() {
		return in, invalid(fmt.Sprintf("unsupported engine %q", in.Engine))
	}
	if strings.TrimSpace(in.ServerID) == "" {
		return in, invalid("server_id is required")
	}
	if err := validateLimits(in.CPULimit, in.MemoryLimitMB, in.ExposePort); err != nil {
		return in, err
	}
	// Default version from engine matrix if not specified.
	if strings.TrimSpace(in.Version) == "" {
		switch engine {
		case domain.EnginePostgreSQL:
			in.Version = "16"
		case domain.EngineMySQL:
			in.Version = "8.4"
		case domain.EngineMariaDB:
			in.Version = "11"
		case domain.EngineMongoDB:
			in.Version = "7.0"
		case domain.EngineRedis:
			in.Version = "7.2"
		case domain.EngineValkey:
			in.Version = "8.0"
		}
	}
	return in, nil
}

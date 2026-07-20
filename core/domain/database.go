package domain

import "time"

// Phase 3 resource model (docs/features/managed-databases.md §1): the Managed
// Database resource and its supporting types — engine matrix and revisions.
// (Backup targets/schedules are a follow-up, stripped at commit 1d83f0a.) Types
// stay persistence- and transport-free; the store maps pgx types to these,
// services seal/unseal secrets, handlers serialize DTOs.

// DbEngine identifies a database engine. Stored as a TEXT column; the domain
// validates it and the engine matrix maps it to defaults.
type DbEngine string

const (
	EnginePostgreSQL DbEngine = "postgresql"
	EngineMySQL      DbEngine = "mysql"
	EngineMariaDB    DbEngine = "mariadb"
	EngineMongoDB    DbEngine = "mongodb"
	EngineRedis      DbEngine = "redis"
	EngineValkey     DbEngine = "valkey"
)

// ValidEngine reports whether e is a supported engine name.
func (e DbEngine) Valid() bool {
	switch e {
	case EnginePostgreSQL, EngineMySQL, EngineMariaDB,
		EngineMongoDB, EngineRedis, EngineValkey:
		return true
	}
	return false
}

// NeedsPassword reports whether this engine requires a root password for
// provisioning. Redis and Valkey treat passwords as optional.
func (e DbEngine) NeedsPassword() bool {
	return e != EngineRedis && e != EngineValkey
}

// EngineSpec is the default container configuration for an engine+version,
// derived from the engine matrix (managed-databases.md §1).
type EngineSpec struct {
	Image     string // e.g. "postgres:16"
	DataPath  string // mount target inside the container
	HealthCmd string // Docker HEALTHCHECK CMD
	RootUser  string // default superuser (empty for Redis/Valkey)
	// PasswordEnv is the environment variable the container expects for the
	// root password (e.g. "POSTGRES_PASSWORD"). Empty for engines that don't
	// require one.
	PasswordEnv string
}

// EngineDefaults returns the default container configuration for an engine and
// version. It panics on invalid engines — callers must validate first.
func EngineDefaults(engine DbEngine, version string) EngineSpec {
	switch engine {
	case EnginePostgreSQL:
		return EngineSpec{
			Image:       "postgres:" + version,
			DataPath:    "/var/lib/postgresql/data",
			HealthCmd:   "pg_isready -U postgres",
			RootUser:    "postgres",
			PasswordEnv: "POSTGRES_PASSWORD",
		}
	case EngineMySQL:
		return EngineSpec{
			Image:       "mysql:" + version,
			DataPath:    "/var/lib/mysql",
			HealthCmd:   "mysqladmin ping -u root --silent",
			RootUser:    "root",
			PasswordEnv: "MYSQL_ROOT_PASSWORD",
		}
	case EngineMariaDB:
		return EngineSpec{
			Image:       "mariadb:" + version,
			DataPath:    "/var/lib/mysql",
			HealthCmd:   "mariadb-admin ping -u root --silent",
			RootUser:    "root",
			PasswordEnv: "MARIADB_ROOT_PASSWORD",
		}
	case EngineMongoDB:
		return EngineSpec{
			Image:       "mongo:" + version,
			DataPath:    "/data/db",
			HealthCmd:   `mongosh --eval "db.adminCommand('ping')" --quiet`,
			RootUser:    "root",
			PasswordEnv: "MONGO_INITDB_ROOT_PASSWORD",
		}
	case EngineRedis:
		return EngineSpec{
			Image:     "redis:" + version,
			DataPath:  "/data",
			HealthCmd: "redis-cli ping",
			RootUser:  "",
		}
	case EngineValkey:
		return EngineSpec{
			Image:     "valkey/valkey:" + version,
			DataPath:  "/data",
			HealthCmd: "valkey-cli ping",
			RootUser:  "",
		}
	default:
		panic("domain: EngineDefaults called with invalid engine: " + string(engine))
	}
}

// Database status vocabulary (ui-principles §5). Distinct from ServerStatus
// and Application status — they share the same UI vocabulary but are separate
// Go types to prevent cross-resource assignment.
const (
	DbRunning      = "running"
	DbProvisioning = "provisioning"
	DbStopped      = "stopped"
	DbError        = "error"
	DbUnknown      = "unknown"
)

// Database desired-state intent (managed-databases.md §3). Authoritative for
// the scheduler; distinct from the observed Status vocabulary above.
const (
	DbDesiredRunning = "running"
	DbDesiredStopped = "stopped"
)

// Database is a managed database engine instance: a resource provisioned and
// operated by the panel (lifecycle, credentials, backups). The credential
// fields are sealed at rest (threat-model §5.1); services unseal to inject
// them into the container's environment at provision time.
type Database struct {
	ID            string
	EnvironmentID string
	Name          string
	Engine        DbEngine
	Version       string

	// Runtime placement — same FK semantics as Application.runtime_server_id.
	ServerID string

	// Resource limits (noisy-neighbor control). nil = no limit.
	CPULimit      *float64 // fractional cores
	MemoryLimitMB *int     // MiB

	// Persistence.
	VolumeName string // deterministic: cypher-db-<id>
	DataPath   string // engine-specific mount target

	// Networking.
	ExposePort *int   // host-published TCP port; nil = private only
	Network    string // cypher-<environment_id>

	// Sealed root credentials (threat-model §5.1).
	RootUser          string
	RootPasswordCT    []byte // nil for password-less Redis/Valkey
	RootPasswordNonce []byte

	// Whether the operator opted into password auth for Redis/Valkey.
	RequirePassword bool

	// Desired-state tracking (ADR-005). DesiredState is the operator's intent
	// (DbDesiredRunning / DbDesiredStopped) and is authoritative for the
	// scheduler; Status is the agent's observation and is never used as
	// intent. Keeping them separate is what lets a freshly-created database
	// (observed 'stopped', desired 'running') provision rather than tear down.
	DesiredRevisionID  *string
	DesiredState       string
	Status             string
	StatusDetail       string
	ObservedRevisionID string
	StatusObservedAt   *time.Time

	// PendingDelete is set when the operator requests deletion; the scheduler
	// emits DbRemoveWork and the row is deleted only after the agent confirms.
	PendingDelete bool
	DeleteVolume  bool // explicit opt-in for data destruction

	CreatedAt time.Time
	UpdatedAt time.Time
}

// DatabaseRevision is an immutable snapshot of a Database's configuration at a
// point in time. Config changes (version upgrade, resource limit change) create
// a new revision; rollback re-points desired state at an older one.
type DatabaseRevision struct {
	ID             string
	DatabaseID     string
	ConfigSnapshot []byte // JSON snapshot of the full spec
	CreatedAt      time.Time
}

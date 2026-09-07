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
	// DatabaseEnv is the environment variable the entrypoint reads to create an
	// application database while initializing an empty data directory (e.g.
	// "POSTGRES_DB"). Empty for engines with no named databases.
	DatabaseEnv string
	// DefaultDatabase is what a client connects to when no initial database was
	// requested. Only PostgreSQL ships one.
	DefaultDatabase string
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
			DatabaseEnv: "POSTGRES_DB",
			// PostgreSQL is the only engine that ships a database to connect
			// to when none was requested.
			DefaultDatabase: "postgres",
		}
	case EngineMySQL:
		return EngineSpec{
			Image:       "mysql:" + version,
			DataPath:    "/var/lib/mysql",
			HealthCmd:   "mysqladmin ping -u root --silent",
			RootUser:    "root",
			PasswordEnv: "MYSQL_ROOT_PASSWORD",
			DatabaseEnv: "MYSQL_DATABASE",
		}
	case EngineMariaDB:
		return EngineSpec{
			Image:       "mariadb:" + version,
			DataPath:    "/var/lib/mysql",
			HealthCmd:   "mariadb-admin ping -u root --silent",
			RootUser:    "root",
			PasswordEnv: "MARIADB_ROOT_PASSWORD",
			DatabaseEnv: "MARIADB_DATABASE",
		}
	case EngineMongoDB:
		return EngineSpec{
			Image:       "mongo:" + version,
			DataPath:    "/data/db",
			HealthCmd:   `mongosh --eval "db.adminCommand('ping')" --quiet`,
			RootUser:    "root",
			PasswordEnv: "MONGO_INITDB_ROOT_PASSWORD",
			DatabaseEnv: "MONGO_INITDB_DATABASE",
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
	// DbRestoring is neither running nor stopped: the container is being taken
	// down and brought back with different data underneath it. A screen that
	// cannot say this has to show "stopped" and hope nobody acts on it.
	DbRestoring = "restoring"
)

// Restore statuses and the steps a running one passes through, mirroring the
// agent's DbRestoreEvent vocabulary so the two cannot drift.
const (
	RestoreRunning   = "running"
	RestoreSucceeded = "succeeded"
	RestoreFailed    = "failed"

	RestoreStepFetching   = "fetching"
	RestoreStepStopping   = "stopping"
	RestoreStepApplying   = "applying"
	RestoreStepRestarting = "restarting"
)

// DatabaseRestore is one attempt to put a backup back into a database.
//
// It is a record rather than a field on the database because it outlives the
// operation: "this database was restored from last Tuesday's backup at 03:12,
// and it failed halfway" is the thing someone needs afterwards, and a status
// column can only ever say what is true now.
type DatabaseRestore struct {
	ID             string
	DatabaseID     string
	BackupRecordID string
	Status         string
	// Step is where a running restore has got to; empty once it is finished,
	// because the status then says everything.
	Step       string
	BytesDone  int64
	BytesTotal int64
	Detail     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

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

	// InitialDatabase is an application database the engine creates while
	// initializing an empty data directory (managed-databases.md §2). Empty
	// means "whatever the engine ships with". Set once at creation: the engine
	// images ignore the variable on every start after the first, so changing it
	// later could only lie or require an imperative CREATE DATABASE.
	InitialDatabase string

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

// BackupTarget is a reusable S3-compatible storage destination
// (managed-databases.md §7). Access/secret keys are sealed at rest with the
// master-key Box (threat-model §5.1) and never returned in API responses.
type BackupTarget struct {
	ID             string
	Name           string
	Endpoint       string
	Bucket         string
	Region         string
	AccessKeyCT    []byte
	AccessKeyNonce []byte
	SecretKeyCT    []byte
	SecretKeyNonce []byte
	PathPrefix     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Backup schedule status vocabulary (last_status column).
const (
	BackupRunning   = "running"
	BackupSucceeded = "succeeded"
	BackupFailed    = "failed"
)

// DatabaseBackup is a backup schedule for a Database against a BackupTarget. A
// cron Schedule drives automatic runs (empty = manual only); RetentionCount
// bounds how many succeeded backups are kept.
type DatabaseBackup struct {
	ID             string
	DatabaseID     string
	TargetID       string
	Schedule       string
	RetentionCount int
	Enabled        bool
	LastRunAt      *time.Time
	LastStatus     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BackupRecord is one backup execution's history row: where it landed in S3,
// its size, and its terminal outcome.
type BackupRecord struct {
	ID               string
	DatabaseBackupID string
	ObjectKey        string
	SizeBytes        int64
	Status           string
	Detail           string
	StartedAt        time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
}

// DatabaseConfig is a Database without its sealed root password, for the reason
// ApplicationConfig exists (project-export.md §4).
type DatabaseConfig struct {
	ID              string
	EnvironmentID   string
	Name            string
	Engine          DbEngine
	Version         string
	ServerID        string
	InitialDatabase string
	VolumeName      string
	DataPath        string
	RootUser        string
	ExposePort      *int
}

// ConfigView narrows a Database to the fields that carry no secret.
func (d Database) ConfigView() DatabaseConfig {
	return DatabaseConfig{
		ID: d.ID, EnvironmentID: d.EnvironmentID, Name: d.Name,
		Engine: d.Engine, Version: d.Version, ServerID: d.ServerID,
		InitialDatabase: d.InitialDatabase, VolumeName: d.VolumeName,
		DataPath: d.DataPath, RootUser: d.RootUser, ExposePort: d.ExposePort,
	}
}

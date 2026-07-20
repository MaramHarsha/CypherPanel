package domain

import "time"

// Phase 3 resource model (docs/features/managed-databases.md §1): the Managed
// Database resource and its supporting types — revisions, backup targets,
// backup schedules, and backup records. Types stay persistence- and
// transport-free; the store maps pgx types to these, services seal/unseal
// secrets, handlers serialize DTOs.

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

	// Desired-state tracking (ADR-005).
	DesiredRevisionID  *string
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

// BackupTarget is an S3-compatible storage destination for database backups.
// Credentials are sealed at rest (threat-model §5.1).
type BackupTarget struct {
	ID             string
	Name           string
	Endpoint       string // S3-compatible endpoint URL
	Bucket         string
	Region         string
	AccessKeyCT    []byte
	AccessKeyNonce []byte
	SecretKeyCT    []byte
	SecretKeyNonce []byte
	PathPrefix     string // key prefix inside bucket
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DatabaseBackup is a backup schedule configuration for a Database, pointing at
// a BackupTarget. A cron expression drives automatic backups; manual runs are
// always available.
type DatabaseBackup struct {
	ID             string
	DatabaseID     string
	TargetID       string
	Schedule       string // cron expression; empty = manual only
	RetentionCount int    // keep last N backups
	Enabled        bool
	LastRunAt      *time.Time
	LastStatus     string // succeeded | failed | running | ""
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BackupRecord is a single completed (or failed) backup execution.
type BackupRecord struct {
	ID               string
	DatabaseBackupID string
	ObjectKey        string // S3 key where the backup was stored
	SizeBytes        int64
	Status           string // succeeded | failed
	Detail           string
	StartedAt        time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
}

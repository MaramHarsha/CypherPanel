package domain

import "time"

// The Phase 2 resource model (docs/features/application-deploy.md §1): the
// organizational spine plus the Application resource and its deploy history.
// Types stay persistence- and transport-free; the store maps pgx types to
// these, services seal/unseal secrets, handlers serialize DTOs.

// Project groups environments for one product or customer.
type Project struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Environment is a named context inside a project (production, staging, a
// preview). Holds resources.
type Environment struct {
	ID        string
	ProjectID string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AppSource is where an Application's code comes from.
type AppSource struct {
	Kind        string // "github" | "git_url"
	Repo        string
	Branch      string
	DeployKeyID *string
}

// AppBuild is how the image is produced (Phase 2: dockerfile only).
type AppBuild struct {
	Kind           string // "dockerfile"
	DockerfilePath string
	Context        string
}

// AppRuntime is where and how the container runs.
type AppRuntime struct {
	ServerID string
	Port     int
	Replicas int
}

// AppRoute is the public routing for the Application.
type AppRoute struct {
	Domain     string
	HTTPS      bool
	PathPrefix string
}

// AppHealth gates rollout: the new revision must pass before the route flips.
type AppHealth struct {
	Path            string
	IntervalSeconds int
	TimeoutSeconds  int
	Retries         int
}

// Application is a resource built from a git repository and owned end to end.
// The webhook secret is stored sealed (WebhookSecretCT/Nonce); it is never
// exposed in a domain value in plaintext.
type Application struct {
	ID            string
	EnvironmentID string
	Name          string
	Source        AppSource
	Build         AppBuild
	Runtime       AppRuntime
	Route         AppRoute
	Health        AppHealth
	WebhookID     string
	// Sealed webhook HMAC secret (threat-model §5.1). Services unseal to verify.
	WebhookSecretCT    []byte
	WebhookSecretNonce []byte
	// DesiredRevisionID is nil until the first deploy; rollback re-points it.
	DesiredRevisionID *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// EnvVar is one sealed environment variable belonging to an Application.
type EnvVar struct {
	Key        string
	ValueCT    []byte
	ValueNonce []byte
}

// Revision is an immutable record a Deployment points at: the built image plus
// a config snapshot. Rollback re-points desired state at an older revision.
type Revision struct {
	ID             string
	ApplicationID  string
	Image          string
	SourceCommit   string
	ConfigSnapshot []byte // JSON snapshot of the spec at creation
	CreatedAt      time.Time
}

// DeploymentStatus is the lifecycle of a Deployment. Distinct from the
// resource status vocabulary (ui-principles §5): this tracks the pipeline, that
// tracks what is serving.
type DeploymentStatus string

const (
	DeployQueued       DeploymentStatus = "queued"
	DeployBuilding     DeploymentStatus = "building"
	DeployDistributing DeploymentStatus = "distributing"
	DeployRollingOut   DeploymentStatus = "rolling_out"
	DeploySucceeded    DeploymentStatus = "succeeded"
	DeployFailed       DeploymentStatus = "failed"
)

// Terminal reports whether a deployment has finished (succeeded or failed).
func (s DeploymentStatus) Terminal() bool {
	return s == DeploySucceeded || s == DeployFailed
}

// Deployment is a recorded transition of an Application to a revision.
type Deployment struct {
	ID            string
	ApplicationID string
	RevisionID    string
	Status        DeploymentStatus
	Trigger       string // "manual" | "webhook" | "rollback"
	Detail        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	FinishedAt    *time.Time
}

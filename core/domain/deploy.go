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
	// Status is the ui-principles §5 vocabulary (running · deploying · stopped
	// · error · degraded · unknown). It comes from agent observations (ADR-005)
	// except 'deploying', which the scheduler sets while a pipeline runs, and
	// 'stopped', the birth state before any deploy.
	Status       string
	StatusDetail string
	// ObservedRevisionID is the revision last reported actually serving.
	ObservedRevisionID string
	StatusObservedAt   *time.Time
	// Preview opt-in (preview-environments.md §2). PreviewEnabled makes PRs on
	// this app spawn preview environments at pr-<n>.<PreviewBaseDomain>, auto
	// destroyed after PreviewTTLHours.
	PreviewEnabled    bool
	PreviewBaseDomain string
	PreviewTTLHours   int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Preview status vocabulary (preview-environments.md §3). Orchestration state,
// distinct from the cloned app's own observation-driven status.
const (
	PreviewCreating   = "creating"
	PreviewRunning    = "running"
	PreviewError      = "error"
	PreviewDestroying = "destroying"
)

// Preview tracks one live PR preview and links to the first-class rows it
// created (a child Environment and a cloned Application).
type Preview struct {
	ID            string
	SourceAppID   string
	EnvironmentID string
	PreviewAppID  *string // nil once the cloned app is torn down (ON DELETE SET NULL)
	PRNumber      int
	PRBranch      string
	Domain        string
	Status        string
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Application status vocabulary (ui-principles §5).
const (
	AppRunning   = "running"
	AppDeploying = "deploying"
	AppStopped   = "stopped"
	AppError     = "error"
	AppDegraded  = "degraded"
	AppUnknown   = "unknown"
)

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
	// BuilderServerID is the server the build was routed to when it is not
	// the app's own server; nil means builder = target (the local path,
	// ADR-008). It authorizes that server's build/distribute events and
	// relay pushes (builder-role-and-relay.md §5).
	BuilderServerID *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// DeployKey is an SSH key for private repository access. The private key PEM
// is sealed at rest (same secret.Box as env vars; threat-model §5.1) and never
// returned in API responses. The public key and fingerprint are stored in the
// clear for display and deduplication.
type DeployKey struct {
	ID              string
	Name            string
	PublicKey       string
	Fingerprint     string
	PrivateKeyCT    []byte
	PrivateKeyNonce []byte
	CreatedAt       time.Time
}

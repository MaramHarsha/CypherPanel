package domain

import "time"

// The Phase 2 resource model (docs/features/application-deploy.md §1): the
// organizational spine plus the Application resource and its deploy history.
// Types stay persistence- and transport-free; the store maps pgx types to
// these, services seal/unseal secrets, handlers serialize DTOs.

// Project groups environments for one product or customer.
type Project struct {
	ID     string
	Name   string
	TeamID string
	// Slug is the stable handle used in URLs and by the CLI. Derived from the
	// name at creation and immutable after: renaming a project must not break a
	// bookmark or a script, which is why the two are separate fields.
	Slug string
	// DefaultEnvironmentID is where "open this project" lands and what a deploy
	// targets when none is named. Empty when the project has no environments.
	DefaultEnvironmentID string
	// LastActivityAt is the last time anything happened here — a deploy, a
	// resource created or removed, a setting changed. The projects list orders
	// by it, so it is maintained on those paths rather than derived at read
	// time from a scan of everything underneath.
	LastActivityAt time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProjectRollup is what a project looks like from the list: how much is in it
// and the worst thing happening. Counted across applications and managed
// databases together, because an operator scanning the page does not care which
// kind of resource is broken.
type ProjectRollup struct {
	ProjectID        string
	ApplicationCount int64
	DatabaseCount    int64
	ErrorCount       int64
	// WorstStatus is the most severe observed status among the project's
	// resources, in the shared vocabulary (error, degraded, deploying, running,
	// unknown). Empty when the project holds nothing.
	WorstStatus string
}

// Environment is a named context inside a project (production, staging, a
// preview). Holds resources.
type Environment struct {
	ID        string
	ProjectID string
	Name      string
	// Kind separates a preview from a standing environment. Previews are
	// created and destroyed by the PR lifecycle, so they must never be renamed
	// or deleted by hand — a rule that needs a column, not a guess from the
	// name.
	Kind      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Environment kinds.
const (
	EnvProduction = "production"
	EnvStandard   = "standard"
	EnvPreview    = "preview"
)

// AppSource is where an Application's code comes from. Kind "image" deploys a
// prebuilt OCI image reference directly (feature-matrix V1: deploy from
// container image) — no build stage; the target agent pulls the image itself.
type AppSource struct {
	Kind        string // "github" | "git_url" | "image"
	Repo        string
	Branch      string
	DeployKeyID *string
	Image       string // OCI reference; set iff Kind == "image"
}

// AppBuild is how the image is produced (Phase 2: dockerfile only).
type AppBuild struct {
	Kind           string // "dockerfile"
	DockerfilePath string
	Context        string
}

// AppRuntime is where and how the container runs.
// VolumeMount is one persistent volume an application declares (feature-matrix
// V1). Name is the operator's short label; the deterministic Docker volume name
// is derived from it. Path is the absolute mount point in the container.
type VolumeMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AppRuntime struct {
	ServerID string
	Port     int
	Replicas int
	// Resource limits (noisy-neighbor control, feature-matrix V1). Nil = no
	// limit — the same shape databases use.
	CPULimit      *float64 // fractional cores
	MemoryLimitMB *int     // MiB
}

// AppRoute is the public routing for the Application.
type AppRoute struct {
	Domain     string
	HTTPS      bool
	PathPrefix string
}

// AppHealth gates rollout: the new revision must pass before the route flips.
// Kind selects how: "http" (GET Path, the default) probes an HTTP endpoint;
// "tcp" dials the container port; "none" is liveness-only (container running)
// for raw UDP services with no readiness signal. The probe is always internal.
type AppHealth struct {
	Kind            string
	Path            string
	IntervalSeconds int
	TimeoutSeconds  int
	Retries         int
}

// PortMapping publishes a container port to a host port on one protocol, for
// services reached directly rather than through the HTTP proxy (feature-matrix
// V1). Independent of the (now optional) HTTP route.
type PortMapping struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"` // "tcp" or "udp"
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
	Volumes       []VolumeMount
	Ports         []PortMapping
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
	// Build kinds. "auto" is the default for new applications: the builder
	// looks at the checked-out repository and picks, because that is where the
	// source is — the control plane never fetches a repo (ADR-001).
	BuildDockerfile = "dockerfile"
	BuildStatic     = "static"
	BuildAuto       = "auto"

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
	// SharedRefs names the shared variables this value references as
	// {{shared.KEY}}, recorded in CLEARTEXT at write time from the plaintext
	// the operator just supplied (shared-variables.md §2). Key names are not
	// secret — they are already returned by GET /applications/{id}/env — and
	// storing them is what makes the used-by count and the "redeploy to apply"
	// marker plain SQL, so no read path ever unseals a value to answer
	// "who uses this".
	SharedRefs []string
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

	// DeployAwaitingApproval is a deploy that protection parked before it
	// started (deploy-protection.md §3): the Revision and Deployment exist,
	// no work item was published, and the application's own status is
	// untouched because start() never ran. NON-TERMINAL — a parked deploy has
	// not finished — but it holds no pipeline slot either, so the two queue
	// queries in core/store/queries/deployments.sql exclude it alongside the
	// terminal states.
	//
	// REJECTION does not add a sixth status: it ends the deployment as failed
	// with a detail naming the rejecter. The terminal set ('succeeded',
	// 'failed') is load-bearing in the queue queries, in Terminal() below and
	// in the web isTerminal(), and a fifth terminal status would touch all
	// three for no observable gain. A rejected deploy is a deploy that did not
	// ship; WHY it did not ship lives on the DeployApproval row, which is what
	// answers governance questions anyway.
	DeployAwaitingApproval DeploymentStatus = "awaiting_approval"
)

// Terminal reports whether a deployment has finished (succeeded or failed).
// A parked deploy is deliberately not terminal: it has not finished, it is
// waiting for a person.
func (s DeploymentStatus) Terminal() bool {
	return s == DeploySucceeded || s == DeployFailed
}

// Parked reports whether a deployment is waiting on a gate decision.
func (s DeploymentStatus) Parked() bool { return s == DeployAwaitingApproval }

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

// ApplicationRef names an application without loading it — what a refused
// deploy-key delete reports as the blockers (deploy-key-private-repos.md §3).
type ApplicationRef struct {
	ID   string
	Name string
}

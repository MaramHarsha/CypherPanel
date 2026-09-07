// Package rest is the human/UI-facing HTTP API; it also serves the embedded
// web application (webui). It is API-first (vision.md non-negotiable 3): every
// action here is a plain REST call the web UI makes with a bearer token. All
// responses use glossary vocabulary and mask secrets by default (ENGINEERING
// rules 5, 20).
package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/access"
	"github.com/MaramHarsha/cypherpanel/core/agentupdates"
	"github.com/MaramHarsha/cypherpanel/core/api/rest/webui"
	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/githubapp"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/onboarding"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/protection"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/statuspage"
	"github.com/MaramHarsha/cypherpanel/core/templates"
	"github.com/MaramHarsha/cypherpanel/core/updates"
	"github.com/MaramHarsha/cypherpanel/core/webhooks"
)

// Pinger is the readiness dependency (the store).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deployer starts pipelines and publishes desired absence (consumer-defined;
// *scheduler.Scheduler satisfies it).
//
// The two start verbs are the ATTRIBUTED forms: every deploy reachable from
// this API was asked for by an identified principal, or by a signed webhook
// push that has none, and deploy protection records which (deploy-protection.md
// §2). The unattributed Deploy/Rollback stay on the scheduler for the
// machine-triggered paths — a template install, a preview environment — which
// this package never calls.
type Deployer interface {
	DeployAs(ctx context.Context, appID, trigger, ref, requestedBy string) (domain.Deployment, error)
	RollbackAs(ctx context.Context, deploymentID, requestedBy string) (domain.Deployment, error)
	RemoveApp(ctx context.Context, serverID, appID string) error
	// Cancel ends a deployment the operator has stopped waiting on, and
	// Restart recreates an application's container without shipping anything
	// new (deployment-control.md §§2-3).
	Cancel(ctx context.Context, deploymentID, by string) (domain.Deployment, error)
	Restart(ctx context.Context, appID string) (domain.Application, error)
	// RequestResync nudges the fleet to re-read desired state. Access control
	// is current app state rather than a deploy, so a change must reach the
	// Proxy without shipping a revision (app-access-control.md §8). Best-effort
	// by design: the policy is already in Postgres, which is what makes it
	// true — the nudge only decides whether it applies in a second or at the
	// agent's next reconcile.
	RequestResync(ctx context.Context, reason string) error
}

// ProtectionService is deploy protection (consumer-defined; *protection.Service
// satisfies it — deploy-protection.md §6): the policy document, the approval
// queue, the two decision verbs and the recorded freeze override. nil when the
// feature is not wired, which every handler treats as "nothing is protected" —
// the same answer the default document gives.
type ProtectionService interface {
	Get(ctx context.Context, envID string) (domain.EnvironmentProtection, error)
	Set(ctx context.Context, envID string, in protection.Document) (domain.EnvironmentProtection, error)

	Approvals(ctx context.Context, envID, state string) ([]domain.DeployApproval, error)
	// ApprovalFor is also what the deployment DTO attaches, so it stays a
	// plain lookup; store.ErrNotFound means the deployment was never gated.
	ApprovalFor(ctx context.Context, deploymentID string) (domain.DeployApproval, error)
	// ApprovalsForApplication decorates ONE PAGE of deployments: the ids are
	// the rows being rendered, not the application's whole deploy history.
	ApprovalsForApplication(ctx context.Context, appID string, deploymentIDs []string) (map[string]domain.DeployApproval, error)
	Approve(ctx context.Context, deploymentID string, actor domain.User) (domain.Deployment, domain.DeployApproval, error)
	Reject(ctx context.Context, deploymentID, reason string, actor domain.User) (domain.Deployment, domain.DeployApproval, error)

	OpenBreakGlass(ctx context.Context, envID string, actor domain.User, reason string) (domain.BreakGlassGrant, error)
	BreakGlassGrants(ctx context.Context, envID string) ([]domain.BreakGlassGrant, error)
	// Now is the service's own clock, so a grant's `active` flag in a response
	// and the gate's decision are read from one time source.
	Now() time.Time
}

// DeploymentReader reads deployment records (consumer-defined; *store.Store
// satisfies it).
type DeploymentReader interface {
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	ListDeploymentsByApplication(ctx context.Context, appID string, limit int32) ([]domain.Deployment, error)
	// GetRevision resolves a revision to its application, which is what a
	// promotion needs to authorize the SOURCE end.
	GetRevision(ctx context.Context, id string) (domain.Revision, error)
}

// Opener unseals the webhook HMAC secret for verification (consumer-defined;
// *secret.Box satisfies it).
type Opener interface {
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// BackupOps triggers backup runs and restores (consumer-defined;
// *scheduler.Scheduler satisfies it — managed-databases.md §7).
type BackupOps interface {
	RunBackup(ctx context.Context, scheduleID string) (domain.BackupRecord, error)
	RunRestore(ctx context.Context, dbID, backupRecordID string, confirm bool) (domain.DatabaseRestore, error)
	// Volume backups (volume-backups.md). One run fans out across every volume
	// the application has flagged, so this returns a record per volume.
	RunVolumeBackup(ctx context.Context, appID string) ([]domain.VolumeBackupRecord, error)
}

// PreviewManager drives preview environments from PR events and exposes the
// read/teardown surface (consumer-defined; *previews.Manager satisfies it —
// preview-environments.md).
type PreviewManager interface {
	OnPullRequest(ctx context.Context, source domain.Application, action string, prNumber int, prBranch, prSHA string) error
	List(ctx context.Context, sourceAppID string) ([]domain.Preview, error)
	Get(ctx context.Context, id string) (domain.Preview, error)
	DestroyByID(ctx context.Context, previewID string) error
}

// NotifierService is the notifier CRUD surface (consumer-defined;
// *notify.Service satisfies it — notifications.md §7).
type NotifierService interface {
	Create(ctx context.Context, projectID string, in notify.CreateInput) (domain.Notifier, error)
	Update(ctx context.Context, id string, in notify.UpdateInput) (domain.Notifier, error)
	Get(ctx context.Context, id string) (domain.Notifier, error)
	List(ctx context.Context, projectID string) ([]domain.Notifier, error)
	Delete(ctx context.Context, id string) error
}

// RestoreReader is the read side of database restores (consumer-defined;
// *store.Store satisfies it). Writes belong to the scheduler, which is the only
// thing that learns what an agent did.
type RestoreReader interface {
	ListDatabaseRestores(ctx context.Context, databaseID string, limit int32) ([]domain.DatabaseRestore, error)
	GetDatabaseRestore(ctx context.Context, id string) (domain.DatabaseRestore, error)
}

// NotifierDelivery sends a synthetic event through one notifier — the test
// endpoint (consumer-defined; *notify.Manager satisfies it).
type NotifierDelivery interface {
	Deliver(ctx context.Context, n domain.Notifier, ev domain.NotifyEvent) error
	// TestConfig proves an unsaved configuration by using it, so a connection
	// can be tested before it is stored.
	TestConfig(ctx context.Context, channel string, cfg json.RawMessage) error
}

// WebhookEndpointService is the outbound webhook surface — endpoint CRUD, the
// derived Endpoint Health read model, the paged delivery log, ping and
// redeliver (consumer-defined; *webhooks.Service satisfies it —
// outbound-webhooks.md §7). Get and GetDelivery are also what the
// authorization resolvers walk, so they stay plain lookups.
type WebhookEndpointService interface {
	Create(ctx context.Context, projectID string, in webhooks.CreateInput) (webhooks.Created, error)
	Update(ctx context.Context, id string, in webhooks.UpdateInput) (webhooks.EndpointView, error)
	Get(ctx context.Context, id string) (domain.WebhookEndpoint, error)
	View(ctx context.Context, id string) (webhooks.EndpointView, error)
	ListViews(ctx context.Context, projectID string) ([]webhooks.EndpointView, error)
	Delete(ctx context.Context, id string) error
	RotateSecret(ctx context.Context, id string) (string, error)
	Ping(ctx context.Context, id string) (domain.WebhookDelivery, error)
	Deliveries(ctx context.Context, endpointID string, limit int, before string) (webhooks.Page, error)
	GetDelivery(ctx context.Context, id string) (domain.WebhookDelivery, error)
	Redeliver(ctx context.Context, deliveryID string) (domain.WebhookDelivery, error)
}

// SharedVariableService is the project shared-variable surface — CRUD, the
// used-by read model, and the derived "redeploy to apply" marker
// (consumer-defined; *sharedvars.Service satisfies it — shared-variables.md
// §7). Get is what the authorization resolver walks, so it stays a plain
// lookup; every other read returns a View, which structurally cannot carry a
// value.
type SharedVariableService interface {
	Create(ctx context.Context, projectID string, in sharedvars.CreateInput) (sharedvars.View, error)
	Get(ctx context.Context, id string) (domain.SharedVariable, error)
	View(ctx context.Context, id string) (sharedvars.View, error)
	ListViews(ctx context.Context, projectID string) ([]sharedvars.View, error)
	SetValue(ctx context.Context, id, value string) (sharedvars.View, error)
	Delete(ctx context.Context, id string) error
	UsedBy(ctx context.Context, id string) ([]domain.SharedVariableUsage, error)

	RedeployPending(ctx context.Context, appID string) (bool, error)
	PendingInEnvironment(ctx context.Context, envID string) (map[string]bool, error)
}

// AuditRecorder is the audit log (consumer-defined; *audit.Service satisfies
// it — audit-log.md §4, §5). One write verb and two reads: this package records
// what its handlers did and reads the log back, and knows nothing about how
// either is stored.
//
// nil when the feature is not wired, which every call site treats as "record
// nothing" and both read routes answer as an empty log. A panel with no audit
// service therefore behaves exactly as it did before this feature — it does not
// fail requests because it cannot record them.
type AuditRecorder interface {
	Record(ctx context.Context, e audit.Entry) (domain.AuditEvent, error)
	// List and Get take the VIEWER, not a scope: the service resolves what
	// that user may see from their own panel role and team memberships, so no
	// query parameter can widen it (§5).
	List(ctx context.Context, viewer domain.User, q audit.Query) (audit.Page, error)
	Get(ctx context.Context, viewer domain.User, id string) (domain.AuditEvent, error)
}

// InboxService is the notification inbox (consumer-defined; *inbox.Service
// satisfies it — notification-inbox.md §6). Every method takes the caller's own
// user id as its first argument and none accepts anyone else's: tenancy in this
// feature is a column, which is why it adds no authorization resolver.
type InboxService interface {
	List(ctx context.Context, userID string, opts inbox.ListOptions) (inbox.Page, error)
	UnreadCount(ctx context.Context, userID string) (int64, error)
	MarkRead(ctx context.Context, userID, itemID string) error
	MarkAllRead(ctx context.Context, userID string) (int64, error)
	Preferences(ctx context.Context, userID string) (domain.InboxPreferences, error)
	SetPreferences(ctx context.Context, userID string, muted []string) (domain.InboxPreferences, error)
}

// TeamService is the tenancy surface (consumer-defined; *teams.Service
// satisfies it — teams-and-roles.md). RoleForProject/RoleInTeam are the authz
// queries every project-scoped route runs; the rest back the /teams and /users
// management routes.
type TeamService interface {
	RoleForProject(ctx context.Context, actor domain.User, projectID string) (string, error)
	RoleInTeam(ctx context.Context, actor domain.User, teamID string) (string, error)

	Create(ctx context.Context, name string, creator domain.User) (domain.Team, error)
	Get(ctx context.Context, id string) (domain.Team, error)
	ListFor(ctx context.Context, actor domain.User) ([]domain.TeamWithRole, error)
	Rename(ctx context.Context, id, name string) (domain.Team, error)
	Delete(ctx context.Context, id string) error

	Members(ctx context.Context, teamID string) ([]domain.TeamMember, error)
	AddMember(ctx context.Context, teamID, email, role, actorRole string) (domain.TeamMember, error)
	ChangeMemberRole(ctx context.Context, teamID, userID, role, actorRole string) (domain.TeamMember, error)
	RemoveMember(ctx context.Context, teamID, userID, actorRole string) error

	CreateUser(ctx context.Context, email, password, role, actorRole string) (domain.User, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	SetUserRole(ctx context.Context, userID, role string, actor domain.User) (domain.User, error)
	DeleteUser(ctx context.Context, userID string, actor domain.User) error
}

// InviteService is the team-invitation surface (consumer-defined;
// *access.Invites satisfies it — invitations-and-access-requests.md §7).
//
// Preview and Accept take the CLIENT ADDRESS rather than a principal: they are
// the two public routes, throttled by address exactly like sign-in, and the
// service owns that throttle so the handler cannot forget it.
type InviteService interface {
	Create(ctx context.Context, teamID string, in access.CreateInput, actor domain.User, actorRole string) (access.Created, error)
	List(ctx context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error)
	Revoke(ctx context.Context, teamID, id string) (domain.TeamInvite, error)
	Preview(ctx context.Context, token, clientIP string) (access.Preview, error)
	Accept(ctx context.Context, token string, in access.AcceptInput, clientIP string) (access.Accepted, error)
}

// AccessRequestService is the access-request surface (consumer-defined;
// *access.Requests satisfies it). Get is what the two decision routes resolve
// their team from, so it stays a plain lookup.
type AccessRequestService interface {
	Create(ctx context.Context, teamID string, actor domain.User, actorRole string, in access.RequestInput) (domain.AccessRequest, error)
	List(ctx context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error)
	Get(ctx context.Context, id string) (domain.AccessRequest, error)
	Grant(ctx context.Context, id string, actor domain.User, actorRole string) (domain.AccessRequest, error)
	Deny(ctx context.Context, id, reason string, actor domain.User) (domain.AccessRequest, error)
}

// ScheduledTaskService is the scheduled-task CRUD surface (consumer-defined;
// *scheduledtasks.Service satisfies it — scheduled-tasks.md §7).
type ScheduledTaskService interface {
	Create(ctx context.Context, appID string, in scheduledtasks.Input) (domain.ScheduledTask, error)
	Update(ctx context.Context, id string, in scheduledtasks.Input) (domain.ScheduledTask, error)
	Get(ctx context.Context, id string) (domain.ScheduledTask, error)
	List(ctx context.Context, appID string) ([]domain.ScheduledTask, error)
	Delete(ctx context.Context, id string) error
	Runs(ctx context.Context, taskID string) ([]domain.ScheduledTaskRun, error)
}

// LogSubscriber delivers the retained history and then the live tail of one
// log subject (consumer-defined; *bus.Bus satisfies it). handle is invoked
// from the subscriber's goroutine until stop is called.
type LogSubscriber interface {
	// since bounds where the replay starts; the zero time replays everything
	// the stream still holds (deployment-control.md §4).
	SubscribeLogs(ctx context.Context, subject string, since time.Time, handle func(data []byte)) (stop func(), err error)
	SubscribeRuntimeLogs(ctx context.Context, subject string, since time.Time, handle func(data []byte)) (stop func(), err error)
	// SubscribeStatus delivers new app/database status observations (subject +
	// payload) — the source for the /events SSE stream (ui-principles §10).
	SubscribeStatus(ctx context.Context, handle func(subject string, data []byte)) (stop func(), err error)
}

// Deps are the dependencies the API needs.
// OnboardingService creates the first owner on a fresh panel (consumer-defined;
// *onboarding.Service satisfies it). Optional — nil disables the setup path.
type OnboardingService interface {
	NeedsSetup(ctx context.Context) (bool, error)
	CreateFirstOwner(ctx context.Context, email, password string) (domain.User, error)
	// Progress is the guided band's four derived steps. Same service, because
	// "has this panel been set up" and "how far through setting it up is it"
	// are the same question asked at two resolutions (guided-onboarding.md).
	Progress(ctx context.Context, ps onboarding.ProgressStore) (onboarding.Progress, error)
}

// GitHubAppService owns the App credential and what it can reach
// (consumer-defined; *githubapp.Service satisfies it).
type GitHubAppService interface {
	Get(ctx context.Context) (githubapp.Settings, error)
	Set(ctx context.Context, c githubapp.Config) (githubapp.Settings, error)
	Delete(ctx context.Context) error
	RefreshInstallations(ctx context.Context) ([]domain.GitHubInstallation, error)
	Repositories(ctx context.Context) ([]githubapp.Repository, error)
	WebhookSecret(ctx context.Context) (string, error)
}

// GitHubPushHandler deploys every application a push matches. EVERY one,
// deliberately: a repository can be deployed by several environments, and
// picking one would silently skip the rest (github-app.md §6).
type GitHubPushHandler interface {
	DeployFromPush(ctx context.Context, payload []byte) (int, error)
}

// ProjectExporter writes a project's portable archive. Consumer-defined
// (ENGINEERING rule 6) and deliberately narrow: the handler hands it a writer
// and a project id, and the package on the other side has no key material.
type ProjectExporter interface {
	WriteTo(ctx context.Context, w io.Writer, projectID string) error
}

// StatusPageStore is the read/write surface the status page routes need
// (consumer-defined). It embeds statuspage.Reader because the preview route
// builds the real public payload from the same code the public page uses —
// two renderers would be two chances to disclose different things.
type StatusPageStore interface {
	statuspage.Reader
	GetStatusPage(ctx context.Context, id string) (domain.StatusPage, error)
	GetStatusPageByProject(ctx context.Context, projectID string) (domain.StatusPage, error)
	UpsertStatusPage(ctx context.Context, p domain.StatusPage) (domain.StatusPage, error)
	DeleteStatusPage(ctx context.Context, projectID string) error
	UpsertStatusPageComponent(ctx context.Context, c domain.StatusPageComponent) (domain.StatusPageComponent, error)
	DeleteStatusPageComponentsNotIn(ctx context.Context, pageID string, keep []string) error
	GetStatusInterval(ctx context.Context, id string) (domain.StatusInterval, error)
	SetStatusIntervalMessage(ctx context.Context, id, message string) error
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// AgentUpdateService is the plane's half of ADR-010 (consumer-defined;
// *agentupdates.Service satisfies it).
type AgentUpdateService interface {
	Get(ctx context.Context) (agentupdates.View, error)
	Set(ctx context.Context, channel, version, artifactBase, by string) (domain.AgentChannelRow, error)
	Promote(ctx context.Context, by string) (domain.AgentChannelRow, error)
	SetServerChannel(ctx context.Context, serverID, channel string) (domain.Server, error)
}

// PromotionService plans and performs a revision promotion (consumer-defined).
type PromotionService interface {
	PlanPromotion(ctx context.Context, sourceRevisionID, targetApplicationID string) (scheduler.PromotionPlan, error)
	Promote(ctx context.Context, sourceRevisionID, targetApplicationID, requestedBy string) (domain.Deployment, error)
}

// UpdateChecker reports the running build and the newest release seen
// (consumer-defined; *updates.Checker satisfies it).
type UpdateChecker interface {
	Current() updates.Info
	Latest() *updates.Release
}

// StatusPageRoutes registers the public, unauthenticated status routes.
type StatusPageRoutes interface {
	Routes(mux *http.ServeMux)
}

// StatusPageCache is the rendered-page cache, so an operator who renames or
// disables a page sees it change now rather than waiting out the TTL.
type StatusPageCache interface {
	Invalidate(slug string)
}

// VolumeBackupStore is the schedule and history surface the volume-backup
// routes need (consumer-defined, ENGINEERING rule 6). nil answers 501, the
// shape every optional surface here takes.
type VolumeBackupStore interface {
	GetVolumeBackupByApplication(ctx context.Context, appID string) (domain.VolumeBackup, error)
	UpsertVolumeBackup(ctx context.Context, v domain.VolumeBackup) (domain.VolumeBackup, error)
	DeleteVolumeBackup(ctx context.Context, appID string) error
	ListVolumeBackupRecords(ctx context.Context, scheduleID string, limit int) ([]domain.VolumeBackupRecord, error)
}

type Deps struct {
	Auth            *auth.Authenticator
	Onboarding      OnboardingService
	Servers         *servers.Service
	Projects        *projects.Service
	Applications    *applications.Service
	DeployKeys      *deploykeys.Service
	Databases       *databases.Service
	BackupTargets   *databases.BackupTargetService
	BackupSchedules *databases.BackupScheduleService
	Backups         BackupOps
	// Restores reads the restore records the scheduler writes.
	Restores         RestoreReader
	Previews         PreviewManager
	Notifiers        NotifierService
	NotifyDelivery   NotifierDelivery
	WebhookEndpoints WebhookEndpointService
	// Registries are the team's container registry credentials
	// (registries.md); nil answers 501 on every registry route, which is what
	// a panel that has not wired them looked like before they existed.
	Registries RegistryService
	// Compose is the Compose Stack surface (compose-stacks.md); nil answers 501
	// on every stack route, which is what a panel that has not wired them
	// looked like before they existed.
	Compose ComposeService
	// Export streams a project out as a portable archive (project-export.md);
	// nil answers 501, the same shape every optional surface here takes. It is
	// deliberately given no way to unseal a secret — see core/export.
	Export ProjectExporter
	// VolumeBackups is the volume schedule store (volume-backups.md).
	VolumeBackups VolumeBackupStore
	// StatusPages is the public status page surface (status-pages.md).
	StatusPages  StatusPageStore
	StatusServer StatusPageCache
	// StatusRoutes registers the two PUBLIC routes. Separate from
	// StatusServer so a panel can hold the cache without opening the routes.
	StatusRoutes StatusPageRoutes
	// Metrics is the metrics, traffic and usage surface (metrics-and-usage.md).
	Metrics MetricsStore
	// Alerts is the threshold-rule surface, and AlertBacktest is the SAME
	// evaluator the loop uses — two implementations would be two answers to
	// "what would this rule have done" (threshold-alerts.md §6).
	Alerts        AlertStore
	AlertBacktest AlertBacktester
	// Upgrades is the guided panel upgrade (panel-updates.md). nil is a panel
	// with no helper, and every route here answers 501 rather than pretending.
	Upgrades UpgradeService
	// LogDrains is the panel's outbox for log lines (log-drains.md).
	LogDrains LogDrainService
	// Promotion ships a tested artifact to another environment
	// (revision-promotion.md). *scheduler.Scheduler satisfies it.
	Promotion PromotionService
	// Quotas is admission control on aggregate consumption (ADR-012).
	Quotas QuotaService
	// MailHost is provider-backed email for verified domains (managed-email.md).
	MailHost MailHostService
	// GitHubApp is the panel's App: repository discovery and a short-lived
	// clone credential (github-app.md). nil is a panel that has not enabled it,
	// and every route answers accordingly rather than pretending.
	GitHubApp GitHubAppService
	// GitHubPush turns one App delivery into deployments.
	GitHubPush GitHubPushHandler
	// OnboardingCounts is what the guided band counts. nil answers "done",
	// which is the honest degradation: a band that cannot know what is left
	// must not claim work remains.
	OnboardingCounts onboarding.ProgressStore
	// AgentUpdates owns the two release channels and the gate between them
	// (agent-updates.md, ADR-010). nil answers 503 on every route here, which
	// is a panel that has not wired the feature rather than one that has no
	// version to name.
	AgentUpdates AgentUpdateService
	// PlaneDR is the control plane backing itself up, and PlaneDRFetch reads
	// one object back so a Recovery Key can be proven to still work.
	PlaneDR      PlaneDRService
	PlaneDRFetch func(ctx context.Context, target domain.BackupTarget, key string) ([]byte, error)
	// Updates is the release-feed checker, for what version is available.
	Updates UpdateChecker
	// PanelURL is the panel's own advertised base URL, used to tell the
	// operator where their status page is reachable without any DNS.
	PanelURL string
	Inbox    InboxService
	// Audit records every sensitive action and serves the log back
	// (audit-log.md). nil records nothing and serves an empty log.
	Audit           AuditRecorder
	SharedVariables SharedVariableService
	ScheduledTasks  ScheduledTaskService
	Templates       *templates.Service
	Teams           TeamService
	// Invites and AccessRequests are the two ways into a team from outside it
	// (invitations-and-access-requests.md). nil disables the routes rather than
	// changing anyone's rank: a panel without them behaves exactly as it did
	// before the feature existed.
	Invites        InviteService
	AccessRequests AccessRequestService
	Mail           MailService
	// PanelTLS is the panel's ACME account (agent-identity-and-tls.md §4); nil
	// when it is not wired, which every handler treats as "no certificate
	// resolver" — the honest default rather than an assumed one.
	PanelTLS PanelTLSService
	// DNS is the panel's DNS Provider; nil when DNS automation is not wired,
	// which every handler treats as "nothing is enforced" (dns-automation.md §4.1).
	DNS      DNSService
	DNSZones DNSReader
	// ServerAddresses records where a server's applications' DNS points.
	ServerAddresses ServerAddressWriter
	// Protection is deploy protection (deploy-protection.md); nil when it is
	// not wired, which every handler treats as "nothing is protected".
	Protection  ProtectionService
	Scheduler   Deployer
	Deployments DeploymentReader
	Opener      Opener
	Pinger      Pinger
	CACertPEM   []byte
	EnrollAddr  string // advertised gRPC enrollment address (host:port)
	NATSURL     string // advertised data-plane URL
	Logs        LogSubscriber
	ConsoleURL  string // advertised HTTP base URL (installer + CA fetch)
	// PublicHost is the address agents dial and this host answers at. It names
	// the machine the "use this machine" button will change (local-server.md §8).
	PublicHost string
	// UpgradeDir is the root helper handoff directory, shared by the panel
	// upgrade and the local-agent install. Empty is a container install, where
	// there is no host service manager to install into and both say so rather
	// than drawing a control that cannot work.
	UpgradeDir string
	// TrustedProxies are the peer CIDRs allowed to speak for a client through
	// X-Forwarded-For / X-Real-IP / X-Request-Id. Empty means nothing is
	// trusted and the TCP peer is always the client (§5).
	TrustedProxies []netip.Prefix
	// Panel is the build the process is running and what the update check has
	// found; nil serves 503 on GET /panel/version (§3).
	Panel PanelInfo
	// PanelLogs is the in-memory tail of the panel's own log; nil serves 503
	// on GET /panel/logs (§4).
	PanelLogs PanelLogTail
	// DataDir is cypherd's own durable state directory, reported on
	// GET /panel/version so the one host the fleet view cannot cover is
	// covered (disk-management.md §6). Empty reports zeros, which a client
	// reads as unknown.
	DataDir string
	Log     *slog.Logger
}

// API holds the HTTP handlers and their dependencies.
type API struct {
	deps Deps
}

// New builds the API.
func New(d Deps) *API { return &API{deps: d} }

// Handler returns the fully-routed HTTP handler with global middleware applied.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health (unauthenticated).
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)

	// Public status pages (status-pages.md §8). The third route family this
	// panel opens to people outside it — after the inbound GitHub webhook and
	// the invitation links — and the first meant for an anonymous audience
	// rather than for someone holding a secret. Read-only, no body, no query
	// parameters, and one undifferentiated 404 for an unknown slug, a disabled
	// page and a deleted page alike.
	if a.deps.StatusRoutes != nil {
		a.deps.StatusRoutes.Routes(mux)
	}

	// Live status stream (SSE): the UI subscribes once and refetches the
	// resources it names as they change, instead of polling (ui-principles §10).
	mux.HandleFunc("GET /api/v1/events", a.authed(a.handleEvents))

	// Auth.
	// First-run setup (public, one-time): before any account exists, these
	// let an operator create the first owner in the browser (first-run-setup.md).
	mux.HandleFunc("GET /api/v1/auth/setup", a.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/auth/setup", a.handleSetup)

	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authed(a.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", a.authed(a.handleMe))

	// Live sessions: see where the account is signed in, and sign it out.
	// Session-only — an API token must not be able to cut off the operator.
	mux.HandleFunc("PATCH /api/v1/auth/me", a.sessionOnly(a.handleUpdateProfile))
	mux.HandleFunc("POST /api/v1/auth/password", a.sessionOnly(a.handleChangePassword))
	mux.HandleFunc("GET /api/v1/auth/email/change", a.sessionOnly(a.handleGetPendingEmailChange))
	mux.HandleFunc("POST /api/v1/auth/email/change", a.sessionOnly(a.handleRequestEmailChange))
	mux.HandleFunc("DELETE /api/v1/auth/email/change", a.sessionOnly(a.handleCancelEmailChange))
	mux.HandleFunc("POST /api/v1/auth/email/confirm", a.sessionOnly(a.handleConfirmEmailChange))
	mux.HandleFunc("PUT /api/v1/auth/me/avatar", a.sessionOnly(a.handleSetAvatar))
	mux.HandleFunc("DELETE /api/v1/auth/me/avatar", a.sessionOnly(a.handleDeleteAvatar))
	mux.HandleFunc("GET /api/v1/users/{id}/avatar", a.authed(a.handleGetAvatar))
	mux.HandleFunc("GET /api/v1/auth/sessions", a.sessionOnly(a.handleListSessions))
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{id}", a.sessionOnly(a.handleRevokeSession))
	mux.HandleFunc("POST /api/v1/auth/sessions/revoke-others", a.sessionOnly(a.handleRevokeOtherSessions))

	// Personal access tokens (scoped API tokens for CI/automation). A token
	// authenticates as its owning user, inheriting that user's authorization
	// narrowed by its abilities. Managing tokens is session-only: a leaked
	// token must not be able to mint itself a wider one.
	mux.HandleFunc("POST /api/v1/tokens", a.sessionOnly(a.handleCreateToken))
	mux.HandleFunc("GET /api/v1/tokens", a.sessionOnly(a.handleListTokens))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", a.sessionOnly(a.handleDeleteToken))

	// Two-factor authentication (TOTP + recovery codes). Session-only for the
	// same reason: turning 2FA off is the last step of an account takeover.
	mux.HandleFunc("GET /api/v1/auth/totp", a.sessionOnly(a.handleTOTPStatus))
	mux.HandleFunc("POST /api/v1/auth/totp/enroll", a.sessionOnly(a.handleTOTPEnroll))
	mux.HandleFunc("POST /api/v1/auth/totp/verify", a.sessionOnly(a.handleTOTPVerify))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", a.sessionOnly(a.handleTOTPDisable))

	// Public CA certificate (needed by agents to pin the plane; not secret).
	mux.HandleFunc("GET /api/v1/ca.pem", a.handleCAPem)

	// The API's own contract (ENGINEERING rule 19: the spec is the source of
	// truth, so it ships with the binary that implements it).
	mux.HandleFunc("GET /api/v1/openapi.yaml", a.handleOpenAPI)

	// The agent join installer (public, no secrets — the token and CA
	// fingerprint arrive via the operator's install command). The canonical
	// file is /install/agent.sh; make generate syncs the embedded copy.
	mux.HandleFunc("GET /install/agent.sh", a.handleInstallScript)

	// Servers.
	mux.HandleFunc("GET /api/v1/servers", a.authed(a.handleListServers))
	mux.HandleFunc("POST /api/v1/servers", a.authed(a.handleCreateServer))
	mux.HandleFunc("GET /api/v1/servers/{id}", a.authed(a.handleGetServer))
	// What this server already routes, so a form can warn before it refuses.
	mux.HandleFunc("GET /api/v1/servers/{id}/domains", a.authed(a.handleListServerDomains))
	// What runs here — the first question anyone asks about a host.
	mux.HandleFunc("GET /api/v1/servers/{id}/workloads", a.authed(a.handleListServerWorkloads))
	// Push-to-deploy needs a secret the operator holds. sessionOnly because it
	// is credential management: an API token must not mint one.
	mux.HandleFunc("POST /api/v1/applications/{id}/webhook/rotate", a.sessionOnly(a.handleRotateApplicationWebhook))
	mux.HandleFunc("PATCH /api/v1/servers/{id}", a.authed(a.handlePatchServer))
	mux.HandleFunc("DELETE /api/v1/servers/{id}", a.authed(a.handleDeleteServer))

	// Deploy keys.
	mux.HandleFunc("GET /api/v1/deploy-keys", a.authed(a.handleListDeployKeys))
	mux.HandleFunc("POST /api/v1/deploy-keys", a.authed(a.handleCreateDeployKey))
	mux.HandleFunc("GET /api/v1/deploy-keys/{id}", a.authed(a.handleGetDeployKey))
	mux.HandleFunc("DELETE /api/v1/deploy-keys/{id}", a.authed(a.handleDeleteDeployKey))

	// Projects & environments.
	mux.HandleFunc("GET /api/v1/projects", a.authed(a.handleListProjects))
	mux.HandleFunc("POST /api/v1/projects", a.authed(a.handleCreateProject))
	mux.HandleFunc("GET /api/v1/projects/{id}", a.authed(a.handleGetProject))
	mux.HandleFunc("DELETE /api/v1/projects/{id}", a.authed(a.handleDeleteProject))
	// Portable export (project-export.md). Team admin; the archive carries
	// configuration and env-var KEYS, never a sealed value.
	mux.HandleFunc("GET /api/v1/projects/{id}/export", a.authed(a.handleExportProject))
	mux.HandleFunc("PATCH /api/v1/projects/{id}", a.authed(a.handlePatchProject))
	mux.HandleFunc("PATCH /api/v1/environments/{id}", a.authed(a.handlePatchEnvironment))
	mux.HandleFunc("DELETE /api/v1/environments/{id}", a.authed(a.handleDeleteEnvironment))
	mux.HandleFunc("GET /api/v1/projects/{id}/environments", a.authed(a.handleListEnvironments))
	mux.HandleFunc("POST /api/v1/projects/{id}/environments", a.authed(a.handleCreateEnvironment))

	// Phase 4: bundled application and database templates.
	mux.HandleFunc("GET /api/v1/templates", a.authed(a.handleListTemplates))
	mux.HandleFunc("GET /api/v1/templates/{slug}", a.authed(a.handleGetTemplate))
	mux.HandleFunc("POST /api/v1/templates/{slug}/install", a.authed(a.handleInstallTemplate))

	// Applications (created + listed under an environment; addressed by app id).
	mux.HandleFunc("POST /api/v1/environments/{id}/applications", a.authed(a.handleCreateApplication))
	mux.HandleFunc("GET /api/v1/environments/{id}/applications", a.authed(a.handleListApplications))
	mux.HandleFunc("GET /api/v1/applications/{id}", a.authed(a.handleGetApplication))
	mux.HandleFunc("PATCH /api/v1/applications/{id}", a.authed(a.handlePatchApplication))
	mux.HandleFunc("DELETE /api/v1/applications/{id}", a.authed(a.handleDeleteApplication))
	mux.HandleFunc("GET /api/v1/applications/{id}/domain-check", a.authed(a.handleCheckApplicationDomain))
	mux.HandleFunc("GET /api/v1/applications/{id}/dns", a.authed(a.handleGetApplicationDNS))
	mux.HandleFunc("GET /api/v1/applications/{id}/logs", a.authed(a.handleGetApplicationLogs))
	mux.HandleFunc("GET /api/v1/applications/{id}/env", a.authed(a.handleListEnvVars))
	mux.HandleFunc("PUT /api/v1/applications/{id}/env/{key}", a.authed(a.handleSetEnvVar))
	mux.HandleFunc("DELETE /api/v1/applications/{id}/env/{key}", a.authed(a.handleDeleteEnvVar))

	// Deployments (the pipeline: deploy, inspect, roll back).
	mux.HandleFunc("POST /api/v1/applications/{id}/deploy", a.authed(a.handleDeployApplication))
	mux.HandleFunc("GET /api/v1/applications/{id}/deployments", a.authed(a.handleListDeployments))
	mux.HandleFunc("GET /api/v1/deployments/{id}", a.authed(a.handleGetDeployment))
	mux.HandleFunc("GET /api/v1/deployments/{id}/logs", a.authed(a.handleGetDeploymentLogs))
	mux.HandleFunc("POST /api/v1/deployments/{id}/rollback", a.authed(a.handleRollback))
	// V1.x: deployment control (deployment-control.md). Cancel ends a deploy
	// the operator has stopped waiting on; restart recreates a container
	// without shipping anything new. Both carry the `deploy` ability — the
	// credential that can start a deploy can stop one — and neither is
	// session-only: cancelling and restarting from CI is legitimate.
	mux.HandleFunc("POST /api/v1/deployments/{id}/cancel", a.authed(a.handleCancelDeployment))
	mux.HandleFunc("POST /api/v1/applications/{id}/restart", a.authed(a.handleRestartApplication))
	// Front-door access control (app-access-control.md §9). Member rank: an
	// operator who may deploy the app may decide who reaches it.
	// Volume backups (volume-backups.md §3): one schedule per application,
	// covering every volume it marks as backed up.
	// Guided panel upgrades (panel-updates.md §10). Owner, and session-only, on
	// everything that acts: this is the control that decides what code the
	// control plane runs, and an API token may live in a CI runner.
	mux.HandleFunc("GET /api/v1/panel/updates", a.authed(a.handleGetUpdates))
	mux.HandleFunc("GET /api/v1/panel/changelog", a.authed(a.handleChangelog))
	mux.HandleFunc("GET /api/v1/panel/updates/preflight", a.sessionOnly(a.handlePreflight))
	mux.HandleFunc("POST /api/v1/panel/updates/upgrade", a.sessionOnly(a.handleStartUpgrade))
	mux.HandleFunc("POST /api/v1/panel/updates/cancel", a.sessionOnly(a.handleCancelUpgrade))
	mux.HandleFunc("GET /api/v1/panel/updates/history", a.sessionOnly(a.handleUpgradeHistory))
	mux.HandleFunc("PATCH /api/v1/panel/snapshots/{id}", a.sessionOnly(a.handleSetSnapshotRetention))
	mux.HandleFunc("DELETE /api/v1/panel/snapshots/{id}", a.sessionOnly(a.handleDeleteSnapshot))
	mux.HandleFunc("POST /api/v1/panel/snapshots/{id}/restore", a.sessionOnly(a.handleRestoreSnapshot))

	// The plane's own disaster recovery (plane-disaster-recovery.md §9).
	// Owner and session-only: arming it decides where a complete copy of the
	// panel, master key included, is written.
	mux.HandleFunc("GET /api/v1/panel/disaster-recovery", a.sessionOnly(a.handleGetPlaneDR))
	mux.HandleFunc("PUT /api/v1/panel/disaster-recovery", a.sessionOnly(a.handleArmPlaneDR))
	mux.HandleFunc("DELETE /api/v1/panel/disaster-recovery", a.sessionOnly(a.handleDisarmPlaneDR))
	mux.HandleFunc("POST /api/v1/panel/disaster-recovery/run", a.sessionOnly(a.handleRunPlaneDR))
	mux.HandleFunc("POST /api/v1/panel/disaster-recovery/verify", a.sessionOnly(a.handleVerifyPlaneDR))
	mux.HandleFunc("GET /api/v1/panel/disaster-recovery/snapshots", a.sessionOnly(a.handleListPlaneSnapshots))

	// Revision promotion (revision-promotion.md §6). The plan is a GET because
	// it writes nothing, and it IS the screen: an operator decides from what
	// would change rather than from a confirmation dialog.
	mux.HandleFunc("GET /api/v1/revisions/{id}/promotion-plan", a.authed(a.handlePlanPromotion))
	mux.HandleFunc("POST /api/v1/revisions/{id}/promote", a.authed(a.handlePromote))

	// Resource quotas (resource-quotas.md §9; ADR-012). Reading is a member;
	// SETTING is admin, because capping what a scope may consume is a decision
	// about shared capacity rather than about the scope's own code.
	//
	// Every mutation is sessionOnly for the reason the protection policy
	// already records: an API token inherits its owner's role, so a `write`
	// token belonging to an admin could otherwise raise the cap and then deploy
	// freely — and a control a leaked CI credential can switch off is
	// decorative (§3).
	mux.HandleFunc("GET /api/v1/projects/{id}/quota", a.authed(a.handleGetProjectQuota))
	mux.HandleFunc("PUT /api/v1/projects/{id}/quota", a.sessionOnly(a.handleSetProjectQuota))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/quota", a.sessionOnly(a.handleDeleteProjectQuota))
	mux.HandleFunc("GET /api/v1/teams/{id}/quota", a.authed(a.handleGetTeamQuota))
	mux.HandleFunc("PUT /api/v1/teams/{id}/quota", a.sessionOnly(a.handleSetTeamQuota))
	mux.HandleFunc("DELETE /api/v1/teams/{id}/quota", a.sessionOnly(a.handleDeleteTeamQuota))

	// Email for verified domains, via a provider (managed-email.md). The panel
	// writes DNS and manages mailboxes; it runs no MTA and stores no message.
	mux.HandleFunc("GET /api/v1/mail/provider", a.authed(a.handleGetMailHost))
	mux.HandleFunc("PUT /api/v1/mail/provider", a.authed(a.handleConnectMailHost))
	mux.HandleFunc("DELETE /api/v1/mail/provider", a.authed(a.handleDisconnectMailHost))
	mux.HandleFunc("GET /api/v1/mail/domains", a.authed(a.handleListMailDomains))
	mux.HandleFunc("POST /api/v1/mail/domains", a.authed(a.handleEnableMailDomain))
	mux.HandleFunc("GET /api/v1/mail/domains/{id}/records", a.authed(a.handleMailDomainRecords))
	mux.HandleFunc("POST /api/v1/mail/domains/{id}/records", a.authed(a.handleRewriteMailRecords))
	mux.HandleFunc("DELETE /api/v1/mail/domains/{id}", a.authed(a.handleDisableMailDomain))
	mux.HandleFunc("GET /api/v1/mail/domains/{id}/mailboxes", a.authed(a.handleListMailboxes))
	mux.HandleFunc("POST /api/v1/mail/domains/{id}/mailboxes", a.authed(a.handleCreateMailbox))
	mux.HandleFunc("DELETE /api/v1/mail/domains/{id}/mailboxes", a.authed(a.handleDeleteMailbox))
	mux.HandleFunc("POST /api/v1/mail/domains/{id}/mailboxes/password", a.authed(a.handleResetMailboxPassword))

	// Log drains (log-drains.md §9). Panel admin: a drain spends the panel's
	// stream, CPU and egress, and a project-scoped one still ships lines out
	// of the install.
	mux.HandleFunc("GET /api/v1/log-drains", a.authed(a.handleListLogDrains))
	mux.HandleFunc("POST /api/v1/log-drains", a.authed(a.handleCreateLogDrain))
	mux.HandleFunc("PATCH /api/v1/log-drains/{id}", a.authed(a.handleUpdateLogDrain))
	mux.HandleFunc("DELETE /api/v1/log-drains/{id}", a.authed(a.handleDeleteLogDrain))

	// Threshold alerts (threshold-alerts.md §7).
	mux.HandleFunc("GET /api/v1/alert-rules", a.authed(a.handleListAlertRules))
	mux.HandleFunc("POST /api/v1/alert-rules", a.authed(a.handleCreateAlertRule))
	mux.HandleFunc("POST /api/v1/alert-rules/backtest", a.authed(a.handleBacktestAlertRule))
	mux.HandleFunc("PATCH /api/v1/alert-rules/{id}", a.authed(a.handleSetAlertRuleEnabled))
	mux.HandleFunc("DELETE /api/v1/alert-rules/{id}", a.authed(a.handleDeleteAlertRule))
	mux.HandleFunc("GET /api/v1/alert-rules/{id}/events", a.authed(a.handleListAlertEvents))

	// Metrics, traffic and usage (metrics-and-usage.md §10). Fixed endpoints,
	// not a query language: they answer the questions the screens ask.
	mux.HandleFunc("GET /api/v1/applications/{id}/metrics", a.authed(a.handleApplicationMetrics))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/metrics", a.authed(a.handleComposeStackMetrics))
	mux.HandleFunc("GET /api/v1/databases/{id}/metrics", a.authed(a.handleDatabaseMetrics))
	mux.HandleFunc("GET /api/v1/servers/{id}/metrics", a.authed(a.handleServerMetrics))
	mux.HandleFunc("GET /api/v1/applications/{id}/traffic", a.authed(a.handleApplicationTraffic))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/traffic", a.authed(a.handleComposeStackTraffic))
	mux.HandleFunc("GET /api/v1/usage", a.authed(a.handleUsage))
	mux.HandleFunc("GET /api/v1/usage/export", a.authed(a.handleUsageExport))
	mux.HandleFunc("GET /api/v1/settings/metrics", a.authed(a.handleGetMetricsSettings))
	mux.HandleFunc("PUT /api/v1/settings/metrics", a.authed(a.handleSetMetricsSettings))

	// Status pages (status-pages.md §8). Enabling one is TEAM ADMIN;
	// annotating a live incident is a member, on purpose.
	mux.HandleFunc("GET /api/v1/projects/{id}/status-page", a.authed(a.handleGetStatusPage))
	mux.HandleFunc("PUT /api/v1/projects/{id}/status-page", a.authed(a.handleSetStatusPage))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/status-page", a.authed(a.handleDeleteStatusPage))
	mux.HandleFunc("GET /api/v1/status-pages/{id}/components", a.authed(a.handleListStatusComponents))
	mux.HandleFunc("PUT /api/v1/status-pages/{id}/components", a.authed(a.handleSetStatusComponents))
	mux.HandleFunc("GET /api/v1/status-pages/{id}/domain-check", a.authed(a.handleCheckStatusPageDomain))
	mux.HandleFunc("GET /api/v1/status-pages/{id}/preview", a.authed(a.handlePreviewStatusPage))
	mux.HandleFunc("PATCH /api/v1/status-pages/{id}/incidents/{iid}", a.authed(a.handleAnnotateIncident))

	mux.HandleFunc("GET /api/v1/applications/{id}/volume-backup", a.authed(a.handleGetVolumeBackup))
	mux.HandleFunc("PUT /api/v1/applications/{id}/volume-backup", a.authed(a.handleSetVolumeBackup))
	mux.HandleFunc("DELETE /api/v1/applications/{id}/volume-backup", a.authed(a.handleDeleteVolumeBackup))
	mux.HandleFunc("POST /api/v1/applications/{id}/volume-backup/run", a.authed(a.handleRunVolumeBackup))
	mux.HandleFunc("GET /api/v1/applications/{id}/volume-backup/history", a.authed(a.handleVolumeBackupHistory))
	mux.HandleFunc("GET /api/v1/applications/{id}/access", a.authed(a.handleGetApplicationAccess))
	mux.HandleFunc("PUT /api/v1/applications/{id}/access", a.authed(a.handleSetApplicationAccess))
	mux.HandleFunc("POST /api/v1/applications/{id}/access/preview-password", a.authed(a.handleSetPreviewPassword))
	mux.HandleFunc("PUT /api/v1/applications/{id}/maintenance", a.authed(a.handleSetMaintenance))
	mux.HandleFunc("DELETE /api/v1/applications/{id}/maintenance", a.authed(a.handleClearMaintenance))

	// GitHub webhook: authenticated by per-app HMAC secret, not a session
	// (spec §4) — the only unauthenticated mutating route.
	mux.HandleFunc("POST /webhooks/github/{id}", a.handleGitHubWebhook)

	// Phase 3: Managed Databases (managed-databases.md §4).
	mux.HandleFunc("POST /api/v1/environments/{id}/databases", a.authed(a.handleCreateDatabase))
	mux.HandleFunc("GET /api/v1/environments/{id}/databases", a.authed(a.handleListDatabases))
	mux.HandleFunc("GET /api/v1/databases/{id}", a.authed(a.handleGetDatabase))
	mux.HandleFunc("PATCH /api/v1/databases/{id}", a.authed(a.handlePatchDatabase))
	mux.HandleFunc("DELETE /api/v1/databases/{id}", a.authed(a.handleDeleteDatabase))
	mux.HandleFunc("POST /api/v1/databases/{id}/stop", a.authed(a.handleStopDatabase))
	mux.HandleFunc("POST /api/v1/databases/{id}/start", a.authed(a.handleStartDatabase))
	mux.HandleFunc("POST /api/v1/databases/{id}/reset-password", a.authed(a.handleResetDatabasePassword))
	mux.HandleFunc("GET /api/v1/databases/{id}/connection-info", a.authed(a.handleDatabaseConnectionInfo))

	// Phase 3: database backups (managed-databases.md §7).
	// Panel Mail: panel-wide infrastructure, so panel-admin gated like the
	// backup targets and servers beside it (docs/features/panel-mail.md §2.2).
	mux.HandleFunc("GET /api/v1/panel/mail", a.authed(a.handleGetPanelMail))
	mux.HandleFunc("PUT /api/v1/panel/mail", a.authed(a.handleSetPanelMail))
	mux.HandleFunc("DELETE /api/v1/panel/mail", a.authed(a.handleDeletePanelMail))
	mux.HandleFunc("POST /api/v1/panel/mail/test", a.authed(a.handleTestPanelMail))

	// Panel build, update check and diagnostics (control-plane-hardening.md
	// §§3–4). The version is readable by any authenticated principal — the
	// report-issue dialog needs it for every user; the log tail is owner-only
	// and session-only, because it names hosts and resources and an API token
	// must never be able to lift it.
	mux.HandleFunc("GET /api/v1/panel/version", a.authed(a.handleGetPanelVersion))

	// Agent version channels (agent-updates.md §7, ADR-010). The three mutating
	// routes are owner AND session-only: this is the one control that changes
	// what code runs on every server, and an API token that can move a channel
	// is an API token that owns the fleet.
	// Guided onboarding: the thread between the golden path's four steps
	// (guided-onboarding.md).
	mux.HandleFunc("GET /api/v1/onboarding", a.authed(a.handleGetOnboarding))

	// The GitHub App (github-app.md §7). Writing is owner AND session-only: the
	// private key can mint a token for every repository the App reaches.
	mux.HandleFunc("GET /api/v1/github/app", a.authed(a.handleGetGitHubApp))
	mux.HandleFunc("PUT /api/v1/github/app", a.sessionOnly(a.handleSetGitHubApp))
	mux.HandleFunc("DELETE /api/v1/github/app", a.sessionOnly(a.handleDeleteGitHubApp))
	mux.HandleFunc("POST /api/v1/github/installations/refresh", a.authed(a.handleRefreshGitHubInstallations))
	mux.HandleFunc("GET /api/v1/github/repositories", a.authed(a.handleListGitHubRepositories))
	// Unauthenticated by design, verified by the App's own HMAC — the second
	// such route, beside the per-application webhook it does not replace.
	mux.HandleFunc("POST /webhooks/github/app", a.handleGitHubAppWebhook)

	mux.HandleFunc("GET /api/v1/panel/agent-updates", a.authed(a.handleGetAgentUpdates))
	mux.HandleFunc("PUT /api/v1/panel/agent-updates/{channel}", a.sessionOnly(a.handleSetAgentChannel))
	mux.HandleFunc("POST /api/v1/panel/agent-updates/promote", a.sessionOnly(a.handlePromoteAgentChannel))
	mux.HandleFunc("PUT /api/v1/servers/{id}/agent-channel", a.sessionOnly(a.handleSetServerAgentChannel))

	// "Use this machine" (local-server.md §7). The POST is owner AND
	// session-only: it installs software on the panel's own host as root, and
	// an API token that can do that is an API token that owns the box.
	mux.HandleFunc("GET /api/v1/servers/local", a.authed(a.handleGetLocalServer))
	mux.HandleFunc("POST /api/v1/servers/local", a.sessionOnly(a.handleCreateLocalServer))
	mux.HandleFunc("GET /api/v1/panel/logs", a.sessionOnly(a.handleGetPanelLogs))

	// The panel's ACME account (agent-identity-and-tls.md §4). Owner-only: it
	// decides how every routed application on every server is served to the
	// public internet, and it registers an account in the operator's name.
	mux.HandleFunc("GET /api/v1/panel/tls", a.authed(a.handleGetPanelTLS))
	mux.HandleFunc("PUT /api/v1/panel/tls", a.authed(a.handleSetPanelTLS))

	// DNS automation (dns-automation.md §5). Panel-scoped like mail: a
	// Cloudflare account is an operator-level asset, not a team's.
	mux.HandleFunc("GET /api/v1/panel/dns", a.authed(a.handleGetPanelDNS))
	mux.HandleFunc("PUT /api/v1/panel/dns", a.authed(a.handleSetPanelDNS))
	mux.HandleFunc("DELETE /api/v1/panel/dns", a.authed(a.handleDeletePanelDNS))
	mux.HandleFunc("POST /api/v1/panel/dns/test", a.authed(a.handleTestPanelDNS))
	mux.HandleFunc("GET /api/v1/panel/dns/disconnect-preview", a.authed(a.handleDNSDisconnectPreview))
	mux.HandleFunc("GET /api/v1/panel/dns/zones", a.authed(a.handleListDNSZones))
	mux.HandleFunc("POST /api/v1/panel/dns/zones/refresh", a.authed(a.handleRefreshDNSZones))

	mux.HandleFunc("POST /api/v1/backup-targets", a.authed(a.handleCreateBackupTarget))
	mux.HandleFunc("GET /api/v1/backup-targets", a.authed(a.handleListBackupTargets))
	mux.HandleFunc("GET /api/v1/backup-targets/{id}", a.authed(a.handleGetBackupTarget))
	mux.HandleFunc("PATCH /api/v1/backup-targets/{id}", a.authed(a.handlePatchBackupTarget))
	mux.HandleFunc("DELETE /api/v1/backup-targets/{id}", a.authed(a.handleDeleteBackupTarget))

	mux.HandleFunc("POST /api/v1/databases/{id}/backups", a.authed(a.handleCreateDatabaseBackup))
	mux.HandleFunc("GET /api/v1/databases/{id}/backups", a.authed(a.handleListDatabaseBackups))
	mux.HandleFunc("PATCH /api/v1/databases/{id}/backups/{bak_id}", a.authed(a.handlePatchDatabaseBackup))
	mux.HandleFunc("DELETE /api/v1/databases/{id}/backups/{bak_id}", a.authed(a.handleDeleteDatabaseBackup))
	mux.HandleFunc("GET /api/v1/databases/{id}/backups/{bak_id}/history", a.authed(a.handleListBackupRecords))
	mux.HandleFunc("POST /api/v1/databases/{id}/backups/{bak_id}/run", a.authed(a.handleRunBackup))
	mux.HandleFunc("GET /api/v1/databases/{id}/restores", a.authed(a.handleListDatabaseRestores))
	mux.HandleFunc("GET /api/v1/databases/{id}/restores/{rid}", a.authed(a.handleGetDatabaseRestore))
	mux.HandleFunc("POST /api/v1/databases/{id}/restore", a.authed(a.handleRestoreDatabase))

	// Phase 3: preview environments (preview-environments.md §7).
	mux.HandleFunc("GET /api/v1/applications/{id}/previews", a.authed(a.handleListPreviews))
	mux.HandleFunc("GET /api/v1/previews/{id}", a.authed(a.handleGetPreview))
	mux.HandleFunc("DELETE /api/v1/previews/{id}", a.authed(a.handleDeletePreview))

	// Phase 3: notifications (notifications.md §7).
	mux.HandleFunc("POST /api/v1/projects/{id}/notifiers", a.authed(a.handleCreateNotifier))
	mux.HandleFunc("GET /api/v1/projects/{id}/notifiers", a.authed(a.handleListNotifiers))
	mux.HandleFunc("POST /api/v1/projects/{id}/notifiers/test", a.authed(a.handleTestNotifierConfig))
	mux.HandleFunc("GET /api/v1/notifiers/{id}", a.authed(a.handleGetNotifier))
	mux.HandleFunc("PATCH /api/v1/notifiers/{id}", a.authed(a.handlePatchNotifier))
	mux.HandleFunc("DELETE /api/v1/notifiers/{id}", a.authed(a.handleDeleteNotifier))
	mux.HandleFunc("POST /api/v1/notifiers/{id}/test", a.authed(a.handleTestNotifier))

	// Phase 4: outbound webhooks (outbound-webhooks.md §7). Notifiers talk to
	// people; these talk to machines. Nothing here triggers a deploy, so
	// deployRoutes is untouched: reads need `read`, mutations — including ping
	// and redeliver — need `write`.
	mux.HandleFunc("POST /api/v1/projects/{id}/webhook-endpoints", a.authed(a.handleCreateWebhookEndpoint))
	mux.HandleFunc("GET /api/v1/projects/{id}/webhook-endpoints", a.authed(a.handleListWebhookEndpoints))
	mux.HandleFunc("GET /api/v1/webhook-endpoints/{id}", a.authed(a.handleGetWebhookEndpoint))
	mux.HandleFunc("PATCH /api/v1/webhook-endpoints/{id}", a.authed(a.handlePatchWebhookEndpoint))
	mux.HandleFunc("DELETE /api/v1/webhook-endpoints/{id}", a.authed(a.handleDeleteWebhookEndpoint))
	mux.HandleFunc("POST /api/v1/webhook-endpoints/{id}/rotate-secret", a.authed(a.handleRotateWebhookSecret))
	mux.HandleFunc("POST /api/v1/webhook-endpoints/{id}/ping", a.authed(a.handlePingWebhookEndpoint))
	mux.HandleFunc("GET /api/v1/webhook-endpoints/{id}/deliveries", a.authed(a.handleListWebhookDeliveries))
	mux.HandleFunc("POST /api/v1/webhook-deliveries/{id}/redeliver", a.authed(a.handleRedeliverWebhookDelivery))

	// Phase 4: the notification inbox (notification-inbox.md §6). The
	// collection is `/inbox`, not `/users/{id}/inbox`: the inbox is always the
	// caller's, and the absence of an owner segment is what makes that
	// guarantee syntactic. These are NOT sessionOnly — a token acts as its
	// owner, so it reads and clears its owner's inbox and nobody else's, which
	// is not credential management.
	mux.HandleFunc("GET /api/v1/inbox", a.authed(a.handleListInbox))
	mux.HandleFunc("GET /api/v1/inbox/unread-count", a.authed(a.handleInboxUnreadCount))
	mux.HandleFunc("POST /api/v1/inbox/read-all", a.authed(a.handleMarkAllInboxRead))
	mux.HandleFunc("POST /api/v1/inbox/{id}/read", a.authed(a.handleMarkInboxItemRead))
	mux.HandleFunc("GET /api/v1/inbox/preferences", a.authed(a.handleGetInboxPreferences))
	mux.HandleFunc("PUT /api/v1/inbox/preferences", a.authed(a.handlePutInboxPreferences))

	// V1.x: the audit log (audit-log.md §7). Two reads and no writes — a row
	// is minted by the handler that performed the action, never by a request
	// to this collection, which is what makes the log evidence rather than a
	// place anyone can write to.
	//
	// Neither route carries a role gate: the SCOPE is the authorization. The
	// service resolves what the caller may see (their teams, plus panel-level
	// rows for a panel admin, plus their own actions wherever those landed) and
	// every query parameter narrows inside it. A team_id the caller does not
	// belong to therefore returns an empty page, not a 403 — the log must not
	// become a way to probe for the existence of another tenant.
	mux.HandleFunc("GET /api/v1/audit", a.authed(a.handleListAuditEvents))
	mux.HandleFunc("GET /api/v1/audit/{id}", a.authed(a.handleGetAuditEvent))

	// Phase 4: project shared variables (shared-variables.md §7). The
	// collection hangs off the project because that is the scope that owns
	// them; an environment-scoped variable is the same row with
	// environment_id set, not a second collection. Nothing here triggers a
	// deploy — a change is made VISIBLE as "redeploy to apply" rather than
	// auto-applied (§5) — so deployRoutes is untouched.
	mux.HandleFunc("POST /api/v1/projects/{id}/shared-variables", a.authed(a.handleCreateSharedVariable))
	mux.HandleFunc("GET /api/v1/projects/{id}/shared-variables", a.authed(a.handleListSharedVariables))
	mux.HandleFunc("GET /api/v1/shared-variables/{id}", a.authed(a.handleGetSharedVariable))
	mux.HandleFunc("PATCH /api/v1/shared-variables/{id}", a.authed(a.handlePatchSharedVariable))
	mux.HandleFunc("DELETE /api/v1/shared-variables/{id}", a.authed(a.handleDeleteSharedVariable))
	mux.HandleFunc("GET /api/v1/shared-variables/{id}/used-by", a.authed(a.handleListSharedVariableUsage))

	// V1.x: deploy protection (deploy-protection.md §6). Protection hangs off
	// the ENVIRONMENT because that is the unit an operator reasons about; the
	// two decision routes hang off the DEPLOYMENT because that is what is
	// parked.
	//
	// deployRoutes gains no entry, deliberately: approve triggers a rollout,
	// but no token ability can reach it — approve, reject and break glass are
	// sessionOnly. An API token inherits its owner's role, so a `deploy`-able
	// token in CI could otherwise approve the deploy it had just requested and
	// the gate would be decorative (§5, threat-model §5.8). The deploy routes
	// themselves are unchanged, so CI keeps working: its deploys park.
	//
	// The PUT is sessionOnly for the SAME reason, and with more force: a
	// `write`-ability token whose owner is a team admin could otherwise send
	// `{require_approval:false, freeze_enabled:false, windows:[]}` and then
	// deploy freely. Switching the whole control off is strictly more powerful
	// than the single 30-minute break-glass grant that is already session-only,
	// so it cannot be the one door a leaked CI token is left holding.
	mux.HandleFunc("GET /api/v1/environments/{id}/protection", a.authed(a.handleGetEnvironmentProtection))
	mux.HandleFunc("PUT /api/v1/environments/{id}/protection", a.sessionOnly(a.handleSetEnvironmentProtection))
	mux.HandleFunc("GET /api/v1/environments/{id}/approvals", a.authed(a.handleListDeployApprovals))
	mux.HandleFunc("GET /api/v1/environments/{id}/break-glass", a.authed(a.handleListBreakGlassGrants))
	mux.HandleFunc("POST /api/v1/environments/{id}/break-glass", a.sessionOnly(a.handleOpenBreakGlass))
	mux.HandleFunc("GET /api/v1/deployments/{id}/approval", a.authed(a.handleGetDeployApproval))
	mux.HandleFunc("POST /api/v1/deployments/{id}/approve", a.sessionOnly(a.handleApproveDeployment))
	mux.HandleFunc("POST /api/v1/deployments/{id}/reject", a.sessionOnly(a.handleRejectDeployment))

	// Phase 3: scheduled tasks (scheduled-tasks.md §7).
	mux.HandleFunc("POST /api/v1/applications/{id}/scheduled-tasks", a.authed(a.handleCreateScheduledTask))
	mux.HandleFunc("GET /api/v1/applications/{id}/scheduled-tasks", a.authed(a.handleListScheduledTasks))
	mux.HandleFunc("GET /api/v1/scheduled-tasks/{id}", a.authed(a.handleGetScheduledTask))
	mux.HandleFunc("PATCH /api/v1/scheduled-tasks/{id}", a.authed(a.handlePatchScheduledTask))
	mux.HandleFunc("DELETE /api/v1/scheduled-tasks/{id}", a.authed(a.handleDeleteScheduledTask))
	mux.HandleFunc("GET /api/v1/scheduled-tasks/{id}/runs", a.authed(a.handleListTaskRuns))

	// Phase 3: teams + roles (teams-and-roles.md §4).
	mux.HandleFunc("POST /api/v1/teams", a.authed(a.handleCreateTeam))
	mux.HandleFunc("GET /api/v1/teams", a.authed(a.handleListTeams))
	mux.HandleFunc("GET /api/v1/teams/{id}", a.authed(a.handleGetTeam))
	mux.HandleFunc("PATCH /api/v1/teams/{id}", a.authed(a.handleRenameTeam))
	mux.HandleFunc("DELETE /api/v1/teams/{id}", a.authed(a.handleDeleteTeam))
	mux.HandleFunc("GET /api/v1/teams/{id}/members", a.authed(a.handleListTeamMembers))
	mux.HandleFunc("POST /api/v1/teams/{id}/members", a.authed(a.handleAddTeamMember))
	mux.HandleFunc("PATCH /api/v1/teams/{id}/members/{uid}", a.authed(a.handleChangeTeamMemberRole))
	mux.HandleFunc("DELETE /api/v1/teams/{id}/members/{uid}", a.authed(a.handleRemoveTeamMember))
	// V1.x: invitations and access requests
	// (invitations-and-access-requests.md §7) — the two ways into a team from
	// outside it, beside the existing "an admin adds an account that already
	// exists".
	//
	// The two public routes are the only unauthenticated surface this feature
	// adds. They carry `security: []` in the spec, are gated by the
	// invitation's own bearer secret, are throttled by client address, and
	// answer one undifferentiated 404 for everything that is not currently
	// acceptable.
	//
	// grant and deny are sessionOnly, and for the reason deploy protection made
	// its decisions session-only: an API token inherits its owner's role, so a
	// leaked `write`-able token belonging to an owner could otherwise promote
	// an account to owner — durable, panel-wide privilege from one CI
	// credential (threat-model §5.8). Issuing an INVITATION is deliberately not
	// session-only: it grants nothing by itself, expires in 7 days, is
	// revocable, and scripting team setup from CI is legitimate.
	mux.HandleFunc("POST /api/v1/teams/{id}/invites", a.authed(a.handleCreateInvite))
	mux.HandleFunc("GET /api/v1/teams/{id}/invites", a.authed(a.handleListInvites))
	mux.HandleFunc("DELETE /api/v1/teams/{id}/invites/{inv}", a.authed(a.handleRevokeInvite))
	mux.HandleFunc("GET /api/v1/invites/{token}", a.handleGetInvite)
	mux.HandleFunc("POST /api/v1/invites/{token}/accept", a.handleAcceptInvite)
	mux.HandleFunc("POST /api/v1/teams/{id}/access-requests", a.authed(a.handleCreateAccessRequest))
	mux.HandleFunc("GET /api/v1/teams/{id}/access-requests", a.authed(a.handleListAccessRequests))
	mux.HandleFunc("POST /api/v1/access-requests/{id}/grant", a.sessionOnly(a.handleGrantAccessRequest))
	mux.HandleFunc("POST /api/v1/access-requests/{id}/deny", a.sessionOnly(a.handleDenyAccessRequest))

	// V1.x: container registry credentials (registries.md §7; ADR-008 path 3).
	//
	// Team-scoped rather than project-scoped, like deploy keys and backup
	// targets: one credential for ghcr.io serves every project the team runs,
	// and duplicating it per project would mean rotating it in n places.
	//
	// The test routes are POSTs because they make an outbound request the
	// caller chose the destination of — the notifier tests' precedent, and the
	// reason both are behind admin rank rather than membership.
	mux.HandleFunc("GET /api/v1/registries", a.authed(a.handleListRegistries))
	mux.HandleFunc("POST /api/v1/registries", a.authed(a.handleCreateRegistry))
	mux.HandleFunc("POST /api/v1/registries/test", a.authed(a.handleTestRegistryConfig))
	mux.HandleFunc("GET /api/v1/registries/{id}", a.authed(a.handleGetRegistry))
	mux.HandleFunc("PATCH /api/v1/registries/{id}", a.authed(a.handlePatchRegistry))
	mux.HandleFunc("DELETE /api/v1/registries/{id}", a.authed(a.handleDeleteRegistry))
	mux.HandleFunc("POST /api/v1/registries/{id}/test", a.authed(a.handleTestRegistry))
	mux.HandleFunc("GET /api/v1/registries/{id}/used-by", a.authed(a.handleRegistryUsedBy))

	// V1: Compose Stacks (compose-stacks.md §7). Writing the FILE is team
	// admin; deploying one is a member — a compose file can ask for privileged
	// containers and host mounts, which an Application cannot express, so it
	// must not be reachable at the rank that deploys an application.
	mux.HandleFunc("GET /api/v1/environments/{id}/compose-stacks", a.authed(a.handleListComposeStacks))
	mux.HandleFunc("POST /api/v1/environments/{id}/compose-stacks", a.authed(a.handleCreateComposeStack))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}", a.authed(a.handleGetComposeStack))
	mux.HandleFunc("PATCH /api/v1/compose-stacks/{id}", a.authed(a.handlePatchComposeStack))
	mux.HandleFunc("DELETE /api/v1/compose-stacks/{id}", a.authed(a.handleDeleteComposeStack))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/file", a.authed(a.handleGetComposeFile))
	mux.HandleFunc("POST /api/v1/compose-stacks/{id}/deploy", a.authed(a.handleDeployComposeStack))
	mux.HandleFunc("POST /api/v1/compose-stacks/{id}/rollback", a.authed(a.handleRollbackComposeStack))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/revisions", a.authed(a.handleListComposeRevisions))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/logs", a.authed(a.handleComposeStackLogs))
	mux.HandleFunc("GET /api/v1/compose-stacks/{id}/env", a.authed(a.handleListComposeEnvVars))
	mux.HandleFunc("PUT /api/v1/compose-stacks/{id}/env/{key}", a.authed(a.handleSetComposeEnvVar))
	mux.HandleFunc("DELETE /api/v1/compose-stacks/{id}/env/{key}", a.authed(a.handleDeleteComposeEnvVar))

	mux.HandleFunc("POST /api/v1/users", a.authed(a.handleCreateUser))
	mux.HandleFunc("GET /api/v1/users", a.authed(a.handleListUsers))
	mux.HandleFunc("PATCH /api/v1/users/{id}", a.authed(a.handleSetUserRole))
	mux.HandleFunc("DELETE /api/v1/users/{id}", a.authed(a.handleDeleteUser))

	// The embedded web app, with the SPA fallback for client routes. Unknown
	// /api/* paths must stay JSON 404s, never index.html.
	app, err := webui.Handler()
	if err != nil {
		// Programmer-error invariant: the embedded dist is compiled in.
		panic("webui: embedded app unavailable: " + err.Error())
	}
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		app.ServeHTTP(w, r)
	}))

	// Outermost first: every response gets a trace id before anything can
	// fail, the log line sees the final status (a recovered panic included),
	// and the recoverer sits innermost so it can still write the envelope
	// (control-plane-hardening.md §2).
	return a.requestID(a.logRequests(a.securityHeaders(a.recoverer(mux))))
}

// ─── middleware ─────────────────────────────────────────────────────────────

type ctxKey int

const (
	principalKey ctxKey = iota
	rawTokenKey
	traceIDKey
)

// authed wraps a handler so it runs only for an authenticated caller, whose
// principal it places in the request context. For a personal access token the
// request must also be within the token's abilities (feature-matrix V1):
// safe methods need `read`, deploy triggers need `deploy`, and every other
// mutation needs `write`. Sessions hold the full set, so interactive use is
// unchanged.
func (a *API) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		principal, err := a.deps.Auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		if need := requiredAbility(r); !principal.Can(need) {
			writeError(w, http.StatusForbidden, "this token lacks the "+string(need)+" ability")
			return
		}
		// A project-scoped token is refused panel- and team-level routes
		// outright. Resources that belong to a project are checked where the
		// project is resolved (authz.go), because only there is it known.
		if _, scoped := principal.ScopedToProject(); scoped && outsideProjectScope(r.URL.Path) {
			writeError(w, http.StatusForbidden, "this token is scoped to one project and cannot reach panel-wide routes")
			return
		}
		// The upgrade read-only lock. Reads and SSE continue; anything that
		// mutates answers 503 with a Retry-After, so a deploy submitted while
		// the plane is being replaced is refused clearly rather than half
		// applied across a restart (panel-updates.md §6).
		if a.upgradeLocked(r) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusServiceUnavailable,
				"The panel is upgrading and is read-only for about a minute. Your applications keep serving — they do not depend on the control plane.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, principal)
		ctx = context.WithValue(ctx, rawTokenKey, token)
		next(w, r.WithContext(ctx))
	}
}

// upgradeLocked reports whether this request must be refused because a guided
// upgrade holds the lock.
//
// Only mutating methods are refused, and the upgrade's OWN routes are always
// allowed: an operator watching the progress screen must be able to read status
// and to cancel, and locking them out of the thing they are watching would be
// the worst possible moment to do it.
func (a *API) upgradeLocked(r *http.Request) bool {
	if a.deps.Upgrades == nil {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/panel/updates") {
		return false
	}
	return a.deps.Upgrades.Locked(r.Context())
}

// sessionOnly further restricts a route to interactive sessions. Credential
// management — minting tokens, revoking sessions, turning two-factor off — is
// exactly how a leaked API token would be escalated into durable account
// takeover, so a token may never reach these routes no matter what abilities it
// holds (threat-model §5.8).
func (a *API) sessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return a.authed(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFromContext(r.Context())
		if !ok || p.Kind != auth.KindSession {
			writeError(w, http.StatusForbidden, "this action requires an interactive session, not an API token")
			return
		}
		next(w, r)
	})
}

// deployRoutes are the ServeMux patterns whose handler triggers a rollout,
// matched in full — method and route — not by URL suffix. Suffix matching is
// unsafe here: `PUT /api/v1/applications/{id}/env/{key}` with a variable named
// `deploy` ends in the same segment, which would let a deploy-only token write
// application configuration it has no `write` ability for. An explicit table of
// whole patterns also means a new deploy-shaped route must be added here
// deliberately, never inherit an ability by accident.
var deployRoutes = map[string]bool{
	"POST /api/v1/applications/{id}/deploy":     true,
	"POST /api/v1/deployments/{id}/rollback":    true,
	"POST /api/v1/deployments/{id}/cancel":      true,
	"POST /api/v1/applications/{id}/restart":    true,
	"POST /api/v1/compose-stacks/{id}/deploy":   true,
	"POST /api/v1/compose-stacks/{id}/rollback": true,
}

// envRoutes, serverRoutes and adminRoutes carve narrower grants out of `write`,
// listed the same way and for the same reason: a whole pattern, so a new route
// joins a grant deliberately rather than by resembling one.
//
// `write` still satisfies every one of them (domain.Ability.Implies), so a
// token issued before these existed does exactly what it did before. They are
// here so a NEW token can be minted for one job — a CI credential that sets env
// vars, a provisioning script that enrols servers — instead of for every
// mutation the API has.
var envRoutes = map[string]bool{
	"PUT /api/v1/applications/{id}/env/{key}":      true,
	"DELETE /api/v1/applications/{id}/env/{key}":   true,
	"PUT /api/v1/compose-stacks/{id}/env/{key}":    true,
	"DELETE /api/v1/compose-stacks/{id}/env/{key}": true,
}

var serverRoutes = map[string]bool{
	"POST /api/v1/servers":        true,
	"DELETE /api/v1/servers/{id}": true,
	"PATCH /api/v1/servers/{id}":  true,
}

// adminRoutes change who can reach the panel and how it behaves. Most are
// session-only already; the ability exists so the few that a token may reach
// are refused to one that was not minted for administration.
var adminRoutes = map[string]bool{
	"POST /api/v1/teams":                      true,
	"PATCH /api/v1/teams/{id}":                true,
	"DELETE /api/v1/teams/{id}":               true,
	"POST /api/v1/teams/{id}/members":         true,
	"PATCH /api/v1/teams/{id}/members/{uid}":  true,
	"DELETE /api/v1/teams/{id}/members/{uid}": true,
	"POST /api/v1/teams/{id}/invites":         true,
	"DELETE /api/v1/teams/{id}/invites/{inv}": true,
	"POST /api/v1/access-requests/{id}/grant": true,
	"POST /api/v1/access-requests/{id}/deny":  true,
	"POST /api/v1/users":                      true,
	"PATCH /api/v1/users/{id}":                true,
	"DELETE /api/v1/users/{id}":               true,
	"PUT /api/v1/panel/mail":                  true,
	"DELETE /api/v1/panel/mail":               true,
	"PUT /api/v1/panel/dns":                   true,
	"DELETE /api/v1/panel/dns":                true,
	"PUT /api/v1/panel/tls":                   true,
	// A compose file can ask for privileged containers and host mounts, which
	// is root on the node — writing one is administration (compose-stacks.md §7).
	"POST /api/v1/environments/{id}/compose-stacks": true,
	"PATCH /api/v1/compose-stacks/{id}":             true,
	"DELETE /api/v1/compose-stacks/{id}":            true,
	// A registry credential can pull and push images for every project the
	// team runs; minting one is administration, not deployment.
	"POST /api/v1/registries":           true,
	"PATCH /api/v1/registries/{id}":     true,
	"DELETE /api/v1/registries/{id}":    true,
	"POST /api/v1/registries/test":      true,
	"POST /api/v1/registries/{id}/test": true,
}

// requiredAbility maps a request to the ability a token must carry for it.
// r.Pattern is the route ServeMux matched; when it is empty (a handler invoked
// outside the mux) the safe default applies — a mutation needs `write`, which
// no narrow credential holds.
func requiredAbility(r *http.Request) domain.Ability {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// Reads stay on `read`, including reads of env keys and servers. Making
		// them need the narrow ability would strip listings from every
		// read-only token already issued.
		return domain.AbilityRead
	}
	switch {
	case deployRoutes[r.Pattern]:
		return domain.AbilityDeploy
	case envRoutes[r.Pattern]:
		return domain.AbilityEnv
	case serverRoutes[r.Pattern]:
		return domain.AbilityServers
	case adminRoutes[r.Pattern]:
		return domain.AbilityAdmin
	}
	return domain.AbilityWrite
}

// panelScopeRoutes are the route prefixes a project-scoped token may never
// reach, whatever abilities it holds: they are about the panel or a team rather
// than about one project's resources, so "which project?" has no answer for
// them. Everything else resolves to a project and is checked against the scope
// where that resolution already happens (authz.go).
var panelScopePrefixes = []string{
	"/api/v1/teams",
	"/api/v1/users",
	"/api/v1/panel/",
	"/api/v1/servers",
	"/api/v1/backup-targets",
	"/api/v1/deploy-keys",
	"/api/v1/registries",
	"/api/v1/audit",
	"/api/v1/invites",
	"/api/v1/access-requests",
}

// outsideProjectScope reports whether a project-scoped credential is reaching
// for something that is not any one project's.
func outsideProjectScope(path string) bool {
	for _, p := range panelScopePrefixes {
		if path == strings.TrimSuffix(p, "/") || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// ─── helpers ────────────────────────────────────────────────────────────────

// userFromContext returns the authenticated caller's user record. Handlers that
// only care about identity (the overwhelming majority) use this; those that
// care how the caller authenticated use principalFromContext.
func userFromContext(ctx context.Context) (domain.User, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p.User, ok
}

func principalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p, ok
}

// rawTokenFromContext returns the bearer token the caller presented. Only the
// session-management handlers need it, to identify "this device" without
// trusting a client-supplied id.
func rawTokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(rawTokenKey).(string)
	return t
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// errorBody is the one fault envelope every non-2xx answer uses. TraceID is
// the response's X-Request-Id, repeated in the body so a screenshot of a 500
// carries it (canvas 13s); RetryAfterSeconds appears only on a 429, where it
// is the countdown the sign-in screen shows (canvas 13t). Both are optional —
// rule 17: additive only.
type errorBody struct {
	Error             string `json:"error"`
	TraceID           string `json:"trace_id,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	// TOTPRequired appears only on the 401 that means "the password was right,
	// now send the code": sign-in, and accepting an invitation for an address
	// that already has a 2FA-enabled account. It tells the client to prompt for
	// the second factor rather than for the password again.
	TOTPRequired bool `json:"totp_required,omitempty"`
}

// writeError answers with the fault envelope, carrying the trace id the
// request-id middleware already stamped on the response headers.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg, TraceID: w.Header().Get(TraceIDHeader)})
}

// rateLimited answers 429 with how long to wait — the standard Retry-After
// header and the same number in the body, so a client counts down instead of
// guessing (control-plane-hardening.md §5). The delay comes from
// *auth.RateLimitedError; an error that carries none is one second, never zero,
// because "wait 0" is not a throttle.
func rateLimited(w http.ResponseWriter, err error, msg string) {
	secs := 1
	var rl *auth.RateLimitedError
	if errors.As(err, &rl) {
		secs = rl.RetryAfterSeconds()
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, errorBody{
		Error:             msg,
		TraceID:           w.Header().Get(TraceIDHeader),
		RetryAfterSeconds: secs,
	})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

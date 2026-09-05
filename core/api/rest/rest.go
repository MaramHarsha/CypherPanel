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
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/api/rest/webui"
	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/templates"
	"github.com/MaramHarsha/cypherpanel/core/webhooks"
)

// Pinger is the readiness dependency (the store).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deployer starts pipelines and publishes desired absence (consumer-defined;
// *scheduler.Scheduler satisfies it).
type Deployer interface {
	Deploy(ctx context.Context, appID, trigger, ref string) (domain.Deployment, error)
	Rollback(ctx context.Context, deploymentID string) (domain.Deployment, error)
	RemoveApp(ctx context.Context, serverID, appID string) error
}

// DeploymentReader reads deployment records (consumer-defined; *store.Store
// satisfies it).
type DeploymentReader interface {
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	ListDeploymentsByApplication(ctx context.Context, appID string, limit int32) ([]domain.Deployment, error)
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
	RunRestore(ctx context.Context, dbID, backupRecordID string, confirm bool) error
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

// NotifierDelivery sends a synthetic event through one notifier — the test
// endpoint (consumer-defined; *notify.Manager satisfies it).
type NotifierDelivery interface {
	Deliver(ctx context.Context, n domain.Notifier, ev domain.NotifyEvent) error
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
	SubscribeLogs(ctx context.Context, subject string, handle func(data []byte)) (stop func(), err error)
	SubscribeRuntimeLogs(ctx context.Context, subject string, handle func(data []byte)) (stop func(), err error)
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
}

type Deps struct {
	Auth             *auth.Authenticator
	Onboarding       OnboardingService
	Servers          *servers.Service
	Projects         *projects.Service
	Applications     *applications.Service
	DeployKeys       *deploykeys.Service
	Databases        *databases.Service
	BackupTargets    *databases.BackupTargetService
	BackupSchedules  *databases.BackupScheduleService
	Backups          BackupOps
	Previews         PreviewManager
	Notifiers        NotifierService
	NotifyDelivery   NotifierDelivery
	WebhookEndpoints WebhookEndpointService
	Inbox            InboxService
	SharedVariables  SharedVariableService
	ScheduledTasks   ScheduledTaskService
	Templates        *templates.Service
	Teams            TeamService
	Mail             MailService
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
	Scheduler       Deployer
	Deployments     DeploymentReader
	Opener          Opener
	Pinger          Pinger
	CACertPEM       []byte
	EnrollAddr      string // advertised gRPC enrollment address (host:port)
	NATSURL         string // advertised data-plane URL
	Logs            LogSubscriber
	ConsoleURL      string // advertised HTTP base URL (installer + CA fetch)
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
	Log       *slog.Logger
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
	mux.HandleFunc("GET /api/v1/panel/dns/zones", a.authed(a.handleListDNSZones))
	mux.HandleFunc("POST /api/v1/panel/dns/zones/refresh", a.authed(a.handleRefreshDNSZones))

	mux.HandleFunc("POST /api/v1/backup-targets", a.authed(a.handleCreateBackupTarget))
	mux.HandleFunc("GET /api/v1/backup-targets", a.authed(a.handleListBackupTargets))
	mux.HandleFunc("GET /api/v1/backup-targets/{id}", a.authed(a.handleGetBackupTarget))
	mux.HandleFunc("DELETE /api/v1/backup-targets/{id}", a.authed(a.handleDeleteBackupTarget))

	mux.HandleFunc("POST /api/v1/databases/{id}/backups", a.authed(a.handleCreateDatabaseBackup))
	mux.HandleFunc("GET /api/v1/databases/{id}/backups", a.authed(a.handleListDatabaseBackups))
	mux.HandleFunc("DELETE /api/v1/databases/{id}/backups/{bak_id}", a.authed(a.handleDeleteDatabaseBackup))
	mux.HandleFunc("GET /api/v1/databases/{id}/backups/{bak_id}/history", a.authed(a.handleListBackupRecords))
	mux.HandleFunc("POST /api/v1/databases/{id}/backups/{bak_id}/run", a.authed(a.handleRunBackup))
	mux.HandleFunc("POST /api/v1/databases/{id}/restore", a.authed(a.handleRestoreDatabase))

	// Phase 3: preview environments (preview-environments.md §7).
	mux.HandleFunc("GET /api/v1/applications/{id}/previews", a.authed(a.handleListPreviews))
	mux.HandleFunc("GET /api/v1/previews/{id}", a.authed(a.handleGetPreview))
	mux.HandleFunc("DELETE /api/v1/previews/{id}", a.authed(a.handleDeletePreview))

	// Phase 3: notifications (notifications.md §7).
	mux.HandleFunc("POST /api/v1/projects/{id}/notifiers", a.authed(a.handleCreateNotifier))
	mux.HandleFunc("GET /api/v1/projects/{id}/notifiers", a.authed(a.handleListNotifiers))
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
		ctx := context.WithValue(r.Context(), principalKey, principal)
		ctx = context.WithValue(ctx, rawTokenKey, token)
		next(w, r.WithContext(ctx))
	}
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
	"POST /api/v1/applications/{id}/deploy":  true,
	"POST /api/v1/deployments/{id}/rollback": true,
}

// requiredAbility maps a request to the ability a token must carry for it.
// r.Pattern is the route ServeMux matched; when it is empty (a handler invoked
// outside the mux) the safe default applies — a mutation needs `write`, which
// no deploy-only credential holds.
func requiredAbility(r *http.Request) domain.Ability {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return domain.AbilityRead
	}
	if deployRoutes[r.Pattern] {
		return domain.AbilityDeploy
	}
	return domain.AbilityWrite
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

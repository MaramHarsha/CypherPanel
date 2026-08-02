// Package rest is the human/UI-facing HTTP API; it also serves the embedded
// web application (webui). It is API-first (vision.md non-negotiable 3): every
// action here is a plain REST call the web UI makes with a bearer token. All
// responses use glossary vocabulary and mask secrets by default (ENGINEERING
// rules 5, 20).
package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/api/rest/webui"
	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/servers"
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
	Previews        PreviewManager
	Notifiers       NotifierService
	NotifyDelivery  NotifierDelivery
	ScheduledTasks  ScheduledTaskService
	Teams           TeamService
	Scheduler       Deployer
	Deployments     DeploymentReader
	Opener          Opener
	Pinger          Pinger
	CACertPEM       []byte
	EnrollAddr      string // advertised gRPC enrollment address (host:port)
	NATSURL         string // advertised data-plane URL
	Logs            LogSubscriber
	ConsoleURL      string // advertised HTTP base URL (installer + CA fetch)
	Log             *slog.Logger
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

	// Personal access tokens (scoped API tokens for CI/automation). A token
	// authenticates as its owning user, inheriting that user's authorization.
	mux.HandleFunc("POST /api/v1/tokens", a.authed(a.handleCreateToken))
	mux.HandleFunc("GET /api/v1/tokens", a.authed(a.handleListTokens))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", a.authed(a.handleDeleteToken))

	// Two-factor authentication (TOTP + recovery codes).
	mux.HandleFunc("GET /api/v1/auth/totp", a.authed(a.handleTOTPStatus))
	mux.HandleFunc("POST /api/v1/auth/totp/enroll", a.authed(a.handleTOTPEnroll))
	mux.HandleFunc("POST /api/v1/auth/totp/verify", a.authed(a.handleTOTPVerify))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", a.authed(a.handleTOTPDisable))

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

	// Applications (created + listed under an environment; addressed by app id).
	mux.HandleFunc("POST /api/v1/environments/{id}/applications", a.authed(a.handleCreateApplication))
	mux.HandleFunc("GET /api/v1/environments/{id}/applications", a.authed(a.handleListApplications))
	mux.HandleFunc("GET /api/v1/applications/{id}", a.authed(a.handleGetApplication))
	mux.HandleFunc("PATCH /api/v1/applications/{id}", a.authed(a.handlePatchApplication))
	mux.HandleFunc("DELETE /api/v1/applications/{id}", a.authed(a.handleDeleteApplication))
	mux.HandleFunc("GET /api/v1/applications/{id}/domain-check", a.authed(a.handleCheckApplicationDomain))
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

	return a.recoverer(a.securityHeaders(a.logRequests(mux)))
}

// ─── middleware ─────────────────────────────────────────────────────────────

type ctxKey int

const userKey ctxKey = iota

// authed wraps a handler so it runs only for an authenticated user, which it
// places in the request context.
func (a *API) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, err := a.deps.Auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		next(w, r.WithContext(ctx))
	}
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		a.deps.Log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.deps.Log.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so the SSE log endpoints can stream:
// embedding http.ResponseWriter does not promote Flush (it is not part of that
// interface), so without this the http.Flusher assertion in a streaming
// handler would fail and event streaming would be silently unavailable.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func userFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userKey).(domain.User)
	return u, ok
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

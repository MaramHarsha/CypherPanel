// Command cypherd is the CypherPanel control plane: a single static binary that
// serves the REST API + web console, runs the embedded NATS JetStream bus, and
// hosts the gRPC enrollment endpoint. Its only external dependency is
// PostgreSQL (ADR-001). It stores no server credentials (ADR-002).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// The embedded IANA time-zone database (~450 KB). cypherd is a static
	// binary (ADR-001) that can land on an image with no /usr/share/zoneinfo,
	// and two features resolve operator-supplied zone names at runtime: deploy
	// protection's freeze windows (deploy-protection.md §4) and the profile
	// timezone in core/auth. Without this, time.LoadLocation would fail on such
	// an image — refusing every deploy on a frozen environment, since the gate
	// fails closed. Negligible against vision.md's <300 MB plane budget.
	_ "time/tzdata"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/access"
	grpcapi "github.com/MaramHarsha/cypherpanel/core/api/grpc"
	"github.com/MaramHarsha/cypherpanel/core/api/rest"
	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/bus"
	"github.com/MaramHarsha/cypherpanel/core/compose"
	"github.com/MaramHarsha/cypherpanel/core/config"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/dns"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	"github.com/MaramHarsha/cypherpanel/core/guard"
	"github.com/MaramHarsha/cypherpanel/core/identity"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/logring"
	"github.com/MaramHarsha/cypherpanel/core/mail"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/onboarding"
	"github.com/MaramHarsha/cypherpanel/core/paneltls"
	"github.com/MaramHarsha/cypherpanel/core/previews"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/protection"
	"github.com/MaramHarsha/cypherpanel/core/registries"
	"github.com/MaramHarsha/cypherpanel/core/relay"
	"github.com/MaramHarsha/cypherpanel/core/scheduledtasks"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/sharedvars"
	"github.com/MaramHarsha/cypherpanel/core/status"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/teams"
	"github.com/MaramHarsha/cypherpanel/core/templates"
	"github.com/MaramHarsha/cypherpanel/core/updates"
	"github.com/MaramHarsha/cypherpanel/core/webhooks"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Build stamps, set at link time with -ldflags "-X main.version=... -X
// main.commit=... -X main.buildDate=..." (release.yml, Dockerfile). "dev" is
// what a local build reports, and what tells the update check there is nothing
// to compare against (control-plane-hardening.md §3).
var (
	version   = "dev"
	commit    = "dev"
	buildDate = ""
)

// panelLogLines is how many of the panel's own log lines stay in memory for
// GET /api/v1/panel/logs. 500 short lines is well under a megabyte, which the
// footprint budget (vision.md) will not notice.
const panelLogLines = 500

// sessionPurgeInterval is how often expired sessions are swept. Hourly: an
// expired row is already unusable, so the only cost of leaving one a while
// longer is a row, and a tighter loop would query for nothing all day
// (control-plane-hardening.md §7).
const sessionPurgeInterval = time.Hour

// auditPurgeInterval is how often the audit-log retention sweep runs. Hourly,
// like the session purge and for the same reason: the horizon is measured in
// days (CYPHERD_AUDIT_RETENTION, 90d by default), so a tighter loop would query
// for nothing all day, and each sweep drains in bounded batches anyway
// (audit-log.md §8).
const auditPurgeInterval = time.Hour

func main() {
	// The panel's log has two audiences: stderr, where the operator's
	// journal/docker logs collect it, and a bounded in-memory ring the owner
	// can read back through the API without a shell (§4). One pipeline, so
	// both see exactly the same records.
	ring := logring.New(panelLogLines)
	log := slog.New(logring.Fanout(
		slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		ring.Handler(&slog.HandlerOptions{Level: slog.LevelInfo}),
	))
	if len(os.Args) > 1 && os.Args[1] == "version" {
		printVersion()
		return
	}
	if err := run(log, ring); err != nil {
		log.Error("cypherd exited", "error", err)
		os.Exit(1)
	}
}

// printVersion mirrors `cypher-agent version`: the three build stamps and the
// toolchain, on stdout, for an operator pasting into a bug report.
func printVersion() {
	info := updates.RuntimeInfo(version, commit, buildDate)
	built := "unknown"
	if !info.BuiltAt.IsZero() {
		built = info.BuiltAt.Format(time.RFC3339)
	}
	fmt.Printf("cypherd %s (commit %s, built %s, %s)\n", info.Version, info.Commit, built, info.GoVersion)
}

func run(log *slog.Logger, panelLogs *logring.Ring) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("starting cypherd", "version", version, "commit", commit, "public_host", cfg.PublicHost, "console_url", cfg.AdvertisedConsoleURL())

	// Self-protection: refuse to boot into a nearly-full disk rather than run
	// until Postgres can no longer write (threat-model §8 req 10).
	if err := guard.CheckDiskHeadroom(".", cfg.MinDiskFree, guard.FreeBytes); err != nil {
		if errors.Is(err, guard.ErrUnsupported) {
			log.Warn("disk headroom check unsupported on this platform", "error", err)
		} else {
			return err
		}
	}

	// Migrate before opening the pool so the schema is present.
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	box, err := secret.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}

	ca, err := identity.LoadOrCreateCA(ctx, st, box, time.Now())
	if err != nil {
		return err
	}

	onboardSvc := onboarding.New(st)
	if err := bootstrapAdmin(ctx, onboardSvc, cfg, log); err != nil {
		return err
	}

	// Issue the plane's own server certificate for its TLS listeners.
	dnsNames, ips := planeSANs(cfg.PublicHost)
	planeCert, planeKey, err := ca.IssueServerCert(dnsNames, ips, 365*24*time.Hour, time.Now())
	if err != nil {
		return fmt.Errorf("issuing plane server cert: %w", err)
	}

	// The file-backed WORK stream needs a writable data directory. Create it
	// up front so a missing/unwritable path fails with a clear message here
	// rather than surfacing later as an opaque "NATS server not ready".
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir %q (set CYPHERD_DATA_DIR to a writable path): %w", cfg.DataDir, err)
	}

	// Embedded NATS JetStream bus (mTLS, per-agent authz).
	busTLS, err := pki.ServerTLSConfig(planeCert, planeKey, ca.CertPEM())
	if err != nil {
		return fmt.Errorf("building bus TLS: %w", err)
	}
	b, err := bus.Start(ctx, bus.Options{
		ListenAddr:          cfg.NATSAddr,
		TLSConfig:           busTLS,
		Authorizer:          st,
		Log:                 log.With("component", "bus"),
		StoreDir:            cfg.DataDir,
		RuntimeLogsMaxAge:   cfg.RuntimeLogsMaxAge,
		RuntimeLogsMaxBytes: int64(cfg.RuntimeLogsMaxBytes),
	})
	if err != nil {
		return err
	}
	defer b.Close()
	log.Info("event bus ready", "addr", cfg.NATSAddr, "advertised", cfg.AdvertisedNATSURL())

	// Heartbeats → observed state.
	recorder := status.NewRecorder(st, log)
	consume, err := b.ConsumeHeartbeats(ctx, func(data []byte) {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		recorder.Record(c, data)
	})
	if err != nil {
		return err
	}
	defer consume.Stop()

	// Stale sweeper.
	var wg sync.WaitGroup
	sweeper := status.NewSweeper(st, cfg.HeartbeatStale, cfg.SweepInterval, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweeper.Run(ctx)
	}()

	// Services.
	enrollSvc := enroll.NewService(st, ca, cfg.AgentCertTTL, cfg.AdvertisedNATSURL())
	serverSvc := servers.NewService(st, b, cfg.JoinTokenTTL, log)
	projectSvc := projects.NewService(st)
	appSvc := applications.NewService(st, box)
	deployKeySvc := deploykeys.NewService(st, box)
	// Container registry credentials (registries.md; ADR-008 path 3). Nothing
	// in the deploy path requires one — this is for pulling a private base
	// image and pushing builds somewhere the operator already runs.
	registrySvc := registries.NewService(st, box)
	teamSvc := teams.NewService(st)
	// Two throttle dimensions on sign-in: per client address (5 failures / 15
	// min) and per account (10 / 15 min, derived). One attacker behind a shared
	// proxy therefore cannot lock every account out, and a distributed guess at
	// one account is still bounded (control-plane-hardening.md §5).
	authr := auth.NewAuthenticator(st, box, auth.NewLimiter(5, 15*time.Minute), cfg.SessionTTL)

	// Deploy pipeline: the scheduler publishes work items and advances
	// deployments from the agents' observed reports (ADR-005).
	sched := scheduler.New(st, b, box, log)

	// DNS automation (dns-automation.md). The verifier is what gates routing on
	// domain ownership; with no provider configured it reports "not enforced"
	// and every domain routes, exactly as before this feature existed.
	dnsSvc := dns.New(st, box)
	// Compose Stacks: the file is the desired state, and the scheduler is what
	// publishes it (compose-stacks.md §4).
	composeSvc := compose.NewService(st, box, sched)
	sched.SetDomainVerifier(dnsSvc)
	// Private-registry credentials are unsealed per work item, never cached, so
	// rotating one takes effect on the next deploy (registries.md §5).
	sched.SetRegistries(registrySvc)
	// How many of an application's images a node keeps: the whole
	// garbage-collection policy, converged to rather than swept for
	// (disk-management.md §2).
	sched.SetRevisionRetain(cfg.RevisionRetain)

	// The panel's ACME account (agent-identity-and-tls.md §4): one setting,
	// carried to every node in its desired state. The scheduler is the fleet
	// seam — a settings change nudges every enrolled server to re-read desired
	// state instead of waiting for its next reconnect.
	panelTLS := paneltls.NewService(st, sched, log.With("component", "paneltls"))

	// The audit log: one immutable row per sensitive action, written by the
	// handler that performed it and queryable per team (audit-log.md). It is a
	// dependency of the REST layer and of gRPC enrollment, and nothing else —
	// no bus subject, no agent path, no reconciler.
	auditSvc := audit.NewService(st, cfg.AuditRetention, log.With("component", "audit"))

	// The notification inbox: the same observed outcomes, persisted per user and
	// counted on a bell (notification-inbox.md). It is the one channel that
	// needs no configuration, no webhook and no secret, so it hangs off the
	// notify fan-out rather than adding a second event source — and its write
	// runs BEFORE the notifier lookup, which is what makes the bell work on a
	// panel with no channels configured at all.
	inboxSvc := inbox.New(st, log)
	// Disk alerting, once the inbox exists to receive it. A server belongs to
	// no project, so the panel's owners and admins are the audience
	// (disk-management.md §5). Zero percent disables it.
	recorder.WatchDisk(cfg.DiskWarnPercent, serverDiskAnnouncer{inbox: inboxSvc})

	// Notifications: terminal deploy/backup outcomes fan out to a project's
	// configured channels (notifications.md). Delivery is best-effort and
	// detached, so it never blocks the pipeline.
	notifySvc := notify.NewService(st, box)
	notifyMgr := notify.New(st, box, log, inboxSvc)
	sched.AddSink(notifyMgr)

	// Outbound webhooks: the same terminal outcomes, POSTed as signed JSON to
	// the operator's own systems, with a delivery id, bounded retry and a
	// per-attempt record (outbound-webhooks.md). A second sink on the same
	// seam — delivery is detached, so it never blocks the pipeline.
	webhookMgr := webhooks.New(st, box, log)
	webhookSvc := webhooks.NewService(st, box, webhookMgr)
	sched.AddSink(webhookMgr)

	// Project shared variables: one sealed value defined once per project (or
	// per environment), referenced from any application's env vars as
	// {{shared.KEY}} (shared-variables.md). It adds no path to the agent — the
	// expansion happens inside the scheduler's existing sealed-env assembly —
	// so this service is CRUD plus the used-by and drift read models.
	sharedVarSvc := sharedvars.NewService(st, box)

	// Panel Mail: one transport owned by the panel, used by the account mail
	// the panel itself must send (panel-mail.md). Constructed here rather than
	// inline in the REST deps because invitations and access requests send
	// through it too.
	mailSvc := mail.New(st, box)

	// Team invitations and access requests: the two ways into a team from
	// outside it (invitations-and-access-requests.md). Neither adds desired
	// state, a subject or an agent path — they are authorization records, so
	// this is service wiring and nothing else.
	//
	// The invitation limiter throttles the two PUBLIC routes by client address:
	// 10 failed lookups in 15 minutes, twice sign-in's budget because a
	// legitimate invitee retrying a link they mistyped is not an attack, and a
	// guess still has to find ~130 bits of secret.
	inviteSvc := access.NewInvites(st, authr, mailSvc, inboxSvc,
		auth.NewLimiter(10, 15*time.Minute), cfg.AdvertisedConsoleURL(),
		log.With("component", "invites"))
	accessRequestSvc := access.NewRequests(st, teamSvc, mailSvc, inboxSvc,
		log.With("component", "access-requests"))

	// Deploy protection: an Environment declares who must approve a deploy
	// there and when deploys are refused outright (deploy-protection.md). The
	// gate is consulted once, where a Deployment is born, and BEFORE any work
	// item is published — so it adds no path to the agent and no NATS subject.
	// The two directions are wired separately on purpose: the scheduler asks
	// the gate whether to admit, and the gate asks the scheduler to release or
	// end what it parked, so the pipeline keeps its single owner and its lock.
	protectionSvc := protection.New(st, sched, inboxSvc, log.With("component", "protection"))
	sched.SetGate(protectionSvc)

	// Scheduled tasks: cron declared on an app, run by the agent in the app's
	// own container (scheduled-tasks.md, ADR-011). CRUD converges via the
	// scheduler; run observations flow back on state.<server>.task.
	scheduledTaskSvc := scheduledtasks.NewService(st, sched, log)

	dbSvc := databases.NewService(st, box, sched)
	backupTargetSvc := databases.NewBackupTargetService(st, box)
	backupScheduleSvc := databases.NewBackupScheduleService(st)
	templateSvc, err := templates.New(appSvc, dbSvc, sched, log.With("component", "templates"))
	if err != nil {
		return err
	}

	// Preview environments: PR events (via the app webhook) spawn/destroy
	// templated child environments; a sweeper reclaims any past their TTL
	// (preview-environments.md).
	previewMgr := previews.New(st, appSvc, sched, log, previews.WithAudit(auditSvc))
	wg.Add(1)
	go func() {
		defer wg.Done()
		previewMgr.RunSweeper(ctx, cfg.SweepInterval)
	}()

	// Scheduled database backups: the plane-side cron evaluator fires each
	// enabled schedule when its next run is due (managed-databases.md §7).
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.RunBackupSweeper(ctx, cfg.SweepInterval)
	}()

	// Outbound webhook retries: next_attempt_at lives in Postgres and the
	// delivery row is written before the first attempt, so a plane restart
	// mid-backoff loses nothing (outbound-webhooks.md §4).
	wg.Add(1)
	go func() {
		defer wg.Done()
		webhookMgr.RunRetrySweeper(ctx, cfg.SweepInterval)
	}()

	// DNS convergence: creates the records a verified domain needs, and — the
	// half that actually leaks if it is missed — deletes the ones whose
	// application, environment or project is gone (dns-automation.md §4.4).
	wg.Add(1)
	go func() {
		defer wg.Done()
		dnsSvc.RunSweeper(ctx, cfg.SweepInterval, log)
	}()

	// Expired sessions were invisible but never deleted, so the table grew by a
	// row per sign-in forever (control-plane-hardening.md §7). One owned
	// goroutine sweeps them hourly — an expired row is already unusable, so
	// nothing is gained by looking more often.
	wg.Add(1)
	go func() {
		defer wg.Done()
		authr.RunSessionPurge(ctx, sessionPurgeInterval, log.With("component", "session-purge"))
	}()

	// Audit retention: one owned goroutine deleting events past the horizon in
	// bounded batches (audit-log.md §8). With CYPHERD_AUDIT_RETENTION=0 it
	// returns immediately and events are kept forever — "keep everything"
	// should cost nothing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		auditSvc.RunRetention(ctx, auditPurgeInterval, log.With("component", "audit-retention"))
	}()

	// The update check: one owned goroutine polling a release feed, off by a
	// single environment variable, telling owners once per version through the
	// inbox (control-plane-hardening.md §3). ADR-010 keeps the plane from
	// updating itself — this only tells the operator.
	updateChecker, err := updates.New(updates.Options{
		Current:   updates.RuntimeInfo(version, commit, buildDate),
		FeedURL:   cfg.UpdateFeedURL,
		Enabled:   cfg.UpdateCheck,
		Announcer: panelUpdateAnnouncer{inbox: inboxSvc},
		Log:       log.With("component", "updates"),
	})
	if err != nil {
		return err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		updateChecker.Run(ctx)
	}()

	if err := sched.Recover(ctx); err != nil {
		return err
	}
	if err := b.RespondDesiredState(func(serverID string) ([]byte, error) {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return sched.DesiredStateFor(c, serverID)
	}); err != nil {
		return err
	}
	deployConsume, err := b.ConsumeDeployEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DeployEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling deploy event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleDeployEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer deployConsume.Stop()
	statusConsume, err := b.ConsumeAppStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.AppStatus
		if err := proto.Unmarshal(data, &st); err != nil {
			log.Error("unmarshaling app status", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleAppStatus(c, serverID, &st)
	})
	if err != nil {
		return err
	}
	defer statusConsume.Stop()

	dbStatusConsume, err := b.ConsumeDbStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.DbStatus
		if err := proto.Unmarshal(data, &st); err != nil {
			log.Error("unmarshaling db status", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleDbStatus(c, serverID, &st)
	})
	if err != nil {
		return err
	}
	defer dbStatusConsume.Stop()

	composeStatusConsume, err := b.ConsumeComposeStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.ComposeStatus
		if err := proto.Unmarshal(data, &st); err != nil {
			log.Error("unmarshaling compose status", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleComposeStatus(c, serverID, &st)
	})
	if err != nil {
		return err
	}
	defer composeStatusConsume.Stop()

	dbBackupConsume, err := b.ConsumeDbBackupEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbBackupEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db backup event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbBackupEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbBackupConsume.Stop()

	dbRestoreConsume, err := b.ConsumeDbRestoreEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbRestoreEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db restore event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbRestoreEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbRestoreConsume.Stop()

	dbBackupPruneConsume, err := b.ConsumeDbBackupPruneEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DbBackupPruneEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling db backup prune event", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sched.HandleDbBackupPruneEvent(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer dbBackupPruneConsume.Stop()

	taskRunConsume, err := b.ConsumeTaskRuns(ctx, func(serverID string, data []byte) {
		var ev agentv1.ScheduledTaskRun
		if err := proto.Unmarshal(data, &ev); err != nil {
			log.Error("unmarshaling scheduled task run", "server_id", serverID, "error", err)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.HandleScheduledTaskRun(c, serverID, &ev)
	})
	if err != nil {
		return err
	}
	defer taskRunConsume.Stop()

	// gRPC enrollment + image-relay endpoint. Enroll needs no client cert
	// (first contact, join-token gated); the relay RPCs require a verified
	// agent certificate on the same listener (builder-role-and-relay.md §3).
	relaySrv := grpcapi.NewRelayServer(relay.New(0), st, log)
	grpcSrv, err := startEnrollmentServer(cfg, planeCert, planeKey, ca.CertPEM(), enrollSvc, auditSvc, relaySrv, log)
	if err != nil {
		return err
	}

	// REST API + console.
	api := rest.New(rest.Deps{
		Auth:             authr,
		Onboarding:       onboardSvc,
		Servers:          serverSvc,
		Projects:         projectSvc,
		Applications:     appSvc,
		DeployKeys:       deployKeySvc,
		Registries:       registrySvc,
		Compose:          composeSvc,
		Databases:        dbSvc,
		BackupTargets:    backupTargetSvc,
		BackupSchedules:  backupScheduleSvc,
		Backups:          sched,
		Restores:         st,
		Previews:         previewMgr,
		Notifiers:        notifySvc,
		NotifyDelivery:   notifyMgr,
		ScheduledTasks:   scheduledTaskSvc,
		WebhookEndpoints: webhookSvc,
		Inbox:            inboxSvc,
		Audit:            auditSvc,
		Protection:       protectionSvc,
		SharedVariables:  sharedVarSvc,
		Templates:        templateSvc,
		Teams:            teamSvc,
		Invites:          inviteSvc,
		AccessRequests:   accessRequestSvc,
		Mail:             mailSvc,
		PanelTLS:         panelTLS,
		DNS:              dnsSvc,
		DNSZones:         st,
		ServerAddresses:  st,
		Scheduler:        sched,
		Deployments:      st,
		Opener:           box,
		Pinger:           st,
		CACertPEM:        ca.CertPEM(),
		EnrollAddr:       cfg.AdvertisedEnrollAddr(),
		NATSURL:          cfg.AdvertisedNATSURL(),
		Logs:             b,
		ConsoleURL:       cfg.AdvertisedConsoleURL(),
		TrustedProxies:   cfg.TrustedProxies,
		Panel:            updateChecker,
		PanelLogs:        panelLogs,
		DataDir:          cfg.DataDir,
		Log:              log,
	})
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 2)
	go func() {
		log.Info("http api + console listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server: %w", err)
		}
	}()

	// Wait for a signal or a fatal serve error.
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serveErr:
		log.Error("server failed", "error", err)
		stop() // trigger the same graceful path
	}

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http graceful shutdown", "error", err)
	}
	grpcSrv.GracefulStop()
	wg.Wait()
	log.Info("cypherd stopped")
	return nil
}

// serverDiskAnnouncer turns a server crossing the disk threshold into one inbox
// item per panel owner and admin (disk-management.md §5). Like the update
// announcer it lives in the wiring, because it is the only place that knows
// both halves — and it is the inbox rather than a notifier because a Server
// belongs to no project.
type serverDiskAnnouncer struct {
	inbox *inbox.Service
}

func (a serverDiskAnnouncer) AnnounceServerDisk(ctx context.Context, server domain.Server, kind, detail string) error {
	title := "Disk is filling up on " + server.Name
	body := detail + " Reclaim space, or move workloads to another server."
	if kind == domain.InboxKindServerDiskRecovered {
		title = "Disk recovered on " + server.Name
		body = detail
	}
	return a.inbox.RecordServerDisk(ctx, server.ID, server.Name, kind, title, body)
}

// panelUpdateAnnouncer turns "a newer release exists" into one inbox item per
// owner, once per version (control-plane-hardening.md §3). It lives here, in
// the wiring, because it is the only place that knows both halves.
type panelUpdateAnnouncer struct {
	inbox *inbox.Service
}

func (p panelUpdateAnnouncer) AnnounceUpdate(ctx context.Context, current updates.Info, latest updates.Release) error {
	return p.inbox.RecordPanelUpdate(ctx, inbox.PanelUpdate{
		Current:  current.Version,
		Latest:   latest.Version,
		Kind:     latest.Kind,
		NotesURL: latest.NotesURL,
	})
}

func startEnrollmentServer(cfg config.Config, certPEM, keyPEM, caPEM []byte, svc *enroll.Service, rec grpcapi.AuditRecorder, relaySrv *grpcapi.RelayServer, log *slog.Logger) (*grpc.Server, error) {
	tlsCfg, err := pki.ServerBootstrapTLSConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("building enrollment TLS: %w", err)
	}
	// Client certificates are optional at the TLS layer (Enroll is the first
	// contact that issues them) but verified against the agent CA when
	// presented; the relay handlers then require a verified identity.
	pool, err := pki.CertPool(caPEM)
	if err != nil {
		return nil, fmt.Errorf("building agent CA pool: %w", err)
	}
	tlsCfg.ClientCAs = pool
	tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	lis, err := net.Listen("tcp", cfg.EnrollAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on enroll addr: %w", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	agentv1.RegisterEnrollmentServiceServer(srv, grpcapi.NewEnrollmentServer(svc, rec, log))
	agentv1.RegisterImageRelayServiceServer(srv, relaySrv)
	go func() {
		log.Info("enrollment endpoint listening", "addr", cfg.EnrollAddr, "advertised", cfg.AdvertisedEnrollAddr())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error("enrollment server", "error", err)
		}
	}()
	return srv, nil
}

// bootstrapAdmin creates the first owner from CYPHERD_ADMIN_EMAIL/PASSWORD when
// the panel has none. It shares CreateFirstOwner with the in-browser setup path
// (onboarding), so an env-var boot and a browser setup produce the same shape.
// When no admin is configured, the operator completes setup in the browser on
// first visit (first-run-setup.md) — no longer a dead-end login screen.
func bootstrapAdmin(ctx context.Context, onb *onboarding.Service, cfg config.Config, log *slog.Logger) error {
	needs, err := onb.NeedsSetup(ctx)
	if err != nil {
		return err
	}
	if !needs {
		return nil // already bootstrapped; do not overwrite
	}
	if cfg.AdminEmail == "" {
		log.Info("no admin account yet — complete first-run setup in the browser, or set CYPHERD_ADMIN_EMAIL/PASSWORD")
		return nil
	}
	if _, err := onb.CreateFirstOwner(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return fmt.Errorf("bootstrapping admin: %w", err)
	}
	log.Info("bootstrapped admin account", "email", cfg.AdminEmail)
	return nil
}

// planeSANs derives the certificate SANs for the plane's server cert from the
// configured public host, always including loopback so local tooling works.
func planeSANs(publicHost string) (dnsNames []string, ips []net.IP) {
	dnsNames = []string{"localhost"}
	ips = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if publicHost == "" || publicHost == "localhost" {
		return dnsNames, ips
	}
	if ip := net.ParseIP(publicHost); ip != nil {
		ips = append(ips, ip)
	} else {
		dnsNames = append(dnsNames, publicHost)
	}
	return dnsNames, ips
}

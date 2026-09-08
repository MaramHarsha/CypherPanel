// Package previews automates preview environments from PR lifecycle events
// (preview-environments.md). A preview is an ordinary child Environment holding
// a cloned Application; this package only adds the trigger, the templating, and
// the destroy — everything else is the normal deploy pipeline.
package previews

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence the manager needs (consumer-defined, rule 6).
type Store interface {
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	CreateEnvironmentOfKind(ctx context.Context, id, projectID, name, kind string) (domain.Environment, error)
	DeleteEnvironment(ctx context.Context, id string) error
	CreatePreview(ctx context.Context, p domain.Preview) (domain.Preview, error)
	GetPreview(ctx context.Context, id string) (domain.Preview, error)
	GetPreviewByPR(ctx context.Context, sourceAppID string, prNumber int) (domain.Preview, error)
	ListPreviewsBySourceApp(ctx context.Context, sourceAppID string) ([]domain.Preview, error)
	SetPreviewStatus(ctx context.Context, id, status string) error
	ListExpiredPreviews(ctx context.Context, cutoff time.Time) ([]domain.Preview, error)
	DeletePreview(ctx context.Context, id string) error
}

// AppService clones and deletes applications (consumer-defined;
// *applications.Service satisfies it).
type AppService interface {
	Create(ctx context.Context, envID string, in applications.CreateInput) (domain.Application, string, error)
}

// Deployer drives the pipeline and desired absence (consumer-defined;
// *scheduler.Scheduler satisfies it).
type Deployer interface {
	Deploy(ctx context.Context, appID, trigger, ref string) (domain.Deployment, error)
	RemoveApp(ctx context.Context, serverID, appID string) error
}

// AuditRecorder records the preview environment lifecycle (consumer-defined;
// *audit.Service satisfies it). A preview environment is created and destroyed
// with NO operator in the loop, so without this the `environment.created` and
// `environment.deleted` verbs would be true only of the environments a person
// made by hand — and the audit log would quietly under-report the environments
// that actually come and go most often (audit-log.md §3).
//
// nil records nothing, which keeps previews working on a panel wired without
// the audit log.
type AuditRecorder interface {
	Record(ctx context.Context, e audit.Entry) (domain.AuditEvent, error)
}

// auditTimeout bounds a recording write. It runs on a context detached from the
// caller's, so it needs a deadline of its own.
const auditTimeout = 5 * time.Second

// systemActor attributes an automated preview lifecycle change: nobody was
// signed in. The manual DELETE is audited by its handler instead, with the
// operator's name on it.
var systemActor = domain.AuditActor{Kind: domain.AuditActorSystem, Label: "preview automation"}

// Manager reconciles previews against PR lifecycle events. Construct with New.
type Manager struct {
	store Store
	apps  AppService
	sched Deployer
	log   *slog.Logger
	now   func() time.Time
	audit AuditRecorder
}

// Option tunes a Manager at construction (ENGINEERING rule 5: no setters after
// the fact).
type Option func(*Manager)

// WithAudit makes the automated halves of the preview lifecycle — the
// environment a PR spawns and the one a close or a TTL sweep reclaims —
// first-class audit events.
func WithAudit(rec AuditRecorder) Option { return func(m *Manager) { m.audit = rec } }

// New wires the manager.
func New(st Store, apps AppService, sched Deployer, log *slog.Logger, opts ...Option) *Manager {
	m := &Manager{store: st, apps: apps, sched: sched, log: log, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	return m
}

// record writes one audit row and never fails the operation it describes: a
// preview that was created must not be torn down because the record of it
// could not be written (audit-log.md §9). The context is detached from
// cancellation — a sweep shutting down still owes the log the teardown it just
// performed — and carries its own deadline.
func (m *Manager) record(ctx context.Context, e audit.Entry) {
	if m.audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if _, err := m.audit.Record(ctx, e); err != nil {
		m.log.Error("recording preview audit event",
			"action", e.Action, "resource_id", e.Resource.ID, "error", err)
	}
}

// PR lifecycle actions the manager acts on (GitHub pull_request payload).
const (
	ActionOpened      = "opened"
	ActionReopened    = "reopened"
	ActionSynchronize = "synchronize"
	ActionClosed      = "closed"
)

// OnPullRequest is the single entry point from the webhook. source is the
// Application whose webhook delivered the event; it must have previews enabled
// (the caller checks). Errors are operational; the manager keeps a failed
// preview visible as status=error rather than pretending success.
func (m *Manager) OnPullRequest(ctx context.Context, source domain.Application, action string, prNumber int, prBranch, prSHA string) error {
	switch action {
	case ActionOpened, ActionReopened, ActionSynchronize:
		return m.ensureAndDeploy(ctx, source, prNumber, prBranch, prSHA)
	case ActionClosed:
		return m.destroyByPR(ctx, source.ID, prNumber)
	default:
		return nil // labeled/assigned/etc — not our concern
	}
}

func (m *Manager) ensureAndDeploy(ctx context.Context, source domain.Application, prNumber int, prBranch, prSHA string) error {
	if source.PreviewBaseDomain == "" {
		return fmt.Errorf("previews: app %s has previews enabled but no base domain", source.ID)
	}

	existing, err := m.store.GetPreviewByPR(ctx, source.ID, prNumber)
	if err == nil {
		// Synchronize: redeploy the existing preview app at the new SHA.
		if existing.PreviewAppID == nil {
			return fmt.Errorf("previews: preview %s has no app to redeploy", existing.ID)
		}
		if _, err := m.sched.Deploy(ctx, *existing.PreviewAppID, "preview", prSHA); err != nil {
			_ = m.store.SetPreviewStatus(ctx, existing.ID, domain.PreviewError)
			return fmt.Errorf("previews: redeploying preview: %w", err)
		}
		m.log.Info("preview redeployed", "preview_id", existing.ID, "pr", prNumber, "sha", prSHA)
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("previews: checking existing preview: %w", err)
	}

	// Provision a new preview.
	env, err := m.store.GetEnvironment(ctx, source.EnvironmentID)
	if err != nil {
		return fmt.Errorf("previews: loading source environment: %w", err)
	}
	domainName := previewDomain(prNumber, source.PreviewBaseDomain)
	// Marked as a preview so the operator-facing lifecycle refuses to rename or
	// delete it by hand: this environment belongs to the pull request.
	childEnv, err := m.store.CreateEnvironmentOfKind(ctx, ids.New(ids.PrefixEnvironment), env.ProjectID,
		previewEnvName(prNumber, source.ID), domain.EnvPreview)
	if err != nil {
		return fmt.Errorf("previews: creating child environment: %w", err)
	}

	clone, _, err := m.apps.Create(ctx, childEnv.ID, applications.CreateInput{
		Name: fmt.Sprintf("%s-pr-%d", source.Name, prNumber),
		Source: domain.AppSource{
			Kind: source.Source.Kind, Repo: source.Source.Repo,
			Branch: prBranch, DeployKeyID: source.Source.DeployKeyID,
		},
		Build:   source.Build,
		Runtime: source.Runtime,
		// Inherit the source's TLS posture: an HTTPS production app (real
		// domain + ACME) yields HTTPS previews under the wildcard base; an
		// HTTP app yields HTTP previews (preview-environments.md §2).
		Route:  domain.AppRoute{Domain: domainName, HTTPS: source.Route.HTTPS, PathPrefix: source.Route.PathPrefix},
		Health: source.Health,
		// No env vars in previews at v1 (preview-environments.md §5/§6:
		// fork-PR secret exfiltration risk).
	})
	if err != nil {
		// Roll back the empty child env so a failed clone doesn't orphan it.
		_ = m.store.DeleteEnvironment(ctx, childEnv.ID)
		return fmt.Errorf("previews: cloning application: %w", err)
	}

	appID := clone.ID
	expires := m.now().Add(time.Duration(ttlHours(source)) * time.Hour)
	preview, err := m.store.CreatePreview(ctx, domain.Preview{
		ID:            ids.New(ids.PrefixPreview),
		SourceAppID:   source.ID,
		EnvironmentID: childEnv.ID,
		PreviewAppID:  &appID,
		PRNumber:      prNumber,
		PRBranch:      prBranch,
		Domain:        domainName,
		Status:        domain.PreviewCreating,
		ExpiresAt:     &expires,
	})
	if err != nil {
		// Roll back both the cloned app and the child env, matching the
		// clone-failure path — a failed record must leave no orphans. Deleting
		// the child env cascades the cloned app row.
		_ = m.store.DeleteEnvironment(ctx, childEnv.ID)
		return fmt.Errorf("previews: recording preview: %w", err)
	}
	// Recorded only once the preview exists for real: the two rollback paths
	// above leave nothing behind, so they leave no row either. The environment
	// id is enough for the insert to resolve the project and the team.
	m.record(ctx, audit.Entry{
		Action:        audit.ActionEnvironmentCreated,
		Actor:         systemActor,
		Resource:      audit.Resource(audit.ResourceEnvironment, childEnv.ID, childEnv.Name),
		EnvironmentID: childEnv.ID,
		Detail: map[string]any{
			"kind":                  domain.EnvPreview,
			"preview_id":            preview.ID,
			"pr":                    prNumber,
			"source_application_id": source.ID,
			"domain":                domainName,
		},
	})

	if _, err := m.sched.Deploy(ctx, clone.ID, "preview", prSHA); err != nil {
		_ = m.store.SetPreviewStatus(ctx, preview.ID, domain.PreviewError)
		return fmt.Errorf("previews: deploying preview: %w", err)
	}
	_ = m.store.SetPreviewStatus(ctx, preview.ID, domain.PreviewRunning)
	m.log.Info("preview created", "preview_id", preview.ID, "pr", prNumber, "domain", domainName)
	return nil
}

func (m *Manager) destroyByPR(ctx context.Context, sourceAppID string, prNumber int) error {
	p, err := m.store.GetPreviewByPR(ctx, sourceAppID, prNumber)
	if errors.Is(err, store.ErrNotFound) {
		return nil // never provisioned, or already destroyed
	}
	if err != nil {
		return fmt.Errorf("previews: loading preview: %w", err)
	}
	return m.destroyAndRecord(ctx, p, "pull request closed")
}

// DestroyByID tears a preview down by id. It is the MANUAL path (the operator's
// DELETE), which its handler audits with the operator's name; the automated
// paths record themselves, so this one does not.
func (m *Manager) DestroyByID(ctx context.Context, previewID string) error {
	p, err := m.store.GetPreview(ctx, previewID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("previews: loading preview: %w", err)
	}
	return m.destroy(ctx, p)
}

// destroyAndRecord is the automated teardown: the destroy plus the
// `environment.deleted` row that says a preview environment stopped existing
// without anyone asking.
//
// The environment is read BEFORE the delete on purpose. This handler destroys
// its own ownership chain: once the environment row is gone there is nothing
// left for the INSERT to resolve team_id from, and an entry that cannot be
// scoped cannot be read by the team it belongs to (audit-log.md §4).
func (m *Manager) destroyAndRecord(ctx context.Context, p domain.Preview, reason string) error {
	var env domain.Environment
	if m.audit != nil {
		env, _ = m.store.GetEnvironment(ctx, p.EnvironmentID)
	}
	if err := m.destroy(ctx, p); err != nil {
		return err
	}
	m.record(ctx, audit.Entry{
		Action:    audit.ActionEnvironmentDeleted,
		Actor:     systemActor,
		Resource:  audit.Resource(audit.ResourceEnvironment, p.EnvironmentID, env.Name),
		ProjectID: env.ProjectID,
		Detail: map[string]any{
			"kind":       domain.EnvPreview,
			"preview_id": p.ID,
			"pr":         p.PRNumber,
			"reason":     reason,
		},
	})
	return nil
}

// destroy tears down the cloned container/route and deletes the child
// environment — whose cascade removes the cloned app row and the preview row
// (preview-environments.md §4). Idempotent.
func (m *Manager) destroy(ctx context.Context, p domain.Preview) error {
	_ = m.store.SetPreviewStatus(ctx, p.ID, domain.PreviewDestroying)

	// Publish desired absence before the row is gone — RemoveApp needs the
	// server id, and the agent tears down the container + route fragment.
	if p.PreviewAppID != nil {
		if app, err := m.store.GetApplication(ctx, *p.PreviewAppID); err == nil {
			if err := m.sched.RemoveApp(ctx, app.Runtime.ServerID, app.ID); err != nil {
				m.log.Error("preview destroy: removing app", "preview_id", p.ID, "error", err)
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			m.log.Error("preview destroy: loading app", "preview_id", p.ID, "error", err)
		}
	}

	// Deleting the child environment cascades the cloned app row and the
	// preview row in one shot.
	if err := m.store.DeleteEnvironment(ctx, p.EnvironmentID); err != nil {
		return fmt.Errorf("previews: deleting child environment: %w", err)
	}
	m.log.Info("preview destroyed", "preview_id", p.ID, "pr", p.PRNumber)
	return nil
}

// RunSweeper destroys expired previews on each tick until ctx is cancelled. It
// owns its ticker's lifecycle (ENGINEERING rule 7).
func (m *Manager) RunSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.SweepExpired(ctx)
		}
	}
}

// SweepExpired destroys every preview past its TTL (missed-close backstop).
func (m *Manager) SweepExpired(ctx context.Context) {
	expired, err := m.store.ListExpiredPreviews(ctx, m.now())
	if err != nil {
		m.log.Error("preview sweep: listing expired", "error", err)
		return
	}
	for _, p := range expired {
		m.log.Info("preview expired, destroying", "preview_id", p.ID, "pr", p.PRNumber)
		if err := m.destroyAndRecord(ctx, p, "ttl expired"); err != nil {
			m.log.Error("preview sweep: destroying", "preview_id", p.ID, "error", err)
		}
	}
}

// List returns a source app's previews (read model for the API).
func (m *Manager) List(ctx context.Context, sourceAppID string) ([]domain.Preview, error) {
	return m.store.ListPreviewsBySourceApp(ctx, sourceAppID)
}

// Get returns one preview.
func (m *Manager) Get(ctx context.Context, id string) (domain.Preview, error) {
	return m.store.GetPreview(ctx, id)
}

func ttlHours(source domain.Application) int {
	if source.PreviewTTLHours > 0 {
		return source.PreviewTTLHours
	}
	return 72
}

func previewDomain(prNumber int, base string) string {
	return fmt.Sprintf("pr-%d.%s", prNumber, base)
}

// previewEnvName is deterministic and unique per (source app, PR): the source
// suffix prevents a collision when two apps in one project share a PR number.
func previewEnvName(prNumber int, sourceAppID string) string {
	suffix := sourceAppID
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return fmt.Sprintf("pr-%d-%s", prNumber, suffix)
}

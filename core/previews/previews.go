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
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence the manager needs (consumer-defined, rule 6).
type Store interface {
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	CreateEnvironment(ctx context.Context, id, projectID, name string) (domain.Environment, error)
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

// Manager reconciles previews against PR lifecycle events. Construct with New.
type Manager struct {
	store Store
	apps  AppService
	sched Deployer
	log   *slog.Logger
	now   func() time.Time
}

// New wires the manager.
func New(st Store, apps AppService, sched Deployer, log *slog.Logger) *Manager {
	return &Manager{store: st, apps: apps, sched: sched, log: log, now: time.Now}
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
	childEnv, err := m.store.CreateEnvironment(ctx, ids.New(ids.PrefixEnvironment), env.ProjectID, previewEnvName(prNumber, source.ID))
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
	return m.destroy(ctx, p)
}

// DestroyByID tears a preview down by id (manual DELETE and the TTL sweeper).
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
		if err := m.destroy(ctx, p); err != nil {
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

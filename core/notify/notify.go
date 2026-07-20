// Package notify turns the control plane's observed terminal state transitions
// (deploy succeeded/failed, backup succeeded/failed) into messages delivered to
// an operator's configured channels — Email, Discord, Slack, Telegram
// (notifications.md). Delivery is best-effort, fire-and-forget, and logged: a
// dead channel never blocks or slows a deploy (spec §1).
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Store is the persistence the manager needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	ListEnabledNotifiersForEvent(ctx context.Context, projectID, eventType string) ([]domain.Notifier, error)
}

// Opener unseals a notifier's channel config (consumer-defined; *secret.Box
// satisfies it). Config is unsealed only at send time, never logged.
type Opener interface {
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// deliveryTimeout bounds one channel send so a hung endpoint can't pin a
// goroutine indefinitely.
const deliveryTimeout = 10 * time.Second

// telegramAPI is the Telegram Bot API base; a package var so tests can point it
// at a local server (the token-in-path URL is otherwise unmockable).
var telegramAPI = "https://api.telegram.org"

// Manager fans one event out to a project's subscribed channels. Construct with
// New.
type Manager struct {
	store  Store
	opener Opener
	http   *http.Client
	log    *slog.Logger
}

// New wires the manager.
func New(st Store, opener Opener, log *slog.Logger) *Manager {
	return &Manager{
		store:  st,
		opener: opener,
		http:   &http.Client{Timeout: deliveryTimeout},
		log:    log,
	}
}

// NotifyDeploy delivers a deploy's terminal outcome. One method covers both:
// dep.Status is DeploySucceeded or DeployFailed at the call site (spec §5).
// Returns immediately — delivery runs detached from the caller's context so it
// never blocks the scheduler pipeline (spec §1).
func (m *Manager) NotifyDeploy(ctx context.Context, app domain.Application, dep domain.Deployment) {
	ev := domain.NotifyEvent{Type: domain.EventDeploySucceeded, Level: domain.NotifyInfo}
	if dep.Status == domain.DeployFailed {
		ev.Type, ev.Level = domain.EventDeployFailed, domain.NotifyError
	}
	ev.Title = fmt.Sprintf("Deploy %s: %s", outcomeWord(ev.Level), app.Name)
	ev.Body = fmt.Sprintf("Application %q (revision %s) %s.", app.Name, dep.RevisionID, outcomeWord(ev.Level))
	if dep.Detail != "" {
		ev.Body += "\n" + dep.Detail
	}
	m.dispatch(ctx, app.EnvironmentID, ev)
}

// NotifyBackup delivers a database backup's terminal outcome.
func (m *Manager) NotifyBackup(ctx context.Context, db domain.Database, rec domain.BackupRecord) {
	ev := domain.NotifyEvent{Type: domain.EventBackupSucceeded, Level: domain.NotifyInfo}
	if rec.Status != domain.BackupSucceeded {
		ev.Type, ev.Level = domain.EventBackupFailed, domain.NotifyError
	}
	ev.Title = fmt.Sprintf("Backup %s: %s", outcomeWord(ev.Level), db.Name)
	ev.Body = fmt.Sprintf("Database %q backup %s.", db.Name, outcomeWord(ev.Level))
	if rec.Detail != "" {
		ev.Body += "\n" + rec.Detail
	}
	m.dispatch(ctx, db.EnvironmentID, ev)
}

// dispatch resolves the environment's project, loads its subscribed notifiers,
// and delivers to each in parallel. It detaches from the caller's context so a
// short-lived request/handler context can't cancel an in-flight send.
func (m *Manager) dispatch(ctx context.Context, envID string, ev domain.NotifyEvent) {
	base := context.WithoutCancel(ctx)
	go func() {
		c, cancel := context.WithTimeout(base, deliveryTimeout)
		defer cancel()

		env, err := m.store.GetEnvironment(c, envID)
		if err != nil {
			m.log.Error("notify: resolving project", "env_id", envID, "error", err)
			return
		}
		ev.Project = env.Name
		notifiers, err := m.store.ListEnabledNotifiersForEvent(c, env.ProjectID, ev.Type)
		if err != nil {
			m.log.Error("notify: listing notifiers", "project_id", env.ProjectID, "event", ev.Type, "error", err)
			return
		}
		m.fanOut(base, notifiers, ev)
	}()
}

// fanOut delivers ev to every notifier concurrently, each isolated: one
// failure is logged and dropped, never blocking the others (spec §8 case 5).
func (m *Manager) fanOut(base context.Context, notifiers []domain.Notifier, ev domain.NotifyEvent) {
	var wg sync.WaitGroup
	for _, n := range notifiers {
		wg.Add(1)
		go func(n domain.Notifier) {
			defer wg.Done()
			c, cancel := context.WithTimeout(base, deliveryTimeout)
			defer cancel()
			if err := m.deliver(c, n, ev); err != nil {
				m.log.Error("notify: delivery failed", "notifier_id", n.ID, "channel", n.Channel, "event", ev.Type, "error", err)
			}
		}(n)
	}
	wg.Wait()
}

// Deliver sends ev through one notifier now, surfacing the error — the test
// endpoint's synchronous path (spec §7). Delivery for real events goes through
// dispatch/fanOut.
func (m *Manager) Deliver(ctx context.Context, n domain.Notifier, ev domain.NotifyEvent) error {
	return m.deliver(ctx, n, ev)
}

// deliver unseals the notifier's config and hands it to the channel sender.
func (m *Manager) deliver(ctx context.Context, n domain.Notifier, ev domain.NotifyEvent) error {
	cfg, err := m.opener.Open(n.ConfigCT, n.ConfigNonce)
	if err != nil {
		return fmt.Errorf("unsealing config: %w", err)
	}
	return m.send(ctx, n.Channel, cfg, ev)
}

// outcomeWord renders a level as a past-tense outcome word for message copy.
func outcomeWord(l domain.NotifyLevel) string {
	if l == domain.NotifyError {
		return "failed"
	}
	return "succeeded"
}

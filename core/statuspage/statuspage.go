// Package statuspage turns observed resource health into the interval time
// series a public status page is drawn from (status-pages.md §6).
//
// The whole feature rests on one rule: an interval is written when the state
// CHANGES and has held for the dwell — never per tick. A healthy component has
// exactly one open row, forever, and the thirty daily bars, the uptime figure
// and the incident list are all queries over those rows. That is what keeps a
// public page off the write budget entirely.
//
// The dwell does three jobs with one rule. It suppresses blips, so a container
// that restarts and recovers inside a minute is not reported as an outage
// nobody could have acted on. It bounds writes, so a crash-looping application
// produces one continuous `down` interval rather than three hundred rows. And
// it matches the resolution of the input: the agent reports on change and on
// its 60-second drift pass, so an observation is up to a minute old by
// construction and a shorter dwell would claim precision the data does not
// have. The cost — an outage shorter than the dwell is invisible here — is
// stated on the page rather than hidden.
package statuspage

import (
	"context"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence the evaluator needs (consumer-defined).
type Store interface {
	ListAllTrackedComponents(ctx context.Context) ([]domain.StatusPageComponent, error)
	GetOpenStatusInterval(ctx context.Context, componentID string) (domain.StatusInterval, error)
	OpenStatusInterval(ctx context.Context, id, componentID, state string, at time.Time) (domain.StatusInterval, error)
	CloseStatusInterval(ctx context.Context, id string, at time.Time) error
	LastEvaluation(ctx context.Context) (time.Time, error)
	StampEvaluation(ctx context.Context, at time.Time) error
	DeleteStatusIntervalsBefore(ctx context.Context, cutoff time.Time, limit int) error

	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetComposeStack(ctx context.Context, id string) (domain.ComposeStack, error)
	GetDatabase(ctx context.Context, id string) (domain.Database, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
}

// Evaluator is the one owned goroutine (ENGINEERING rule 7).
type Evaluator struct {
	store Store
	log   *slog.Logger
	now   func() time.Time

	tick      time.Duration
	dwell     time.Duration
	retention time.Duration

	// pending is the dwell's memory: a state observed but not yet recorded,
	// and when it was first seen. In memory on purpose — a plane restart
	// forgets it, which costs one dwell of delay on a transition in flight and
	// is cheaper than a table whose only reader is this loop.
	pending map[string]pendingState
}

type pendingState struct {
	state string
	since time.Time
}

// Config carries the three knobs of §11. Zero values take the documented
// defaults, so a panel that configures nothing behaves as the spec says.
type Config struct {
	Tick      time.Duration
	Dwell     time.Duration
	Retention time.Duration
}

func New(store Store, cfg Config, log *slog.Logger) *Evaluator {
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.Dwell <= 0 {
		cfg.Dwell = time.Minute
	}
	if cfg.Retention < 0 {
		cfg.Retention = 0
	}
	return &Evaluator{
		store: store, log: log, now: time.Now,
		tick: cfg.Tick, dwell: cfg.Dwell, retention: cfg.Retention,
		pending: map[string]pendingState{},
	}
}

// SetClock injects the clock (ENGINEERING rule 9).
func (e *Evaluator) SetClock(now func() time.Time) { e.now = now }

// Run ticks until the context is cancelled. It closes the plane's own absence
// first, then evaluates on every tick.
func (e *Evaluator) Run(ctx context.Context) {
	e.CloseGap(ctx)
	t := time.NewTicker(e.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

// CloseGap is §6.3, and it is the difference between an honest uptime figure
// and a flattering one.
//
// Uptime derived only from recorded intervals has one systematic lie in it:
// while the plane is not running it observes nothing, so an open `operational`
// interval silently spans the outage and the page later claims a perfect day.
// So a gap wider than two ticks is drawn as `unknown` on every tracked
// component — grey on the bars, and in neither the numerator nor the
// denominator of the percentage. The page still cannot report its own absence
// in real time; it just never pretends the absence did not happen.
func (e *Evaluator) CloseGap(ctx context.Context) {
	last, err := e.store.LastEvaluation(ctx)
	if err != nil || last.IsZero() {
		// Never evaluated: there is no "before" to have missed.
		return
	}
	now := e.now()
	if now.Sub(last) <= 2*e.tick {
		return
	}
	components, err := e.store.ListAllTrackedComponents(ctx)
	if err != nil {
		e.log.Error("status page: listing components for gap", "error", err)
		return
	}
	for _, c := range components {
		open, err := e.store.GetOpenStatusInterval(ctx, c.ID)
		if err == nil {
			if err := e.store.CloseStatusInterval(ctx, open.ID, last); err != nil {
				e.log.Error("status page: closing interval across a gap", "component_id", c.ID, "error", err)
				continue
			}
		}
		gap, err := e.store.OpenStatusInterval(ctx, ids.New(ids.PrefixStatusInterval), c.ID, domain.PublicUnknown, last)
		if err != nil {
			e.log.Error("status page: opening gap interval", "component_id", c.ID, "error", err)
			continue
		}
		if err := e.store.CloseStatusInterval(ctx, gap.ID, now); err != nil {
			e.log.Error("status page: closing gap interval", "component_id", c.ID, "error", err)
		}
	}
	e.log.Info("status page: recorded a plane outage as unobserved time",
		"from", last.UTC(), "to", now.UTC(), "components", len(components))
}

// Tick evaluates every tracked component once.
func (e *Evaluator) Tick(ctx context.Context) {
	now := e.now()
	components, err := e.store.ListAllTrackedComponents(ctx)
	if err != nil {
		e.log.Error("status page: listing components", "error", err)
		return
	}
	live := make(map[string]bool, len(components))
	for _, c := range components {
		live[c.ID] = true
		e.evaluate(ctx, c, now)
	}
	// Forget dwell state for components that are gone, so the map cannot grow
	// with the history of every component ever added.
	for id := range e.pending {
		if !live[id] {
			delete(e.pending, id)
		}
	}
	if err := e.store.StampEvaluation(ctx, now); err != nil {
		e.log.Error("status page: stamping evaluation", "error", err)
	}
	e.sweep(ctx, now)
}

func (e *Evaluator) evaluate(ctx context.Context, c domain.StatusPageComponent, now time.Time) {
	observed, ok := e.observe(ctx, c)
	if !ok {
		// The resource is gone. The component is not rendered (the public read
		// is a join) and its history is left alone rather than rewritten.
		return
	}
	if observed == "" {
		// `deploying`: keep whatever is already recorded. A zero-downtime
		// rollout means the previous revision is serving, so a deploy is not
		// news — and it is not the public's business either (§2).
		delete(e.pending, c.ID)
		return
	}

	open, err := e.store.GetOpenStatusInterval(ctx, c.ID)
	if err != nil {
		// No open interval: the component's first observation. It is recorded
		// immediately with no dwell — there is no previous state for a blip to
		// be a blip away from.
		if _, oerr := e.store.OpenStatusInterval(ctx, ids.New(ids.PrefixStatusInterval), c.ID, observed, now); oerr != nil {
			e.log.Error("status page: opening first interval", "component_id", c.ID, "error", oerr)
		}
		delete(e.pending, c.ID)
		return
	}
	if open.State == observed {
		delete(e.pending, c.ID)
		return
	}

	p, held := e.pending[c.ID]
	if !held || p.state != observed {
		e.pending[c.ID] = pendingState{state: observed, since: now}
		return
	}
	if now.Sub(p.since) < e.dwell {
		return
	}

	// The dwell is satisfied: the change is real. Closing and opening at the
	// SAME instant is what makes the bars contiguous — a component is in
	// exactly one state at every moment it was observed.
	if err := e.store.CloseStatusInterval(ctx, open.ID, now); err != nil {
		e.log.Error("status page: closing interval", "component_id", c.ID, "error", err)
		return
	}
	if _, err := e.store.OpenStatusInterval(ctx, ids.New(ids.PrefixStatusInterval), c.ID, observed, now); err != nil {
		e.log.Error("status page: opening interval", "component_id", c.ID, "error", err)
		return
	}
	e.log.Info("status page: component state changed",
		"component_id", c.ID, "from", open.State, "to", observed)
}

// observe reads one component's current public state. The second return is
// false when the resource no longer exists.
//
// Every branch joins the resource to its SERVER, and that join is the point:
// a resource's stored status is the last thing an agent said about it, so a
// host that fell off the internet an hour ago still reads `running` in the
// row. Without the join the page would confidently publish "operational" for a
// machine nobody can reach — the precise failure ui-principles §10 exists to
// forbid, made public.
func (e *Evaluator) observe(ctx context.Context, c domain.StatusPageComponent) (string, bool) {
	var status, serverID string
	var everRan bool

	switch c.ResourceKind {
	case domain.StatusResourceApplication:
		app, err := e.store.GetApplication(ctx, c.ResourceID)
		if err != nil {
			return "", false
		}
		status, serverID, everRan = app.Status, app.Runtime.ServerID, app.StatusObservedAt != nil
	case domain.StatusResourceComposeStack:
		st, err := e.store.GetComposeStack(ctx, c.ResourceID)
		if err != nil {
			return "", false
		}
		status, serverID, everRan = st.Status, st.ServerID, st.StatusObservedAt != nil
	case domain.StatusResourceDatabase:
		d, err := e.store.GetDatabase(ctx, c.ResourceID)
		if err != nil {
			return "", false
		}
		status, serverID, everRan = d.Status, d.ServerID, d.StatusObservedAt != nil
	default:
		return "", false
	}

	reporting := false
	if srv, err := e.store.GetServer(ctx, serverID); err == nil {
		reporting = srv.Status != domain.StatusUnknown
	}
	return domain.PublicStateFor(status, everRan, reporting), true
}

// sweep drops intervals past retention, in bounded batches — the same
// discipline the audit log's retention uses.
func (e *Evaluator) sweep(ctx context.Context, now time.Time) {
	if e.retention <= 0 {
		return
	}
	if err := e.store.DeleteStatusIntervalsBefore(ctx, now.Add(-e.retention), 500); err != nil {
		e.log.Error("status page: sweeping intervals", "error", err)
	}
}

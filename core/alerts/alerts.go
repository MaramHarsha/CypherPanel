// Package alerts evaluates threshold rules against the stored metric series
// (threshold-alerts.md).
//
// THE CONDITION IS NOT "the value is above the threshold". It is: every bucket
// covering the last window_seconds breached, and there are no gaps in that
// span. Three consequences follow, and each one is a rule this package holds:
//
//   - WHOLE BUCKETS, NOT INSTANTS. One bucket is already an aggregate over
//     roughly twenty samples, so a four-second spike moves a five-minute mean by
//     about 1% and does not fire. The compared value is the bucket's MEAN, from
//     the accumulator — never cpu_percent_peak, which is a single sample by
//     definition and would reintroduce exactly what the window removes. The peak
//     is not discarded: it is the notification's "peak 96% at 14:35", the number
//     an operator wants once they know the alert is real.
//
//   - COVERAGE IS A PRECONDITION, NOT A VALUE. A bucket covering under half its
//     nominal length is UNKNOWN, neither high nor low. Dividing a spike by forty
//     seconds of coverage would manufacture a convincing 300% CPU reading out of
//     an agent restart.
//
//   - A GAP BREAKS THE RUN AND RESOLVES NOTHING. Any missing or unknown bucket
//     means the rule does not fire — and does not resolve. It goes to no_data
//     until the span is whole. Treating missing as below-threshold is how an
//     alerting system reports all-clear during an outage of its own collection
//     path, which is the worst thing it can say.
package alerts

import (
	"context"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence the evaluator needs (consumer-defined).
type Store interface {
	ListEnabledAlertRules(ctx context.Context) ([]domain.AlertRule, error)
	SetAlertRuleState(ctx context.Context, id, state string, since time.Time, rearmUntil, quietNotifiedAt *time.Time) error
	OpenAlertEvent(ctx context.Context, id, ruleID string, at time.Time, peak float64, delivered bool) (domain.AlertEvent, error)
	ResolveAlertEvent(ctx context.Context, id string, at time.Time) error
	UpdateAlertEventPeak(ctx context.Context, id string, peak float64) error
	GetOpenAlertEvent(ctx context.Context, ruleID string) (domain.AlertEvent, error)
	CountAlertEpisodesSince(ctx context.Context, ruleID string, since time.Time) (int, error)

	ListResourceMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.ResourceMetricBucket, error)
	ListRequestMetrics(ctx context.Context, kind, id string, since time.Time) ([]domain.RequestMetricBucket, error)
	LatestResourceDisk(ctx context.Context, kind, id string) (domain.ResourceDiskBucket, error)

	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
}

// Notifier delivers one alert through the notifier the RULE names — not
// through a project's subscription fan-out. That is the whole point of putting
// notifier_id in the rule: "page on-call" and "post to a status channel" are
// two rules over one signal, and a subscription cannot express that.
type Notifier interface {
	AnnounceAlert(ctx context.Context, rule domain.AlertRule, targetName, sentence string, firing bool, value float64) error
}

// QuietSink receives the two states that deliver nothing — no_data and flapping
// — as inbox items. Inbox-only rather than subscribable: "your rule stopped
// watching" is governance news for the person who wrote it, not an observed
// transition of a resource.
type QuietSink interface {
	RecordAlertQuiet(ctx context.Context, kind string, rule domain.AlertRule, targetName, detail string) error
}

const (
	// FlapEpisodes is the fourth episode inside FlapWindow.
	FlapEpisodes = 4
	FlapWindow   = time.Hour
	// FlapHold is how long a flapping rule must hold one state before it
	// delivers again.
	FlapHold = time.Hour
	// NoDataQuiet is how long a rule may see nothing before that itself is
	// news. "Your alert has not been watching anything since Tuesday" is worth
	// one message; saying it every tick is not.
	NoDataQuiet = 24 * time.Hour
	// coverageFloor is the fraction of a bucket that must be observed for its
	// value to be a value at all.
	coverageFloor = 0.5
)

// Evaluator is the one owned goroutine (ENGINEERING rule 7).
type Evaluator struct {
	store  Store
	notify Notifier
	quiet  QuietSink
	log    *slog.Logger
	now    func() time.Time
}

func New(st Store, notify Notifier, quiet QuietSink, log *slog.Logger) *Evaluator {
	return &Evaluator{store: st, notify: notify, quiet: quiet, log: log, now: time.Now}
}

// SetClock injects the clock (ENGINEERING rule 9).
func (e *Evaluator) SetClock(now func() time.Time) { e.now = now }

// Run evaluates every enabled rule on a tick until the context is cancelled.
func (e *Evaluator) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
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

func (e *Evaluator) Tick(ctx context.Context) {
	rules, err := e.store.ListEnabledAlertRules(ctx)
	if err != nil {
		e.log.Error("alerts: listing rules", "error", err)
		return
	}
	for _, r := range rules {
		e.evaluate(ctx, r)
	}
}

// Reading is what the window says: breached, clear, or unknown.
type Reading struct {
	Breached bool
	Clear    bool
	Peak     float64
	Latest   float64
}

// Evaluate reads the window for one rule. Exported so the backtest and the
// evaluator share exactly one implementation — two would be two answers to
// "what would this rule have done".
func (e *Evaluator) Evaluate(ctx context.Context, r domain.AlertRule, at time.Time) Reading {
	since := at.Add(-time.Duration(r.WindowSeconds) * time.Second)
	values, ok := e.series(ctx, r, since)
	if !ok || len(values) == 0 {
		return Reading{}
	}
	out := Reading{Breached: true, Clear: true}
	for _, v := range values {
		if v > out.Peak {
			out.Peak = v
		}
		if v <= r.Threshold {
			out.Breached = false
		}
		// Strictly below, symmetric with fire: one threshold, not two. A
		// hysteresis band doubles what an operator must reason about and puts
		// the second number in the value dimension when flapping happens in the
		// time dimension, where the window is already the defence.
		if v >= r.Threshold {
			out.Clear = false
		}
	}
	out.Latest = values[len(values)-1]
	return out
}

// series returns one value per covered bucket in the window. ok is false when
// the span has a gap — a missing bucket, or one whose coverage is too thin to
// be a value.
func (e *Evaluator) series(ctx context.Context, r domain.AlertRule, since time.Time) ([]float64, bool) {
	switch r.Signal {
	case domain.SignalCPU, domain.SignalMemory:
		buckets, err := e.store.ListResourceMetrics(ctx, r.TargetKind, r.TargetID, since)
		if err != nil || len(buckets) == 0 {
			return nil, false
		}
		out := make([]float64, 0, len(buckets))
		for _, b := range buckets {
			if !covered(b) {
				return nil, false
			}
			switch r.Signal {
			case domain.SignalCPU:
				out = append(out, float64(b.CPUCoreMs)/float64(b.CoveredSeconds*1000)*100)
			default:
				mean := float64(b.MemoryByteSeconds) / float64(b.CoveredSeconds)
				if r.ThresholdUnit == domain.UnitPercent {
					if b.MemoryLimitBytes <= 0 {
						// No denominator: a percentage rule against a container
						// with no limit has nothing honest to compute, so it
						// reads unknown rather than inventing one.
						return nil, false
					}
					mean = mean / float64(b.MemoryLimitBytes) * 100
				}
				out = append(out, mean)
			}
		}
		return out, true

	case domain.SignalDisk:
		// Disk is hourly, so the window is a single latest reading rather than
		// a run: asking for five minutes of hourly data would never be whole.
		d, err := e.store.LatestResourceDisk(ctx, r.TargetKind, r.TargetID)
		if err != nil {
			return nil, false
		}
		total := d.ImageBytes + d.VolumeBytes + d.ContainerBytes
		if r.ThresholdUnit == domain.UnitPercent {
			srv, serr := e.store.GetServer(ctx, r.TargetID)
			if serr != nil || srv.DiskTotalBytes == 0 {
				return nil, false
			}
			used := float64(srv.DiskTotalBytes-srv.DiskFreeBytes) / float64(srv.DiskTotalBytes) * 100
			return []float64{used}, true
		}
		return []float64{float64(total)}, true

	case domain.SignalP95LatencyMs, domain.SignalRequestsPerSecond:
		buckets, err := e.store.ListRequestMetrics(ctx, r.TargetKind, r.TargetID, since)
		if err != nil || len(buckets) == 0 {
			return nil, false
		}
		out := make([]float64, 0, len(buckets))
		for _, b := range buckets {
			rate := int64(b.SampleRate)
			if rate < 1 {
				rate = 1
			}
			if r.Signal == domain.SignalP95LatencyMs {
				out = append(out, domain.Percentile(b.LatencyBuckets, 0.95))
				continue
			}
			// The bucket length is not stored on the row, so the rate uses the
			// same nominal 300s every other reader assumes.
			out = append(out, float64(b.Requests*rate)/300)
		}
		return out, true
	}
	return nil, false
}

func covered(b domain.ResourceMetricBucket) bool {
	if b.CoveredSeconds <= 0 {
		return false
	}
	// Nominal length is not stored per row, so the floor is against the panel's
	// default bucket. A shorter configured bucket makes this stricter, which is
	// the safe direction: it produces no_data rather than a value computed over
	// a sliver of coverage.
	return float64(b.CoveredSeconds) >= coverageFloor*300
}

func (e *Evaluator) evaluate(ctx context.Context, r domain.AlertRule) {
	now := e.now()
	reading := e.Evaluate(ctx, r, now)

	switch {
	case !reading.Breached && !reading.Clear:
		e.enterNoData(ctx, r, now)
	case reading.Breached:
		e.fire(ctx, r, now, reading)
	default:
		e.resolve(ctx, r, now)
	}
}

func (e *Evaluator) enterNoData(ctx context.Context, r domain.AlertRule, now time.Time) {
	if r.State == domain.AlertNoData {
		// A rule that has seen nothing for a day is news, ONCE. Saying it every
		// tick is how the message stops being read.
		if now.Sub(r.StateSince) >= NoDataQuiet && (r.QuietNotifiedAt == nil || now.Sub(*r.QuietNotifiedAt) >= NoDataQuiet) {
			e.announceQuiet(ctx, r, domain.InboxAlertNoData,
				"This rule has had no complete data to evaluate for over a day. Its target may have been stopped, or its agent may not be reporting.")
			stamped := now
			if err := e.store.SetAlertRuleState(ctx, r.ID, r.State, r.StateSince, r.RearmUntil, &stamped); err != nil {
				e.log.Error("alerts: stamping no-data notice", "rule_id", r.ID, "error", err)
			}
		}
		return
	}
	// Leaving `firing` for no_data does NOT resolve the episode: we do not know
	// that it recovered, and saying so would be the all-clear-during-an-outage
	// failure this whole design exists to avoid.
	if err := e.store.SetAlertRuleState(ctx, r.ID, domain.AlertNoData, now, r.RearmUntil, nil); err != nil {
		e.log.Error("alerts: entering no-data", "rule_id", r.ID, "error", err)
	}
}

func (e *Evaluator) fire(ctx context.Context, r domain.AlertRule, now time.Time, reading Reading) {
	if r.State == domain.AlertFiring {
		// Already firing: keep the peak current so the notification's "peak
		// 96%" is the worst of the episode rather than the first reading.
		if ev, err := e.store.GetOpenAlertEvent(ctx, r.ID); err == nil {
			if err := e.store.UpdateAlertEventPeak(ctx, ev.ID, reading.Peak); err != nil {
				e.log.Debug("alerts: updating peak", "rule_id", r.ID, "error", err)
			}
		}
		return
	}

	// The flap guard counts EPISODES, delivered or not: slow oscillation walks
	// straight through a re-arm delay, and that is how a channel gets muted.
	episodes, err := e.store.CountAlertEpisodesSince(ctx, r.ID, now.Add(-FlapWindow))
	if err != nil {
		e.log.Error("alerts: counting episodes", "rule_id", r.ID, "error", err)
	}
	flapping := episodes+1 >= FlapEpisodes
	held := r.RearmUntil != nil && now.Before(*r.RearmUntil)
	deliver := !flapping && !held

	if _, err := e.store.OpenAlertEvent(ctx, ids.New(ids.PrefixAlertEvent), r.ID, now, reading.Peak, deliver); err != nil {
		e.log.Error("alerts: opening episode", "rule_id", r.ID, "error", err)
		return
	}

	state := domain.AlertFiring
	if flapping {
		state = domain.AlertFlapping
	}
	if err := e.store.SetAlertRuleState(ctx, r.ID, state, now, r.RearmUntil, r.QuietNotifiedAt); err != nil {
		e.log.Error("alerts: recording fire", "rule_id", r.ID, "error", err)
	}

	if flapping && r.State != domain.AlertFlapping {
		// The one place this feature deliberately stops telling an operator
		// something — so the one message that goes out names the fix.
		e.announceQuiet(ctx, r, domain.InboxAlertFlapping,
			"This rule has fired four times in the last hour and is held until it settles. Its threshold or its window is probably too tight.")
		return
	}
	if !deliver {
		return
	}
	e.deliver(ctx, r, true, reading.Peak)
}

func (e *Evaluator) resolve(ctx context.Context, r domain.AlertRule, now time.Time) {
	if r.State == domain.AlertOK {
		return
	}
	// A flapping rule must hold ONE state for an hour before it speaks again.
	if r.State == domain.AlertFlapping && now.Sub(r.StateSince) < FlapHold {
		return
	}
	wasFiring := r.State == domain.AlertFiring || r.State == domain.AlertFlapping
	var peak float64
	if ev, err := e.store.GetOpenAlertEvent(ctx, r.ID); err == nil {
		peak = ev.PeakValue
		if err := e.store.ResolveAlertEvent(ctx, ev.ID, now); err != nil {
			e.log.Error("alerts: resolving episode", "rule_id", r.ID, "error", err)
		}
	}
	rearm := now.Add(domain.RearmDelay(r.WindowSeconds))
	if err := e.store.SetAlertRuleState(ctx, r.ID, domain.AlertOK, now, &rearm, nil); err != nil {
		e.log.Error("alerts: recording resolve", "rule_id", r.ID, "error", err)
	}
	// Only announce a recovery from something that was announced: a rule that
	// went no_data → ok never said anything to recover from.
	if wasFiring && r.State == domain.AlertFiring {
		e.deliver(ctx, r, false, peak)
	}
}

func (e *Evaluator) targetName(ctx context.Context, r domain.AlertRule) string {
	switch r.TargetKind {
	case domain.AlertTargetApplication:
		if app, err := e.store.GetApplication(ctx, r.TargetID); err == nil {
			return app.Name
		}
	case domain.AlertTargetServer:
		if srv, err := e.store.GetServer(ctx, r.TargetID); err == nil {
			return srv.Name
		}
	}
	return r.TargetID
}

func (e *Evaluator) deliver(ctx context.Context, r domain.AlertRule, firing bool, value float64) {
	if e.notify == nil {
		return
	}
	name := e.targetName(ctx, r)
	if err := e.notify.AnnounceAlert(ctx, r, name, domain.AlertSentence(r, name, ""), firing, value); err != nil {
		e.log.Error("alerts: delivering", "rule_id", r.ID, "error", err)
	}
}

func (e *Evaluator) announceQuiet(ctx context.Context, r domain.AlertRule, kind, detail string) {
	if e.quiet == nil {
		return
	}
	name := e.targetName(ctx, r)
	if err := e.quiet.RecordAlertQuiet(ctx, kind, r, name, detail); err != nil {
		e.log.Error("alerts: writing an inbox item", "rule_id", r.ID, "error", err)
	}
}

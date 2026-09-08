package alerts

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

type fake struct {
	rules    []domain.AlertRule
	buckets  []domain.ResourceMetricBucket
	events   []domain.AlertEvent
	states   []string
	delivers []bool
	quiet    []string
}

func (f *fake) ListEnabledAlertRules(context.Context) ([]domain.AlertRule, error) {
	return f.rules, nil
}
func (f *fake) SetAlertRuleState(_ context.Context, id, state string, since time.Time, rearm, quiet *time.Time) error {
	f.states = append(f.states, state)
	for i := range f.rules {
		if f.rules[i].ID == id {
			f.rules[i].State = state
			f.rules[i].StateSince = since
			f.rules[i].RearmUntil = rearm
			f.rules[i].QuietNotifiedAt = quiet
		}
	}
	return nil
}
func (f *fake) OpenAlertEvent(_ context.Context, id, ruleID string, at time.Time, peak float64, delivered bool) (domain.AlertEvent, error) {
	ev := domain.AlertEvent{ID: id, RuleID: ruleID, StartedAt: at, PeakValue: peak, Delivered: delivered}
	f.events = append(f.events, ev)
	return ev, nil
}
func (f *fake) ResolveAlertEvent(_ context.Context, id string, at time.Time) error {
	for i := range f.events {
		if f.events[i].ID == id {
			end := at
			f.events[i].ResolvedAt = &end
		}
	}
	return nil
}
func (f *fake) UpdateAlertEventPeak(context.Context, string, float64) error { return nil }
func (f *fake) GetOpenAlertEvent(_ context.Context, ruleID string) (domain.AlertEvent, error) {
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i].RuleID == ruleID && f.events[i].ResolvedAt == nil {
			return f.events[i], nil
		}
	}
	return domain.AlertEvent{}, io.EOF
}
func (f *fake) CountAlertEpisodesSince(_ context.Context, ruleID string, since time.Time) (int, error) {
	n := 0
	for _, e := range f.events {
		if e.RuleID == ruleID && !e.StartedAt.Before(since) {
			n++
		}
	}
	return n, nil
}
func (f *fake) ListResourceMetrics(context.Context, string, string, time.Time) ([]domain.ResourceMetricBucket, error) {
	return f.buckets, nil
}
func (f *fake) ListRequestMetrics(context.Context, string, string, time.Time) ([]domain.RequestMetricBucket, error) {
	return nil, nil
}
func (f *fake) LatestResourceDisk(context.Context, string, string) (domain.ResourceDiskBucket, error) {
	return domain.ResourceDiskBucket{}, io.EOF
}
func (f *fake) GetApplication(context.Context, string) (domain.Application, error) {
	return domain.Application{Name: "web"}, nil
}
func (f *fake) GetServer(context.Context, string) (domain.Server, error) {
	return domain.Server{Name: "node"}, nil
}

func (f *fake) AnnounceAlert(_ context.Context, _ domain.AlertRule, _, _ string, firing bool, _ float64) error {
	f.delivers = append(f.delivers, firing)
	return nil
}
func (f *fake) RecordAlertQuiet(_ context.Context, kind string, _ domain.AlertRule, _, _ string) error {
	f.quiet = append(f.quiet, kind)
	return nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func rule() domain.AlertRule {
	return domain.AlertRule{
		ID: "alr_1", TargetKind: domain.AlertTargetApplication, TargetID: "app_1",
		Signal: domain.SignalCPU, Threshold: 80, ThresholdUnit: domain.UnitPercent,
		WindowSeconds: 900, NotifierID: "ntf_1", Enabled: true, State: domain.AlertOK,
	}
}

// cpuBucket makes a fully covered bucket at the given percentage.
func cpuBucket(at time.Time, percent float64) domain.ResourceMetricBucket {
	return domain.ResourceMetricBucket{
		BucketStart: at, CoveredSeconds: 300,
		CPUCoreMs: int64(percent / 100 * 300 * 1000),
	}
}

// The whole point of the window: one bucket over the line does not fire, three
// do. A four-second spike moves a five-minute mean by about a percent, and
// never storing sample-level data is what makes that true by construction.
func TestOneBucketOverTheLineDoesNotFire(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f := &fake{rules: []domain.AlertRule{rule()}}
	f.buckets = []domain.ResourceMetricBucket{
		cpuBucket(now.Add(-15*time.Minute), 20),
		cpuBucket(now.Add(-10*time.Minute), 95),
		cpuBucket(now.Add(-5*time.Minute), 20),
	}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })
	e.Tick(context.Background())

	if len(f.events) != 0 {
		t.Fatalf("opened %d episodes for a single high bucket; the window exists precisely so it opens none", len(f.events))
	}
	if len(f.delivers) != 0 {
		t.Fatalf("delivered %d messages for a spike", len(f.delivers))
	}
}

func TestASustainedBreachFiresOnce(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f := &fake{rules: []domain.AlertRule{rule()}}
	f.buckets = []domain.ResourceMetricBucket{
		cpuBucket(now.Add(-15*time.Minute), 91),
		cpuBucket(now.Add(-10*time.Minute), 96),
		cpuBucket(now.Add(-5*time.Minute), 88),
	}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })

	e.Tick(context.Background())
	e.Tick(context.Background())
	e.Tick(context.Background())

	if len(f.events) != 1 {
		t.Fatalf("opened %d episodes across three ticks of one continuous breach; want 1 — an episode is not an evaluation", len(f.events))
	}
	if len(f.delivers) != 1 || !f.delivers[0] {
		t.Fatalf("delivered %v, want exactly one firing message", f.delivers)
	}
	// The peak is the worst bucket, which is the number an operator wants once
	// they know the alert is real.
	if f.events[0].PeakValue < 95 {
		t.Errorf("peak = %v, want the worst bucket (~96)", f.events[0].PeakValue)
	}
}

// The worst thing an alerting system can say is "all clear" during an outage
// of its own collection path.
func TestAGapDoesNotResolveAndDoesNotFire(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	r := rule()
	r.State = domain.AlertFiring
	f := &fake{rules: []domain.AlertRule{r}}
	f.buckets = []domain.ResourceMetricBucket{
		cpuBucket(now.Add(-15*time.Minute), 95),
		// An agent restart: 40 seconds of a 300-second bucket. Dividing a spike
		// by that would manufacture a convincing 300% reading.
		{BucketStart: now.Add(-10 * time.Minute), CoveredSeconds: 40, CPUCoreMs: 40_000},
		cpuBucket(now.Add(-5*time.Minute), 95),
	}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })
	e.Tick(context.Background())

	if len(f.delivers) != 0 {
		t.Fatalf("delivered %v across a gap; a gap must neither fire nor resolve", f.delivers)
	}
	if len(f.states) != 1 || f.states[0] != domain.AlertNoData {
		t.Fatalf("states = %v, want a single transition to no_data", f.states)
	}
	// Crucially, the open episode is NOT resolved: we do not know that it
	// recovered.
	for _, ev := range f.events {
		if ev.ResolvedAt != nil {
			t.Error("an episode was resolved across a gap")
		}
	}
}

// A partially covered bucket is unknown, neither high nor low — never a value.
func TestThinCoverageIsNotAValue(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f := &fake{rules: []domain.AlertRule{rule()}}
	f.buckets = []domain.ResourceMetricBucket{
		// 40 seconds carrying 40 core-seconds: 100% if you divide by coverage,
		// which is exactly the reading this rule refuses to produce.
		{BucketStart: now.Add(-5 * time.Minute), CoveredSeconds: 40, CPUCoreMs: 40_000},
	}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })
	e.Tick(context.Background())

	if len(f.events) != 0 {
		t.Fatal("a bucket covering 40 of 300 seconds produced a breach")
	}
}

// Slow oscillation walks straight through a re-arm delay, and that is how a
// channel gets muted — so the fourth episode in an hour holds the rule.
func TestTheFourthEpisodeInAnHourHoldsTheRule(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	r := rule()
	f := &fake{rules: []domain.AlertRule{r}}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })

	high := []domain.ResourceMetricBucket{cpuBucket(now.Add(-5*time.Minute), 95)}
	low := []domain.ResourceMetricBucket{cpuBucket(now.Add(-5*time.Minute), 10)}

	for i := 0; i < 4; i++ {
		// Each cycle: breach, then clear. The re-arm delay is stepped past so
		// the guard is what is being tested, not the cooldown.
		f.buckets = high
		e.Tick(context.Background())
		now = now.Add(20 * time.Minute)
		f.rules[0].RearmUntil = nil
		f.buckets = low
		e.Tick(context.Background())
	}

	if len(f.events) != 4 {
		t.Fatalf("opened %d episodes, want 4", len(f.events))
	}
	if f.events[3].Delivered {
		t.Error("the fourth episode inside an hour was delivered; the flap guard must hold it")
	}
	found := false
	for _, k := range f.quiet {
		if k == domain.InboxAlertFlapping {
			found = true
		}
	}
	if !found {
		t.Errorf("entering flapping wrote no inbox item (%v) — the one message that goes out has to name the fix", f.quiet)
	}
}

// The recovery is symmetric: same window, same coverage rule, same gap rule,
// and ONE threshold rather than a hysteresis band.
func TestRecoveryUsesTheSameThreshold(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	r := rule()
	r.State = domain.AlertFiring
	f := &fake{rules: []domain.AlertRule{r}, events: []domain.AlertEvent{
		{ID: "ale_1", RuleID: "alr_1", StartedAt: now.Add(-time.Hour), PeakValue: 96},
	}}
	f.buckets = []domain.ResourceMetricBucket{
		cpuBucket(now.Add(-10*time.Minute), 79),
		cpuBucket(now.Add(-5*time.Minute), 12),
	}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })
	e.Tick(context.Background())

	if len(f.delivers) != 1 || f.delivers[0] {
		t.Fatalf("delivers = %v, want one resolve message", f.delivers)
	}
	if f.events[0].ResolvedAt == nil {
		t.Error("the episode was not closed")
	}
	if f.rules[0].RearmUntil == nil {
		t.Error("no re-arm delay was set: a value sitting on the threshold would otherwise message every tick")
	}
}

// A value at exactly the threshold is neither a breach nor a recovery, so it
// cannot chatter between the two.
func TestExactlyOnTheThresholdIsNeither(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f := &fake{rules: []domain.AlertRule{rule()}}
	f.buckets = []domain.ResourceMetricBucket{cpuBucket(now.Add(-5*time.Minute), 80)}
	e := New(f, f, f, quietLog())
	e.SetClock(func() time.Time { return now })

	reading := e.Evaluate(context.Background(), f.rules[0], now)
	if reading.Breached || reading.Clear {
		t.Fatalf("a value exactly on the threshold read as breached=%v clear=%v; it must be neither", reading.Breached, reading.Clear)
	}
}

// The rule's sentence is its name, and it is rendered from the row so nothing
// can drift.
func TestTheSentenceSaysWhichDenominator(t *testing.T) {
	server := domain.AlertRule{
		TargetKind: domain.AlertTargetServer, Signal: domain.SignalCPU,
		Threshold: 90, ThresholdUnit: domain.UnitPercent, WindowSeconds: 600,
	}
	app := domain.AlertRule{
		TargetKind: domain.AlertTargetApplication, Signal: domain.SignalCPU,
		Threshold: 190, ThresholdUnit: domain.UnitPercent, WindowSeconds: 600,
	}
	s := domain.AlertSentence(server, "node-1", "on-call")
	aSentence := domain.AlertSentence(app, "web", "on-call")
	if s == aSentence {
		t.Fatal("the two CPU sentences read identically; same signal, two denominators, and the sentence has to say which")
	}
	if !contains(s, "all its cores") || !contains(aSentence, "one core") {
		t.Errorf("denominators not named:\n  server: %s\n  app:    %s", s, aSentence)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

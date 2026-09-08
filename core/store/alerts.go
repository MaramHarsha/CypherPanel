package store

// Threshold alert rules and their episodes (threshold-alerts.md §2).

import (
	"context"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func alertRuleFromRow(r db.AlertRule) domain.AlertRule {
	return domain.AlertRule{
		ID: r.ID, TargetKind: r.TargetKind, TargetID: r.TargetID,
		Signal: r.Signal, Threshold: r.Threshold, ThresholdUnit: r.ThresholdUnit,
		WindowSeconds: int(r.WindowSeconds), NotifierID: r.NotifierID,
		Enabled: r.Enabled, State: r.State, StateSince: r.StateSince.Time,
		RearmUntil: ptrTime(r.RearmUntil), QuietNotifiedAt: ptrTime(r.QuietNotifiedAt),
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func alertEventFromRow(r db.AlertEvent) domain.AlertEvent {
	return domain.AlertEvent{
		ID: r.ID, RuleID: r.RuleID, StartedAt: r.StartedAt.Time,
		ResolvedAt: ptrTime(r.ResolvedAt), PeakValue: r.PeakValue, Delivered: r.Delivered,
	}
}

func (s *Store) CreateAlertRule(ctx context.Context, r domain.AlertRule) (domain.AlertRule, error) {
	row, err := s.q.CreateAlertRule(ctx, db.CreateAlertRuleParams{
		ID: r.ID, TargetKind: r.TargetKind, TargetID: r.TargetID,
		Signal: r.Signal, Threshold: r.Threshold, ThresholdUnit: r.ThresholdUnit,
		WindowSeconds: int32(r.WindowSeconds), NotifierID: r.NotifierID, Enabled: r.Enabled,
	})
	if err != nil {
		return domain.AlertRule{}, wrap("creating alert rule", err)
	}
	return alertRuleFromRow(row), nil
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (domain.AlertRule, error) {
	row, err := s.q.GetAlertRule(ctx, id)
	if err != nil {
		return domain.AlertRule{}, wrap("reading alert rule", err)
	}
	return alertRuleFromRow(row), nil
}

func (s *Store) ListAlertRules(ctx context.Context) ([]domain.AlertRule, error) {
	rows, err := s.q.ListAlertRules(ctx)
	if err != nil {
		return nil, wrap("listing alert rules", err)
	}
	return mapAlertRules(rows), nil
}

func (s *Store) ListAlertRulesForTarget(ctx context.Context, kind, id string) ([]domain.AlertRule, error) {
	rows, err := s.q.ListAlertRulesForTarget(ctx, db.ListAlertRulesForTargetParams{TargetKind: kind, TargetID: id})
	if err != nil {
		return nil, wrap("listing alert rules", err)
	}
	return mapAlertRules(rows), nil
}

func (s *Store) ListEnabledAlertRules(ctx context.Context) ([]domain.AlertRule, error) {
	rows, err := s.q.ListEnabledAlertRules(ctx)
	if err != nil {
		return nil, wrap("listing enabled alert rules", err)
	}
	return mapAlertRules(rows), nil
}

func mapAlertRules(rows []db.AlertRule) []domain.AlertRule {
	out := make([]domain.AlertRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, alertRuleFromRow(r))
	}
	return out
}

func (s *Store) SetAlertRuleEnabled(ctx context.Context, id string, enabled bool) (domain.AlertRule, error) {
	row, err := s.q.SetAlertRuleEnabled(ctx, db.SetAlertRuleEnabledParams{ID: id, Enabled: enabled})
	if err != nil {
		return domain.AlertRule{}, wrap("updating alert rule", err)
	}
	return alertRuleFromRow(row), nil
}

func (s *Store) SetAlertRuleState(ctx context.Context, id, state string, since time.Time, rearmUntil, quietNotifiedAt *time.Time) error {
	if err := s.q.SetAlertRuleState(ctx, db.SetAlertRuleStateParams{
		ID: id, State: state, StateSince: tsFromTime(since),
		RearmUntil: tsFromPtr(rearmUntil), QuietNotifiedAt: tsFromPtr(quietNotifiedAt),
	}); err != nil {
		return wrap("updating alert rule state", err)
	}
	return nil
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	if err := s.q.DeleteAlertRule(ctx, id); err != nil {
		return wrap("deleting alert rule", err)
	}
	return nil
}

// DeleteAlertRulesForTarget drops a deleted resource's rules. Configuration for
// something that no longer exists is noise; the audit log keeps the record that
// it happened.
func (s *Store) DeleteAlertRulesForTarget(ctx context.Context, kind, id string) error {
	if err := s.q.DeleteAlertRulesForTarget(ctx, db.DeleteAlertRulesForTargetParams{TargetKind: kind, TargetID: id}); err != nil {
		return wrap("deleting alert rules", err)
	}
	return nil
}

// CountAlertRulesByNotifier answers the 409 that names the rules rather than
// letting a notifier vanish and leave a smoke detector with no battery.
func (s *Store) CountAlertRulesByNotifier(ctx context.Context, notifierID string) (int, error) {
	n, err := s.q.CountAlertRulesByNotifier(ctx, notifierID)
	if err != nil {
		return 0, wrap("counting alert rules", err)
	}
	return int(n), nil
}

func (s *Store) OpenAlertEvent(ctx context.Context, id, ruleID string, at time.Time, peak float64, delivered bool) (domain.AlertEvent, error) {
	row, err := s.q.OpenAlertEvent(ctx, db.OpenAlertEventParams{
		ID: id, RuleID: ruleID, StartedAt: tsFromTime(at), PeakValue: peak, Delivered: delivered,
	})
	if err != nil {
		return domain.AlertEvent{}, wrap("opening alert event", err)
	}
	return alertEventFromRow(row), nil
}

func (s *Store) ResolveAlertEvent(ctx context.Context, id string, at time.Time) error {
	if err := s.q.ResolveAlertEvent(ctx, db.ResolveAlertEventParams{ID: id, ResolvedAt: tsFromTime(at)}); err != nil {
		return wrap("resolving alert event", err)
	}
	return nil
}

func (s *Store) UpdateAlertEventPeak(ctx context.Context, id string, peak float64) error {
	if err := s.q.UpdateAlertEventPeak(ctx, db.UpdateAlertEventPeakParams{ID: id, PeakValue: peak}); err != nil {
		return wrap("updating alert event peak", err)
	}
	return nil
}

func (s *Store) GetOpenAlertEvent(ctx context.Context, ruleID string) (domain.AlertEvent, error) {
	row, err := s.q.GetOpenAlertEvent(ctx, ruleID)
	if err != nil {
		return domain.AlertEvent{}, wrap("reading open alert event", err)
	}
	return alertEventFromRow(row), nil
}

func (s *Store) ListAlertEvents(ctx context.Context, ruleID string, limit int) ([]domain.AlertEvent, error) {
	rows, err := s.q.ListAlertEvents(ctx, db.ListAlertEventsParams{RuleID: ruleID, Limit: int32(limit)})
	if err != nil {
		return nil, wrap("listing alert events", err)
	}
	out := make([]domain.AlertEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, alertEventFromRow(r))
	}
	return out, nil
}

// CountAlertEpisodesSince is the flap guard's input: episodes in a rolling
// window, delivered or not.
func (s *Store) CountAlertEpisodesSince(ctx context.Context, ruleID string, since time.Time) (int, error) {
	n, err := s.q.CountAlertEpisodesSince(ctx, db.CountAlertEpisodesSinceParams{RuleID: ruleID, StartedAt: tsFromTime(since)})
	if err != nil {
		return 0, wrap("counting alert episodes", err)
	}
	return int(n), nil
}

package rest

// Threshold alerts (threshold-alerts.md §§2, 6, 7).
//
// The BACKTEST is the feature. Every alerting product treats "what number
// should I type" as the operator's problem and hands them nothing to solve it
// with; this panel already keeps a fortnight of exactly the series the rule
// reads, so "what would this have done" is one indexed range scan over data
// already on disk. It runs before Create does anything.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/alerts"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// AlertStore is the rules-and-episodes surface (consumer-defined).
type AlertStore interface {
	CreateAlertRule(ctx context.Context, r domain.AlertRule) (domain.AlertRule, error)
	GetAlertRule(ctx context.Context, id string) (domain.AlertRule, error)
	ListAlertRules(ctx context.Context) ([]domain.AlertRule, error)
	SetAlertRuleEnabled(ctx context.Context, id string, enabled bool) (domain.AlertRule, error)
	DeleteAlertRule(ctx context.Context, id string) error
	ListAlertEvents(ctx context.Context, ruleID string, limit int) ([]domain.AlertEvent, error)
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
	GetNotifier(ctx context.Context, id string) (domain.Notifier, error)
}

// AlertBacktester answers "what would this rule have done" over the stored
// series. It is the same evaluator the loop uses — two implementations would be
// two answers to one question.
type AlertBacktester interface {
	Evaluate(ctx context.Context, r domain.AlertRule, at time.Time) alerts.Reading
}

type alertRuleDTO struct {
	ID            string  `json:"id"`
	TargetKind    string  `json:"target_kind"`
	TargetID      string  `json:"target_id"`
	TargetName    string  `json:"target_name"`
	Signal        string  `json:"signal"`
	Threshold     float64 `json:"threshold"`
	ThresholdUnit string  `json:"threshold_unit"`
	WindowSeconds int     `json:"window_seconds"`
	NotifierID    string  `json:"notifier_id"`
	NotifierName  string  `json:"notifier_name"`
	Enabled       bool    `json:"enabled"`
	State         string  `json:"state"`
	StateSince    string  `json:"state_since"`
	// Sentence is the rule's name, rendered from the row. Everything that
	// displays a rule shows this, so no label can drift from what the rule does.
	Sentence string `json:"sentence"`
}

type alertEventDTO struct {
	ID         string  `json:"id"`
	StartedAt  string  `json:"started_at"`
	ResolvedAt *string `json:"resolved_at"`
	PeakValue  float64 `json:"peak_value"`
	Delivered  bool    `json:"delivered"`
}

type createAlertRuleRequest struct {
	TargetKind    string  `json:"target_kind"`
	TargetID      string  `json:"target_id"`
	Signal        string  `json:"signal"`
	Threshold     float64 `json:"threshold"`
	ThresholdUnit string  `json:"threshold_unit"`
	WindowSeconds int     `json:"window_seconds"`
	NotifierID    string  `json:"notifier_id"`
}

type backtestResponse struct {
	// WouldHaveFired is episodes, not evaluations: an hour above the line is
	// one alert, and counting ticks would make every rule look catastrophic.
	WouldHaveFired int    `json:"would_have_fired"`
	Days           int    `json:"days"`
	Summary        string `json:"summary"`
	// HadData is false when the series has too many gaps to say anything. A
	// backtest that answers "0" from no data is worse than one that says so.
	HadData bool `json:"had_data"`
}

func (a *API) alertsReady(w http.ResponseWriter) bool {
	if a.deps.Alerts == nil {
		writeError(w, http.StatusNotImplemented, "alerts are not enabled on this panel")
		return false
	}
	return true
}

func (a *API) alertRuleDTO(ctx context.Context, r domain.AlertRule) alertRuleDTO {
	targetName := r.TargetID
	switch r.TargetKind {
	case domain.AlertTargetApplication:
		if app, err := a.deps.Alerts.GetApplication(ctx, r.TargetID); err == nil {
			targetName = app.Name
		}
	case domain.AlertTargetServer:
		if srv, err := a.deps.Alerts.GetServer(ctx, r.TargetID); err == nil {
			targetName = srv.Name
		}
	}
	notifierName := ""
	if n, err := a.deps.Alerts.GetNotifier(ctx, r.NotifierID); err == nil {
		notifierName = n.Name
	}
	return alertRuleDTO{
		ID: r.ID, TargetKind: r.TargetKind, TargetID: r.TargetID, TargetName: targetName,
		Signal: r.Signal, Threshold: r.Threshold, ThresholdUnit: r.ThresholdUnit,
		WindowSeconds: r.WindowSeconds, NotifierID: r.NotifierID, NotifierName: notifierName,
		Enabled: r.Enabled, State: r.State,
		StateSince: r.StateSince.UTC().Format(time.RFC3339),
		Sentence:   domain.AlertSentence(r, targetName, notifierName),
	}
}

func (a *API) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleMember) {
		return
	}
	rules, err := a.deps.Alerts.ListAlertRules(r.Context())
	if err != nil {
		a.deps.Log.Error("listing alert rules", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the rules")
		return
	}
	out := make([]alertRuleDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, a.alertRuleDTO(r.Context(), rule))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateAlertRule holds the refusals. Each one exists because there is no
// correct behaviour to fall back to, only ways to be wrong.
func validateAlertRule(req createAlertRuleRequest) (domain.AlertRule, string) {
	switch req.TargetKind {
	case domain.AlertTargetServer:
		if !domain.SignalOnServer(req.Signal) {
			// Substantive, not an omission: a server DOES have request
			// buckets, but they are the unmatched-router rows — scanners and
			// wrong Host headers. "p95 on this server" would silently mean
			// "p95 of the requests that matched nothing", and a signal whose
			// meaning needs a footnote is the wrong signal.
			return domain.AlertRule{}, "latency and request rate are application signals: a server's request buckets are the traffic that matched no route, which is scanners rather than your users"
		}
	case domain.AlertTargetApplication:
		if !domain.SignalOnApplication(req.Signal) {
			return domain.AlertRule{}, "unknown signal"
		}
	default:
		return domain.AlertRule{}, "the target is a server or an application"
	}
	if req.NotifierID == "" {
		return domain.AlertRule{}, "pick where the alert should go"
	}
	if req.Threshold <= 0 {
		return domain.AlertRule{}, "the threshold must be above zero"
	}
	// One bucket is the floor, because sample-level data does not exist on the
	// plane by design — and never having stored it is what makes even the
	// cheapest rule immune to a single spike.
	if req.WindowSeconds < 300 {
		req.WindowSeconds = 300
	}
	if req.WindowSeconds > 24*3600 {
		return domain.AlertRule{}, "the window is at most 24 hours"
	}
	unit := req.ThresholdUnit
	if unit == "" {
		unit = defaultUnit(req.TargetKind, req.Signal)
	}
	switch unit {
	case domain.UnitPercent, domain.UnitBytes, domain.UnitMilliseconds, domain.UnitPerSecond:
	default:
		return domain.AlertRule{}, "unknown threshold unit"
	}
	if unit == domain.UnitPercent && req.Threshold > 1000 {
		return domain.AlertRule{}, "a percentage above 1000 is almost certainly a typo"
	}
	return domain.AlertRule{
		TargetKind: req.TargetKind, TargetID: req.TargetID, Signal: req.Signal,
		Threshold: req.Threshold, ThresholdUnit: unit,
		WindowSeconds: req.WindowSeconds, NotifierID: req.NotifierID, Enabled: true,
	}, ""
}

func defaultUnit(kind, signal string) string {
	switch signal {
	case domain.SignalP95LatencyMs:
		return domain.UnitMilliseconds
	case domain.SignalRequestsPerSecond:
		return domain.UnitPerSecond
	case domain.SignalDisk:
		if kind == domain.AlertTargetServer {
			return domain.UnitPercent
		}
		return domain.UnitBytes
	default:
		return domain.UnitPercent
	}
}

func (a *API) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req createAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rule, reason := validateAlertRule(req)
	if reason != "" {
		writeError(w, http.StatusBadRequest, reason)
		return
	}
	rule.ID = ids.New(ids.PrefixAlertRule)
	saved, err := a.deps.Alerts.CreateAlertRule(r.Context(), rule)
	if err != nil {
		// The unique constraint is what stops the double-add that would
		// silently deliver every alert twice.
		a.deps.Log.Warn("creating alert rule", "error", err)
		writeError(w, http.StatusConflict, "an identical rule already exists")
		return
	}
	dto := a.alertRuleDTO(r.Context(), saved)
	a.audit(r, audit.Entry{
		Action:   audit.ActionAlertRuleCreated,
		Resource: audit.Resource(audit.ResourcePanel, saved.ID, dto.Sentence),
		Detail:   map[string]any{"rule": dto.Sentence},
	})
	writeJSON(w, http.StatusCreated, dto)
}

func (a *API) handleSetAlertRuleEnabled(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	saved, err := a.deps.Alerts.SetAlertRuleEnabled(r.Context(), r.PathValue("id"), req.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("updating alert rule", "rule_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not update the rule")
		return
	}
	dto := a.alertRuleDTO(r.Context(), saved)
	a.audit(r, audit.Entry{
		Action:   audit.ActionAlertRuleChanged,
		Resource: audit.Resource(audit.ResourcePanel, saved.ID, dto.Sentence),
		Detail:   map[string]any{"enabled": req.Enabled},
	})
	writeJSON(w, http.StatusOK, dto)
}

func (a *API) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	rule, err := a.deps.Alerts.GetAlertRule(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the rule")
		return
	}
	dto := a.alertRuleDTO(r.Context(), rule)
	if err := a.deps.Alerts.DeleteAlertRule(r.Context(), rule.ID); err != nil {
		a.deps.Log.Error("deleting alert rule", "rule_id", rule.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the rule")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionAlertRuleDeleted,
		Resource: audit.Resource(audit.ResourcePanel, rule.ID, dto.Sentence),
		Detail:   map[string]any{"rule": dto.Sentence},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleMember) {
		return
	}
	events, err := a.deps.Alerts.ListAlertEvents(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		a.deps.Log.Error("listing alert events", "rule_id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the history")
		return
	}
	out := make([]alertEventDTO, 0, len(events))
	for _, e := range events {
		var resolved *string
		if e.ResolvedAt != nil {
			s := e.ResolvedAt.UTC().Format(time.RFC3339)
			resolved = &s
		}
		out = append(out, alertEventDTO{
			ID: e.ID, StartedAt: e.StartedAt.UTC().Format(time.RFC3339),
			ResolvedAt: resolved, PeakValue: e.PeakValue, Delivered: e.Delivered,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBacktestAlertRule answers "what would this rule have done" before the
// operator commits to a number.
//
// It walks the retained series one window at a time and counts EPISODES — a
// transition into breach, not every tick above the line — because an hour above
// the threshold is one alert, and counting evaluations would make every rule
// look catastrophic and teach the operator to ignore this number.
func (a *API) handleBacktestAlertRule(w http.ResponseWriter, r *http.Request) {
	if !a.alertsReady(w) {
		return
	}
	if a.deps.AlertBacktest == nil {
		writeError(w, http.StatusNotImplemented, "the backtest is not enabled on this panel")
		return
	}
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	var req createAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rule, reason := validateAlertRule(req)
	if reason != "" {
		writeError(w, http.StatusBadRequest, reason)
		return
	}

	const days = 7
	now := time.Now()
	step := time.Duration(rule.WindowSeconds) * time.Second
	episodes, firing, sawData := 0, false, false
	for at := now.AddDate(0, 0, -days); at.Before(now); at = at.Add(step) {
		reading := a.deps.AlertBacktest.Evaluate(r.Context(), rule, at)
		if !reading.Breached && !reading.Clear {
			// A gap breaks the run and resolves nothing — the same rule the
			// evaluator follows, because two rules would be two answers.
			continue
		}
		sawData = true
		if reading.Breached && !firing {
			episodes++
			firing = true
		}
		if reading.Clear {
			firing = false
		}
	}

	out := backtestResponse{WouldHaveFired: episodes, Days: days, HadData: sawData}
	switch {
	case !sawData:
		out.Summary = "There is not enough recorded history to say — this target has no complete buckets in the last week."
	case episodes == 0:
		out.Summary = "Would not have fired in the last 7 days."
	case episodes == 1:
		out.Summary = "Would have fired once in the last 7 days."
	default:
		out.Summary = fmt.Sprintf("Would have fired %d times in the last 7 days.", episodes)
	}
	writeJSON(w, http.StatusOK, out)
}

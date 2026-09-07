package alerts

// Delivery: turning a rule's state change into the one sentence the rule is.

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// NotifierStore resolves the notifier a rule names.
type NotifierStore interface {
	GetNotifier(ctx context.Context, id string) (domain.Notifier, error)
}

// Sender is the notify Manager's direct-delivery path.
type Sender interface {
	Deliver(ctx context.Context, n domain.Notifier, ev domain.NotifyEvent) error
}

// Delivery adapts the two into the Evaluator's Notifier seam.
type Delivery struct {
	store  NotifierStore
	sender Sender
}

func NewDelivery(store NotifierStore, sender Sender) *Delivery {
	return &Delivery{store: store, sender: sender}
}

// AnnounceAlert renders the rule's own sentence rather than a summary of it, so
// the Discord message, the list and the API all say the same words. The peak is
// the number an operator wants once they know the alert is real — "peak 96%" —
// and it is the worst bucket of the episode rather than the reading that
// happened to trip it.
func (d *Delivery) AnnounceAlert(ctx context.Context, rule domain.AlertRule, targetName, sentence string, firing bool, value float64) error {
	n, err := d.store.GetNotifier(ctx, rule.NotifierID)
	if err != nil {
		return fmt.Errorf("alerts: resolving notifier: %w", err)
	}
	ev := domain.NotifyEvent{
		Level:        domain.NotifyError,
		ResourceKind: resourceKind(rule),
		ResourceID:   rule.TargetID,
		FocusID:      rule.ID,
	}
	if firing {
		ev.Type = domain.EventAlertFiring
		ev.Title = "Alert: " + targetName
		ev.Body = fmt.Sprintf("%s\n\n%s", sentence, peakLine(rule, value))
	} else {
		ev.Type = domain.EventAlertResolved
		ev.Level = domain.NotifyInfo
		ev.Title = "Recovered: " + targetName
		ev.Body = fmt.Sprintf("%s\n\nIt is back below the threshold.\n%s", sentence, peakLine(rule, value))
	}
	return d.sender.Deliver(ctx, n, ev)
}

func peakLine(rule domain.AlertRule, value float64) string {
	if value <= 0 {
		return ""
	}
	switch rule.ThresholdUnit {
	case domain.UnitPercent:
		return fmt.Sprintf("Peak %.1f%% during the episode.", value)
	case domain.UnitMilliseconds:
		return fmt.Sprintf("Peak %.0f ms during the episode.", value)
	case domain.UnitPerSecond:
		return fmt.Sprintf("Peak %.1f/s during the episode.", value)
	default:
		return fmt.Sprintf("Peak %.0f during the episode.", value)
	}
}

func resourceKind(rule domain.AlertRule) string {
	if rule.TargetKind == domain.AlertTargetApplication {
		return domain.WebhookResourceApplication
	}
	return ""
}

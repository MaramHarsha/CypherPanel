package alerts

// The inbox side: the one message a rule that has stopped delivering owes the
// person who wrote it.

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// InboxWriter is the notification inbox (consumer-defined).
type InboxWriter interface {
	RecordAlertQuiet(ctx context.Context, kind, ruleID, title, body string) error
}

// Quiet adapts the inbox into the Evaluator's QuietSink seam.
type Quiet struct{ inbox InboxWriter }

func NewQuiet(inbox InboxWriter) *Quiet { return &Quiet{inbox: inbox} }

// RecordAlertQuiet writes the item. The title names the rule in the same words
// everything else uses, and the body NAMES THE FIX — this is the one place the
// feature deliberately stops telling an operator something, so the message that
// does go out has to be worth the silence.
func (q *Quiet) RecordAlertQuiet(ctx context.Context, kind string, rule domain.AlertRule, targetName, detail string) error {
	if q.inbox == nil {
		return nil
	}
	title := "Alert not delivering: " + targetName
	if kind == domain.InboxAlertFlapping {
		title = "Alert held: " + targetName
	}
	body := fmt.Sprintf("%s\n\n%s", domain.AlertSentence(rule, targetName, ""), detail)
	return q.inbox.RecordAlertQuiet(ctx, kind, rule.ID, title, body)
}

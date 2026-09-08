package domain

import "time"

// Notification channel kinds (notifications.md §2). Stable wire/DB values.
const (
	NotifyChannelEmail    = "email"
	NotifyChannelDiscord  = "discord"
	NotifyChannelSlack    = "slack"
	NotifyChannelTelegram = "telegram"
)

// Notification event keys (notifications.md §3). A Notifier subscribes to a
// subset; these are the transitions the control plane already reaches a
// terminal decision on.
const (
	EventDeploySucceeded = "deploy.succeeded"
	EventDeployFailed    = "deploy.failed"
	EventBackupSucceeded = "backup.succeeded"
	EventBackupFailed    = "backup.failed"

	// EventAppCrashed / EventAppRecovered are the observed health transitions
	// of a running application (deployment-control.md §5).
	//
	// Subscribable rather than inbox-only, unlike the governance kinds: this is
	// an observed transition of a resource, which is exactly what this taxonomy
	// is for — and "tell me in Discord when production dies" is the single
	// most-wanted notification a PaaS has.
	EventAppCrashed   = "app.crashed"
	EventAppRecovered = "app.recovered"
)

// eventTypes is the subscribable taxonomy in one place. Both notifiers
// (notifications.md §3) and outbound webhook endpoints
// (outbound-webhooks.md §3) validate against it, so adding a key is one edit
// and both features gain it at once.
var eventTypes = []string{
	EventDeploySucceeded,
	EventDeployFailed,
	EventBackupSucceeded,
	EventBackupFailed,
	EventAppCrashed,
	EventAppRecovered,
}

// EventTypes returns the subscribable event keys, in documentation order. The
// returned slice is a copy — callers may not mutate the taxonomy.
func EventTypes() []string {
	out := make([]string, len(eventTypes))
	copy(out, eventTypes)
	return out
}

// ValidEventType reports whether key is a subscribable event. Delivery-only
// keys (webhook.ping, outbound-webhooks.md §3) are deliberately not members:
// an endpoint always receives its own pings regardless of subscription, so
// naming one in an `events` array is a mistake, not a subscription.
func ValidEventType(key string) bool {
	for _, e := range eventTypes {
		if e == key {
			return true
		}
	}
	return false
}

// Notifier is a declarative "on these events, deliver to this channel" row,
// scoped to a Project. Channel config (SMTP creds, webhook URL, bot token) is
// sealed at rest and never returned in an API response (rule 20).
type Notifier struct {
	ID          string
	ProjectID   string
	Name        string
	Channel     string
	ConfigCT    []byte
	ConfigNonce []byte
	Events      []string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NotifyLevel classifies an event for channel rendering (colour, prefix).
type NotifyLevel string

const (
	NotifyInfo  NotifyLevel = "info"
	NotifyError NotifyLevel = "error"
)

// NotifyEvent is the plane-domain value a channel sender renders. It carries no
// sealed material — only metadata already surfaced through the API
// (notifications.md §6).
//
// The last four fields are additive (notification-inbox.md §4): they let the
// inbox render a deep link server-side, so a CLI prints the same words the
// drawer does. Channel senders ignore them, which is why adding them changed no
// channel copy.
type NotifyEvent struct {
	Type    string
	Level   NotifyLevel
	Title   string
	Body    string
	Project string

	// ProjectID is the owning project — the inbox's fan-out key. dispatch sets
	// it where it already resolves the environment.
	ProjectID string
	// ResourceKind is application | database (the WebhookResource* constants).
	ResourceKind string
	// ResourceID is the application or database the event describes.
	ResourceID string
	// FocusID is the deployment or backup record the link opens, and the
	// per-event identity the digest de-duplicates on.
	FocusID string
}
